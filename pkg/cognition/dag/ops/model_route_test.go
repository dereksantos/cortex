package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestRoute_DefaultMap(t *testing.T) {
	h := NewRouteHandler(RouteConfig{})
	tests := []struct {
		complexity, wantModel string
	}{
		{"simple", "qwen2.5-coder:1.5b"},
		{"moderate", "anthropic/claude-haiku-4.5"},
		{"hard", "anthropic/claude-haiku-4.5"},
		{"unknown-tag", "anthropic/claude-haiku-4.5"}, // falls through to moderate
	}
	for _, tt := range tests {
		t.Run(tt.complexity, func(t *testing.T) {
			res, _ := h(context.Background(),
				map[string]any{"complexity": tt.complexity},
				dag.Budget{LatencyMS: 100, Tokens: 0, Depth: 5})
			got, _ := res.Out["model"].(string)
			if got != tt.wantModel {
				t.Errorf("complexity=%q: got model=%q, want %q", tt.complexity, got, tt.wantModel)
			}
		})
	}
}

func TestRoute_CustomMap(t *testing.T) {
	h := NewRouteHandler(RouteConfig{
		Map: map[string]string{
			"simple":   "tiny",
			"moderate": "medium",
			"hard":     "frontier",
		},
		DefaultModel: "medium",
	})
	res, _ := h(context.Background(),
		map[string]any{"complexity": "hard"},
		dag.Budget{LatencyMS: 100, Tokens: 0, Depth: 5})
	if got, _ := res.Out["model"].(string); got != "frontier" {
		t.Errorf("custom map: got %q, want frontier", got)
	}
}
