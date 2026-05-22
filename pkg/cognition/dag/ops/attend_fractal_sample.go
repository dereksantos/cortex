package ops

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// FractalSampleConfig wires the attend.fractal_sample op to a default
// Sampler. The default ships as a HierarchicalSampler; a future Lévy
// or RWR sampler can replace it without touching the controller.
type FractalSampleConfig struct {
	DefaultSampler study.Sampler
}

// FractalSampleSpec returns the NodeSpec for attend.fractal_sample.
// Mechanical (no LLM); deterministic given (boundary_output, covered,
// k, rng_seed).
func FractalSampleSpec(cfg FractalSampleConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "fractal_sample",
		Description: "hierarchical/fractal chunk sampler over a BoundaryOutput (Tier 1: hierarchical)",
		Inputs: []dag.ParamSpec{
			{Name: "boundary_output", Type: "*study.BoundaryOutput", Required: true},
			{Name: "covered", Type: "map[string]bool", Required: false},
			{Name: "k", Type: "int", Required: false},
			{Name: "rng_seed", Type: "int64", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "chunk_ids", Type: "[]string"},
			{Name: "chunks", Type: "[]study.Chunk"},
			{Name: "sampler", Type: "string"},
		},
		Cost:    dag.Cost{LatencyMS: 50, Tokens: 0},
		Handler: NewFractalSampleHandler(cfg),
	}
}

// NewFractalSampleHandler returns the handler for attend.fractal_sample.
//
// Inputs:
//   - boundary_output (*study.BoundaryOutput) — required
//   - covered (map[string]bool)                   — default empty
//   - k (int)                                     — default 4
//   - rng_seed (int64)                            — default boundary_output.RNGSeed
//
// Outputs:
//   - chunk_ids ([]string)         — up to k IDs
//   - chunks ([]study.Chunk)   — hydrated chunks in chunk_ids order
//   - sampler (string)             — Name() of the sampler that ran
func NewFractalSampleHandler(cfg FractalSampleConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		out, ok := in["boundary_output"].(*study.BoundaryOutput)
		if !ok || out == nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.fractal_sample: 'boundary_output' (*study.BoundaryOutput) is required")
		}

		covered := readCoveredSet(in, "covered")
		k := readInt(in, "k", 4)
		seed := readInt64(in, "rng_seed", 0)
		if seed == 0 {
			seed = out.RNGSeed
		}

		sampler := cfg.DefaultSampler
		if sampler == nil {
			sampler = &study.HierarchicalSampler{}
		}

		rng := rand.New(rand.NewSource(seed))
		ids := sampler.Next(out, covered, k, rng)

		// Hydrate chunks ordered to match ids (rather than the
		// BoundaryOutput's sorted-by-rel order).
		chunkByID := make(map[string]study.Chunk, len(out.Chunks))
		for _, c := range out.Chunks {
			chunkByID[c.ID] = c
		}
		ordered := make([]study.Chunk, 0, len(ids))
		for _, id := range ids {
			if c, ok := chunkByID[id]; ok {
				ordered = append(ordered, c)
			}
		}

		return dag.NodeResult{
			Out: map[string]any{
				"chunk_ids": ids,
				"chunks":    ordered,
				"sampler":   sampler.Name(),
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
		}, nil
	}
}

// readCoveredSet extracts a map[string]bool from in[key]. Accepts a
// native map[string]bool, a map[string]any (truthy-keys only), or a
// []string (treat all as covered=true).
func readCoveredSet(in map[string]any, key string) map[string]bool {
	v, ok := in[key]
	if !ok {
		return map[string]bool{}
	}
	switch x := v.(type) {
	case map[string]bool:
		return x
	case map[string]any:
		out := make(map[string]bool, len(x))
		for k, vv := range x {
			if b, ok := vv.(bool); ok && b {
				out[k] = true
			}
		}
		return out
	case []string:
		out := make(map[string]bool, len(x))
		for _, id := range x {
			out[id] = true
		}
		return out
	}
	return map[string]bool{}
}

// readInt64 mirrors readInt but produces int64. Accepts int / int64 /
// float64 (YAML decodes numbers as float64).
func readInt64(in map[string]any, key string, def int64) int64 {
	if v, ok := in[key]; ok {
		switch x := v.(type) {
		case int64:
			return x
		case int:
			return int64(x)
		case float64:
			return int64(x)
		}
	}
	return def
}
