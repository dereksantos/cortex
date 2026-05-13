Goal: Turn `cortex journal replay --execute --config-overrides=...` from a
primitive into a tool that produces an actionable recommendation:
"model=X matches model=Y's behavior N% of the time at 1/M the cost."

PREREQUISITE: ABR measurement must already exist (see
docs/prompts/measure-abr.md). Without it the "better" half of the
comparison is gut-feel.

WHY THIS MATTERS: The X2 slice (see docs/journal-design-log.md) landed the
counterfactual primitive — replay re-runs Reflect with overridden config and
emits `replay.counterfactual` journal entries with Jaccard@K. But nothing
reads those entries. Auto-tuning closes the loop: run real eval traffic
against alternative configs, measure quality + cost delta, emit a
recommendation.

INVESTIGATE FIRST:
- internal/journal/replay.go defines `ReplayCounterfactualPayload` with
  Jaccard@K, status, overrides. cmd/cortex/commands/journal_replay.go
  has `counterfactualReflectRerank`.
- The replay class dir is .cortex/journal/replay/.
- internal/eval/v2/spend.go and persist.go track cost (USD per cell).
- internal/cognition/reflect.go is what re-runs.

DEFINITION OF DONE:
1. New subcommand `cortex journal replay-summary` (or
   `cortex journal counterfactual report` — name it whatever fits the CLI's
   verb-noun pattern) reads all `replay.counterfactual` entries in the
   range, groups by `Overrides`, and prints:
   - N source entries replayed
   - Mean Jaccard@5, stddev
   - Decision-flip rate (status==executed entries where the top-1 changed)
   - Failure rate (status==failed entries)
   - Cost ratio if cost data is available (override config's $/call ÷
     baseline $/call)
2. A second new subcommand or a flag on `cortex eval grid` (your call —
   document the choice) runs the full loop:
   - Execute the eval grid with the baseline config (generates real
     reflect.rerank entries)
   - For each non-empty rerank, run counterfactual replay with each candidate
     override
   - Run replay-summary
   - Print recommendation: "alternative {model=X} preserves top-5 set 87%
     of the time at 22% of the cost; consider promoting"
3. Table-driven tests for the aggregation math (group-by + Jaccard mean +
   flip rate) using synthetic counterfactual entries — no LLM calls in
   tests.
4. Run end-to-end against a small scenario set with at least one realistic
   override candidate (e.g., `model=claude-haiku-4-5-20251001` vs
   `claude-opus-4-7`) and report the actual recommendation.

CONSTRAINTS:
- Only `--class=reflect` is supported by the X2 primitive today. Don't
  expand scope to dream/resolve here — that's a separate slice.
- The "recommendation" output is a heuristic, not a decision. Pick a clear
  threshold for "consider promoting" (e.g., flip-rate < 15% AND cost < 50%)
  and document it.
- Don't auto-promote anything. The point is to surface evidence, not to
  swap configs without human review.
- Standard library testing only.

DELIVERABLE: branch off main, commit the new subcommand(s) + tests, run
end-to-end against the eval scenarios, report findings in the session
output. Honest report: "model=X is competitive in N% of cases; here's the
shape of the failures."
