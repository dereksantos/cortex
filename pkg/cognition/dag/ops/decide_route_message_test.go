package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func routeInputs(message, goal string) map[string]any {
	return map[string]any{"message": message, "goal": goal}
}

func TestRouteMessage_newChange(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"new_change","name":"add-rate-limiting","confidence":0.92,"why":"unrelated task"}`,
		available: true,
	}
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: p})
	got, err := h(context.Background(), routeInputs("now add rate limiting to the API", "fix the login redirect bug"), dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if d, _ := got.Out["decision"].(string); d != DecisionNewChange {
		t.Errorf("expected decision=%q, got %v", DecisionNewChange, got.Out["decision"])
	}
	if n, _ := got.Out["name"].(string); n != "add-rate-limiting" {
		t.Errorf("expected name=add-rate-limiting, got %v", got.Out["name"])
	}
	if conf, _ := got.Out["confidence"].(float64); conf < 0.9 {
		t.Errorf("expected confidence >= 0.9, got %v", conf)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on parse success")
	}
}

func TestRouteMessage_continue(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"continue","name":"","confidence":0.8,"why":"follow-up"}`,
		available: true,
	}
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: p})
	got, _ := h(context.Background(), routeInputs("also handle the empty-password case", "fix the login redirect bug"), dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != DecisionContinue {
		t.Errorf("expected decision=continue, got %v", got.Out["decision"])
	}
}

func TestRouteMessage_unknownDecisionFallsBackToContinue(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"switch-everything","confidence":0.7,"why":"x"}`,
		available: true,
	}
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: p})
	got, _ := h(context.Background(), routeInputs("x", "some goal"), dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != DecisionContinue {
		t.Errorf("expected fallback to %q, got %v", DecisionContinue, got.Out["decision"])
	}
	if conf, _ := got.Out["confidence"].(float64); conf != 0 {
		t.Errorf("expected confidence reset to 0 on unknown label, got %v", conf)
	}
}

func TestRouteMessage_emptyGoalContinuesWithoutModel(t *testing.T) {
	// A provider that would say new_change — but with no goal the handler must
	// short-circuit to continue WITHOUT consulting it (nothing to diverge from).
	p := &scriptedProvider{
		response:  `{"decision":"new_change","name":"x","confidence":0.99,"why":"should not be used"}`,
		available: true,
	}
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: p})
	got, _ := h(context.Background(), routeInputs("first message of the session", ""), dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != DecisionContinue {
		t.Errorf("expected decision=continue with empty goal, got %v", got.Out["decision"])
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("expected fallback=true with empty goal")
	}
}

func TestRouteMessage_nilProviderFallsBackToContinue(t *testing.T) {
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: nil})
	got, _ := h(context.Background(), routeInputs("anything", "some goal"), dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != DecisionContinue {
		t.Errorf("expected safe-default continue on nil provider, got %v", got.Out["decision"])
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true with nil provider")
	}
}

func TestRouteMessage_missingMessageErrors(t *testing.T) {
	h := NewRouteMessageHandler(RouteMessageConfig{Provider: &scriptedProvider{available: true}})
	if _, err := h(context.Background(), routeInputs("", "some goal"), dag.DefaultTurnBudget()); err == nil {
		t.Error("expected error when 'message' is missing")
	}
}
