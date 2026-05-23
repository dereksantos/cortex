# DAG Build — Stage 5: Additional DAG Types

> **Historical note:** This loop-prompt describes scheduling `think`
> and `dream` from a long-lived `cortex daemon`. The daemon was
> retired in May 2026 (see
> [../daemon-retirement-plan.md](../daemon-retirement-plan.md)); the
> REPL idle hook now hosts the scheduler that fires `cortex.MaybeThink`
> and `cortex.MaybeDream`. Daemon mentions below describe the
> architecture at the time of writing.

After Stage 4, `cortex run --type=turn` is fully production: parallel
execution, budget rollover, calibrated costs. Stage 5 brings the
other DAG types online: **think**, **dream**, **capture**, **eval**
each get a real body (currently they return
"not yet implemented" stubs from `runSuite`-style dispatchers).

After Stage 5: the runtime has the full 5-type DAG surface from
`docs/dag-protocol.md`. Background ops (think during active periods,
dream during idle) run on a schedule via the daemon; capture fires
from hooks; eval drives test scenarios.

See [`docs/dag-build-plan.md`](../dag-build-plan.md) Stage 5,
[`docs/dag-protocol.md`](../dag-protocol.md) "Per-DAG-type seeds
and initial budgets" table.

## Prerequisites (verify before starting)

```bash
git log --oneline -10
./bin/cortex eval --suite=mechanic              # 5/5 PASS
./bin/cortex run --type=turn --prompt "X"       # full parallel chain
./bin/cortex run --type=think                   # currently errors "not implemented"
./bin/cortex run --type=dream                   # currently errors "not implemented"
```

Stages 1-4 substantively complete.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build/Tests:** standard
- **Daemon:** `cortex daemon` is the scheduler for think + dream;
  this stage adds the DAG-runner hook there too

## Outcome (when this loop stops)

Four DAG types implemented end-to-end:

| Type | Seed | Trigger | Budget profile |
|---|---|---|---|
| `think` | `{think.session_check}` | Mid-session timer (daemon) OR `cortex run --type=think` | Decays with activity (inverse) |
| `dream` | `{maintain.idle_probe}` | Idle-time threshold (daemon) OR `cortex run --type=dream` | Grows with idle time |
| `capture` | `{sense.hook_event}` | Hook payload → `cortex run --type=capture --event=<json>` | Tiny — must not block |
| `eval` | `{sense.cli_invocation}` | `cortex run --type=eval --scenario=<path>` | Explicit `--max-ms` / `--max-tokens` |

Each type has:
- A seed op registered (e.g., `think.session_check`)
- A default DAG shape (template) the executor walks if no planner
  rewrites it
- A budget calculator (per `DefaultThinkBudget` etc. in `budget.go`,
  with the actual activity/idle inputs wired in)
- CLI invocation via `cortex run --type=<x>` working end-to-end
- Daemon scheduler hooks for `think` (timer) + `dream` (idle
  detector)

## Loop

Each iteration:

0. **Verify environment.** `pwd && git rev-parse --abbrev-ref HEAD`.
   Abort if mismatched.

1. **Read state** — `dag-build-plan.md` Stage 5, `docs/dag-protocol.md`
   "Per-DAG-type seeds" table, current `cmd/cortex/commands/run.go`
   (its switch has stubs for these types), existing daemon code
   under `internal/daemon/` (where the scheduler hooks land).

2. **Pick the next DAG type** in this order (easiest first):

   ### A. `eval` type (~30 min, no scheduler)

   Simplest. `cortex run --type=eval --scenario=<path>` loads a v2
   scenario YAML, builds a turn-shaped DAG with the scenario's
   prompt as seed input + scenario-specific budget (defaults from
   `--max-ms` / `--max-tokens` flags), executes through the same
   path as `--type=turn`. Emits per-cell `cell_results.jsonl` rows
   the existing analysis pipeline reads.

   This unifies `cortex eval` scenarios with the DAG runtime —
   evals run through the same executor as production turns.

   ### B. `capture` type (~1 hour, hook integration)

   Hook payloads (JSON) trigger `cortex run --type=capture`. The
   handler:
   - Parses the hook event
   - Constructs the `sense.hook_event` seed with event payload
     as attrs
   - Walks a fixed tiny DAG: `sense.hook_event → maintain.capture
     → maintain.extract_insight (conditional)`
   - 100ms latency budget; must not block

   Update the existing hook installation (`cortex install`) to wire
   hooks to `cortex run --type=capture` instead of `cortex capture`
   directly. Backwards-compat: `cortex capture` still works as a
   thin pass-through to `cortex run --type=capture`.

   ### C. `think` type (~1-2 hours, daemon scheduler)

   Background scheduled invocation. New daemon hook:
   - Every N seconds (default 30s, configurable), check activity
     level (mid-session = high; idle for >2 min = low)
   - High activity → skip (think runs on spare cycles)
   - Low activity → invoke `cortex run --type=think` in-process
   - Think DAG runs `think.session_check` → spawns
     `model.predict_next` + `attend.warm_cache` (warms Reflex for
     predicted queries) + `value.rerank_session` (updates session
     topic weights)

   Budget per `DefaultThinkBudget`, scaled by inverse activity.

   ### D. `dream` type (~1-2 hours, idle-time scheduler)

   Same daemon scheduler, idle-time triggered:
   - Daemon tracks last-activity timestamp
   - When idle > 5 min: invoke `cortex run --type=dream`
   - Dream DAG runs `maintain.idle_probe` → spawns
     `attend.sample` (random substrate samples) +
     `value.extract_insight` (looks for durable findings in samples)
     + `remember.embed_new` (catches up on unindexed content)

   Budget grows with idle time per `DefaultDreamBudget`. Stops on
   activity resume (signal channel from the daemon scheduler).

3. **Test after each type:**
   - `cortex run --type=<x>` runs end-to-end (CLI path)
   - For `think` + `dream`: daemon-scheduled invocation produces
     trace rows in `.cortex/db/dag_traces.jsonl`
   - For `capture`: a sample hook payload produces a capture turn
     with extracted insight (when applicable)
   - For `eval`: a known v2 scenario re-run produces equivalent
     numbers to the legacy `cortex eval` path

4. **Commit per type** (4 commits minimum). Do NOT push.

5. **Update docs:**
   - Check off Stage 5 items in `dag-build-plan.md`
   - `eval-journal.md` "Stage 5 complete" entry naming each type's
     first successful run
   - `dag-protocol.md` may need a small update if any DAG-type
     defaults shifted in practice

6. **Stop** when all 4 types work end-to-end via CLI AND (for think
   + dream) via daemon scheduler.

## Constraints

- **Capture must not block.** 100ms budget hard cap. If
  `maintain.extract_insight` would exceed it, skip (don't run the
  LLM call). Hook caller never waits on extraction.
- **Think during high activity is a no-op.** Inverse-budget model:
  high activity → near-zero budget → no spawn beyond the trivial
  seed check. Don't run heavy LLM calls when the user is mid-typing.
- **Dream stops on activity resume.** When the user comes back, dream
  must stop spawning new children and let in-flight finish. The
  daemon sends a stop signal; the executor honors it (already does
  via budget exhaustion semantics).
- **Eval-type DAGs use the same executor + telemetry as turn-type.**
  Don't fork the runtime. `cortex eval` analysis pipelines stay
  unchanged.
- **Daemon scheduler runs in-process** (calls `RunCommand.Execute`
  directly), not via subprocess. Avoids fork/exec overhead for
  background turns.
- **Don't push to remote.**

## Verification

Per type:
- **(A) eval:** `cortex run --type=eval --scenario=<X>` produces a
  cell_results row matching what `cortex eval -s <X>` produces
  today (within noise on judge-LLM-driven scores).
- **(B) capture:** Sample hook payload (`echo '{...}' | cortex run
  --type=capture`) produces 2-3 trace rows + 1 journal entry.
  Latency < 100ms.
- **(C) think:** Daemon scheduler-invoked think turn produces trace
  rows; topic-weight updates visible in subsequent turn's
  attend.reflex inputs.
- **(D) dream:** Idle-time scheduler-invoked dream turn produces
  trace rows; new embeddings appear in storage; insights extracted
  to journal.

Loop-wide stopping condition:
- ☐ All 4 types implemented + committed
- ☐ Each runs end-to-end via `cortex run --type=<x>` CLI
- ☐ Think + dream run via daemon scheduler with activity-aware
  budget
- ☐ Hook integration uses `cortex run --type=capture` (with
  `cortex capture` as backwards-compat shim)
- ☐ All 5 mechanic evals still PASS
- ☐ Existing eval suites (legacy-cognition + journeys + benchmarks)
  re-run within noise of Stage 4 baseline
- ☐ `docs/eval-journal.md` "Stage 5 complete" entry exists
- ☐ `docs/dag-build-plan.md` Stage 5 deliverables checked off

## When to ask the user

- If activity-level detection requires assumptions about how to
  measure "active vs idle" (typing, recent CLI invocations, hook
  events?) — surface the heuristic before baking it in.
- If the daemon scheduler interval defaults (think every 30s, dream
  every 5min idle) cause unexpected resource usage — propose
  alternatives.
- If `capture`'s 100ms budget proves too tight in practice (the
  micro-LLM extract_insight call may not fit) — surface; either
  raise the budget or skip extraction entirely under tight budget.
- If unifying `cortex eval` paths through `cortex run --type=eval`
  reveals semantic differences (parallel scenario runs, judge
  caching) that need careful handling.

## Reference index

| File | Why it matters |
|---|---|
| `docs/dag-build-plan.md` Stage 5 | Authoritative spec |
| `docs/dag-protocol.md` | Per-DAG-type seeds + initial budgets |
| `pkg/cognition/dag/budget.go` | Default<Type>Budget functions (already exist) |
| `cmd/cortex/commands/run.go` | switch on dagType — currently has stubs |
| `internal/daemon/` | Where scheduler hooks land for think + dream |
| `cmd/cortex/commands/capture.go` | Existing capture path; becomes thin pass-through |
| `cmd/cortex/commands/install.go` | Hook installation (rewires to capture-DAG) |
| `internal/eval/v2/` | Existing eval pipelines — `eval` DAG type plugs in here |
