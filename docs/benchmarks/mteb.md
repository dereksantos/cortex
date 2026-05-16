# MTEB — embedding + rerank substrate benchmark

MTEB (Massive Text Embedding Benchmark) scores Cortex's retrieval
*substrate* — the embedder, the vector index, and (optionally) the
Reflect reranker — against the same datasets the embedding community
publishes leaderboard numbers on.

This complements, rather than replaces, the agent-loop benchmarks:

- **NIAH** answers: is retrieval *plausible* at long context?
- **MTEB** answers: is the retrieval *substrate* competitive?
- **LongMemEval** answers: does the full memory system *help* on real
  questions?
- **SWE-bench Verified** answers: does Cortex-augmented context lift
  end-to-end coding success?

Without MTEB, lift on LongMemEval or SWE-bench is ambiguous — is
Cortex's reasoning helping, or is the embedder just lucky on the
dataset? MTEB pins the substrate-level signal so the agent-loop
benchmarks can be read as agent contributions.

## What this PR wires

Phase A. One BEIR retrieval task: **NFCorpus** (~3.6K docs, 323 test
queries, graded relevance 0/1/2). Computes the three headline MTEB
retrieval metrics:

- **NDCG@10** (primary)
- **MRR@10**
- **Recall@10**

The score is the per-query mean across queries that have non-empty
qrels. Queries with no judged docs are skipped (MTEB convention).

## Dataset

Source: `https://huggingface.co/datasets/mteb/nfcorpus/resolve/main/`

| File                | Format                                            |
|---------------------|---------------------------------------------------|
| `corpus.jsonl.gz`   | `{"_id", "title", "text"}` per line               |
| `queries.jsonl.gz`  | `{"_id", "text"}` per line                        |
| `qrels/test.tsv.gz` | TSV header + `query-id\tcorpus-id\tscore` rows    |

The loader streams gzip decompression directly off the cached file —
nothing is unpacked to disk other than `.gz` originals.

## License

- **MTEB framework**: Apache-2.0 (`embeddings-benchmark/mteb`)
- **NFCorpus**: permissive; see the upstream dataset card

The reference Python `mteb` package is *not* a build-time dependency.
We compute NDCG / MRR / Recall natively in Go so the benchmark runs
anywhere Cortex runs, with no Python in the path.

## How retrieval is wired

The benchmark deliberately *bypasses* the Reflex / Resolve agent loop:

1. Fresh per-Run `<workdir>/.cortex/` storage so the operator's real
   `~/.cortex` is never touched.
2. For each corpus doc: `embedder.Embed(title + text)` →
   `storage.StoreEmbedding(doc_id, "corpus", vec)`.
3. For each query: `embedder.Embed(text)` →
   `storage.SearchByVector(qvec, 10, 0.0)`. Returned `ContentID`s are
   the ranked list scored against `qrels`.
4. Optional reranking via `cognition.Reflect` when `--rerank` is set.
   Reranking errors fall through to the unreranked order so a flaky
   LLM does not tank the run.

Indexing latency is reported separately from retrieval latency — only
retrieval lands in `CellResult.LatencyMs` because indexing is a
one-time amortization that would dwarf real per-query cost.

## The embedder-bound caveat

MTEB numbers reflect *both* Cortex's index/search behavior and the
chosen embedder. A 0.25 NDCG@10 with embedder X is a different result
from 0.25 with embedder Y. The embedder used appears in
`CellResult.Notes` for every run:

```
NDCG@10=0.XXXX MRR@10=0.YYYY Recall@10=0.ZZZZ queries=N embedder=<provider>/<model> …
```

Phase A defaults to Cortex's standard local embedder
(`sentence-transformers/all-MiniLM-L12-v2` via Hugot, 384-dim). A
stronger embedder will shift the headline number without any change
to the runner — that's the substrate the benchmark is measuring.

## Reproduction

Smoke run (~100 queries, fast):

```
cortex eval --benchmark mteb --tasks NFCorpus --limit 100
```

Full NFCorpus test set:

```
cortex eval --benchmark mteb --tasks NFCorpus
```

Compare rerank vs no-rerank on the first 20 queries:

```
cortex eval --benchmark mteb --tasks NFCorpus --limit 20
cortex eval --benchmark mteb --tasks NFCorpus --limit 20 --rerank
```

Reranking adds roughly an LLM call per query (10-20× retrieval latency
typical). The CellResult `LatencyMs` reflects the wall-clock retrieval
time per cell — divide by `queries=N` from `Notes` for per-query mean.

## CLI flags

| Flag              | Notes                                              |
|-------------------|----------------------------------------------------|
| `--tasks NAME`    | Only `NFCorpus` accepted in Phase A; others error  |
| `--limit N`       | Caps the number of queries scored (not the corpus) |
| `--rerank`        | Pass top-K through `cognition.Reflect`             |
| `--embedder ID`   | Reserved; not yet plumbed to `internal/storage`    |
| `-m / --model`    | **Rejected** — MTEB measures the embedder, not LLM |

## First findings

See [`mteb-rerank-findings.md`](mteb-rerank-findings.md) for the
end-to-end Phase A comparison of `nomic-embed-text` (embedder-only)
vs three rerank-LLM choices (qwen2.5-coder:1.5b, gemma2:2b,
claude-haiku-4.5). Headline: Cortex's mechanical retrieval lands at
the embedder's leaderboard expectation, and frontier-tier rerank lifts
NDCG@10 +0.022 / MRR@10 +0.09 on top of that.

## Phase B — what's deferred

- The rest of the BEIR retrieval suite (~15 tasks: SciFact, FiQA,
  NQ, HotpotQA, …). Each task plugs into the same loader/runner once
  its URL + qrels format match.
- MTEB reranking-only tasks (the "RetrievalReranking" family) — only
  the Reflect-driven portion of the pipeline is exercised; corpus
  indexing is replaced with the supplied candidate list.
- Classification, STS, clustering tasks. These measure embedding
  quality at the wrong layer for Cortex (we are a retrieval system,
  not an encoder benchmark target). Probably never wired.
- A `--embedder` switch wired to a real swap point in
  `internal/storage` — currently the default Hugot model is hardcoded
  in `embedderFactory`; surfacing a real switch waits for the
  storage layer to expose per-instance embedder selection.

## Where the result lands

One `CellResult` per task per Run, persisted through the standard
`Persister`:

- `benchmark = "mteb"`
- `scenario_id = "mteb/NFCorpus"`
- `harness = "cortex"` (label — no agent harness runs at this layer)
- `provider = "local"` (the *embedder* provider; not the LLM)
- `model = "<embedder model ID>"`
- `context_strategy = "cortex"`
- `task_success_criterion = "tests_pass_all"`
- `task_success = (NDCG@10 >= 0.20)` — conservative passing floor
  documented as "any retrieval better than near-random"
- `latency_ms` — retrieval time only, not indexing
- `notes` — the headline metrics + embedder + index/retrieve wall time
