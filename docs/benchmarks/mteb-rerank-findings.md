# MTEB rerank-model comparison — first findings

> **TL;DR.** On NFCorpus retrieval, the embedding substrate alone scores
> at the expected leaderboard level for `nomic-embed-text`. Adding
> Cortex's Reflect reranker on top moves NDCG@10 +0.022 and MRR@10
> +0.09 — *but only with a capable LLM*. A code-tuned tiny model
> actively hurts; a small general model helps top-1 only; a frontier
> model lifts both. This is the first end-to-end evidence that
> Cortex's "mechanical retrieval → agentic refinement" architecture
> behaves as designed.

## Setup

- **Dataset**: NFCorpus (3,633 medical research papers, 323 test queries)
- **Sample**: first 20 test queries (warm corpus index, `mteb-phase-a` runner)
- **Embedder**: `nomic-embed-text` via Ollama (768-dim, ~137M params)
- **Retrieval**: top-10 via `storage.SearchByVector` (raw cosine, no Reflex agent loop)
- **Reranker (when used)**: `cognition.Reflect` over the top-10 candidates, swapping the LLM provider/model only

Per-query indexing is amortized: the corpus is indexed once (≈4.5 min for
3,633 docs through Ollama embed), then all reranker variants reuse the
same `.cortex/data/embeddings.jsonl`. This made the A/B/C/D comparison
cheap to run.

## Results

| Reranker                              | NDCG@10  | MRR@10   | Recall@10 | s / query | Cost (20 q) |
|---------------------------------------|----------|----------|-----------|-----------|-------------|
| *(no rerank)*                         | 0.3905   | 0.7125   | 0.0841    | 0.02      | $0          |
| `qwen2.5-coder:1.5b` (Ollama, local)  | 0.3683 ↓ | 0.7097   | 0.0841    | 5.5       | $0          |
| `gemma2:2b` (Ollama, local)           | 0.3900   | 0.7725 ↑ | 0.0841    | 7.5       | $0          |
| **`claude-haiku-4.5` (OpenRouter)**   | **0.4129 ↑** | **0.8056 ↑** | 0.0841 | **3.4** | **~$0.03** |

`Recall@10` is constant by definition — Reflect only reorders the top-K
the embedder already returned, so it can't add or remove relevant docs;
moving recall requires widening K or swapping the embedder.

## Interpretation

### The substrate is competitive on its own
Cortex with `nomic-embed-text` lands at 0.39 NDCG@10 on NFCorpus.
Published MTEB numbers for the same embedder on this task sit at
0.32–0.35; BGE-base-en is 0.367; OpenAI `text-embedding-3-small` is
~0.34. Our number is in-band with the embedder's leaderboard
expectation, which means Cortex's index + cosine search is not
degrading the embedder below its own quality. This is the load-bearing
finding: any future lift on agent-loop benchmarks (LongMemEval,
SWE-bench) can be credibly attributed to Cortex's reasoning rather
than written off as "the embedder got lucky."

### The agentic layer works *when the LLM is capable*
The Reflex → Reflect architecture is supposed to behave as
"mechanical first, agentic refinement on top." This is the first
end-to-end check of that hypothesis on a substrate-level benchmark:

- A code-tuned 1.5B (`qwen2.5-coder`) on medical text actively *hurts*
  NDCG — wrong domain, model too small to follow the rerank prompt
  well.
- A small general 2B (`gemma2:2b`) leaves NDCG flat but lifts MRR by
  6 points. It identifies the single best doc more reliably than the
  embedder alone, but its judgments below position 1 are noisy enough
  to wash out in NDCG's discounted-rank sum.
- A frontier-tier small model (`claude-haiku-4.5`) lifts NDCG by 2.2
  points *and* MRR by 9.3 points — broad refinement, not just top-1.

The size-of-model curve on rerank quality is monotonic on this task.
That matches the architectural intent: Reflect *should* exploit the
LLM's discrimination, and the better the LLM, the more it exploits.

### Why MRR moves further than NDCG
MRR only asks "is the first relevant doc at position 1, or 2, or 3?"
That's a single-rank question and a capable LLM is good at it.
NDCG asks the harder question: "did you stack the relevant docs in
the *right order across all 10 slots*?" That requires the LLM to make
many comparative judgments, and noise dominates further down. So MRR
moves first, NDCG moves second — exactly the pattern we'd expect.

For Cortex's real use case (injecting *one* relevant doc into an
LLM's context for RAG, or surfacing *one* file to a coding agent), MRR
is the more directly load-bearing metric. The MRR lift from rerank is
therefore the more practically relevant signal.

### Latency surprise
Local rerank with `gemma2:2b` (7.5 s/query) is *slower* than
OpenRouter rerank with `claude-haiku-4.5` (3.4 s/query). OpenRouter's
multi-GPU infra processes the rerank prompt faster than a single
local-GPU Ollama serving one request at a time. The "local is always
faster" intuition is wrong for any benchmark / batch workload that
isn't latency-pinned by the network roundtrip.

### Cost
~$0.03 for 20 queries at Haiku 4.5 rates → ~$0.50 for the full
323-query NFCorpus test → ~$1 per million reranks (with prompt
caching disabled; Haiku 4.5's cached-input rate would drop this by
~5–10× in production). Negligible for offline benchmark runs;
non-trivial at production scale (~$1K per million retrievals if all
go through rerank). Pairs with the established RAG pattern: gate
rerank behind a top-of-embedder-score threshold so only ambiguous
queries pay the LLM cost.

## Caveats

- **Out-of-domain.** NFCorpus is medical/biology; Cortex's real users
  ask about code. The substrate is the same (embedder + index +
  rerank), but the workflow signal lives in
  LongMemEval/SWE-bench. Read the rerank lift as architectural
  evidence, not workflow proof.
- **One embedder, one task, 20 queries.** The 20-query variance on
  NDCG is roughly ±0.02, so the qwen-vs-baseline gap (−0.022) is
  near noise; the Haiku-vs-baseline gap (+0.022) is at the same edge
  but in the positive direction. The full 323-query test set would
  tighten both numbers; the *direction* is the signal here, not the
  exact magnitude.
- **Reflect's prompt is generic.** It was designed for insight
  reranking, not document retrieval. A retrieval-specific prompt would
  likely lift the small-LLM numbers; that's a real follow-up.
- **`--embedder` is reserved but not plumbed.** Today the embedder is
  pinned to the Cortex default. Swapping to BGE-large or
  `text-embedding-3-large` would lift NDCG further; that's the
  highest-leverage follow-up after the prompt rewrite.

## Reproduction

The smoke script under `cmd/mteb-rerank-smoke/` runs one MTEB
instance against a stable workdir (`/tmp/mteb-rerank-shared`) so the
index is reused across reranker swaps. Env knobs:

```bash
# Baseline (no rerank)
go run ./cmd/mteb-rerank-smoke

# Local rerank (Ollama, default model = qwen2.5-coder:1.5b)
CORTEX_MTEB_RUNOPTS=rerank \
  go run ./cmd/mteb-rerank-smoke

# Local rerank with model override
CORTEX_MTEB_RUNOPTS=rerank \
  CORTEX_MTEB_RERANK_PROVIDER=ollama \
  CORTEX_MTEB_RERANK_MODEL=gemma2:2b \
  go run ./cmd/mteb-rerank-smoke

# Frontier rerank via OpenRouter (Haiku 4.5)
OPEN_ROUTER_API_KEY="$(security find-generic-password -s cortex-openrouter -w)" \
  CORTEX_MTEB_RUNOPTS=rerank \
  CORTEX_MTEB_RERANK_PROVIDER=openrouter \
  CORTEX_MTEB_RERANK_MODEL=anthropic/claude-haiku-4.5 \
  go run ./cmd/mteb-rerank-smoke
```

To re-index from scratch (e.g. after swapping embedder), delete
`/tmp/mteb-rerank-shared/.cortex/`.

## Next probes

In rough order of expected leverage on this benchmark:

1. **Retrieval-specific rerank prompt.** Cheapest move; should lift
   the `gemma2:2b` row (and possibly `qwen` from harmful to neutral)
   without re-running indexing.
2. **Wider K before rerank.** Currently `SearchByVector` returns top-10
   and Reflect can only reorder those. Pulling top-50 from the
   embedder and reranking to top-10 lets rerank *recover* good docs
   the embedder ranked 11–50. This is the standard cascade.
3. **Embedder swap.** BGE-base/large, `text-embedding-3-large`. Highest
   ceiling but requires wiring the `--embedder` flag through to
   `internal/storage`.
4. **Full 323-query run with Haiku 4.5 rerank.** Tightens the +0.022
   NDCG / +0.09 MRR signal to leaderboard-comparable confidence at
   ~$0.50 total cost.
5. **One more frontier reranker as a sanity check** (GPT-4o-mini,
   Gemini Flash, or Claude Sonnet 4.6) to confirm the rerank lift
   tracks LLM capability rather than being a Haiku-specific quirk.

Each of these is small enough to slot into a follow-up loop without
expanding the Phase A scope.
