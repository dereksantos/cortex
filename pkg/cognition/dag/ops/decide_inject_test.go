package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestInject_modelInject(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"inject","inject_ids":["jwt_handler"],"confidence":0.9,"why":"strong match"}`,
		available: true,
	}
	h := NewInjectHandler(InjectConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"query":      "jwt expiry",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if d, _ := got.Out["decision"].(string); d != "inject" {
		t.Errorf("expected decision=inject, got %s", d)
	}
	ids, _ := got.Out["inject_ids"].([]string)
	if !reflect.DeepEqual(ids, []string{"jwt_handler"}) {
		t.Errorf("expected [jwt_handler], got %v", ids)
	}
}

func TestInject_modelWait(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"wait","inject_ids":["a"],"confidence":0.4,"why":"ambiguous"}`,
		available: true,
	}
	h := NewInjectHandler(InjectConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	ids, _ := got.Out["inject_ids"].([]string)
	if len(ids) != 0 {
		t.Errorf("decision=wait should force inject_ids=[], got %v", ids)
	}
}

func TestInject_injectWithEmptyIdsDowngradesToWait(t *testing.T) {
	// Model said "inject" but emitted no IDs — should sanitize to wait.
	p := &scriptedProvider{
		response:  `{"decision":"inject","inject_ids":[],"confidence":0.5,"why":"x"}`,
		available: true,
	}
	h := NewInjectHandler(InjectConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != "wait" {
		t.Errorf("expected sanitization to wait, got %s", d)
	}
}

func TestInject_unknownDecisionFallsBack(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"decision":"sing-a-song","inject_ids":[],"confidence":0.5,"why":"x"}`,
		available: true,
	}
	h := NewInjectHandler(InjectConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": mkResults(),
	}, dag.DefaultTurnBudget())
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("unknown decision should trigger fallback")
	}
}

func TestInject_scoreFallback_injectPath(t *testing.T) {
	h := NewInjectHandler(InjectConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query": "x",
		"candidates": []cognition.Result{
			{ID: "a", Score: 0.9},
			{ID: "b", Score: 0.75},
			{ID: "c", Score: 0.3},
		},
	}, dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != "inject" {
		t.Errorf("expected inject (max >= 0.8), got %s", d)
	}
	ids, _ := got.Out["inject_ids"].([]string)
	if len(ids) != 2 { // a + b are >= 0.7
		t.Errorf("expected 2 inject_ids (>=0.7), got %v", ids)
	}
}

func TestInject_scoreFallback_waitPath(t *testing.T) {
	h := NewInjectHandler(InjectConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query": "x",
		"candidates": []cognition.Result{
			{ID: "a", Score: 0.3},
			{ID: "b", Score: 0.4},
		},
	}, dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != "wait" {
		t.Errorf("expected wait (max < 0.5), got %s", d)
	}
}

func TestInject_scoreFallback_queuePath(t *testing.T) {
	h := NewInjectHandler(InjectConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query": "x",
		"candidates": []cognition.Result{
			{ID: "a", Score: 0.6},
		},
	}, dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != "queue" {
		t.Errorf("expected queue (0.5 <= max < 0.8), got %s", d)
	}
}

func TestInject_scoreFallback_emptyCandidates(t *testing.T) {
	h := NewInjectHandler(InjectConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"query":      "x",
		"candidates": []cognition.Result{},
	}, dag.DefaultTurnBudget())
	if d, _ := got.Out["decision"].(string); d != "wait" {
		t.Errorf("expected wait for empty candidates, got %s", d)
	}
}

func TestInject_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(InjectSpec(InjectConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.Get("decide.inject"); err != nil {
		t.Fatalf("get: %v", err)
	}
}
