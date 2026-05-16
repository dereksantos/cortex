Goal: Ship `internal/eval/benchmarks/mteb/` — fetches a small retrieval task
from the Massive Text Embedding Benchmark (start with NFCorpus, ~3.6K docs),
indexes the corpus into Cortex's storage layer, runs each query through
the Reflex layer (optionally + Reflect for reranking), and computes NDCG@10,
MRR@10, Recall@10 against the gold `qrels`. This scores the embedding +
rerank substrate directly — no agent harness, no LLM judge.

PREREQUISITE: 01-skeleton.md must be merged. This loop registers a benchmark
against the `internal/eval/benchmarks` registry that landed there.

WHY THIS MATTERS: NIAH (02) tells you whether retrieval is *plausible*.
LongMemEval (03) and SWE-bench (04) tell you whether the full agent stack
*helps* on real tasks. MTEB tells you whether the *substrate* — the
embedder, the index, the rerank — is actually competitive. That answers a
different question than the agent-loop benchmarks; without it, lift on
LongMemEval is ambiguous (is Cortex's reasoning helping, or is the
embedder just lucky on this dataset?). MTEB is also the HuggingFace
ecosystem's reference embedding benchmark — registering on it (even on
one task) is leaderboard-comparable signal in the embeddings community
the rest of the field reads.

INVESTIGATE FIRST:
- `internal/eval/benchmarks/{benchmark,registry,cache}.go` — substrate
  from 01-skeleton.
- `internal/storage/storage.go` — what storage backend, what embedder is
  wired in, what's the search API surface. Critical: identify the
  embedder. If it's a small local model (Ollama, hugot), MTEB numbers
  will be embedder-bound, not Cortex-bound — that's still useful but
  must be reported honestly. If there's a flag/config to swap embedders,
  surface it as `--embedder` on the benchmark.
- `internal/cognition/reflect.go` — the Reflect (rerank) entry point. The
  benchmark's reranking option calls this; mechanical scoring needs no
  LLM, but Reflect uses one. Run mechanical baseline by default
  (`--no-rerank` is default); `--rerank` opts in.
- `internal/cognition/cortex.go` — the Cognition orchestrator (`Fast` vs
  `Full`). MTEB doesn't need the agent loop; it talks to storage and
  rerank directly. Look for the lowest-level retrieval entry point that
  returns ranked candidates without going through `cortex_search`.
- MTEB upstream:
  * GitHub: `embeddings-benchmark/mteb` (Apache-2.0 framework)
  * Leaderboard: `https://huggingface.co/spaces/mteb/leaderboard`
  * NFCorpus dataset: `mteb/nfcorpus` on HF Hub
    - Files: `corpus.jsonl.gz`, `queries.jsonl.gz`, `qrels/test.tsv.gz`
    - Format: BEIR-style (corpus: `{_id, title, text}`, queries:
      `{_id, text}`, qrels: `query-id\tcorpus-id\tscore`)
  * Standard metric: NDCG@10 (primary), plus MRR@10, Recall@10
  * Reference Python package: `mteb` (don't use; we compute metrics
    natively in Go).

DEFINITION OF DONE:
1. Package `internal/eval/benchmarks/mteb/` with:
   - `loader.go` —
     * `Load()` honors `LoadOpts.Subset` (interpret as `--tasks NFCorpus`
       for now; only `NFCorpus` accepted in this PR; reject others with
       `"Phase B"` error).
     * Fetches `corpus.jsonl.gz`, `queries.jsonl.gz`, `qrels/test.tsv.gz`
       via skeleton cache from
       `https://huggingface.co/datasets/mteb/nfcorpus/resolve/main/`.
       Streams gzip decompression (don't load 60MB into memory if
       avoidable).
     * Parses into typed structs: `Corpus map[string]Doc`,
       `Queries []Query`, `Qrels map[string]map[string]int`.
     * Returns ONE Instance per task (so `Run()` is called once for
       NFCorpus, not once per query — the corpus indexing cost amortizes
       across queries).
   - `metrics.go` — pure Go implementations:
     ```go
     func NDCG(retrieved []string, qrels map[string]int, k int) float64
     func MRR(retrieved []string, qrels map[string]int, k int) float64
     func Recall(retrieved []string, qrels map[string]int, k int) float64
     ```
   - `metrics_test.go` — table-driven, hand-computed expected values for:
     * Perfect ranking
     * Reversed ranking
     * Empty `retrieved`
     * `retrieved` shorter than k
     * Graded relevance scores (NFCorpus uses 0/1/2)
   - `runner.go` —
     `Run()` per task instance:
     a) Fresh `<workdir>/.cortex/`.
     b) Index the corpus: for each doc, capture + ingest. Batch where
        possible (this is the slow part; expect minutes for NFCorpus).
        Status updates to stderr every N docs so the operator sees
        progress.
     c) For each query (or up to `--limit N` queries if set):
        - Call the lowest-level retrieval API (NOT cortex_search; that
          adds the Resolve layer). Get top-K candidates by id.
        - Optionally rerank via `cognition.Reflect` if `--rerank` set.
        - Compute NDCG@10, MRR@10, Recall@10 for this query using
          metrics.go.
     d) Aggregate per-task means.
     e) Emit ONE `*evalv2.CellResult` per task (NFCorpus):
        - `Benchmark="mteb"`
        - `ScenarioID="mteb/NFCorpus"`
        - `Harness=HarnessCortex` (label, even though no agent harness)
        - `Provider`: report the embedder provider, not the LLM
          provider (this is the substrate under test). Use
          `ProviderLocal` if embedder is in-process; else the matching
          enum value. If no exact match, raise it as an open question.
        - `Model`: the embedder model ID
        - `ContextStrategy=StrategyCortex`
        - `TaskSuccessCriterion=CriterionTestsPassAll`
        - `TaskSuccess`: `NDCG@10 >= <threshold>` where threshold is a
          per-task floor (NFCorpus published numbers vary 0.20-0.35
          depending on embedder; pick 0.20 as a conservative passing
          floor in this PR, documented as "any retrieval better than
          near-random").
        - `TestsPassed=1, TestsFailed=0` accordingly
        - `Notes`: `"NDCG@10=0.X MRR@10=0.Y Recall@10=0.Z queries=N"`
        - `LatencyMs`: total wall time for retrieval (NOT including
          indexing — indexing is one-time amortized).
2. CLI integration in `cmd/cortex/commands/eval.go`:
   - `--benchmark mteb`
   - `--tasks NFCorpus` (required; only `NFCorpus` accepted in this PR)
   - `--limit N` (skeleton flag; bounds the number of queries scored,
     useful for fast smoke runs)
   - `--rerank` (boolean; default false)
   - `--embedder <id>` (optional; if surfaced, plumbs through to whatever
     `internal/storage` accepts; if not — document why and defer)
3. Docs: `docs/benchmarks/mteb.md` —
   - What the benchmark is, dataset license note (Apache-2.0 framework;
     per-dataset licenses vary — NFCorpus is permissive)
   - What this measures vs what NIAH/LongMemEval/SWE-bench measure
     (embedding substrate vs agent stack — different layer)
   - The embedder-bound caveat: numbers reflect both Cortex's index/search
     and the chosen embedder. Pin the embedder in the report.
   - Phase B: reranking-only tasks, classification, STS, the full
     retrieval suite (~15 retrieval tasks in MTEB).
   - Reproduction commands.
4. End-to-end smoke (in-session report):
   - `./cortex eval --benchmark mteb --tasks NFCorpus --limit 100` runs
     100 queries against the full corpus. Report NDCG@10, MRR@10,
     Recall@10.
   - `--rerank` adds maybe 10-20× latency per query; run a short
     comparison (e.g. `--limit 20 --rerank` vs `--limit 20`) and report
     the metric delta.
   - Confirm the single CellResult appears in all three projections with
     `benchmark=mteb`, `scenario_id=mteb/NFCorpus`, and the `Notes` field
     carries the three metrics.

CONSTRAINTS:
- Standard library testing only. Metric implementations are the highest-
  value test surface — hand-compute expected values from small example
  inputs.
- Do NOT vendor the NFCorpus files into the repo (BEIR-style datasets
  are typically ~10MB compressed; cache them under
  `~/.cortex/benchmarks/mteb/nfcorpus/`).
- Do NOT use the upstream Python `mteb` package — we compute metrics
  natively in Go. This keeps Python out of the build path and lets the
  benchmark run anywhere Cortex runs.
- This benchmark does NOT use the agent harness. Do not pull it in for
  symmetry with other benchmarks — it adds latency and noise for no
  measurement value at this layer.
- Cost: this benchmark has zero LLM cost when `--rerank` is off, and
  cheap LLM cost (one short rerank per query) when on. The spend ceiling
  still applies via the existing `Persister` path.
- Embedder identity matters and MUST be reported in the per-task `Notes`
  field. A 0.25 NDCG@10 with embedder X is a different result from 0.25
  with embedder Y.

DELIVERABLE: a branch `feat/bench-mteb` off main (or stacked on
`feat/benchmarks-skeleton`), commits for the package + metrics + CLI +
docs + tests, end-to-end smoke run report:
- NDCG@10, MRR@10, Recall@10 on NFCorpus full or `--limit 100`
- The embedder used (model ID + provider)
- Indexing time, retrieval time (per query mean), total wall time
- Honest assessment: "Cortex on NFCorpus with embedder X scores
  NDCG@10=0.YY. MTEB leaderboard reference for this embedder is ZZ. Gap
  is +/- N pp; suspect cause: indexing/normalization/rerank/embedder
  choice."
- Any open question on embedder choice or rerank wiring that needs to
  feed back to a future loop (e.g. "swap to a stronger embedder for
  leaderboard parity runs").
