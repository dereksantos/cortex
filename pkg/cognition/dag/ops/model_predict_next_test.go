package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestPredictNext_modelPredictions(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"predictions":["how do refresh tokens work","what is the JWT signing algorithm","where is the auth middleware"]}`,
		available: true,
	}
	h := NewPredictNextHandler(PredictNextConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"current":        "how do JWT tokens expire?",
		"recent_queries": []string{"what is auth.go", "show me the login handler"},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	preds, _ := got.Out["predictions"].([]string)
	if len(preds) != 3 {
		t.Fatalf("expected 3 predictions, got %d", len(preds))
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on successful parse")
	}
}

func TestPredictNext_truncatesToThree(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"predictions":["a","b","c","d","e"]}`,
		available: true,
	}
	h := NewPredictNextHandler(PredictNextConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"current": "x"}, dag.DefaultTurnBudget())
	preds, _ := got.Out["predictions"].([]string)
	if len(preds) != 3 {
		t.Errorf("expected truncation to 3, got %d", len(preds))
	}
}

func TestPredictNext_emptyPredictionsFallback(t *testing.T) {
	p := &scriptedProvider{response: `{"predictions":[]}`, available: true}
	h := NewPredictNextHandler(PredictNextConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"current": "how does authentication work",
	}, dag.DefaultTurnBudget())
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("empty predictions should fall back to noun-echo")
	}
	preds, _ := got.Out["predictions"].([]string)
	if len(preds) == 0 {
		t.Errorf("noun-echo fallback should yield predictions for 'authentication'; got 0")
	}
}

func TestPredictNext_nounEchoFallback(t *testing.T) {
	h := NewPredictNextHandler(PredictNextConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"current": "where is the authentication module",
	}, dag.DefaultTurnBudget())
	preds, _ := got.Out["predictions"].([]string)
	if len(preds) == 0 {
		t.Fatal("expected nun-echo predictions for non-stopword tokens")
	}
	// Every prediction should mention a noun from the query.
	joined := strings.Join(preds, " ")
	if !strings.Contains(joined, "authentication") && !strings.Contains(joined, "module") {
		t.Errorf("fallback predictions should echo a query noun; got %v", preds)
	}
}

func TestPredictNext_missingCurrent(t *testing.T) {
	h := NewPredictNextHandler(PredictNextConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when current absent")
	}
}

func TestPredictNext_recentQueriesOptional(t *testing.T) {
	// Should work when recent_queries absent.
	p := &scriptedProvider{response: `{"predictions":["a","b","c"]}`, available: true}
	h := NewPredictNextHandler(PredictNextConfig{Provider: p})
	_, err := h(context.Background(), map[string]any{"current": "x"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("recent_queries should be optional: %v", err)
	}
}

func TestPredictNext_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(PredictNextSpec(PredictNextConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.Get("model.predict_next"); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestNounEchoPredict_skipsStopwords(t *testing.T) {
	// "the" / "does" / "how" are stopwords; "system" is the only valid
	// noun. Fallback should emit one prediction echoing "system".
	got := nounEchoPredict("how does the system work")
	if len(got) == 0 {
		t.Fatal("expected at least one prediction for 'system'")
	}
	for _, p := range got {
		if !strings.Contains(p, "system") && !strings.Contains(p, "work") {
			t.Errorf("prediction %q should echo a non-stopword noun from query", p)
		}
	}
}

func TestNounEchoPredict_skipsShortTokens(t *testing.T) {
	// All tokens < 4 chars → no nouns → empty predictions.
	got := nounEchoPredict("is it ok")
	if len(got) != 0 {
		t.Errorf("expected 0 predictions for all-short-token query, got %v", got)
	}
}
