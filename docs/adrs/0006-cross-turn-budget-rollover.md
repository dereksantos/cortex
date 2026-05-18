# ADR-006: Cross-turn budget rollover (deferred spawn queue)

**Status:** Accepted (Stage 4)
**Date:** 2026-05-18
**Context:** `docs/dag-build-plan.md` Stage 4 — parallelism + budget
rollover; `docs/dag-protocol.md` "Budget model".

## Decision

Spawns refused for `budget_exceeded` mid-turn are persisted to a
per-project **deferred-spawn queue** and prepended to the seed of
the next turn that observes the queue, up to a staleness cap.

Queue file: `.cortex/db/deferred_spawns.jsonl`
File format: JSONL, one `DeferredSpawn` per line.
Default staleness cap: 1 hour (`DefaultDeferredSpawnMaxAge`).

## Record shape

```json
{
  "turn_id":        "turn-A",
  "parent_node_id": "n5",
  "child":          {"function": "decide", "op": "inject", "id": "...", "attrs": {...}},
  "reason":         "latency_ms",
  "deferred_at":    "2026-05-18T12:34:56Z"
}
```

`child` is the persistable projection of `NodeSpec` — only
identity-shaped fields (function, op, id, parent, attrs) roundtrip.
Handler + registration-time metadata (Cost, Inputs, Outputs,
MaxFanout, AxisContract, Description) reconstitute from the
registry on replay via `QualifiedName()`. `NodeSpec.MarshalJSON`
enforces this projection.

## Semantics

| Event | Behavior |
|---|---|
| Spawn refused for `budget_exceeded` during a Run | `DeferredSpawn` appended to queue (best-effort: write failure is logged but does not derail the turn) |
| Spawn refused for `depth_exceeded` or `max_fanout_exceeded` | Not queued — these are structural caps, not budget pressure |
| `Executor.Run` start, if `DeferredQueue` wired | `ReadAndConsume()` drains fresh entries (younger than `MaxAge`); each is prepended to the pending set with `Parent = original ParentNodeID` preserved for trace lineage |
| Stale entries (older than `MaxAge`) on read | Dropped silently |
| `ReadAndConsume()` after drain | File truncated to zero bytes (entries replay exactly once) |
| Corrupt or partial JSONL line | Skipped (read tolerates incomplete writes from a crashed appender) |

## Cross-process safety

The queue file is mutated by potentially multiple processes
simultaneously — a `cortex daemon` background loop and a foreground
`cortex code` invocation can both refuse spawns into the same
project's queue. The implementation uses POSIX advisory locking
(`syscall.Flock`) on the fd, matching the existing
`internal/journal/lock_unix.go` pattern. Windows is a no-op; the
queue tolerates a corrupted line from interleaved writes.

In-process safety uses `sync.Mutex` to prevent lock recursion and
serialize appends from the same goroutine pool (the parallel
executor batch may schedule multiple budget refusals concurrently).

## Why the policies

**Why 1-hour staleness?** A deferred spawn replaying a workday later
would surprise the user — context has moved on, the spawn's
preconditions are likely stale. 1 hour matches a working-session
expectation: a deferred spawn should still be relevant within the
same coding session. Configurable per-project via
`NewFileDeferredQueue(path, maxAge)`.

**Why JSONL not a SQLite table?** Cortex's existing `.cortex/db/`
artifacts (`cell_results.jsonl`, `dag_traces.jsonl`) are JSONL for
the same reasons: human-readable, `jq`-friendly, append-only, no
schema migration. The queue is small (deferred spawns are a tiny
fraction of all spawns) so the read+truncate-on-consume cycle stays
cheap.

**Why drop on consume, not on success?** Tracking per-replay
success would require a second cycle (read → execute → mark done) and
risks losing a spawn if the executor crashes between read and mark.
Consume-on-read means a crashing turn loses the deferred spawn —
the trade-off favors "never replay twice" over "always replay until
done." Real fault-tolerance would need a write-ahead log on top, not
worth it for V0.

**Why preserve `ParentNodeID` across turns?** Trace consumers (the
journal, the eval cell-results sink) need lineage. A deferred spawn
replaying with its original parent ID makes cross-turn analysis
possible: "this spawn was scheduled in turn A by n5 but executed in
turn B as `replayed-1`." Trace consumers should treat
`parent_node_id` as a soft pointer that may resolve to a node from
a different turn.

## What this does NOT change

- The `Exhausted` semantic: a turn whose budget is exhausted still
  stops spawning *within that turn*. Rollover only changes whether
  the refused spawn gets a second chance in a *later* turn.
- The pre-spawn `CanAfford` check still gates whether a spawn enters
  the pending set. Rollover happens at refusal time, so it's only
  triggered when `CanAfford` returns false.
- Handlers see no difference — they receive the same `(ctx, attrs,
  budget)` whether they're a replayed deferred spawn or a normal
  seed.

## What follows

ADR-007 (or a separate slice) may revisit:
- Per-spawn retry counts (cap replay attempts to prevent infinite
  carry-over)
- Cross-project queue isolation when `~/.cortex/` becomes a global
  fallback for multi-project setups
- Telemetry on the queue (how often spawns defer, how often they
  successfully replay, what reasons dominate)

## Verification

- `TestFileDeferredQueue_*` (5 tests) covers roundtrip, staleness,
  consume-on-read, missing-file, concurrent appends
- `TestExecutor_RolloverAppendsBudgetRefusals` proves the executor
  writes to the queue on budget refusal
- `TestExecutor_RolloverReplaysOnNextRun` proves fresh deferred
  spawns prepend to the next Run's seed with parent lineage
  preserved
- `go test -race ./pkg/cognition/dag/` clean
- All 5 mechanic evals still PASS (rollover is opt-in; mechanic
  runner doesn't wire a queue)
