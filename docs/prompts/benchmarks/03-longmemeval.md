Goal: Ship `internal/eval/benchmarks/longmemeval/` — fetches the LongMemEval
Oracle split from HuggingFace Hub, hydrates each instance's multi-session
haystack into a per-eval `.cortex/` store, asks the question through the
in-process coding harness with `cortex_search` enabled, and scores the
response via LLM-as-judge using LongMemEval's reference prompt.

PREREQUISITE: 01-skeleton.md must be merged. This loop registers a benchmark
against the `internal/eval/benchmarks` registry that landed there and reuses
the in-process harness from PR #29.

WHY THIS MATTERS: LongMemEval is the closest published benchmark to what
Cortex's memory layer is built to do — cross-session knowledge retention,
temporal reasoning, abstention, multi-hop. The Oracle split (evidence
sessions only) is the smallest and cheapest entry point; it produces the
first cross-comparable industry number on Cortex's memory thesis. The paper
defines five ability axes (extraction, multi-session reasoning, temporal,
knowledge updates, abstention); reporting per-axis pass rates lets us argue
about *which* memory ability Cortex helps with, not just an aggregate.

PRIOR INSIGHT (already in the repo): `docs/prompts/llm-judge-locomo.md`
implemented LoCoMo-style scenarios and the `--judge` scoring path. That
work covered the LoCoMo dataset shape and an `--judge` flag in
`internal/eval/v2/eval.go`. This loop adds the *real* LongMemEval dataset
fetcher and a per-question-type score breakdown. Reuse the judge wiring
that already exists; do not rebuild it.

INVESTIGATE FIRST:
- `internal/eval/benchmarks/{benchmark,registry,cache}.go` — the substrate
  from 01-skeleton.
- `internal/eval/v2/eval.go` and any `ScoreWithJudge`-like function added
  by `llm-judge-locomo.md`. The judge prompt template, judge provider
  resolution, and the judge result struct already exist; reuse them.
- `internal/harness/loop.go` and `internal/harness/tool_cortex_search.go`
  — the in-process harness Cortex now drives. `evalv2.NewCortexHarness(model)`
  is the constructor (see `internal/eval/v2/library_service_cortex_harness.go`).
- `internal/capture/capture.go` — in-process capture for haystack
  hydration. Do NOT shell out to `cortex capture` per turn; that's slow
  enough to make the Oracle split painful.
- `internal/eval/v2/coding_runner.go` — the orchestrator pattern for
  per-attempt CellResult emission. LongMemEval mirrors the single-attempt
  case (no retry loop).
- `pkg/llm/openrouter_with_key.go` + `pkg/secret/keychain.go` — judge
  provider resolution. The OpenRouter key is in the macOS keychain
  (memory says `security find-generic-password -s cortex-openrouter -w`).
- `~/.cortex/benchmarks/longmemeval/` will be the cache target (skeleton's
  `cache.go`).
- LongMemEval upstream:
  * HF dataset: `xiaowu0162/longmemeval-cleaned` (MIT)
  * Three files: `longmemeval_oracle.json`, `longmemeval_s_cleaned.json`,
    `longmemeval_m_cleaned.json`
  * Per-instance fields: `question_id`, `question_type`, `question`,
    `answer`, `question_date`, `haystack_session_ids`, `haystack_dates`,
    `haystack_sessions` (list of user/assistant turns; some carry
    `has_answer`), `answer_session_ids`
  * Reference judge: `evaluate_qa.py` in the upstream repo; canonical
    judge is GPT-4o. We will deviate to a Cortex-internal default and
    document it.

DEFINITION OF DONE:
1. Package `internal/eval/benchmarks/longmemeval/` with:
   - `loader.go` —
     * `Load()` honors `LoadOpts.Subset` (`oracle` only in this PR; reject
       `s`/`m` with `"Phase B"` error message), `LoadOpts.Limit`,
       and a `--question-type` filter (one of `single-hop|multi-hop|
       temporal|knowledge-update|abstention` — exact strings from the
       upstream `question_type` field; verify against the JSON).
     * Fetches `longmemeval_oracle.json` via the skeleton's `cache.go`.
       HF Hub URL: `https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_oracle.json`.
     * Parses into a typed Instance per question.
   - `runner.go` —
     `Run()` per instance:
       a) Fresh `<workdir>/.cortex/`.
       b) For each session in `haystack_sessions`, for each turn, capture
          via `internal/capture` with timestamp from `haystack_dates`
          parallel array. Use a single category (decision/observation —
          pick what fits least awkwardly and document) and tag the
          capture with `session_id`.
       c) Ingest in-process.
       d) Construct `evalv2.NewCortexHarness(model)` and call
          `RunSessionWithResult(ctx, instance.Question, workdir)`. The
          `cortex_search` tool is registered by default (good).
       e) Extract the final assistant message from the harness result.
       f) Score via judge: pass the question, gold answer, and hypothesis
          to the existing judge path. Judge model defaults to
          `anthropic/claude-haiku-4.5` via OpenRouter (document deviation
          from upstream's GPT-4o reference).
       g) Emit `*evalv2.CellResult`:
          - `Benchmark="longmemeval"`
          - `ScenarioID="longmemeval/<question_id>"`
          - `SessionID=""` (single-turn, no multi-attempt)
          - `Harness=HarnessCortex`, `Provider=ProviderOpenRouter`,
            `Model=<flag>`, `ContextStrategy=StrategyCortex`
          - `TaskSuccessCriterion=CriterionJudgeLLM`
          - `TaskSuccess=<judge verdict>`
          - `TokensIn/Out` from harness result; `InjectedContextTokens`
            from harness result
          - `Notes` records `question_type=...` so downstream rollups can
            group by ability axis.
   - `judge.go` — if the existing `ScoreWithJudge` is too generic to reuse
     cleanly, add a thin `LongMemEvalJudge` adapter that ports the
     reference prompt from `evaluate_qa.py`. Otherwise reuse and document
     the reuse.
   - `judge_test.go` — golden test on the judge prompt template (so it
     doesn't drift silently), one round-trip with a mock provider, the
     parser of the judge's JSON/text verdict.
2. Baseline split for lift measurement:
   - Add a `--strategy baseline,cortex` (comma-separated, defaults to
     `cortex`). When `baseline` is selected, the runner builds the harness
     with the `cortex_search` tool *not* registered (or returning empty),
     does NOT hydrate the haystack, and sets `ContextStrategy=StrategyBaseline`
     on the emitted cell. This is the apples-to-apples baseline.
   - Running with `--strategy baseline,cortex` emits TWO cells per
     instance — that's the design.
3. CLI integration in `cmd/cortex/commands/eval.go`:
   - `--benchmark longmemeval` activates this benchmark
   - `--subset oracle` (required; only `oracle` accepted in this PR)
   - `--question-type single-hop|multi-hop|temporal|knowledge-update|abstention`
     (optional; repeatable)
   - `--limit N` (skeleton flag)
   - `--strategy baseline,cortex` (per above)
   - `--judge` enables judging; `--judge-model <openrouter-id>` overrides
     default
   - `--model <openrouter-id>` selects the model under test (e.g.
     `anthropic/claude-haiku-4.5`, `qwen/qwen3-coder`)
4. Docs: `docs/benchmarks/longmemeval.md` —
   - What the benchmark is, dataset license (MIT), upstream paper
   - Why we use the Oracle split for Phase A and what's deferred to Phase B
   - The judge-model deviation (we use Claude Haiku 4.5, paper uses GPT-4o);
     surface the `--judge-model` flag for parity runs
   - How to interpret per-ability-axis pass rates
   - Reproduction commands
5. End-to-end smoke (in-session report):
   - Run `--limit 5 --strategy baseline,cortex --judge`. Report the pass
     rate per ability axis for both strategies. Compute lift.
   - Confirm CellResults show in journal + JSONL + SQLite with
     `benchmark=longmemeval`.

CONSTRAINTS:
- Standard library testing only. The judge prompt golden test is the
  highest-value test surface; spend budget there.
- Honor the spend ceiling (`internal/eval/v2/spend.go`). Haiku 4.5 +
  Oracle is cheap, but full-split runs aren't — the limit-by-default
  posture matters. Hard-fail with a clear message if the projected cost
  exceeds the cap.
- Fetch dataset at runtime via skeleton's cache; do NOT vendor JSON into
  the repo (MIT-licensed but 500 questions is big and noisy in diffs).
- Use the existing `--judge` infrastructure. Do NOT introduce a parallel
  judging path.
- Per-question-type breakdown is reported at the session level (stdout +
  the in-session report), NOT a new SQLite column. The `Notes` field
  carries the axis label and downstream report tooling can group on it.
- No backwards-compat shims; if you find a real issue with the existing
  judge path that requires breaking it, raise it explicitly and propose
  a separate refactor PR — don't rewrite it inside this loop.

DELIVERABLE: a branch `feat/bench-longmemeval` off main (or stacked on
`feat/benchmarks-skeleton`), commits for the package + CLI flags + docs +
tests, end-to-end smoke run report:
- Per-axis pass rate for baseline and cortex strategies
- Lift per axis (cortex − baseline)
- Cost in USD and wall-clock time for the smoke run
- Honest assessment: "Cortex lifts X by N pp; flat on Y; regression on Z.
  Suspect cause: ..."
- Any open question to feed back to the skeleton owner or to defer to
  Phase B (S/M splits, parity judge runs against GPT-4o, etc.)
