package ops

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestDetectContradiction_modelEmitsConflict(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"conflicts":true,"conflicts_with":["p_2"],"why":"p_2 says avoid redis"}`,
		available: true,
	}
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"candidate": "Use Redis for session storage",
		"priors": []cognition.Result{
			{ID: "p_1", Content: "Use postgres for everything"},
			{ID: "p_2", Content: "Avoid Redis — adds infrastructure overhead"},
		},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if c, _ := got.Out["conflicts"].(bool); !c {
		t.Error("expected conflicts=true")
	}
	ids, _ := got.Out["conflicts_with"].([]string)
	if !reflect.DeepEqual(ids, []string{"p_2"}) {
		t.Errorf("expected [p_2], got %v", ids)
	}
}

func TestDetectContradiction_emitClaimButEmptyListIsFalse(t *testing.T) {
	// Model claims conflicts=true but emits empty list — sanitize.
	p := &scriptedProvider{
		response:  `{"conflicts":true,"conflicts_with":[],"why":"x"}`,
		available: true,
	}
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"candidate": "x",
		"priors":    []cognition.Result{{ID: "a", Content: "y"}},
	}, dag.DefaultTurnBudget())
	if c, _ := got.Out["conflicts"].(bool); c {
		t.Error("conflicts should be false when conflicts_with is empty")
	}
}

func TestDetectContradiction_noPriors(t *testing.T) {
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: &scriptedProvider{available: true}})
	got, err := h(context.Background(), map[string]any{
		"candidate": "anything",
		"priors":    []cognition.Result{},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if c, _ := got.Out["conflicts"].(bool); c {
		t.Error("conflicts should be false when no priors")
	}
}

func TestDetectContradiction_keywordFallback(t *testing.T) {
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"candidate": "use redis for caching",
		"priors": []cognition.Result{
			{ID: "p_1", Content: "use postgres for data"},
			{ID: "p_2", Content: "avoid redis — adds ops burden"},
		},
	}, dag.DefaultTurnBudget())
	ids, _ := got.Out["conflicts_with"].([]string)
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"p_2"}) {
		t.Errorf("expected [p_2] from keyword fallback, got %v", ids)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true")
	}
}

func TestDetectContradiction_fallbackNoOverlap(t *testing.T) {
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"candidate": "use redis for caching",
		"priors": []cognition.Result{
			{ID: "p_1", Content: "use postgres for data"},
		},
	}, dag.DefaultTurnBudget())
	if c, _ := got.Out["conflicts"].(bool); c {
		t.Errorf("expected no conflicts when nothing opposes redis")
	}
}

func TestDetectContradiction_missingCandidate(t *testing.T) {
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{
		"priors": []cognition.Result{{ID: "x", Content: "y"}},
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when candidate absent")
	}
}

func TestDetectContradiction_acceptsAnyMapPriors(t *testing.T) {
	priors := []any{
		map[string]any{"id": "p_1", "content": "use redis"},
	}
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: nil})
	got, err := h(context.Background(), map[string]any{
		"candidate": "avoid redis here",
		"priors":    priors,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ids, _ := got.Out["conflicts_with"].([]string)
	if !reflect.DeepEqual(ids, []string{"p_1"}) {
		t.Errorf("expected [p_1] (any-map coercion), got %v", ids)
	}
}

func TestDetectContradiction_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(DetectContradictionSpec(DetectContradictionConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.Get("value.detect_contradiction"); err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestExtractMarkerTerms(t *testing.T) {
	got := extractMarkerTerms("use redis here, prefer postgres elsewhere", []string{"use ", "prefer "})
	want := []string{"redis", "postgres"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %v, got %v", want, got)
	}
}
