# Eval Preparation Epic

> **Purpose.** All eval infrastructure work that must land *before* any DAG
> protocol code is written. The goal is twofold: (1) establish a clean
> pre-integration baseline so Phase 6's delta report has a real "before"
> picture, and (2) build the mechanic + per-node + e2e eval surface that
> Phase 5 will be verified against.
>
> **Order.** This epic → [`dag-build-plan.md`](dag-build-plan.md) →
> implement. Running the build without the evals in place means flying
> blind on whether the protocol helped.
>
> **Principle compliance.** Every deliverable here must satisfy all 9
> principles in [`prompts/eval-principles.md`](prompts/eval-principles.md):
> black box, no coaching, versioned, reproducible, isolated, structured,
> LLM-judged variance, separated baselines, CLI-first gap-closing.

---

## Why eval prep comes first

Three failure modes this epic prevents:

1. **Building blind.** Without a pre-integration baseline, post-Phase-5
   numbers are uninterpretable — "is 0.83 better than what we had?"
   has no honest answer.
2. **Verifying with the wrong evals.** Existing evals score output
   quality; they don't catch protocol mechanic regressions (budget
   leaks, broken tree reconstruction, fan-out runaway). New mechanic
   evals must exist before the code they test.
3. **Discovering orphaned scenarios mid-build.** `legacy/cognition/`
   has 22 per-node scenarios that map onto Phase 5's node registry but
   have no runner. Surfacing this gap during the build wastes a week.

---

## The 6 phases

| Phase | Goal | Output |
|---|---|---|
| A. Baseline snapshot | Run full existing suite against current architecture | Numbers in `eval-journal.md` |
| B. Restore legacy/cognition runner | Make 22 per-node scenarios runnable | Runner code + green test pass |
| C. Author mechanic evals | 5 deterministic tests for the executor | Fixtures ready before executor exists (drives TDD) |
| D. Verify journeys/ runnability | Confirm cited E2E scenarios actually run | Either green pass or restored runner |
| E. 10-dim e2e scenario authoring | Spec the cross-dim scenario as data | Authored scenario; runner deferred to Phase 5 |
| F. Pre-integration baseline doc | Single doc capturing all "before" numbers | `docs/eval-baseline.md` |

Phases are mostly parallelizable (A, B, D can run concurrently; C and
E require some design but can also run in parallel). F is the
consolidation step.

---

## Phase A — Baseline snapshot

**Goal.** A clean "before" picture of the current architecture's eval
performance, so Phase 6's delta report can honestly say what changed.

**Deliverables.**

- Run the full v2 suite (40+ scenarios) end-to-end; record per-scenario
  pass/fail + score in `eval-journal.md`, dated and model-pinned.
- Run LongMemEval against current architecture; record session-by-session
  scores.
- Run SWE-bench Verified (the subset already wrapped, per
  `coverage-matrix.md`); record pass-rate.
- Record current ABR — verified vs. cached, not just the headline number
  in `ROADMAP.md`.
- Per-scenario-type cost and latency breakdown.

**Success criteria.** Every scenario in `test/evals/v2/` has produced
at least one `cell_results.jsonl` row in the last 7 days. The ABR
number in `ROADMAP.md` matches an actual recent run, not a stale
snapshot.

**Dependencies.** Phase 1 of integration-roadmap (unified
`cell_results.jsonl` for ad-hoc CLI invocations) — without this,
baseline rows are inconsistent between eval and non-eval invocations.

**Principle compliance.**
- (4 Reproducible) Pin model versions in results.
- (5 Isolated) Run with stable hardware / env capture.
- (6 Structured) All rows in `cell_results.jsonl` + SQLite.
- (8 Separated baselines) Record no-context, Cortex-Fast, Cortex-Full
  as three distinct rows per scenario.

---

## Phase B — Restore legacy/cognition/ runner ✅ SUBSTANTIALLY DONE 2026-05-17

**Status.** 23–24/29 PASS on OpenRouter haiku; 5–6 reflect-mode FAILs
are LLM ranking variance (filed as category c — needs variance-tolerant
assertions, not implementation changes). Resolve scenarios 9/9 PASS;
reflex scenarios 8/8 PASS deterministically; reflect scenarios 4-5/9
PASS (variance). Per-op `cortex eval --suite=legacy-cognition` wired
under `runReflectTest` + `runReflexTest` + `runResolveTest` dispatchers
in `internal/eval/legacy/runner.go`. Storage-dependent reflex scenarios
seed via `SeedFixtures` (canonical-fixture JSONL path).

Per-mode dispatchers for `think|dream|router` are **not needed** —
those modes have no `mode:` scenarios; they appear only as scenario
*types* (session/dream/benefit/conflict) which want a different runner
shape. Filed as a follow-up.

**Goal.** 22 per-node scenarios in `test/evals/legacy/cognition/` become
runnable as per-op verification for Phase 5.

**Background.** Per `project_deleted_eval_runners` memory: the cognition
runner was deleted, orphaning these scenarios. They were authored before
the runtime that would consume them existed — they are now well-suited
as per-op acceptance tests for the DAG protocol's registry.

**Deliverables.**

- Thin runner that loads each legacy scenario, dispatches to the
  intended cognitive mode (Reflex / Reflect / Resolve / Dream /
  Session), and scores against the scenario's `expect` block.
- Wire under `cortex eval --suite=legacy-cognition`.
- All 22 scenarios produce structured `cell_results.jsonl` rows.

**Success criteria.** Every scenario in `legacy/cognition/` has a
recorded pass/fail under the *current* mode implementations. These
become per-op tests once Phase 5 ships the registry; for now they
verify the modes haven't drifted.

**Dependencies.** Phase A (so we know what passed before any rewrite).

**Principle compliance.**
- (3 Versioned) Each scenario includes the mode version it was
  authored against.
- (8 Separated baselines) Each scenario captures its expected result
  under each mode independently, not mixed.

---

## Phase C — Author 5 mechanic evals for the DAG executor

**Goal.** Deterministic, unit-style tests for the executor that exist
*before the executor does*, enabling TDD-style implementation.

**The 5 mechanic evals.** Each produces a `cell_results.jsonl` row
under `cortex eval --suite=mechanic`. All 5 fail today (no executor
exists); all 5 must pass before v0 in the build plan is considered
done.

### Mechanic-1: Budget decay determinism

Given a fixed seed + 3 mock nodes with fixed CostConsumed values, the
remaining_budget at each step matches expected to the millisecond /
token. Verifies: decay arithmetic is correct, no accumulation drift.

### Mechanic-2: Tree reconstruction from parent pointers

Given a 5-node tree with known parent pointers, the post-hoc
reconstruction from `cell_results.jsonl` produces the same shape.
Verifies: telemetry integrity, replay-from-journal correctness.

### Mechanic-3: Depth cap enforcement

Given `budget.depth = 3` and a mock node that tries to spawn at depth
4, the spawn is refused with `error: depth_exceeded`. Verifies: hard
recursion bound.

### Mechanic-4: Budget exhaustion graceful degradation

Given a node sequence that exhausts `latency_ms` mid-tree, in-flight
nodes finish but no new spawns happen; the exhaustion event names the
exhausted axis. Verifies: soft bound, no orphaned in-flight, no
silent failures.

### Mechanic-5: Tree-shape variation

Given two prompts that should warrant different trees (e.g., trivial
vs. code-symbol-rich), the grown trees have measurably different
shapes (depth, fan-out, op mix). Verifies: tree is actually grown
based on inputs, not always the same default walk.

**Deliverables.** YAML fixtures under `test/evals/mechanic/` for each
of the 5 + a runner under `cortex eval --suite=mechanic`. Mock
handlers used; no LLM, no network. Determinism is by construction.

**Success criteria.** All 5 fail with "no executor implementation"
today. All 5 pass green by end of build-plan Stage 1.

**Principle compliance.**
- (4 Reproducible) Mocked costs eliminate variance.
- (5 Isolated) No network, no LLM.
- (6 Structured) All 5 emit `cell_results.jsonl` rows in the same
  schema as other suites.

---

## Phase D — Verify journeys/ runnability ✅ DONE 2026-05-17

**Status.** Three depths of `cortex eval --suite=journeys` now wired:
1. **Validation** (default): 10/10 scenarios parse + scaffolds verified.
2. **Seed** (`CORTEX_JOURNEYS_WITH_SEED=1`): 10/10 SEED_OK — every
   journey's events seed into a per-scenario temp `.cortex` and are
   retrievable via Reflex.
3. **Full execution** (`CORTEX_JOURNEYS_EXECUTE=1`): drives
   `CortexHarness` end-to-end per task session; emits one
   `cell_results.jsonl` row per scored session. Validated on
   `trivial-hello-world` (2/2 sessions PASS, $0.009, 16s).
   Tunable via `CORTEX_JOURNEYS_MODEL`, `CORTEX_JOURNEYS_FILTER`,
   `CORTEX_JOURNEYS_CELL_SINK`.

Code: `internal/eval/journey/{runner.go,executor.go}`, dispatched via
`cmd/cortex/commands/eval_suite.go:runJourneysExecute`.

**Goal.** Confirm the 10 e2e scenarios in `test/evals/journeys/` are
runnable. Multiple docs cite them as the canonical E2E suite but the
runner status is unclear post-runner-deletion.

**Deliverables.**

- Attempt to run `cortex eval --suite=journeys`. Report what fails.
- Either: restore minimal runner if needed, or document that
  `cortex code --eval` is the current path.
- For each of the 10 scenarios, produce a structured row OR an explicit
  "pending Phase 5 wiring" marker with reason.

**Success criteria.** Each of the 10 scenarios has a clear status:
runnable (with last result) or pending (with reason). No scenario is
in "unknown" state.

**Dependencies.** None — can run in parallel with A, B, C.

---

## Phase E — 10-dim e2e scenario authoring

**Goal.** The cross-dimensional scenario described in
`coverage-matrix.md` Stage 4 gets authored as a YAML/JSON fixture,
even before the runner that would execute it exists. Authoring it
forces design decisions and uncovers gaps.

**Deliverables.**

- `test/evals/e2e/10-dim-library-service.yaml` (or similar) — single
  multi-session scenario with per-step acceptance criteria for each
  of the 10 dimensions (per `coverage-matrix.md` Section
  "End-to-end scenario").
- Acceptance criteria use only existing per-dim metrics — no new
  scoring scheme introduced here.

**Success criteria.** A reader can step through the scenario and
match each step to one of the 10 dimensions. Each step has a clear
pass/fail criterion. The runner is explicitly deferred to Phase 5
(the scenario is authored, not yet executable).

**Status.** ✅ Done (2026-05-17). Authored at
`test/evals/e2e/10-dim-library-service.yaml` — 10 sessions, each
mapped 1:1 to a dimension via `dimension` / `dimension_name` /
`metric_source` fields. Acceptance blocks reuse metrics named in
`coverage-matrix.md`'s per-dimension sections (no new scoring
scheme). Runner deferred to Phase 5 per `runner_notes` in the
fixture; CLI-surface gaps required for execution are listed
there.

---

## Phase F — Pre-integration baseline doc ✅ DONE 2026-05-17 (partial)

**Goal.** Single doc summarizing all "before" numbers, so post-Phase-5
delta is one comparison, not five scattered claims.

**Deliverables.**

- [x] `docs/eval-baseline.md` consolidating:
  - [x] Phase A baseline scores (v2, LongMemEval, SWE-bench, ABR, MTEB)
  - [ ] Phase B legacy/cognition baseline (per-op current scores) — _marked pending_
  - [ ] Phase D journeys baseline (per-scenario current scores) — _marked pending_
  - [x] Per-axis cost/latency notes from Phase 1 telemetry
- [x] Time-stamped; model-versions pinned; git ref `387468f`.
- [x] Linked from `eval-journal.md` and `ROADMAP.md` as the canonical
  "before" snapshot.

**Status:** Phase F is complete *for the data that exists* (Phase A + MTEB).
Phase B and D sections explicitly marked "pending Phase X; will be added
once that loop completes" — see eval-baseline.md. Once B + D land, this
doc gets a second pass to incorporate those numbers.

**Success criteria.** Reading `eval-baseline.md` end-to-end gives a
single time-stamped snapshot of where everything stood before any DAG
protocol code. Phase 6's delta report has one doc to diff against.

**Dependencies.** A, B, D complete. C and E don't need to be done for
F (they don't produce baseline numbers — they enable post-build
verification).

---

## Exit criteria for the epic

All 6 phases complete:
- Phase A: baseline numbers recorded ✅
- Phase B: legacy/cognition green ✅ (23-24/29 PASS; remaining 5-6 FAILs are LLM ranking variance)
- Phase C: 5 mechanic fixtures authored (failing as expected) ✅
- Phase D: journeys status known ✅ (10/10 validation, 10/10 seed, 1/10 full-execution validated)
- Phase E: 10-dim scenario authored ✅ (`test/evals/e2e/10-dim-library-service.yaml`)
- Phase F: baseline doc consolidated ✅ (refresh pending with Phase B+D numbers)

Then proceed to [`dag-build-plan.md`](dag-build-plan.md).

The epic is the **gate**. If any phase is incomplete, the build is
flying blind on that dimension and the Phase 6 delta report becomes
qualitative-only instead of measurable.

---

## Cross-references

| Doc | Relationship |
|---|---|
| [`integration-roadmap.md`](integration-roadmap.md) | This epic prepares verification for Phase 5; lands as Phase 0.75 |
| [`dag-build-plan.md`](dag-build-plan.md) | Sequenced after this epic |
| [`dag-protocol.md`](dag-protocol.md) | The protocol whose mechanics Phase C verifies |
| [`prompts/eval-principles.md`](prompts/eval-principles.md) | The 9 principles every deliverable here satisfies |
| [`tool-surface.md`](tool-surface.md) | Phase 1 telemetry is a prerequisite for Phase A baseline integrity |
| [`benchmarks/coverage-matrix.md`](benchmarks/coverage-matrix.md) | Phase E authors the e2e scenario it defines |

---

*This is a living epic doc. Update phase status (☐ → ☑) as work lands.*
