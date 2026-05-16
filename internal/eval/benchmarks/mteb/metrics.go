// Package mteb implements the MTEB (Massive Text Embedding Benchmark)
// retrieval evaluator for Cortex's embedding + rerank substrate.
//
// Phase A — this PR — wires NFCorpus (the smallest BEIR retrieval task,
// ~3.6K docs / 323 queries) end-to-end: load corpus + queries + qrels
// from HuggingFace, index the corpus into a fresh Cortex storage, run
// each query through the lowest-level vector search, optionally rerank
// via cognition.Reflect, and score NDCG@10, MRR@10, Recall@10.
//
// Phase B — future loops — registers the rest of the BEIR retrieval
// suite (~15 tasks) plus the reranking-only, classification, and STS
// task families.
//
// MTEB measures the *substrate* — what the embedder + index can recall
// before any agent loop runs on top. It is not a replacement for
// NIAH/LongMemEval/SWE-bench; it is the floor under them. A low MTEB
// score with a stronger downstream score means the agent is compensating;
// a high MTEB score with a weak downstream score means the retrieval
// quality is being lost in Reflect/Resolve/synthesis.
package mteb

import (
	"math"
	"sort"
)

// NDCG returns the Normalized Discounted Cumulative Gain at rank k.
// Relevance grades come from qrels (e.g. NFCorpus uses {0, 1, 2}).
// Following BEIR convention, k≤0 is interpreted as "rank over the
// full retrieved list" rather than returning 0.
//
// Returns 0 when retrieved is empty, when no positive-relevance qrels
// exist, or when the ideal-DCG denominator is 0 (avoids NaN on edge
// cases the caller may aggregate over many queries).
func NDCG(retrieved []string, qrels map[string]int, k int) float64 {
	if len(retrieved) == 0 || len(qrels) == 0 {
		return 0
	}
	if k <= 0 || k > len(retrieved) {
		k = len(retrieved)
	}

	// DCG over the retrieved ranking (positions 1..k).
	var dcg float64
	for i := 0; i < k; i++ {
		rel := qrels[retrieved[i]]
		if rel <= 0 {
			continue
		}
		// Position discount log2(i+2): rank 1 → log2(2)=1, rank 2 → log2(3), …
		dcg += float64(rel) / math.Log2(float64(i)+2)
	}

	// IDCG: the DCG of the best possible ranking under the same k.
	// Sort all positive-relevance qrels descending, take the top k.
	rels := make([]int, 0, len(qrels))
	for _, r := range qrels {
		if r > 0 {
			rels = append(rels, r)
		}
	}
	if len(rels) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.IntSlice(rels)))
	if k < len(rels) {
		rels = rels[:k]
	}
	var idcg float64
	for i, r := range rels {
		idcg += float64(r) / math.Log2(float64(i)+2)
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// MRR returns the Mean Reciprocal Rank at k: 1/(rank of first relevant
// doc within top-k), or 0 if no relevant doc lands within k.
// Relevance grade 0 is treated as not-relevant (matches BEIR/MTEB).
func MRR(retrieved []string, qrels map[string]int, k int) float64 {
	if len(retrieved) == 0 || len(qrels) == 0 {
		return 0
	}
	if k <= 0 || k > len(retrieved) {
		k = len(retrieved)
	}
	for i := 0; i < k; i++ {
		if qrels[retrieved[i]] > 0 {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// Recall returns Recall@k: (# relevant in top-k) / (total relevant).
// Grade-0 entries in qrels are treated as not-relevant for both the
// numerator and denominator, so a query whose qrels are entirely 0
// returns 0 (no judged relevant docs to recover).
func Recall(retrieved []string, qrels map[string]int, k int) float64 {
	if len(retrieved) == 0 || len(qrels) == 0 {
		return 0
	}
	var totalRelevant int
	for _, r := range qrels {
		if r > 0 {
			totalRelevant++
		}
	}
	if totalRelevant == 0 {
		return 0
	}
	if k <= 0 || k > len(retrieved) {
		k = len(retrieved)
	}
	var hit int
	for i := 0; i < k; i++ {
		if qrels[retrieved[i]] > 0 {
			hit++
		}
	}
	return float64(hit) / float64(totalRelevant)
}
