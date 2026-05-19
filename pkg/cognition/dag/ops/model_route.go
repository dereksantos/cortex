// Package ops — model.route.
//
// Maps a sub-task's complexity tag to a model id. Default policy:
//
//	simple   → qwen2.5-coder:1.5b   (local Ollama; fast, cheap)
//	moderate → anthropic/claude-haiku-4.5
//	hard     → anthropic/claude-haiku-4.5  (sonnet stays opt-in)
//
// Callers (the project chain in commands/run.go) read the chosen
// model from this op's Out and set it as the `model` attr on the
// downstream decide.coding_turn spawn. The route policy is
// data-driven via RouteConfig.Map so callers can override per
// project (e.g., a security-sensitive project might prefer
// frontier-only).
//
// Mechanical (no LLM). Cheap to call.
package ops

import (
	"context"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// RouteConfig configures the complexity → model mapping. When
// Map is nil, DefaultRouteMap is used.
type RouteConfig struct {
	Map map[string]string

	// DefaultModel is returned when the complexity tag isn't in Map.
	// When empty, falls through to DefaultRouteMap["moderate"].
	DefaultModel string
}

// DefaultRouteMap is the V0 routing policy. Frontier "hard" via
// haiku (not sonnet) keeps per-project costs bounded; callers can
// opt into a sonnet-tier mapping by passing a custom Map.
var DefaultRouteMap = map[string]string{
	"simple":   "qwen2.5-coder:1.5b",
	"moderate": "anthropic/claude-haiku-4.5",
	"hard":     "anthropic/claude-haiku-4.5",
}

// RouteSpec returns the NodeSpec for model.route.
func RouteSpec(cfg RouteConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncModel,
		Op:          "route",
		Description: "pick a model id from a sub-task's complexity tag",
		Inputs: []dag.ParamSpec{
			{Name: "complexity", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "model", Type: "string"},
		},
		Cost:    dag.Cost{LatencyMS: 1, Tokens: 0},
		Handler: NewRouteHandler(cfg),
	}
}

// NewRouteHandler returns the dag.Handler.
func NewRouteHandler(cfg RouteConfig) dag.Handler {
	routeMap := cfg.Map
	if routeMap == nil {
		routeMap = DefaultRouteMap
	}
	defaultModel := cfg.DefaultModel
	if defaultModel == "" {
		defaultModel = routeMap["moderate"]
		if defaultModel == "" {
			defaultModel = DefaultRouteMap["moderate"]
		}
	}
	return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
		complexity := readString(in, "complexity")
		model, ok := routeMap[complexity]
		if !ok {
			model = defaultModel
		}
		return dag.NodeResult{
			Out: map[string]any{
				"model":      model,
				"complexity": complexity,
			},
			CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
		}, nil
	}
}
