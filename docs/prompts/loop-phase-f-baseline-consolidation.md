# Phase F — Consolidate Baseline Doc (one-shot)

`docs/eval-baseline.md` exists as a single time-stamped, model-pinned
snapshot of every "before" number across Phases A / B / D. Phase 6 of
the integration roadmap will diff its post-integration results
against this doc. `ROADMAP.md` and `docs/eval-journal.md` both link
to it as the canonical baseline.

This is the consolidation step — no new measurements, no eval re-runs.
Pure synthesis of numbers already recorded.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase F for the
deliverable spec.

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **No code changes** — authoring + synthesis only
- **Dependencies:** Phase A done (required); Phase B and D done
  (preferred — if pending, the doc marks those sections as such)

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

This is a one-shot task; one iteration usually suffices.

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Read all baseline entries** in `docs/eval-journal.md`. Capture,
   for each suite that has a recorded baseline:
   - Suite name
   - Date of run
   - Model versions (LLM + embedding)
   - Cortex config (cognitive mode versions, retrieval params)
   - Per-scenario or aggregate scores
   - Cost / latency where recorded
   - Any anomalies or BLOCKED entries

2. **Compose `docs/eval-baseline.md`** with these sections:

   ```markdown
   # Eval Baseline — Pre-DAG-Protocol Snapshot

   **Snapshot date:** YYYY-MM-DD
   **Model pins:** <LLM provider/model>, <embedding model>
   **Git ref:** <commit SHA of dag-build at consolidation time>
   **Purpose:** The "before" picture Phase 6 of the integration
   roadmap diffs against. Linked from ROADMAP.md as canonical.

   ## Suite-by-suite baselines

   ### v2 (test/evals/v2/) — N scenarios
   - Aggregate pass rate: X / N
   - Mean cost: $X.XX per scenario
   - Mean latency: Xms per scenario
   - Detail: see `eval-journal.md` entry from <date>

   ### LongMemEval
   ### SWE-bench Verified subset
   ### ABR (post-diagnostic, post-auto-capture)
   ### MTEB (NFCorpus subset)
   ### legacy-cognition (Phase B output)            [if done]
   ### journeys (Phase D output)                    [if done]

   ## Per-axis cost / latency breakdown

   Aggregated from .cortex/db/cell_results.jsonl unified telemetry
   (Phase 1 sink). Per tool-axis category, mean / p50 / p95.

   ## Aggregate health summary

   - Overall pass rate across all suites
   - Total cost per full-suite run
   - Total wall time per full-suite run
   - Outstanding BLOCKED / pending entries

   ## How to re-baseline

   To produce a new baseline after Phase 5 lands, run:
       cortex eval --suite=<each> --output=cell_results
   then re-run this consolidation by reading eval-journal.md entries
   dated after this snapshot.
   ```

3. **Link from `ROADMAP.md`** — add a line in the "Eval results"
   section pointing at `docs/eval-baseline.md` as the canonical
   baseline. Update the existing ABR table to reference the consolidated
   doc.

4. **Link from `docs/eval-journal.md`** — add a header or top-of-file
   note pointing at `docs/eval-baseline.md` for the time-stamped
   snapshot view.

5. **Mark pending sections explicitly.** If Phase B or D didn't land
   before this Phase F runs, the corresponding section is present
   in the doc with "_(pending Phase X; will be added once that loop
   completes)_" — do NOT omit it silently.

6. **Commit** `docs(eval): consolidate pre-DAG baseline into
   eval-baseline.md`. Update `docs/eval-prep-epic.md` Phase F to
   checked. Do NOT push.

7. **Stop.** Phase F is one-shot; do not start another phase.

## Constraints

- **No new measurements.** Do not re-run any eval suite. If a number
  needs re-measuring, that's a separate task; flag it and move on.
- **Pin git ref.** Capture the commit SHA at consolidation time so
  the snapshot is anchored to a known tree state.
- **Don't push to remote.**
- **Mark pending sections; don't omit them.** Future reviewers should
  see what's expected vs. what's present.
- **Per eval-principles 3 (Versioned):** the baseline doc itself has
  a `Snapshot date` and `Git ref` at the top — it's a versioned
  artifact, not a living doc.
- **Per eval-principles 4 (Reproducible):** every number has provenance
  (which `eval-journal.md` entry, which run date) so a re-baselining
  can repeat the methodology.

## Verification

- ☐ `docs/eval-baseline.md` exists; sections cover every suite that
  has a recorded baseline.
- ☐ Snapshot date + git ref + model pins at top.
- ☐ Pending sections explicitly marked (not omitted).
- ☐ `ROADMAP.md` links to it.
- ☐ `docs/eval-journal.md` links to it.
- ☐ `docs/eval-prep-epic.md` Phase F checked off.
- ☐ Single commit; clean `git status`.

## When to ask the user

- If `eval-journal.md` has multiple baseline entries for the same
  suite and you're unsure which is canonical (e.g. before vs. after
  the ABR diagnostic).
- If a suite has only BLOCKED entries and you're unsure whether to
  include it as pending or omit until unblocked.
- If consolidating reveals an inconsistency between two recorded
  numbers (worth surfacing rather than averaging silently).
