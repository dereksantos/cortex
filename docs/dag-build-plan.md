# DAG Build Plan

> **Purpose.** Implementation plan for the DAG protocol from
> [`dag-protocol.md`](dag-protocol.md). Begins after
> [`archive/eval-prep-epic.md`](archive/eval-prep-epic.md) is complete — without the
> eval prep in place, verification is impossible.
>
> **Order.** [`archive/eval-prep-epic.md`](archive/eval-prep-epic.md) → this doc →
> implement.
>
> **Discipline.** **v0 first.** One DAG type, four ops, end-to-end,
> validated against the 5 mechanic evals from Phase C of the eval prep
> epic. Only then expand to the full registry.
>
> **Surface.** **CLI-first.** `cortex run --type=<x>` is the canonical
> entry point. Coding harness (`cortex code`, REPL) is the primary
> consumer and becomes a thin wrapper. Other DAG types
> (think / dream / capture / eval) are additional `cortex run`
> subcommands.
>
> **Related.** The project-bootstrap DAG is a composition built on top
> of this protocol — see [`bootstrap-dag-plan.md`](bootstrap-dag-plan.md)
> for its hierarchical sampler + extract-overview op + REPL first-run hook.

---

## Why v0 first

The DAG protocol is well-designed but unproven against real code.
Common pattern: elegant-on-paper designs have rough edges that surface
in week 1 of implementation. A thin v0 (1 week) validates the protocol
before committing the full architecture. If v0 reveals the design needs
adjustment, the rewrite is small.

---

## Prerequisites

| Prerequisite | Status check |
|---|---|
| Eval prep epic complete | All 6 phases of `archive/eval-prep-epic.md` checked |
| Phase 0 + 1 of integration-roadmap | Tool-surface foundation done; unified `cell_results.jsonl` for ad-hoc CLI invocations |
| Phase 2 of integration-roadmap | CLI surfaces (event stream, multi-turn driver, MCP flag, `--confirm`) |
| `cortex run` CLI scaffold | `cortex run --type=<x>` exists as a stub command in `cmd/cortex/commands/run.go` that prints "not implemented" |

If any prerequisite is missing, stop and land it first. Building
without these means measuring against ghosts.

---

## Stage 1 — v0: Minimum viable DAG (~1 week)

**Goal.** Smallest end-to-end DAG that works: one DAG type, four ops,
seed-and-grow executor, CLI entry point, telemetry. Validates the
protocol against reality.

### Scope

| Component | What |
|---|---|
| DAG type | `turn` only |
| Ops | `sense.prompt`, `attend.reflex`, `decide.inject`, `decide.coding_turn` |
| Budget | 3 axes (`latency_ms`, `tokens`, `depth`); turn defaults from `pkg/config` |
| Executor | Single-threaded; no parallelism; spawn-by-spawn |
| CLI | `cortex run --type=turn --prompt "<text>"` |
| Telemetry | `cell_results.jsonl` rows with `parent_node_id` |
| Loop integration | NOT YET — `cortex code` / REPL still use the legacy loop |

### Deliverables

1. **`pkg/cognition/registry.go`** — Registry interface; 4 ops
   registered with `NodeSpec` (`Function`, `Op`, `Description`,
   `Inputs`, `Outputs`, `AxisContract` for `act`, `Cost`, `MaxFanout`,
   `Handler`).
2. **`pkg/cognition/dag/budget.go`** — `Budget` type, decay logic,
   per-type seeds + initial budgets loaded from `pkg/config`.
3. **`pkg/cognition/dag/executor.go`** — Single-threaded executor;
   spawn scheduling via a `pending` deque; depth cap; exhaustion
   handling; per-node `cell_results.jsonl` writes with
   `parent_node_id`.
4. **`pkg/cognition/dag/spawn.go`** — Spawn-spec serialization (terse
   line-per-node form + JSON canonical).
5. **`cmd/cortex/commands/run.go`** — `cortex run --type=turn --prompt
   "<text>"` entry point. Routes to the executor with the turn-DAG
   seed.
6. **`decide.coding_turn` handler** — Wraps the existing LLM agent
   loop. For v0: does NOT spawn `act.*` children (just runs the inner
   loop and returns the final response + `CostConsumed`). The
   spawning-children variant lands in Stage 3 after ADR-001 settles.

### Test gates (must pass before v0 is "done")

- ☑ All 5 mechanic evals from `archive/eval-prep-epic.md` Phase C pass green
  (`cortex eval --suite=mechanic` → 5/5 PASS as of 2026-05-17, commit
  `4abbf96`).
- ☑ `cortex run --type=turn --prompt "..."` returns a sensible response
  (v0 stub chain — real LLM response lands when Stage 3 wires
  decide.coding_turn to the agent loop).
- ☑ Produces 4 trace rows with correct parent pointers (seed → reflex
  → inject → capture). Currently in `.cortex/db/dag_traces.jsonl`;
  unification with `cell_results.jsonl` deliberately deferred (separate
  sinks is the defensible architecture per the unified-vs-separate
  discussion in eval-baseline.md).
- ☑ Budget decay observable in rows; `BudgetAfter` per node matches
  `Initial - sum(CostConsumed)`.
- ☑ Determinism — mocked-handler tests in `executor_test.go` re-run
  byte-identical; cortex run timestamps differ but counts + structure
  are stable.

**Stage 1 v0 substantially done.** Remaining work for "fully done":
- `decide.coding_turn` handler wrapping the existing LLM agent loop
  (per ADR-001 V0 plan: inline, no spawn). This is the largest
  remaining piece — ~1-2h for inline form, ~3-4h for Stage 3's
  spawn-children form.
- Real `pkg/config` load for per-DAG-type budget overrides (currently
  hardcoded defaults).

### Week-1 questions (decisions land as ADRs)

| ID | Question | Decision destination |
|---|---|---|
| ADR-001 | Does `decide.coding_turn` spawn `act.*` children, or run inline with the LLM's classical tool-calling? | `docs/adrs/0001-coding-turn-structure.md` |
| ADR-002 | How does budget pass through to LLM tool calls inside `coding_turn`? | `docs/adrs/0002-budget-passthrough.md` |
| ADR-003 | What's the first-turn bootstrap when the journal is empty? | `docs/adrs/0003-cold-start.md` |
| (config) | Cost defaults per op (initial guesses) | Captured in `pkg/config` |
| (config) | MaxFanout defaults per op | Captured in op registration |

Author each ADR as the question gets answered, not before. Stub ADR
files with "decision pending" if useful.

### Out of scope for v0

Parallelism. Other DAG types. Loop rewrite. Cross-turn budget rollover.
Cost-hint self-calibration. Restored `legacy/cognition/` runner (that
lands in Stage 2 once the full registry exists).

---

## Stage 2 — Expand registry to per-node corpus (~1-2 weeks)

**Goal.** Register all ops corresponding to scenarios in
`legacy/cognition/`. Each scenario becomes a per-op acceptance test.

### Scope

| Component | What |
|---|---|
| New ops | `attend.rerank`, `value.score`, `value.detect_contradiction`, `decide.should_capture`, `model.predict_next`, `maintain.extract_insight`, `maintain.capture`, `remember.vector_search`, `represent.embed` |
| Per-op prompt templates | Each micro-LLM op gets a versioned, narrow prompt under `pkg/cognition/prompts/<function>_<op>.tmpl` |
| Cost defaults | Calibrated against v0 telemetry |

### Deliverables

1. ☑ 9 new registered ops with handlers + prompts (under
   `pkg/cognition/dag/ops/`):
   - mechanical: `represent.embed`, `remember.vector_search`
   - LLM-backed: `attend.rerank`, `value.score`,
     `value.detect_contradiction`, `decide.inject`,
     `decide.should_capture`, `model.predict_next`,
     `maintain.extract_insight`
2. ☑ `pkg/cognition/prompts/` directory with versioned `.tmpl`
   prompts per LLM op, bundled via `embed.FS`. Format and
   versioning per [ADR-004](adrs/0004-prompt-templates.md).
3. ☑ Cost hints declared per op (per `dag.NodeSpec.Cost`) with
   headroom over Haiku 4.5 measurement. Re-calibration from
   `cell_results.jsonl` after first real-key run is a Stage-3
   follow-up (not Stage 2 — telemetry pipeline is in place but
   the calibration loop hasn't fired yet).
4. ☑ Default chain in `cortex run --type=turn` walks 8 nodes using
   real ops:
   `sense.prompt → represent.embed → remember.vector_search
    → attend.rerank → decide.inject → decide.coding_turn
    → maintain.extract_insight → maintain.capture`.
   Centralized registration via `ops.RegisterDefaults(reg, cfg)`.

### Test gates

- ☑ All 9 Stage-2 ops registered with handlers + versioned prompts
  + ≥6 unit-test scenarios each (parse path, sanitize path, fallback
  paths, schema-coercion paths, missing-input rejection,
  registration). Total: ~60 unit tests across
  `pkg/cognition/dag/ops/`. **Note:** the legacy-cognition runner
  still dispatches to `internal/cognition.Reflect/Reflex/Resolve`,
  not to the new ops — wiring it to the new ops is Stage 3's "Loop
  rewrite" follow-up. The new ops' acceptance comes from unit-test
  coverage; cross-suite acceptance lands once the runner is rewired.
- ☑ Cost defaults declared per op as `dag.NodeSpec.Cost` with
  headroom over measured p50 on Anthropic Haiku 4.5 (calibration
  notes in each op file). Recalibration from `cell_results.jsonl`
  is a follow-up after the first real-key run lands traces.
- ☑ Every LLM op has a deterministic mechanical fallback that
  fires on: budget < 200ms remaining, no provider configured, LLM
  call error, JSON parse failure, template load failure. Fallback
  path exposed via `Out["fallback"]: bool` for eval differentiation.

### ADRs landed

| ID | Question | Destination | Status |
|---|---|---|---|
| ADR-004 | Per-op prompt template format and versioning | [`docs/adrs/0004-prompt-templates.md`](adrs/0004-prompt-templates.md) | ☑ Landed |

---

## Stage 3 — Loop rewrite (~1 week)

**Goal.** `cortex code` and the REPL become thin wrappers around
`cortex run --type=turn`. The 5 existing tools become registered `act`
ops. `decide.coding_turn` spawns `act.*` children per ADR-001.

### Scope

| Component | What |
|---|---|
| Loop rewrite | `internal/harness/loop.go` — `seed → walk → finalize` (calls executor in-process) |
| Tool registration | `list_dir`, `read_file`, `write_file`, `run_shell`, `cortex_search` → registered as `act` ops |
| `coding_turn` revisited | Spawns `act.*` children for tool calls (per ADR-001) |
| REPL adaptation | `cmd/cortex/commands/repl.go` loops over `cortex run --type=turn`; preserves transcript |
| `cortex code` adaptation | Wraps `cortex run --type=turn --one-shot` |

### Test gates

- ☑ act-op adapter (Deliverable A): `internal/harness/dagnode/act_ops.go`
  wraps any `harness.ToolHandler` as a `dag.NodeSpec` with axis-5
  enforcement; `DefaultActOpContracts()` declares the canonical
  contracts for the 5 existing tools; 7 unit tests cover the adapter.
- ☑ coding_turn dispatcher (Deliverable B): `CodingTurnConfig` grows
  `ActRegistry` + `TraceCB`; when set, the handler installs a
  `harness.ToolDispatcher` on the `CortexHarness` that routes each
  tool call through `act.<name>` and emits one `dag.TraceEntry` per
  call with `parent_node_id` = this node's ID (surfaced via the new
  `dag.NodeIDFromContext` helper). 6 unit tests cover hit/miss/
  normalization/auto-confirm/multi-call/trace-shape.
- ☑ cortex code + REPL opt-in (Deliverables C+D, **partial**):
  `--dag` flag on both commands opts into the act-op dispatcher
  with a synthetic parent ID; per-tool rows land in
  `dag_traces.jsonl`. Default behavior unchanged.
- ☐ Full thin-wrapper rewrite of cortex code + REPL (the larger
  Deliverables C+D vision) is **deferred** — would require
  reworking ~2,600 LOC of CLI surface in a single landing; deferred
  to keep the structural Stage 3 piece (dispatcher + act ops) on
  one branch and reduce CLI-regression risk. Follow-up.
- ☐ Existing journey scenarios from eval-prep Phase D pass under the
  new loop — pending until the thin-wrapper rewrite lands; the opt-in
  `--dag` path doesn't materially change the agent loop.
- ☐ No regression on the baseline numbers from `eval-baseline.md`
  (within noise envelope captured in Phase A) — pending real-LLM
  runs with `--dag` enabled.

---

## Stage 4 — Parallelism + budget refinement (~1 week)

**Goal.** Independent sibling nodes run in parallel; budget
pass-through is honest; cross-turn budget rollover lands.

### Scope

| Component | What |
|---|---|
| Parallel executor | Goroutine-per-independent-sibling; budget contention via shared atomic |
| Budget pass-through | When parent spawns multiple children, budget splits proportionally to `cost_hint` |
| Cross-turn rollover | Spawns deferred for budget enter next turn's seed (per ADR-006) |
| Cost self-calibration (basic) | Per-op rolling average from `cell_results.jsonl` updates `cost_hint` |

### Test gates

- ☐ Mechanic-5 (tree-shape variation) shows parallel branches in the
  trace.
- ☐ Tree-depth distribution across a journey suite varies (doesn't
  max-out every turn, doesn't always stay at depth 3).
- ☐ A previously-deferred spawn shows up in the next turn's trace
  with a marker indicating it was rolled over.

### ADRs likely to land here

| ID | Question | Destination |
|---|---|---|
| ADR-005 | Parallel sibling budget contention strategy | `docs/adrs/0005-parallel-budget.md` |
| ADR-006 | Cross-turn budget rollover semantics | `docs/adrs/0006-budget-rollover.md` |

---

## Stage 5 — Additional DAG types (~1 week)

**Goal.** `think`, `dream`, `capture`, `eval` DAG types become invokable
via `cortex run --type=<x>`.

### Scope

| Type | Seed | Trigger surface |
|---|---|---|
| `think` | `{think.session_check}` | REPL idle hook (was: daemon, retired May 2026 — see [daemon-retirement-plan.md](./daemon-retirement-plan.md)) or `cortex run --type=think` |
| `dream` | `{maintain.idle_probe}` | Idle-time scheduled or `cortex run --type=dream` |
| `capture` | `{sense.hook_event}` | Hook handoff → `cortex run --type=capture --event=...` |
| `eval` | `{sense.cli_invocation}` | `cortex run --type=eval --scenario=<path>` |

### Test gates

- ☐ Each DAG type runs end-to-end via CLI.
- ☐ Per-DAG-type initial budget defaults respected (turn tight,
  dream large, etc.).
- ☐ `cortex eval --suite=journeys` invokes `cortex run --type=eval`
  per scenario (CLI-first verified).
- ☐ Hook integration: at least one Claude Code hook fires
  `cortex run --type=capture` successfully.

---

## Stage 6 — Full eval suite review (integration-roadmap Phase 6)

Per [`integration-roadmap.md`](integration-roadmap.md) Phase 6: run
full suite against integrated architecture, comparative delta report
in `eval-journal.md`, regression triage, update `ROADMAP.md` ABR
target.

This is the acceptance test for the entire build. Concrete acceptance:
the comparative delta report exists, every dimension is scored, and
each regression has a triage decision (noise / real / expected
trade-off).

---

## ADR table (lives in `docs/adrs/`)

| ID | Subject | Authored in stage |
|---|---|---|
| ADR-001 | `decide.coding_turn` internal structure | Stage 1 |
| ADR-002 | Budget pass-through to LLM tool calls | Stage 1 |
| ADR-003 | First-turn bootstrap with empty journal | Stage 1 |
| ADR-004 | Per-op prompt template format and versioning | Stage 2 |
| ADR-005 | Parallel sibling budget contention strategy | Stage 4 |
| ADR-006 | Cross-turn budget rollover semantics | Stage 4 |

Add new ADRs as questions emerge during the build. The ADR list is
not closed.

---

## Rough time estimate

These are **rough**; treat as planning aid, not commitment.

| Stage | Focused effort | Cumulative |
|---|---|---|
| Eval prep epic | 1-2 weeks | 1-2 weeks |
| 1 — v0 | 1 week | 2-3 weeks |
| 2 — Registry expansion | 1-2 weeks | 3-5 weeks |
| 3 — Loop rewrite | 1 week | 4-6 weeks |
| 4 — Parallelism + budget | 1 week | 5-7 weeks |
| 5 — Additional DAG types | 1 week | 6-8 weeks |
| 6 — Full eval suite review | 1 week | 7-9 weeks |

Total: 7-9 weeks of focused work. With interruptions, design pivots,
and ADR debates, double that is realistic.

---

## CLI-first discipline (the recurring constraint)

Every stage's deliverables must be reachable via `cortex` CLI. No
library-only features, no harness-only paths. The reason: eval-principles
9 (CLI-first gap-closing) means evals call the same surface humans
call. Library-only APIs create asymmetric instrumentation and force
parallel implementations.

Concretely:
- Stage 1: `cortex run --type=turn` works
- Stage 3: `cortex code` and the REPL go through that same `cortex run`
- Stage 5: each DAG type is `cortex run --type=<x>`
- Phase 6 eval review: every scenario invokes via CLI

If a feature has no CLI surface, it's not in this build plan.

---

## Cross-references

| Doc | Relationship |
|---|---|
| [`archive/eval-prep-epic.md`](archive/eval-prep-epic.md) | Must complete before this; provides the 5 mechanic evals + baseline |
| [`dag-protocol.md`](dag-protocol.md) | The protocol this builds |
| [`integration-roadmap.md`](integration-roadmap.md) | Phase 5 + Phase 6 framing |
| [`tool-surface.md`](tool-surface.md) | Phase 1 prerequisites for axis contracts on `act` ops |
| [`prompts/eval-principles.md`](prompts/eval-principles.md) | The 9 principles every stage's test gates honor |

---

*This is a living build plan. Update stage status (☐ → ☑) as work
lands. Add ADRs as decisions accumulate.*
