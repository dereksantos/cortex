Goal: Make `cortex eval grid` produce a trustworthy Agentic Benefit Ratio
(ABR) number per run, persisted and trended.

ABR is defined in docs/CLAUDE.md: ABR = quality(Fast+Think) / quality(Full).
The intent is: if Think correctly pre-computes what Full mode would produce,
Fast mode ≈ Full mode quality. Goal: ABR → 1.0 as a session progresses.

WHY THIS MATTERS: Right now Cortex's whole thesis ("inverse activity gradient,
budget-bounded cognition") rests on Think actually amplifying Fast mode. We
believe it works. We don't measure it routinely. Every other improvement
(counterfactual tuning, decay policies, prompt changes) is unfalsifiable
without a stable ABR number. This is the prerequisite for evidence-based
iteration.

INVESTIGATE FIRST (don't assume — read the code):
- internal/eval/v2/persist.go has columns `avg_abr` on both `eval_runs` and
  `eval_scenario_results`. Are they currently being populated, or are they
  dead columns? Run a recent eval and check.
- internal/cognition/cortex.go::Retrieve supports `cognition.Fast` and
  `cognition.Full` modes. Is there an existing path that runs both?
- internal/eval/v2/ has multiple scoring files (score_naming, score_shape,
  score_smell, score_test_parity). Which "quality" metric is the right
  numerator/denominator for ABR? Pick the one that's most stable; document
  the choice.
- pkg/cognition/cognition.go has `CachedReflect` etc. on SessionContext —
  this is what Think populates. Is it actually being warmed during the eval
  run, or does the eval skip Think?

DEFINITION OF DONE:
1. A scenario run captures BOTH Fast-mode quality and Full-mode quality (same
   query, same candidate pool, run twice).
2. ABR = q(Fast) / q(Full) is computed per scenario and aggregated per run.
3. ABR is persisted to eval_runs.avg_abr (use what's there, don't add a new
   column).
4. `cortex eval grid` output prints the ABR number prominently.
5. A new table-driven test exercises the Fast-vs-Full code path with mock
   results so ABR is deterministic and CI-safe.
6. Run the eval against test/evals/scenarios/ at least once and report the
   actual number. If ABR is 0.4, that's information — don't tune it, just
   measure it honestly.

CONSTRAINTS:
- TDD where the code path supports it (the integration with cognition is
  hard to unit-test; the math + persistence aren't).
- Don't add new metrics. Pick one quality function, justify it, use it.
- Standard library testing only (table-driven t.Run subtests).
- No backwards-compat shims; this is a measurement layer, not a wire format.

DELIVERABLE: a commit on a new branch off main, plus a short report at the
end of the session: "I ran 12 scenarios; ABR = 0.X with stddev Y; this seems
[low/expected/high] because Z." Be honest about whether the number is
trustworthy yet.
