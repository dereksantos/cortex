# DAG Build — Stage 4: Parallelism + Budget Rollover

After Stage 3, every coding turn runs through the executor and tool
calls are first-class trace nodes. Stage 4 makes the executor
**parallel** for independent siblings, adds **cross-turn budget
rollover** for deferred spawns, and adds **cost-hint self-calibration**
so registered ops' `Cost` values stay accurate as real usage shifts.

After Stage 4: a turn whose `attend.reflex` spawns 3 candidate-batch
`attend.rerank` children executes them in parallel under shared budget.
A `maintain.capture` deferred for budget in turn N appears in turn
N+1's seed. Ops whose actual costs drift get their hints updated
automatically.

See [`docs/dag-build-plan.md`](../dag-build-plan.md) Stage 4 +
[`docs/dag-protocol.md`](../dag-protocol.md) "Spawning" / "Budget
model" sections.

## Prerequisites (verify before starting)

```bash
git log --oneline -10
./bin/cortex eval --suite=mechanic              # 5/5 PASS
./bin/cortex run --type=turn --prompt "X" -v    # multi-node tree
./bin/cortex code --help                        # Stage 3 rewrite live
```

Stage 3 substantively complete — cortex code goes through the
executor; coding_turn spawns act children.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build/Tests:** standard
- **Concurrency note:** Stage 4 introduces goroutines into the
  executor — race-detector runs (`go test -race ./...`) become part
  of the gate

## Outcome (when this loop stops)

Three deliverables landed:

1. **Parallel sibling execution.** The pending-set walker becomes
   concurrent: independent siblings (no edge between them) execute
   in parallel goroutines. Shared budget mutex; cost atomically
   consumed. Per-node trace rows still emit individually with
   accurate ordering (by wall_start_unix, not insertion order).

2. **Cross-turn budget rollover.** When a spawn is refused for
   budget mid-turn, it's recorded in a per-project deferred-spawn
   queue. The next turn's seed includes deferred spawns from the
   prior turn, prepended before the user's sense.prompt. ADR-006
   captures the semantics.

3. **Per-op cost-hint self-calibration.** A background process (or
   eval-time analysis) reads `.cortex/db/dag_traces.jsonl` rolling
   window, computes per-op p50 latency + p50 tokens, updates the
   registered op's `NodeSpec.Cost`. Lives in `pkg/cognition/dag/
   calibrate.go` — invoked at executor construction time or on a
   schedule.

## Loop

Each iteration:

0. **Verify environment.** `pwd && git rev-parse --abbrev-ref HEAD`.
   Abort if mismatched.

1. **Read state** — `dag-build-plan.md` Stage 4,
   `pkg/cognition/dag/executor.go` (single-threaded baseline),
   `pkg/cognition/dag/budget.go`, the 5 mechanic eval fixtures.

2. **Pick the next deliverable** in this order:

   ### A. Parallel sibling execution

   Refactor `executor.go`'s pending-set loop:
   - When dequeueing, identify all currently-pending nodes whose
     dependencies are satisfied (no upstream node still pending or
     in-flight that they `$node.out`-reference)
   - Launch them in parallel goroutines
   - Shared `Budget` protected by sync.Mutex (or atomic Int64s per
     axis)
   - Per-goroutine: invoke handler → on return, mutex.Lock + apply
     CostConsumed + check Exhausted + schedule spawn children +
     mutex.Unlock
   - Wait for all in-flight goroutines before checking exhaustion
     for next iteration (so no race on the budget read)
   - Trace entries written under the mutex, ordered by wall_start

   **Race-detector test:** `go test -race ./pkg/cognition/dag/` must
   pass cleanly.

   **Mechanic-5 (tree-shape variation)** should now show parallel
   branches in the trace (multiple nodes with similar wall_start
   times, distinct wall_end).

   ### B. Cross-turn budget rollover (ADR-006)

   New per-project queue file: `.cortex/db/deferred_spawns.jsonl`.
   - On budget_exceeded spawn refusal, the refusal is also written
     here as a deferred-spawn record (parent_node_id from prior
     turn, child NodeSpec, reason).
   - On next turn seed construction, the file is read; any deferred
     spawns are prepended to the seed set with their original
     parent_node_id preserved (for trace continuity).
   - On successful deferred-spawn execution, the record is removed
     from the queue (or marked complete).

   **ADR-006** lands here documenting:
   - Queue file format
   - Replay-vs-discard policy for stale deferrals (>1 hour?)
   - Cross-project isolation (per-project file path)

   ### C. Per-op cost-hint self-calibration

   `pkg/cognition/dag/calibrate.go`:
   - Reads `.cortex/db/dag_traces.jsonl` last N runs (default 100)
   - Groups by `qualified_name`, computes p50 of `cost_latency_ms`
     and `cost_tokens`
   - Updates the in-memory Registry's NodeSpec.Cost for each op
   - Persists current calibration to `.cortex/db/op_cost_hints.json`
     so next process startup loads from disk instead of recomputing
   - Invoked on Executor construction (cheap; in-memory read)

   Mechanic evals MUST stay PASS — they use mocked handlers, so the
   calibration shouldn't disturb them (mocked handlers' costs are
   in the fixture, not in the registry's Cost field).

3. **Test after each:**
   - `go test ./...` — green
   - `go test -race ./pkg/cognition/dag/` — green (Stage 4-A
     specifically requires this)
   - Verify mechanic-5 trace shows parallel branches
   - Verify a deferred spawn (engineered via tight budget) appears
     in next turn's trace

4. **Commit per deliverable** (3 commits minimum). Do NOT push.

5. **Update docs:**
   - Check off Stage 4 items in `dag-build-plan.md`
   - ADR-005 (parallel sibling budget contention) + ADR-006
     (cross-turn rollover) authored under `docs/adrs/`
   - `docs/eval-journal.md` "Stage 4 complete" entry

6. **Stop** when all 3 deliverables landed + race-detector clean +
   no mechanic regressions.

## Constraints

- **Race-free.** `go test -race ./...` must stay green. Concurrent
  budget access is the highest-risk surface; design accordingly.
- **Trace ordering by wall_start.** When parallel siblings emit
  rows, the trace order in JSONL is by wall_start_unix_ns. Don't
  rely on insertion order for parent reconstruction.
- **Backwards-compatible.** Single-threaded fall-through must still
  work (a config flag or empty parallel pool size = sequential).
  Useful for debugging + the mechanic eval runs that prefer
  determinism.
- **Cross-turn rollover must be opt-in OR have a clear staleness
  cap.** A 24-hour-old deferred spawn replaying surprisingly is a
  bug. Cap at ~1 hour by default; configurable.
- **Cost calibration must be auditable** — calibration source data
  (which traces, which window) recorded alongside the calibrated
  costs.
- **Don't push to remote.**

## Verification

Per deliverable:
- **(A)** `go test -race ./pkg/cognition/dag/` green; mechanic-5
  trace shows parallel branches (multiple nodes' wall_start within
  ~10ms of each other); per-turn wall time < sum(per-node wall
  time) when parallelism applies.
- **(B)** Engineered tight-budget run produces a deferred spawn in
  `.cortex/db/deferred_spawns.jsonl`; next turn's trace shows that
  spawn executed with parent_node_id preserved.
- **(C)** `op_cost_hints.json` exists with per-op p50 values;
  Executor construction reads it; mechanic evals still PASS.

Loop-wide stopping condition:
- ☐ All 3 deliverables committed
- ☐ ADR-005 + ADR-006 authored
- ☐ Race-detector clean across the codebase
- ☐ All 5 mechanic evals still PASS
- ☐ A previously-engineered "high fan-out" prompt produces parallel
  trace
- ☐ `docs/eval-journal.md` "Stage 4 complete" entry exists

## When to ask the user

- If race-detector reveals contention that's hard to resolve without
  a structural redesign of the executor — surface before going
  deep on lock optimization.
- If the cross-turn rollover staleness cap should be longer/shorter
  for specific workloads.
- If cost calibration shows wild swings (e.g., an op's cost varies
  10× between runs) — surface; might indicate a real bug rather
  than calibration data.
- If parallel execution surfaces ordering-dependent bugs in handlers
  (handlers should be order-independent; if any aren't, that's a
  bug worth fixing rather than working around).

## Reference index

| File | Why it matters |
|---|---|
| `docs/dag-build-plan.md` Stage 4 | Authoritative spec |
| `pkg/cognition/dag/executor.go` | The walker to make concurrent |
| `pkg/cognition/dag/budget.go` | Budget type → needs atomic/mutex |
| `pkg/cognition/dag/executor_test.go` | Existing tests; add parallel + rollover variants |
| `test/evals/mechanic/mechanic-5-tree-shape-variation.yaml` | Where parallel branches should surface in trace |
| `internal/eval/dagtrace/writer.go` | Trace JSONL writer — needs concurrent-safe (already mutex-guarded) |
| `.cortex/db/dag_traces.jsonl` | Source for cost calibration |
