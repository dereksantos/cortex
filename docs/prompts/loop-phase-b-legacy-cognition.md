# Phase B — Restore legacy/cognition Runner

All 22 per-node scenarios in `test/evals/legacy/cognition/` run via
`cortex eval --suite=legacy-cognition` and produce structured rows in
`.cortex/db/cell_results.jsonl`. Both self-contained scenarios (e.g.
`resolve_inject.yaml`, which inlines all fixtures) and storage-dependent
scenarios (e.g. `reflex_quality.yaml`, which references fixture IDs
like `auth_module` without defining them) work end-to-end. The
22 scenarios become the per-op acceptance suite for Phase 5's DAG
node registry.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase B for the
deliverable, [`docs/dag-protocol.md`](../dag-protocol.md) for the node
types these will eventually test, and
[`docs/prompts/eval-principles.md`](eval-principles.md) for the
principles every recorded result must satisfy.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` after each change

Cognitive mode implementations live in
`internal/cognition/{reflex,reflect,resolve,think,dream,router}.go` —
constructors like `NewReflex(store, embedder)` with method signatures
matching the `Reflexer`/`Reflector`/`Resolver`/`Thinker`/`Dreamer`
interfaces in `pkg/cognition/cognition.go`.

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read state.** Open `docs/eval-prep-epic.md` Phase B. Check what's
   done in `eval-journal.md` and `internal/eval/legacy/` (if it
   exists). Inspect 2-3 scenarios spanning both patterns
   (self-contained + storage-dependent) to confirm your understanding
   of the YAML shapes.

2. **Pick the next deliverable** in this order:
   - **(i) Scenario migration decision** — survey the 22 scenarios;
     classify each as self-contained or storage-dependent; decide
     between approach (a) build seed-then-dispatch into the runner or
     (b) migrate storage-dependent scenarios to include inline
     fixtures. Recommend (b) — one-shot migration is cleaner and the
     scenarios become self-explanatory. Document the decision in
     `eval-journal.md`.
   - **(ii) Scenario migration (if (b))** — for each
     storage-dependent scenario, embed the referenced fixtures inline.
     Validate each YAML loads.
   - **(iii) Runner skeleton** — `internal/eval/legacy/runner.go` (or
     similar). Loads a scenario, dispatches to the right mode by the
     `mode:` field, compares results to `expected:`, returns a
     structured result.
   - **(iv) Mode dispatchers** — one per cognitive function. Each
     constructs the implementation via the existing `NewReflex` /
     `NewReflect` / etc constructors (use the storage adapter the v2
     framework already uses for consistency).
   - **(v) CLI wiring** — `cortex eval --suite=legacy-cognition`
     routes to the new runner. Lives alongside the existing v2 path
     in `cmd/cortex/commands/eval.go`.
   - **(vi) Telemetry** — each scenario execution writes one row per
     `mode_test` to `.cortex/db/cell_results.jsonl` using Phase 1's
     unified sink. Schema: `cortex_function=<mode>`, `tool=<scenario_id>`,
     `latency_ms`, `ok`, `error_code`, `cell_id=<scenario_id>/<test_id>`.
   - **(vii) Full-suite run + journal** — execute all 22 scenarios;
     record pass/fail + per-test details in `eval-journal.md`.

3. **Test.** `go build ./...` then `go test ./...` after each
   deliverable. Land tests alongside the change.

4. **Commit** with conventional-commits style. One commit per
   deliverable. Examples:
   - `eval(legacy): migrate storage-dependent scenarios to inline fixtures`
   - `feat(eval/legacy): runner skeleton for legacy/cognition scenarios`
   - `feat(eval): cortex eval --suite=legacy-cognition wiring`
   - `docs(eval): record legacy-cognition baseline 2026-MM-DD`
   Do NOT push.

5. **Update `docs/eval-prep-epic.md`** Phase B to check off the
   completed deliverable.

6. **Check stopping condition.** If all 7 sub-deliverables done +
   all 22 scenarios produce rows + tests green, STOP and write a
   "Phase B complete" summary in `eval-journal.md`.

## Constraints

- **Do not modify the cognitive mode implementations themselves**
  (`internal/cognition/*`). They become DAG nodes in Phase 5; modify
  there, not here.
- **Do not change the v2 eval framework's behavior** — extend it
  alongside; don't refactor it.
- **Do not push to remote.** Local commits only.
- **Preserve self-contained scenarios as-is** — they don't need
  fixture migration; only the storage-dependent ones do.
- **Per eval-principles 3 (Versioned):** each scenario keeps a
  `version` field; if migrating fixtures inline, bump the version.
- **Per eval-principles 6 (Structured):** every `mode_test` result
  lands as one row in `cell_results.jsonl`. No row = silent failure;
  that's not allowed.
- **Per eval-principles 8 (Separated baselines):** record per-test
  pass/fail; don't average within a scenario.

## Verification

Per deliverable:
- ☐ (i) Migration decision documented; per-scenario classification done.
- ☐ (ii) If (b): all storage-dependent scenarios load standalone (no
  external state needed).
- ☐ (iii) Runner exists; can execute one scenario end-to-end.
- ☐ (iv) Each of reflex/reflect/resolve/think/dream/router has a
  dispatcher; a scenario routed to each mode runs and returns
  results.
- ☐ (v) `cortex eval --suite=legacy-cognition` runs all 22 from CLI.
- ☐ (vi) After a run, `cat .cortex/db/cell_results.jsonl | jq` shows
  one row per `mode_test` across all 22 scenarios.
- ☐ (vii) `eval-journal.md` has a "legacy-cognition baseline
  YYYY-MM-DD" section with per-scenario pass rates.

Loop-wide stopping condition:
- ☐ All 22 scenarios run via CLI without runner-side crashes.
- ☐ Cell-results rows count matches `sum(mode_tests count)` across
  all scenarios.
- ☐ Pass/fail recorded per scenario; failures are categorized
  (mode-implementation issue vs. scenario-expectation drift).
- ☐ `docs/eval-prep-epic.md` Phase B checked off.

## When to ask the user

- If a scenario's `expected:` block uses metrics the current mode
  implementations no longer expose (mode drift since the scenario was
  authored).
- If the storage adapter the v2 framework uses can't be reused and
  you'd need to author a parallel one.
- If more than 5 of the 22 scenarios fail in ways that look like real
  regressions (worth pausing to triage rather than mass-record).
