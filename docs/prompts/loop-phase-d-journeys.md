# Phase D — Verify journeys/ Runnability

All 10 e2e scenarios in `test/evals/journeys/` have explicit status:
either runnable end-to-end (with a baseline recorded in
`eval-journal.md`) or pending with a documented reason. Multiple docs
cite this suite as the canonical E2E test ladder
(`docs/prompts/eval-data-gathering.md`, `docs/archive/eval-harness-loop.md`,
`docs/prompts/eval-abr-focus.md`); without verification, those
references are floating claims.

The runner work has two viable paths; this prompt scopes the audit
and the chosen implementation. See
[`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase D for the
deliverable spec.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Build:** `go build -o bin/cortex ./cmd/cortex` after each change
- **Tests:** `go test ./...` after each change
- **Substantial v2 infrastructure** to template from:
  `internal/eval/v2/library_service_e2e.go`,
  `coding_scenario.go`, `coding_capture.go`,
  and harness adapters (`library_service_*_harness.go`)

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read state.** Open `docs/eval-prep-epic.md` Phase D. List the 10
   scenarios in `test/evals/journeys/` and the project scaffolds in
   `test/evals/projects/`. Inspect 2 contrasting journey YAMLs (e.g.
   `trivial-hello-world.yaml` and `large-auth.yaml`) and one v2
   multi-session scenario for shape comparison.

2. **Pick the next deliverable** in order:

   - **(i) Audit.** Try to run each of the 10 via `cortex eval`. For
     each: record success / failure / partial. Catalog what the
     specific failure modes look like. Output: a status table in
     `eval-journal.md` titled "Journeys audit YYYY-MM-DD".

   - **(ii) Path decision.** Based on (i), choose between:
     - **(a) Thin journey-runner:** new loader that handles
       `type: e2e` YAML; wraps the existing v2 harness adapters; CLI
       wiring `cortex eval --suite=journeys`.
     - **(b) Port to v2 format:** translate the 10 journey YAMLs to
       v2 scenario shape; they become v2 scenarios going forward; no
       separate runner needed.

     Document the decision and rationale in `eval-journal.md`.
     Heuristic: if more than 7 of the 10 fields per journey map 1:1
     to v2 fields, prefer (b). Otherwise (a).

   - **(iii) Implement the chosen path.**
     - For (a): journey loader in
       `internal/eval/journey/runner.go`; CLI wiring; harness adapter
       reuse from v2.
     - For (b): port each YAML; verify it loads in the existing v2
       runner; delete or archive the legacy journey/ copy.

   - **(iv) Run all 10.** Execute via the chosen path. Where a
     scenario can't run for non-runner reasons (missing scaffold,
     model unavailability), record as "pending: <reason>".

   - **(v) Record baseline.** Append a "journeys baseline YYYY-MM-DD"
     entry to `eval-journal.md` with per-scenario pass/fail + cost /
     latency. Each successful run writes to
     `.cortex/db/cell_results.jsonl` via Phase 1's unified sink.

3. **Test.** `go build ./...` + `go test ./...` after each
   implementation change.

4. **Commit** with conventional-commits style. Examples:
   - `docs(eval): journeys runnability audit 2026-MM-DD`
   - `feat(eval/journey): runner + CLI for type:e2e scenarios` (if (a))
   - `eval: port 10 journeys to v2 format` (if (b))
   - `docs(eval): record journeys baseline 2026-MM-DD`
   Do NOT push.

5. **Update `docs/eval-prep-epic.md`** Phase D to check off each
   deliverable.

6. **Check stopping condition.** All 10 scenarios have explicit
   status (runnable + baseline recorded, or pending + documented
   reason). STOP and write "Phase D complete" summary.

## Constraints

- **Don't modify project scaffolds** (`test/evals/projects/*`)
  unless a scaffold needs a one-line fix to enable a run; in that
  case commit it separately with `eval(scaffold):` prefix.
- **Don't refactor the v2 framework.** Extend or reuse; don't
  refactor. If a refactor would clean things up, file as a follow-up
  in `eval-journal.md` rather than do it here.
- **Don't push to remote.** Local commits only.
- **No silent failures.** Every scenario gets a status — either pass,
  fail, or explicit pending with reason.
- **Per eval-principles 6 (Structured):** every successful run lands
  a `cell_results.jsonl` row.
- **Per eval-principles 8 (Separated baselines):** if a journey
  benefits from no-context / Cortex-Fast / Cortex-Full split, record
  the three separately; don't average.

## Verification

Per deliverable:
- ☐ (i) Audit table in `eval-journal.md` covers all 10 scenarios.
- ☐ (ii) Path decision documented with rationale.
- ☐ (iii) Implementation lands; tests pass; existing v2 suite still
  runs.
- ☐ (iv) All 10 attempted; each has a recorded outcome.
- ☐ (v) Baseline entry has per-scenario rows + aggregate pass rate.

Loop-wide stopping condition:
- ☐ Each of 10 scenarios has explicit status (no "unknown").
- ☐ At least one scenario produces a structured cell_results row
  (proving the new path works end-to-end).
- ☐ `eval-journal.md` "Phase D complete" entry exists with the
  status table.
- ☐ `docs/eval-prep-epic.md` Phase D checked off.

## When to ask the user

- If the audit reveals the 10 scenarios are mostly unrunnable for
  reasons unrelated to the missing runner (model gone, scaffold
  drift) and Phase D scope should shrink to "verify status only,
  defer restoration."
- If the path decision is borderline — neither (a) nor (b) is
  clearly better.
- If porting (path (b)) would lose information from journey YAMLs
  that v2 format can't represent.
