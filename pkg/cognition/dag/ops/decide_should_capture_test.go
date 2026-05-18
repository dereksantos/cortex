package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestShouldCapture_modelCaptureTrue(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"capture":true,"tag":"decision","why":"chose pgx"}`,
		available: true,
	}
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"event": "decided to use pgx for postgres queries",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if cap, _ := got.Out["capture"].(bool); !cap {
		t.Error("expected capture=true")
	}
	if tag, _ := got.Out["tag"].(string); tag != "decision" {
		t.Errorf("expected tag=decision, got %s", tag)
	}
}

func TestShouldCapture_modelCaptureFalse(t *testing.T) {
	p := &scriptedProvider{response: `{"capture":false,"tag":"none","why":"routine edit"}`, available: true}
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"event": "minor refactor"}, dag.DefaultTurnBudget())
	if cap, _ := got.Out["capture"].(bool); cap {
		t.Error("expected capture=false")
	}
}

func TestShouldCapture_unknownTagNormalizes(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"capture":true,"tag":"a-novel-tag","why":"x"}`,
		available: true,
	}
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"event": "x"}, dag.DefaultTurnBudget())
	if tag, _ := got.Out["tag"].(string); tag != "none" {
		t.Errorf("expected unknown tag normalized to 'none', got %s", tag)
	}
	if cap, _ := got.Out["capture"].(bool); cap {
		t.Error("capture should flip to false when tag normalizes to none")
	}
}

func TestShouldCapture_keywordFallback_constraint(t *testing.T) {
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"event": "Don't use Redis for sessions here",
	}, dag.DefaultTurnBudget())
	if cap, _ := got.Out["capture"].(bool); !cap {
		t.Error("expected capture=true from 'don't' marker")
	}
	if tag, _ := got.Out["tag"].(string); tag != "constraint" {
		t.Errorf("expected tag=constraint, got %s", tag)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true")
	}
}

func TestShouldCapture_keywordFallback_decision(t *testing.T) {
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"event": "Chose chi router after evaluation",
	}, dag.DefaultTurnBudget())
	if tag, _ := got.Out["tag"].(string); tag != "decision" {
		t.Errorf("expected tag=decision, got %s", tag)
	}
}

func TestShouldCapture_keywordFallback_nothing(t *testing.T) {
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: nil})
	got, _ := h(context.Background(), map[string]any{
		"event": "Refactored function signature for clarity",
	}, dag.DefaultTurnBudget())
	if cap, _ := got.Out["capture"].(bool); cap {
		t.Error("expected capture=false for non-marker event")
	}
	if tag, _ := got.Out["tag"].(string); tag != "none" {
		t.Errorf("expected tag=none, got %s", tag)
	}
}

func TestShouldCapture_missingEvent(t *testing.T) {
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when event absent")
	}
}

func TestShouldCapture_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(ShouldCaptureSpec(ShouldCaptureConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := reg.Get("decide.should_capture"); err != nil {
		t.Fatalf("get: %v", err)
	}
}
