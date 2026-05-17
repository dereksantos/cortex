# Phase E — Author the 10-Dimension E2E Scenario

`test/evals/e2e/10-dim-library-service.yaml` exists. Single multi-session
scenario that exercises each of the 10 UX dimensions from
[`docs/benchmarks/coverage-matrix.md`](../benchmarks/coverage-matrix.md).
Each step has a clear pass/fail criterion using existing per-dim metrics.
**Runner deferred to Phase 5** — this prompt produces the authored
fixture only; no Go code.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase E for the
deliverable spec and
[`docs/prompts/eval-principles.md`](eval-principles.md) for the
principles every authored scenario must satisfy.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Reference YAML shape:** `test/evals/journeys/large-auth.yaml`
- **Authoritative spec:** `docs/benchmarks/coverage-matrix.md` →
  "End-to-end scenario (target shape)" section
- **No code changes** — authoring task only

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

This is a bounded authoring task — usually one iteration suffices, but
the loop format is preserved so the prompt can self-pace if
clarification cycles are needed.

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read sources** before authoring (do not skip):
   - `docs/benchmarks/coverage-matrix.md` — full doc, particularly
     "The 10 dimensions" table and "End-to-end scenario (target
     shape)" section
   - `docs/eval-prep-epic.md` Phase E — deliverable + success criteria
   - `test/evals/journeys/large-auth.yaml` — reference for multi-session
     YAML structure
   - One v2 multi-session scenario from `test/evals/v2/` for additional
     format comparison
   - List `test/evals/projects/` to see which project scaffolds exist
     and pick one (or note that one needs creation)

2. **Draft the scenario.** Required structure:
   - **Project**: library-service-style (similar pattern to existing
     `library-service-seed` or `auth-service` scaffolds). Either
     reference an existing scaffold or note that one needs creation
     in a follow-up.
   - **10 steps** in the canonical order from coverage-matrix.md's
     "End-to-end scenario":
     1. Ingress — ambiguous request; agent must ask
     2. Plan — constraint-respecting plan
     3. Ground — repo exploration
     4. Execute — multi-file edit
     5. Observe — streamed event cadence
     6. Steer — mid-task redirect
     7. Safety — destructive op gated
     8. Continuity — recall across sessions
     9. Present — diff + summary + verification
     10. Extend — register and use a new MCP tool
   - **Per-step pass/fail criterion** using existing metrics named in
     coverage-matrix.md's per-dimension sections. No new scoring
     scheme.

3. **Write to** `test/evals/e2e/10-dim-library-service.yaml`. Create
   the `test/evals/e2e/` directory if it doesn't exist.

4. **Validate** by re-reading the file top-to-bottom. Each of the 10
   steps should map cleanly to exactly one dimension; each criterion
   is concrete enough that a runner could mechanically score it.

5. **Commit** with `docs(eval): author 10-dim e2e scenario for
   library-service`. Update `docs/eval-prep-epic.md` Phase E to
   checked. Do NOT push.

6. **Stop.** This phase is one-and-done; do not start another phase
   in this loop.

## Constraints

- **No runner code.** The scenario must be authorable without writing
  any Go. The Phase 5 build will land the runner.
- **No new scoring scheme.** Every per-step criterion must reuse a
  metric already named in `coverage-matrix.md` per-dimension sections
  or in upstream benchmarks referenced there.
- **Self-contained.** No external network calls, no machine-specific
  paths, no credentials. Anything the scenario needs must be either
  in the repo or noted as a Phase 5 follow-up.
- **Use a project scaffold** under `test/evals/projects/` — either
  reference an existing one (preferred) or note that one needs to be
  authored separately (do NOT author it in this task; scope creep).
- **No push to remote.**
- **Per eval-principles 3 (Versioned):** include a `version` field on
  the scenario so future schema changes can be detected.
- **Per eval-principles 6 (Structured):** the YAML must be machine-
  parseable; do not embed scoring rationale in free-text fields that
  a runner can't programmatically consume.

## Verification

- ☐ `test/evals/e2e/10-dim-library-service.yaml` exists.
- ☐ Each of the 10 dimensions from `coverage-matrix.md` appears
  exactly once in the scenario (no duplicates, no omissions).
- ☐ Each step has a measurable pass/fail criterion (not just prose).
- ☐ The YAML parses without error (`go run` a quick validator or
  `python -c 'import yaml; yaml.safe_load(open(...))'`).
- ☐ A reader can step through the scenario and identify which
  dimension each step tests without consulting an index.
- ☐ `docs/eval-prep-epic.md` Phase E is checked off.
- ☐ Commit landed locally; clean `git status`.

## When to ask the user

- If a project scaffold for the library-service pattern doesn't
  exist and you're unsure whether to (a) author one as part of this
  task or (b) defer to a follow-up.
- If a dimension's existing metric (per coverage-matrix.md) is too
  vague to mechanically score and you'd need to propose a sharper
  one.
- If you discover the canonical 10-step order in coverage-matrix.md
  doesn't quite match the 10 dimensions table (any inconsistency
  worth raising before authoring).
