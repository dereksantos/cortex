package ops

import (
	"context"
	"fmt"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestClassifyIntent_greeting(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"intent":"greeting","confidence":0.95,"why":"plain hello"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{"prompt": "hello"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if intent, _ := got.Out["intent"].(string); intent != "greeting" {
		t.Errorf("expected intent=greeting, got %v", got.Out["intent"])
	}
	if conf, _ := got.Out["confidence"].(float64); conf < 0.9 {
		t.Errorf("expected confidence >= 0.9, got %v", conf)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on parse success")
	}
}

func TestClassifyIntent_code(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"intent":"code","confidence":0.85,"why":"file edit requested"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "add a print to main.go"}, dag.DefaultTurnBudget())
	if intent, _ := got.Out["intent"].(string); intent != "code" {
		t.Errorf("expected intent=code, got %v", got.Out["intent"])
	}
}

func TestClassifyIntent_unknownLabelFallsBackToCode(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"intent":"random-label","confidence":0.7,"why":"x"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "x"}, dag.DefaultTurnBudget())
	if intent, _ := got.Out["intent"].(string); intent != IntentCode {
		t.Errorf("expected fallback to %q, got %v", IntentCode, got.Out["intent"])
	}
	if conf, _ := got.Out["confidence"].(float64); conf != 0 {
		t.Errorf("expected confidence reset to 0 on unknown label, got %v", conf)
	}
}

func TestClassifyIntent_providerUnavailableFallback(t *testing.T) {
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{"prompt": "hello"}, dag.DefaultTurnBudget())
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true with nil provider")
	}
	if intent, _ := got.Out["intent"].(string); intent != IntentCode {
		t.Errorf("expected safe-default code intent on fallback, got %v", got.Out["intent"])
	}
}

func TestClassifyIntent_providerNotAvailableFallback(t *testing.T) {
	p := &scriptedProvider{available: false}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "hello"}, dag.DefaultTurnBudget())
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true when provider.IsAvailable() is false")
	}
}

func TestClassifyIntent_parseErrorFallsBackToCode(t *testing.T) {
	p := &scriptedProvider{response: "not json at all", available: true}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "hi"}, dag.DefaultTurnBudget())
	if intent, _ := got.Out["intent"].(string); intent != IntentCode {
		t.Errorf("expected safe-default on parse error, got %v", got.Out["intent"])
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true on parse error")
	}
}

func TestClassifyIntent_missingPrompt(t *testing.T) {
	p := &scriptedProvider{available: true}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Error("expected error when prompt is missing")
	}
}

func TestClassifyIntent_confidenceClamped(t *testing.T) {
	tests := []struct {
		raw, want float64
	}{
		{-0.5, 0},
		{1.5, 1},
		{0.5, 0.5},
	}
	for _, tc := range tests {
		resp := fmt.Sprintf(`{"intent":"greeting","confidence":%v,"why":"x"}`, tc.raw)
		p := &scriptedProvider{response: resp, available: true}
		h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
		got, _ := h(context.Background(), map[string]any{"prompt": "hi"}, dag.DefaultTurnBudget())
		if conf, _ := got.Out["confidence"].(float64); conf != tc.want {
			t.Errorf("raw=%v: expected clamped=%v, got %v", tc.raw, tc.want, conf)
		}
	}
}

func TestClassifyIntent_costConsumedTracksTokens(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"intent":"greeting","confidence":0.9,"why":"x"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "hi"}, dag.DefaultTurnBudget())
	// scriptedProvider reports 50 input + 30 output = 80
	if got.CostConsumed.Tokens != 80 {
		t.Errorf("expected 80 tokens consumed, got %d", got.CostConsumed.Tokens)
	}
}

func TestClassifyIntent_spec(t *testing.T) {
	spec := ClassifyIntentSpec(ClassifyIntentConfig{})
	if spec.QualifiedName() != "sense.classify_intent" {
		t.Errorf("qualified name = %q, want sense.classify_intent", spec.QualifiedName())
	}
	if !spec.Exposable {
		t.Error("classify_intent should be Exposable so decide.next can re-invoke it")
	}
	if spec.Cost.LatencyMS == 0 {
		t.Error("Cost.LatencyMS should be non-zero")
	}
	// Per-node routing substrate: classification is structured output
	// but NOT function-call shape — tool-call specialists (xLAM et al.)
	// silently fail on this schema. Bare CapToolCalling routes to any
	// tool-callable chat model; the workhorse handles classification
	// fine.
	if len(spec.Requires) != 1 || spec.Requires[0] != "tool-calling" {
		t.Errorf("Requires: got %v want [tool-calling]", spec.Requires)
	}
}

// TestClassifyIntent_BudgetProviderWinsOverCfg — when the executor's
// Router has pre-resolved a provider into Budget.Provider, the handler
// must use it instead of cfg.Provider. Slice 6 of
// docs/per-node-routing-plan.md — verifies the migration pattern works
// for non-decide nodes too.
func TestClassifyIntent_BudgetProviderWinsOverCfg(t *testing.T) {
	cfgP := &scriptedProvider{
		response:  `{"intent":"code","confidence":0.5,"why":"cfg fired"}`,
		available: true,
	}
	budgetP := &scriptedProvider{
		response:  `{"intent":"greeting","confidence":0.95,"why":"budget fired"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: cfgP})

	b := dag.DefaultTurnBudget()
	b.Provider = budgetP
	got, err := h(context.Background(), map[string]any{"prompt": "hello"}, b)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// budgetP's response should drive the output.
	if intent, _ := got.Out["intent"].(string); intent != "greeting" {
		t.Errorf("Budget.Provider should drive output (greeting), got %v", got.Out["intent"])
	}
	if why, _ := got.Out["why"].(string); why != "budget fired" {
		t.Errorf("expected why from Budget.Provider, got %q", why)
	}
}

// TestClassifyIntent_LegacyFallbackWhenNoBudgetProvider — without a
// Router wired (Budget.Provider nil), cfg.Provider is still used.
// Pins backwards-compat for callers that haven't adopted the Router.
func TestClassifyIntent_LegacyFallbackWhenNoBudgetProvider(t *testing.T) {
	cfgP := &scriptedProvider{
		response:  `{"intent":"recall","confidence":0.9,"why":"cfg fired"}`,
		available: true,
	}
	h := NewClassifyIntentHandler(ClassifyIntentConfig{Provider: cfgP})
	got, err := h(context.Background(), map[string]any{"prompt": "what did i say earlier"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if intent, _ := got.Out["intent"].(string); intent != "recall" {
		t.Errorf("cfg.Provider should drive output without Router, got %v", got.Out["intent"])
	}
}
