package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// VectorSearchConfig wires NewVectorSearchHandler to a storage layer.
// Storage may be nil at registration time — the handler errors at call
// time rather than crashing, so wiring can be deferred.
type VectorSearchConfig struct {
	Storage *storage.Storage
}

// VectorSearchSpec returns the NodeSpec for remember.vector_search.
// Mechanical op — no LLM, no spawn.
func VectorSearchSpec(cfg VectorSearchConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncRemember,
		Op:          "vector_search",
		Description: "find content similar to a query vector via storage (mechanical)",
		Inputs: []dag.ParamSpec{
			{Name: "query_vector", Type: "[]float32", Required: true},
			{Name: "limit", Type: "int", Required: false},
			{Name: "threshold", Type: "float64", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "results", Type: "[]VectorSearchResult"},
			{Name: "ids", Type: "[]string"},
			{Name: "count", Type: "int"},
		},
		Cost:    vectorSearchCostHint,
		Handler: NewVectorSearchHandler(cfg),
	}
}

// vectorSearchCostHint — in-memory cosine over the embeddings map is
// O(N) but tiny per item; measured p50 on 100 embeddings is 1–3ms.
// Set 15ms for headroom on larger corpora.
var vectorSearchCostHint = dag.Cost{LatencyMS: 15, Tokens: 0}

// NewVectorSearchHandler returns a dag.Handler for remember.vector_search.
//
// Inputs:
//   - query_vector ([]float32) — required
//   - limit (int)              — default 10
//   - threshold (float64)      — default 0.0
//
// Outputs:
//   - results ([]storage.VectorSearchResult) — sorted by similarity DESC
//   - ids ([]string)                          — convenience: just the content IDs
//   - count (int)                             — len(results)
//
// No LLM. Returns handler_error if storage not configured or
// query_vector missing/empty.
func NewVectorSearchHandler(cfg VectorSearchConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		if cfg.Storage == nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("remember.vector_search: storage not configured")
		}

		vec, ok := readFloat32Slice(in, "query_vector")
		if !ok || len(vec) == 0 {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("remember.vector_search: 'query_vector' ([]float32) is required and must be non-empty")
		}
		limit := readInt(in, "limit", 10)
		threshold := readFloat64(in, "threshold", 0.0)

		results, err := cfg.Storage.SearchByVector(vec, limit, threshold)
		if err != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("remember.vector_search: %w", err)
		}
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ContentID
		}
		return dag.NodeResult{
			Out: map[string]any{
				"results": results,
				"ids":     ids,
				"count":   len(results),
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
		}, nil
	}
}
