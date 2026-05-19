package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// TestPlan_FallbackWithoutProvider — nil Provider returns a single
// pass-through subtask so the chain still walks end-to-end.
func TestPlan_FallbackWithoutProvider(t *testing.T) {
	h := NewPlanHandler(PlanConfig{})
	res, err := h(context.Background(),
		map[string]any{"prompt": "build me a CLI that pings a URL"},
		dag.Budget{LatencyMS: 30000, Tokens: 1000, Depth: 10})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	subtasks, _ := res.Out["subtasks"].([]Subtask)
	if len(subtasks) != 1 {
		t.Fatalf("expected 1 fallback subtask; got %d", len(subtasks))
	}
	if subtasks[0].Description != "build me a CLI that pings a URL" {
		t.Errorf("fallback subtask should echo prompt; got %q", subtasks[0].Description)
	}
	if fb, _ := res.Out["fallback"].(bool); !fb {
		t.Errorf("expected fallback=true; got %v", res.Out["fallback"])
	}
}

// TestPlan_MissingPromptIsError
func TestPlan_MissingPromptIsError(t *testing.T) {
	h := NewPlanHandler(PlanConfig{})
	_, err := h(context.Background(), map[string]any{},
		dag.Budget{LatencyMS: 30000, Tokens: 1000, Depth: 10})
	if err == nil {
		t.Errorf("expected error for missing prompt")
	}
}

// TestParsePlanResponse_StripsFences — model that wraps JSON in
// markdown ```json ... ``` fences still parses.
func TestParsePlanResponse_StripsFences(t *testing.T) {
	raw := "```json\n{\"project_intent\":\"build x\",\"subtasks\":[{\"id\":\"s1\",\"description\":\"do a\",\"complexity\":\"simple\"}]}\n```"
	got, err := parsePlanResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ProjectIntent != "build x" {
		t.Errorf("intent: got %q, want build x", got.ProjectIntent)
	}
	if len(got.Subtasks) != 1 || got.Subtasks[0].Description != "do a" {
		t.Errorf("subtasks: got %+v", got.Subtasks)
	}
}

// TestParsePlanResponse_Bare — JSON without fences parses too.
func TestParsePlanResponse_Bare(t *testing.T) {
	raw := `{"project_intent":"y","subtasks":[{"id":"s1","description":"x","complexity":"hard"}]}`
	got, err := parsePlanResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Subtasks[0].Complexity != "hard" {
		t.Errorf("complexity: got %q", got.Subtasks[0].Complexity)
	}
}
