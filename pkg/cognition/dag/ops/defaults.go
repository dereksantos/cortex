package ops

import (
	"context"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// DefaultsConfig wires the default op set to its real dependencies.
// Any nil field disables the relevant op's LLM/storage path; the op
// falls back to its mechanical default (or, for sense.prompt /
// maintain.capture, the v0 stub) and the executor still walks.
type DefaultsConfig struct {
	Provider llm.Provider     // for LLM-backed ops (attend, value.*, decide.*, model.*, maintain.extract_insight)
	Embedder llm.Embedder     // for represent.embed
	Storage  *storage.Storage // for remember.vector_search
}

// RegisterDefaults registers the Stage 2 op set onto reg, plus the
// two still-stubbed chain pieces (sense.prompt + maintain.capture)
// that aren't in the 9-op set yet — maintain.capture's real
// implementation lands in Stage 3 per the build plan.
//
// The 11 registered ops:
//
//	sense.prompt                — stub (trigger)
//	represent.embed             — mechanical (embedder)
//	remember.vector_search      — mechanical (storage)
//	attend.rerank               — LLM
//	value.score                 — LLM
//	value.detect_contradiction  — LLM
//	decide.inject               — LLM
//	decide.should_capture       — LLM
//	model.predict_next          — LLM
//	maintain.extract_insight    — LLM
//	maintain.capture            — stub (Stage 3)
//
// All ops are registered without chain-wiring spawn relationships —
// callers compose chains by wrapping handlers (see
// cmd/cortex/commands/run.go's buildTurnRegistry for the
// `--type=turn` chain).
//
// Returns the count of ops registered (useful for tools.json
// generation and registry validation).
func RegisterDefaults(reg *dag.Registry, cfg DefaultsConfig) (int, error) {
	specs := []dag.NodeSpec{
		// Stub: sense.prompt is the ingress trigger; no handler logic
		// yet (Stage 3 will give it real session-context plumbing).
		{
			Function:    dag.FuncSense,
			Op:          "prompt",
			Description: "ingress: user prompt arrives (stub for Stage 2; real impl in Stage 3)",
			Inputs:      []dag.ParamSpec{{Name: "prompt", Type: "string", Required: true}},
			Outputs:     []dag.ParamSpec{{Name: "prompt", Type: "string"}},
			Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
			Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
				return dag.NodeResult{
					Out:          map[string]any{"prompt": readString(in, "prompt")},
					CostConsumed: dag.Cost{LatencyMS: 5, Tokens: 0},
				}, nil
			},
		},

		// Stage 2 mechanical ops.
		EmbedSpec(EmbedConfig{Embedder: cfg.Embedder}),
		VectorSearchSpec(VectorSearchConfig{Storage: cfg.Storage}),

		// Stage 2 LLM-backed ops.
		RerankSpec(RerankConfig{Provider: cfg.Provider}),
		ScoreSpec(ScoreConfig{Provider: cfg.Provider}),
		DetectContradictionSpec(DetectContradictionConfig{Provider: cfg.Provider}),
		InjectSpec(InjectConfig{Provider: cfg.Provider}),
		ShouldCaptureSpec(ShouldCaptureConfig{Provider: cfg.Provider}),
		PredictNextSpec(PredictNextConfig{Provider: cfg.Provider}),
		ExtractInsightSpec(ExtractInsightConfig{Provider: cfg.Provider}),

		// Stub: maintain.capture's real impl lands in Stage 3 (per
		// docs/dag-build-plan.md Stage 3 "Loop rewrite").
		{
			Function:    dag.FuncMaintain,
			Op:          "capture",
			Description: "persist turn outcome to journal (stub for Stage 2; real impl in Stage 3)",
			Inputs:      []dag.ParamSpec{},
			Outputs:     []dag.ParamSpec{},
			Cost:        dag.Cost{LatencyMS: 20, Tokens: 10},
			Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
				return dag.NodeResult{
					Out:          map[string]any{"captured": true},
					CostConsumed: dag.Cost{LatencyMS: 20, Tokens: 10},
				}, nil
			},
		},
	}

	for _, s := range specs {
		if err := reg.Register(s); err != nil {
			return 0, err
		}
	}
	return len(specs), nil
}
