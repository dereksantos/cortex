# ABR Diagnostic — Resolve 0.586 vs 0.77 Discrepancy

The ABR=0.586 recorded by Phase A in `docs/eval-journal.md` disagrees
with the ABR=0.77 in `ROADMAP.md` by 24%. This discrepancy is resolved
by EXACTLY ONE of the following outcomes landed and committed:

- **(a) Stale doc** — ROADMAP.md updated to reflect current measurement
  reality, with a note explaining what changed. Commit:
  `docs(roadmap): update ABR baseline to 0.586 (was stale 0.77)`.
- **(b) Real regression** — regression entry added to
  `docs/eval-journal.md` naming suspected cause + a minimal reproducer.
  Commit: `eval: file ABR regression investigation`. Do NOT attempt to
  fix the regression in this task.
- **(c) Measurement drift** — the two numbers came from different
  configs and "right" is ambiguous. Add reconciliation note to
  `eval-journal.md`; propose a canonical config going forward; commit:
  `docs(eval): reconcile ABR measurement config drift`.

After the chosen path lands, `ROADMAP.md` and `eval-journal.md` no
longer disagree on ABR. Diagnostic only — not a fix.

See [`docs/eval-prep-epic.md`](../eval-prep-epic.md) Phase A (the
source of 0.586) and the Phase A memory entry
(`project_phase_a_baseline_2026_05_17`).

## Working environment

- **Worktree:** `/Users/dereksantos/eng/projects/cortex-dag-build/`
- **Branch:** `derek.s/dag-build`
- **Time-box:** half-day. If you can't categorize cleanly within
  4 hours, escalate per "When to ask" below — do not spiral.

All paths in the loop instructions below are **relative to the
worktree root**. If `pwd` is not the worktree root, `cd` there first.

## Loop

0. **Verify environment.** Run `pwd && git rev-parse --abbrev-ref HEAD`.
   If `pwd` is not `/Users/dereksantos/eng/projects/cortex-dag-build`
   or the branch is not `derek.s/dag-build`, STOP and report.

1. **Capture the new measurement context** from
   `docs/eval-journal.md`'s Phase A entry:
   - Model version (LLM provider + model id; embedding model)
   - Cortex config (cognitive mode versions, embedding model, top-k,
     similarity threshold)
   - Scenario set (which scenarios were averaged into 0.586?)
   - Timestamp
   - Runner version (`git rev-parse HEAD` from the run)

2. **Trace the original 0.77 measurement** via git history:
   - `git log --all --grep="0.77\|ABR" --oneline` to find the commit
     where 0.77 first appeared in `ROADMAP.md`
   - `git log --all -p -- ROADMAP.md | grep -B2 -A2 "0.77"` to see the
     surrounding context
   - Check archived eval docs under `docs/archive/` for the original
     measurement run
   - Capture the same fields as step 1 for the 0.77 context

3. **Compare conditions** field by field:
   - Same LLM model? Same version?
   - Same scenario set, or has v2 churned since 0.77 was measured?
   - Same Cortex config (modes, embedding model, retrieval params)?
   - Same runner version, or has the scoring logic changed?

4. **Categorize the gap** into exactly one of:

   **(a) Stale doc** — Conditions in step 3 differ in ways that
   plausibly explain the gap (e.g., different model, different
   scenario set, scoring logic refactor). The 0.586 is the current
   reality; 0.77 was a snapshot under different conditions.

   **(b) Real regression** — Conditions are equivalent and 0.586 is
   genuinely lower than 0.77 used to be on the same configuration.
   Something regressed between the 0.77 commit and HEAD.

   **(c) Measurement drift** — Conditions differ in ways where it's
   not obvious which "should" be the baseline. Neither stale nor
   regression — needs a decision about what canonical config to use
   going forward.

5. **Land the chosen path** (exactly one):

   For **(a) Stale doc:**
   - Edit `ROADMAP.md`: replace 0.77 with 0.586. Add a 1-sentence
     note: "Baseline rebaselined <date> under <model> on <scenario
     set>; prior 0.77 measured <date> under <different model/set>."
   - Commit: `docs(roadmap): update ABR baseline to 0.586 (was stale 0.77)`.

   For **(b) Real regression:**
   - Append a section to `docs/eval-journal.md` titled "ABR regression
     investigation YYYY-MM-DD" capturing:
     - Suspected cause (named change between the 0.77 commit and HEAD)
     - Minimal reproducer (the commit + scenario + command to re-run)
     - Severity (does this block the DAG build, or is it a known
       trade-off?)
   - Do NOT attempt the fix. File the investigation; stop.
   - Commit: `eval: file ABR regression investigation (0.77 → 0.586)`.

   For **(c) Measurement drift:**
   - Append a reconciliation section to `docs/eval-journal.md` naming:
     - What the two configs differed on
     - The proposed canonical config going forward (with rationale)
     - What ROADMAP.md should reflect once consensus is reached
   - Edit `ROADMAP.md` to mark the ABR row "under reconciliation, see
     eval-journal.md".
   - Commit: `docs(eval): reconcile ABR measurement config drift`.

6. **Update `MEMORY.md`** if the resolution needs to be referenced
   going forward (e.g., the new baseline number, the canonical
   config). Otherwise skip.

7. **Stop.** Diagnostic complete; do not start any other phase.

## Constraints

- **Diagnosis only.** If you land in case (b), do NOT attempt to fix
  the regression in this task. File the issue and stop.
- **Don't change the eval framework or scenarios.** The diagnosis
  must work on the existing eval as-is.
- **Don't push to remote.**
- **Time-box: 4 hours.** If still ambiguous after that, escalate per
  "When to ask".
- **Per eval-principles 3 (Versioned) + 4 (Reproducible):** the
  resolution must capture enough state that someone re-running it
  later can confirm the diagnosis.

## Verification

- ☐ Exactly one of (a), (b), (c) landed and committed.
- ☐ `ROADMAP.md` and `docs/eval-journal.md` no longer contradict on
  ABR (either the number matches, or one points to the other as the
  authoritative source).
- ☐ The chosen path's commit message names the resolution category.
- ☐ If (b), a reproducer is captured (commit SHA + command).
- ☐ `git status` clean after the commit.

## When to ask the user

- If the diagnosis could reasonably be two of (a)/(b)/(c) and a
  judgment call is needed.
- If (b) and the regression looks severe enough that the DAG build
  should pause until it's fixed (not just filed).
- If the 0.77 measurement's provenance can't be traced from git
  history (no clear commit established the number) — that itself is
  a finding worth surfacing.
- If the 4-hour time-box passes without a clean categorization.
