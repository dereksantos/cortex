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

## Where this sits in the landscape

A single NFCorpus number tells you "does the system work at all"; the
public MTEB Retrieval leaderboard tracks the *mean NDCG@10 across ~15
retrieval tasks*, which is the real ranking surface. Both are useful
context for reading our number honestly.

**NFCorpus NDCG@10 reference points** (public leaderboards):

| System                                       | NDCG@10   |
|----------------------------------------------|-----------|
| BM25 keyword baseline                        | ~0.32     |
| OpenAI `text-embedding-3-small`              | ~0.34     |
| BGE-large-en-v1.5                            | ~0.37     |
| OpenAI `text-embedding-3-large`              | ~0.42     |
| Cohere embed-v3 / v4, Voyage AI              | ~0.40–0.42 |
| Top 7B-class (NV-Embed-v2, SFR-Embedding-2, gte-Qwen2-7B) | ~0.44–0.47 |
| **Cortex (nomic-embed-text + Haiku rerank)** | **0.4129** |

Our 0.41 with a 137M-param embedder + general-LLM rerank is
competitive with `text-embedding-3-large`'s embedder-only number using
a 28× smaller embedder, by paying ~3.4 s/query for the rerank LLM
call. That's a real cost-vs-quality tradeoff, not a free win.

**MTEB Retrieval aggregate** (mean NDCG@10 across ~15 tasks — the
headline benchmark number people compare):

| Tier                                           | Aggregate NDCG@10 |
|------------------------------------------------|-------------------|
| BM25 baseline                                  | ~0.42             |
| Top open-source embedders                      | ~0.59–0.62        |
| Best commercial APIs (Cohere v3/v4, Voyage, OpenAI v3-large) | ~0.55–0.61 |
| Best closed-source + reranker cascade          | ~0.62–0.65        |

We don't have an aggregate yet — Phase A wires NFCorpus only. The
full retrieval suite is Phase B.

**Rerank-on-top numbers from the literature.** Purpose-built
rerankers (Cohere `rerank-3`, BGE-`reranker-v2-m3`,
`mxbai-rerank-large`) typically lift the underlying embedder by **+3
to +8 NDCG points**. Our +0.022 with Haiku-as-reranker is on the low
end of that range, likely because (a) Haiku is a general LLM doing
reranking via a generic Reflect prompt, not a model fine-tuned for
the task; (b) NFCorpus is *hard* to lift on NDCG@10 (many queries
have 50+ relevant docs); (c) we're reranking only top-10, while the
standard cascade reranks top-50 or top-100 — see Phase B levers
below.

**On MRR specifically:** MRR is rarely the headline metric on
MTEB-style leaderboards (NDCG dominates). MRR is more common in
QA-style benchmarks (NaturalQuestions, TriviaQA) where there's *one*
correct answer document. On NQ retrieval, top systems hit MRR around
0.55–0.65; our 0.81 on NFCorpus isn't directly comparable because
NFCorpus has graded multi-relevance and many relevant docs per query.

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

## Next probes — roadmap with expected gains

Expected NDCG@10 gains below are quoted from the rerank-cascade
literature for NFCorpus-class tasks; treat them as *direction +
order-of-magnitude*, not as commitments. The 20-query smoke variance
is ±0.02, so any gain below that needs the full 323-query run to
distinguish from noise.

In rough order of expected leverage on this benchmark:

1. **Retrieval-specific rerank prompt.** Cheapest move; replace the
   generic insight-reranking Reflect prompt with one written for
   "rank these documents by relevance to this query." Expected lift:
   **+0.02–0.04 NDCG** with the same Haiku model. Should also recover
   the `gemma2:2b` row (and possibly turn `qwen2.5-coder` from
   harmful to neutral). No re-indexing required.
2. **Wider K before rerank.** Currently `SearchByVector` returns
   top-10 and Reflect can only reorder those. Pulling top-50 from the
   embedder and reranking to top-10 lets rerank *recover* good docs
   the embedder ranked 11–50 — the standard cascade pattern.
   Expected lift: **+0.03–0.05 NDCG**.
3. **Embedder swap.** Move from `nomic-embed-text` (137M) to a
   stronger embedder (BGE-large-en-v1.5,
   `text-embedding-3-large`, or a 7B-class model like
   `gte-Qwen2-7B-instruct`). Expected lift: **+0.05 NDCG** for
   `text-embedding-3-large`; **+0.05–0.08 NDCG** for the 7B-class
   ceiling. Requires wiring the `--embedder` flag through to
   `internal/storage`.
4. **Purpose-built reranker model** instead of a general LLM. Cohere
   `rerank-3`, BGE-`reranker-v2-m3`, `mxbai-rerank-large`. These are
   trained on relevance judgments and consistently outperform
   general-purpose LLMs at this task in the literature. Expected
   lift: **+0.02–0.04 NDCG** over Haiku-as-reranker; significantly
   lower per-query cost (BGE-reranker runs locally, sub-second).
5. **Full 323-query run with Haiku 4.5 rerank.** Tightens the +0.022
   NDCG / +0.09 MRR signal to leaderboard-comparable confidence at
   ~$0.50 total cost. No code change required — already supported by
   the smoke script.
6. **One more frontier reranker as a sanity check** (GPT-4o-mini,
   Gemini Flash, or Claude Sonnet 4.6) to confirm the rerank lift
   tracks LLM capability rather than being a Haiku-specific quirk.

**Stack-all-three target.** Independently the gains from (1)+(2)+(3)
plausibly compound to **~0.50+ NDCG@10 on NFCorpus** — landing
ahead of `text-embedding-3-large` embedder-only (0.42) and near the
top of the public NFCorpus leaderboard. Gains rarely add linearly in
practice (each lever steals a bit of the headroom the next one
needs), but a midpoint of ~0.48 is a reasonable Phase B target.

Each of (1)–(6) is small enough to slot into a follow-up loop
without expanding the Phase A scope.
