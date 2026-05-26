# Eval Suite: Codebase Reading & Navigation

A MECE eval suite for the harness's ability to understand, navigate, and read
codebases of varying sizes/languages. Built so every prompt-tune or
architectural change produces a measurable delta — dial-and-measure instead of
one-off probes.

## Why

The multi-hop architecture work (`[[project-multi-hop-via-spawn]]`) shipped
through several iterations of "tune → eyeball one prompt → iterate." Each tune
risks regressing other question shapes (see "synth directive hedge-bias removal
helped audits but plausibly degrades simple summaries"). Without a fixed suite
the dial moves are guesswork.

The suite has to cover:

- Read mechanics (single-chunk, paged, large-file navigation)
- Question shapes (pinpoint → audit)
- Budget/scope behavior (the estimator is right-sized; termination is honest)
- Cross-cutting axes: project size, language

## MECE axes

| Axis | Values | Notes |
|---|---|---|
| Question shape | factual / relationship / audit / synthesis / refactor / locate | Group Q |
| Scope | pinpoint / local / cross-file / module / whole-project | Drives `sense.estimate_scope` test cases |
| Read mechanics | single-chunk / paged / fan-out / deep-nest | Group R |
| Multi-hop depth | 0 / 1 / 2 / 3+ | Counted in `dag_traces.jsonl` |
| Project size | tiny <10 / small / medium / large 1000+ | Cross-cutting |
| Language | Go / JS-TS / Python / Rust / mixed | Cross-cutting |

Project size and language are cross-cutting; every other axis is the matrix.

## Eval taxonomy (R / Q / B — 11 evals)

| Group | Eval | Tests | Pass signal |
|---|---|---|---|
| **R**ead | R1 Single-chunk read | small file, one read | Answer cites a fact only in that file; hop count = 1 |
| | R2 Paged read | file > emission budget | NEED_MORE emitted for unread range; hop ≥ 2; final answer aggregates both halves |
| | R3 Symbol-in-large-file | `enclosing_symbol` nav | Correct enclosing function named from one slice read |
| **Q**uestion | Q1 Pinpoint factual | "what's the default MaxFanout?" | Exact value cited; hop = 1; 0 NEED_MORE |
| | Q2 Cross-file relationship | "how does X consume Y?" | Both files cited; hop ≥ 2 |
| | Q3 Whole-project audit | "does README match impl?" | ≥ N items each with non-README citation; 0 hedges |
| | Q4 Refactor proposal | "suggest file split for X" | Proposed groupings cite functions actually in the file; NEED_MORE for unread ranges (not hedge) |
| | Q5 Search/locate | "where is the error path for X?" | Final answer cites file:line range, not raw grep dump |
| **B**ehavior | B1 Scope estimator sizing | Budget grows with prompt scope | Pinpoint → tokens < 10k; audit → tokens > 50k |
| | B2 Termination (no over-hopping) | Pinpoint doesn't waste hops | NEED_MORE count = 0 on Q1-class prompts |
| | B3 Honest unknown | Genuinely missing info → NEED_MORE | 0 "not confirmed" / "appears to" / "may be" phrases |

Matrix: **11 evals × 2 sizes × 2 languages = 44 cells** for a baseline run.
Cortex + leanjs already cover (Go × medium) and (JS × tiny). Adding Python +
Rust fixtures later closes the language axis.

## Per-eval fixture shape

YAML / JSON in `test/evals/scenarios/codebase-reading/`. Schema:

```yaml
id: q3-audit-cortex
group: Q
eval: Q3
project: cortex                        # workdir relative to a configured fixture root
prompt: "Does the README match the actual project implementation? What would you change?"
expected:
  hop_count_min: 2                     # multi-hop required for audit
  hop_count_max: 5                     # cap on runaway
  citation_rate_min: 0.7               # ≥70% of claims must have a file/line citation
  hedge_count_max: 0                   # no "not confirmed" language
  unverified_tail_count_max: 0         # synth should NEED_MORE not park
  must_cite_paths:                     # at least one of these must appear in the answer
    - pkg/cognition/dag/
    - cmd/cortex/main.go
  must_not_invent:                     # these strings would only appear if hallucinated
    - cortex-data-warehouse
    - NewCodingTurnNode                # we know the real name is NewCodingTurnHandler
  budget_token_min: 50000              # B1 also tested here: audit scope → > 50k
  budget_token_max: 150000
```

Tiny canary fixtures (run on every CI change):
- R1 + Q1 + B2 on cortex (Go) — fast, validates mechanics
- Q3 on cortex — catches hedge-bias regressions

Full suite (run on each prompt-tune commit):
- All 11 × 2 × 2 — produces a metric delta vs the prior baseline

## Metric extraction

All extracted from `cell_results.jsonl` + `dag_traces.jsonl`. No LLM judge for
the mechanical ones.

| Metric | Source | Computation |
|---|---|---|
| `hop_count` | `dag_traces.jsonl` | Count of `qualified_name == "decide.next"` rows for the turn |
| `read_count` | same | Count of `act.read_file` rows |
| `shell_count` | same | Count of `act.run_shell` rows |
| `citation_rate` | answer text | Regex `[\w/.-]+\.\w+(:\d+)?` matches divided by claim count (claim = paragraph or list item) |
| `hedge_count` | answer text | Count of regex `not directly confirmed\|appears to\|may be\|not seen in\|cannot verify` |
| `unverified_tail_count` | answer text | Count items under a section literally named "Unverified" or "Unverified Claims" |
| `must_cite_paths_satisfied` | answer text | At least one fixture path appears verbatim |
| `must_not_invent_clean` | answer text | None of the fixture strings appear |
| `budget_token_in_range` | `cell_results.jsonl` | `budget_estimate.tokens` between fixture min/max |
| `wall_time_ms` | `cell_results.jsonl` | direct |
| `tokens_used_ratio` | both | `total_tokens / budget_estimate.tokens` |

LLM judge (only for the few cases where mechanical isn't enough — e.g. R2 final
answer aggregation correctness): use `decide.coding_turn` with a fixed
"is-this-answer-substantively-correct" prompt on Haiku 4.5 or OpenRouter
gpt-5.4. Output: bool + one-sentence reason. Stamp into `cell_results.jsonl`.

## Implementation slices

### Slice 1 — Fixture loader + metric extractor (no judging yet)
- New package `internal/eval/codebase/` with:
  - `fixture.go` — parse YAML/JSON, validate schema
  - `metrics.go` — pure functions over (answer_text, dag_traces, cell_result) → metric struct
  - `runner.go` — run cortex against a workdir+prompt, capture output + traces
- Wire into `internal/eval/v2/` runner alongside existing scenarios. Reuse the
  unified `cell_results.jsonl` sink — add fields, don't fork the schema.
- Land 3 fixtures: R1 + Q1 + Q3 on cortex. Validate the extractor works
  end-to-end before authoring the rest.

### Slice 2 — Author full fixture set
- 11 fixtures × 2 sizes × 2 languages = 44 files under
  `test/evals/scenarios/codebase-reading/{r1,r2,r3,q1,...}.yaml`
- Each cell carries `must_cite_paths` and `must_not_invent` lists derived from
  the actual fixture project. (For cortex these come from `cmd/cortex/main.go`,
  `pkg/cognition/dag/`, etc. For leanjs from its actual structure.)
- Python and Rust fixture projects: defer to slice 5; start with Go + JS.

### Slice 3 — LLM judge for substantive correctness
- New op-style handler in `pkg/cognition/dag/ops/judge_answer.go` running
  Haiku 4.5 / OR gpt-5.4 against the fixture-supplied "correct answer" or
  rubric. Emits `judge_pass: bool, judge_reason: string` per cell.
- Only run on the cells where mechanical metrics don't fully cover correctness
  — Q-class evals mainly. R-class and B-class are fully mechanical.

### Slice 4 — Baseline + regression dashboard
- `cortex eval codebase --baseline` runs the suite, writes
  `.cortex/db/eval_baselines/<commit>.jsonl`.
- `cortex eval codebase --compare <ref>` diffs current run vs baseline.
- Surface in `cortex status --eval` (one-line aggregate: "N/44 passing,
  citation_rate p50=X").

### Slice 5 — Fill the cross-cutting axes
- Add a small Python fixture project (Flask-style or click-CLI app, ~50 files)
  and a small Rust fixture project (axum-style, ~50 files) under
  `test/evals/fixtures/`. They need their own README + manifests so the audit
  evals have something to cross-reference.
- Extend the fixture YAML language axis. Now matrix = 11 × sizes × 4 languages.

### Slice 6 — Multi-hop final-answer aggregation
- Fix the REPL printing the synth's `NEED_MORE:` line as terminal text on
  paged/multi-hop reads. The executor already routes the follow-up `decide.next`,
  but the REPL prints the FIRST synth's final_text (which contained the
  NEED_MORE). The right behavior: only print the TERMINAL synth's text — the
  one that returns Path A answer, not a NEED_MORE.
- This is a one-spot REPL change; reference: `cmd/cortex/commands/repl.go`
  around `runREPLChainTurn`'s final-text capture.

## Acceptance criteria

The suite is "done" (slices 1–4) when:

1. `cortex eval codebase` runs end-to-end and emits a `cell_results.jsonl`
   row per eval cell.
2. All 11 evals exist as fixtures for at least cortex + leanjs.
3. Mechanical metrics extract cleanly with no regex false-positives on the
   baseline run.
4. `--compare` produces a readable diff (regressions highlighted, > 10%
   change flagged).
5. The current baseline is recorded and stored. Future commits diff against it.

Slices 5–6 close the language axis and the paging-aggregation bug; they're
useful but not required to call the suite shippable.

## Open questions

- **Fixture project licensing.** Python + Rust fixtures need to be small,
  self-contained, MIT-or-compatible. Hand-authored stubs are safer than
  cloning a real project.
- **LLM-judge stability.** Haiku 4.5 judging Cortex's own output works fine
  for now, but if Cortex starts shipping multi-paragraph audits the judge
  prompt budget grows. Cache rubric-comparison results per fixture+answer-hash.
- **Hedge regex precision.** "may be" can appear legitimately ("the file may
  be referenced elsewhere as a hint to the user") — false-positive rate
  matters. Validate on a baseline run before treating hedge_count as gospel.
- **Multi-hop final aggregation** (slice 6) might change the trace shape and
  invalidate slice 1's `hop_count` extraction. Land slice 6 first if the
  bug is bothering you, or pin the extractor on the deepest synth turn only.

## References

- Multi-hop architecture: `[[project-multi-hop-via-spawn]]` (memory)
- Scope estimator: `pkg/cognition/dag/ops/sense_estimate_scope.go`
- Synth directive: `internal/harness/dagnode/coding_turn.go` —
  `synthesizerDirective` const
- Existing eval runner: `internal/eval/v2/`
- Trace schema: `dag_traces.jsonl` (per-row JSON, schema_version=2)
- Cell result sink: `.cortex/db/cell_results.jsonl`
