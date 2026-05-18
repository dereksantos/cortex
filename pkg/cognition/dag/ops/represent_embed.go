package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// EmbedConfig wires NewEmbedHandler to an embedder at registration
// time. The embedder may be nil — the handler will then error per call
// rather than crashing, so a registry can be built up before an
// embedder is plumbed in.
type EmbedConfig struct {
	Embedder llm.Embedder
}

// EmbedSpec returns the NodeSpec for represent.embed with the given
// embedder. Mechanical op — no LLM, no spawn. Caller registers it on
// a *dag.Registry.
func EmbedSpec(cfg EmbedConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncRepresent,
		Op:          "embed",
		Description: "embed text into a vector via the configured embedder (mechanical)",
		Inputs: []dag.ParamSpec{
			{Name: "text", Type: "string", Required: false},
			{Name: "texts", Type: "[]string", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "vector", Type: "[]float32"},
			{Name: "vectors", Type: "[][]float32"},
			{Name: "dim", Type: "int"},
		},
		Cost:    embedCostHint,
		Handler: NewEmbedHandler(cfg),
	}
}

// embedCostHint is the registered cost used by the executor's
// pre-spawn budget check. Calibrated against a Hugot-backed embedder
// on darwin/arm64: single-string embed measures 1–5ms p50. Set to 10
// to give a small headroom margin.
var embedCostHint = dag.Cost{LatencyMS: 10, Tokens: 0}

// NewEmbedHandler returns a dag.Handler for represent.embed.
//
// Inputs (exactly one of):
//   - text  (string)
//   - texts ([]string) — batch
//
// Outputs:
//   - vector  ([]float32)    when "text"  was provided
//   - vectors ([][]float32)  when "texts" was provided
//   - dim     (int)          vector dimensionality (0 if no vectors produced)
//
// No LLM. Returns handler_error if no embedder configured or the
// embedder fails. Reports measured wall time as CostConsumed; tokens=0.
func NewEmbedHandler(cfg EmbedConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		if cfg.Embedder == nil || !cfg.Embedder.IsEmbeddingAvailable() {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("represent.embed: no embedder configured")
		}

		if text := readString(in, "text"); text != "" {
			vec, err := cfg.Embedder.Embed(ctx, text)
			if err != nil {
				return dag.NodeResult{
					CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
				}, fmt.Errorf("represent.embed: %w", err)
			}
			return dag.NodeResult{
				Out: map[string]any{
					"vector": vec,
					"dim":    len(vec),
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		if texts, ok := readStringSlice(in, "texts"); ok && len(texts) > 0 {
			vectors := make([][]float32, len(texts))
			dim := 0
			for i, t := range texts {
				vec, err := cfg.Embedder.Embed(ctx, t)
				if err != nil {
					return dag.NodeResult{
						CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
					}, fmt.Errorf("represent.embed[%d]: %w", i, err)
				}
				vectors[i] = vec
				if dim == 0 {
					dim = len(vec)
				}
			}
			return dag.NodeResult{
				Out: map[string]any{
					"vectors": vectors,
					"dim":     dim,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		return dag.NodeResult{
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
		}, fmt.Errorf("represent.embed: must provide non-empty 'text' (string) or 'texts' ([]string)")
	}
}
