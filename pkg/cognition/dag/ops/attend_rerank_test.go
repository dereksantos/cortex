package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func mkResults() []cognition.Result {
	return []cognition.Result{
		{ID: "auth_module", Content: "Use JWT with HS256 for auth", Score: 0.5},
		{ID: "jwt_handler", Content: "JWT validation including expiry checks", Score: 0.7},
		{ID: "db_schema", Content: "Postgres schema for users table", Score: 0.3},
	}
}

func TestRerank_appliesModelRanking(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"ranking":["jwt_handler","auth_module","db_schema"],"reason":"jwt_handler directly answers token-expiry question"}`,
		available: true,
	}
	h := NewRerankHandler(RerankConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"query":      "how do JWT tokens expire?",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if len(reranked) != 3 {
		t.Fatalf("expected 3 reranked, got %d", len(reranked))
	}
	if reranked[0].ID != "jwt_handler" {
		t.Errorf("expected jwt_handler first, got %s", reranked[0].ID)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Error("fallback should be false on successful parse")
	}
}

func TestRerank_omittedCandidatesAppendedInOriginalOrder(t *testing.T) {
	// Model omits db_schema; handler should still include it at the end.
	p := &scriptedProvider{
		response:  `{"ranking":["jwt_handler","auth_module"],"reason":"db_schema irrelevant"}`,
		available: true,
	}
	h := NewRerankHandler(RerankConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":      "JWT",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if len(reranked) != 3 {
		t.Fatalf("expected 3 reranked (omitted ones appended), got %d", len(reranked))
	}
	if reranked[2].ID != "db_schema" {
		t.Errorf("expected db_schema last, got %s", reranked[2].ID)
	}
}

func TestRerank_malformedFallsBackToScore(t *testing.T) {
	p := &scriptedProvider{response: "not valid json", available: true}
	h := NewRerankHandler(RerankConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":      "anything",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if reranked[0].ID != "jwt_handler" { // highest Score=0.7
		t.Errorf("score fallback should put jwt_handler first, got %s", reranked[0].ID)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true on parse error")
	}
}

func TestRerank_noProviderUsesScoreFallback(t *testing.T) {
	h := NewRerankHandler(RerankConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if reranked[0].ID != "jwt_handler" {
		t.Errorf("score fallback put wrong candidate first: %s", reranked[0].ID)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true with nil provider")
	}
}

func TestRerank_emptyCandidates(t *testing.T) {
	h := NewRerankHandler(RerankConfig{Provider: &scriptedProvider{available: true}})
	got, err := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": []cognition.Result{},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("empty candidates should not error: %v", err)
	}
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if len(reranked) != 0 {
		t.Errorf("expected 0 reranked, got %d", len(reranked))
	}
}

func TestRerank_missingQuery(t *testing.T) {
	h := NewRerankHandler(RerankConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when query absent")
	}
}

func TestRerank_acceptsAnyMapCandidates(t *testing.T) {
	// YAML-decoded scenarios feed candidates as []any of maps.
	cs := []any{
		map[string]any{"id": "a", "content": "x", "score": 0.1},
		map[string]any{"id": "b", "content": "y", "score": 0.9},
	}
	p := &scriptedProvider{response: `{"ranking":["b","a"],"reason":"b wins"}`, available: true}
	h := NewRerankHandler(RerankConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"query":      "?",
		"candidates": cs,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	reranked, _ := got.Out["reranked"].([]cognition.Result)
	if reranked[0].ID != "b" {
		t.Errorf("expected b first, got %s", reranked[0].ID)
	}
}

func TestRerank_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(RerankSpec(RerankConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	spec, err := reg.Get("attend.rerank")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(spec.Description, "rerank") {
		t.Errorf("description should mention rerank: %s", spec.Description)
	}
}
