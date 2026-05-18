package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestScore_loadBearingTrue(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"load_bearing":true,"confidence":0.9,"why":"directly answers"}`,
		available: true,
	}
	h := NewScoreHandler(ScoreConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"query":     "how do JWT tokens expire?",
		"candidate": "JWT validation including expiry checks",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if lb, _ := got.Out["load_bearing"].(bool); !lb {
		t.Errorf("expected load_bearing=true")
	}
	if conf, _ := got.Out["confidence"].(float64); conf != 0.9 {
		t.Errorf("expected confidence=0.9, got %f", conf)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on successful parse")
	}
}

func TestScore_loadBearingFalse(t *testing.T) {
	p := &scriptedProvider{response: `{"load_bearing":false,"confidence":0.8,"why":"unrelated"}`, available: true}
	h := NewScoreHandler(ScoreConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":     "auth",
		"candidate": "postgres schema",
	}, dag.DefaultTurnBudget())
	if lb, _ := got.Out["load_bearing"].(bool); lb {
		t.Errorf("expected load_bearing=false")
	}
}

func TestScore_confidenceClamping(t *testing.T) {
	p := &scriptedProvider{response: `{"load_bearing":true,"confidence":1.7,"why":"x"}`, available: true}
	h := NewScoreHandler(ScoreConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"query": "a", "candidate": "b"}, dag.DefaultTurnBudget())
	if conf, _ := got.Out["confidence"].(float64); conf > 1.0 {
		t.Errorf("confidence should clamp to <=1, got %f", conf)
	}
}

func TestScore_keywordOverlapFallback(t *testing.T) {
	h := NewScoreHandler(ScoreConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query":     "authentication module",
		"candidate": "Use JWT for authentication",
	}, dag.DefaultTurnBudget())
	if lb, _ := got.Out["load_bearing"].(bool); !lb {
		t.Error("expected load_bearing=true from keyword overlap fallback")
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true")
	}
}

func TestScore_noOverlapFallback(t *testing.T) {
	h := NewScoreHandler(ScoreConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query":     "auth",
		"candidate": "postgres schema",
	}, dag.DefaultTurnBudget())
	if lb, _ := got.Out["load_bearing"].(bool); lb {
		// Note: "auth" is 4 chars, not in "postgres schema" → false expected.
		t.Errorf("expected load_bearing=false for no-overlap fallback")
	}
}

func TestScore_missingQuery(t *testing.T) {
	h := NewScoreHandler(ScoreConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{"candidate": "x"}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when query absent")
	}
}

func TestScore_missingCandidate(t *testing.T) {
	h := NewScoreHandler(ScoreConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{"query": "x"}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when candidate absent")
	}
}

func TestScore_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(ScoreSpec(ScoreConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	spec, err := reg.Get("value.score")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if spec.Function != dag.FuncValue || spec.Op != "score" {
		t.Errorf("wrong qualified name: %s", spec.QualifiedName())
	}
}
