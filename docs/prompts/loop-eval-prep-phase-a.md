# Eval Prep — Phase A: Baseline Snapshot

Establish a clean pre-integration baseline. Every v2 scenario, LongMemEval,
SWE-bench (the wrapped subset), and the ABR metric have current numbers
recorded in `docs/eval-journal.md` with timestamps and model versions
pinned. This is the "before" picture Phase 6 of the integration roadmap
will compare against — without it, every later claim of "this got better"
is unmeasurable.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase A and
[`docs/prompts/eval-principles.md`](eval-principles.md) for the
principles every recorded result must satisfy.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Cortex binary:** prefer the locally-built `./bin/cortex` (run
  `go build -o bin/cortex ./cmd/cortex` first if missing); if not
  present, fall back to whatever `cortex` resolves to on PATH and
  record the resolved path in `eval-journal.md`.

All paths in the loop instructions below are **relative to the worktree
root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

Each iteration:

0. **Verify environment first.** Run `pwd && git rev-parse
   --abbrev-ref HEAD`. If `pwd` is not
   `/Users/dereksantos/eng/projects/cortex-dag-build` or the branch is
   not `derek.s/dag-build`, STOP and report the mismatch to the user —
   do not proceed (committing on the wrong branch or in the wrong
   worktree would be hard to reverse).

1. **Read state.** Open `docs/eval-prep-epic.md` Phase A. Open
   `docs/eval-journal.md` and find the most recent entry. Identify the
   next suite that needs a fresh baseline (suites in priority order
   below).

2. **Pick the next suite** in this order (skip any suite that already
   has a complete entry from this session in `eval-journal.md`):
   - v2 scenarios (`test/evals/v2/` — 40+ scenarios, run via `cortex
     eval --suite=v2 --json`)
   - LongMemEval (via `cortex eval --bench=longmemeval --json`)
   - SWE-bench Verified subset (via `cortex eval --bench=swebench --json`)
   - ABR scenario (whatever the canonical ABR run is in the current code)

3. **Run the suite.** Capture stdout/stderr; do NOT silence errors.
   Run with `--max-cost-usd $5` (or the existing default cap) so an
   unexpected blowout fails closed.

4. **Verify telemetry.** Confirm each scenario produced a row in
   `.cortex/db/cell_results.jsonl`. If rows are missing, that's a
   Phase 1 dependency gap — record it in `eval-journal.md` as
   "BLOCKED: needs Phase 1 unified telemetry" and move to the next
   suite.

5. **Record results** in `docs/eval-journal.md`. Append a new section
   with:
   - ISO timestamp
   - Suite name
   - Model versions used (LLM provider + model id; embedding model
     where relevant)
   - Per-scenario pass/fail + score
   - Per-scenario cost (tokens, $) and latency (ms)
   - Aggregate: pass rate, mean cost, mean latency
   - Three separated baselines per scenario where applicable (no-context,
     Cortex-Fast, Cortex-Full) — principle 8
   - Any anomalies or scenarios that crashed (record crash + skip; do
     NOT try to fix the scenario in this loop)

6. **Commit** the journal update with a conventional-commits message
   (`docs(eval): record <suite> baseline <date>`). One commit per
   suite. Do NOT push.

7. **Check stopping condition** (next section). If satisfied, write a
   final summary entry in `eval-journal.md` titled "Phase A baseline
   complete" and STOP.

## Constraints

- **Do not write any DAG protocol code.** Phase 5 is later; this loop
  is only baseline recording against current architecture.
- **Do not modify existing eval scenarios** in `test/evals/`. Preserve
  baselines; if a scenario is buggy, record the bug in
  `eval-journal.md` and move on.
- **Do not push to remote.** Local commits only. The user will review
  and push.
- **Do not silence failures.** If a suite crashes, the crash + traceback
  go in `eval-journal.md` verbatim. Do not catch-and-continue silently.
- **Per eval-principles 4 (Reproducible):** every recorded run must
  name the model version and any non-default config.
- **Per eval-principles 5 (Isolated):** runs may call LLM APIs but
  must not require unrelated network services.
- **Per eval-principles 6 (Structured):** every result either lands as
  a `cell_results.jsonl` row OR is explicitly recorded as a
  telemetry-gap blocker.
- **Per eval-principles 8 (Separated baselines):** where the harness
  supports it, record no-context / Cortex-Fast / Cortex-Full as
  distinct entries, not averaged.

## Verification

Per iteration:
- The just-recorded entry in `eval-journal.md` names a timestamp,
  model versions, and per-scenario rows.
- The commit message follows conventional commits.
- `git status` is clean after the commit (no stray files).

Loop-wide (the stopping condition):
- Every v2 scenario in `test/evals/v2/` has a row in the most-recent
  `eval-journal.md` entries.
- LongMemEval has a recorded score.
- SWE-bench (the wrapped subset) has a recorded pass rate.
- ABR has a recorded current number.
- Each blocked suite (if any) has an explicit BLOCKED entry naming
  the prerequisite.
- A final "Phase A baseline complete" summary entry exists in
  `eval-journal.md` with aggregate numbers.

When all of the above hold, STOP. Do not start Phase B (restoring
the legacy/cognition runner) in this loop — that's a separate prompt.

## When to ask for human input

Ask (don't guess) if:
- The `cortex eval` CLI doesn't exist for a suite (it should — flag
  if missing).
- A suite requires API keys or credentials not already configured.
- A baseline number contradicts a number already in `ROADMAP.md` by
  more than 20% (could be a regression worth surfacing immediately).
- More than 3 suites in a row hit telemetry-gap blockers — Phase 1 is
  probably not landed; recommend pausing this loop until Phase 1 ships.
