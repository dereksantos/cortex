package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestCompact_SinglePassthrough_NoLLM(t *testing.T) {
	// One small snapshot that already fits the budget → passthrough.
	h := newCompactHandler(CompactConfig{}) // nil provider
	got, err := h(context.Background(), map[string]any{
		"snapshots":  []string{"fact A; fact B"},
		"max_tokens": 200,
		"intent":     "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snap, _ := got.Out["snapshot"].(string)
	if !strings.Contains(snap, "fact A") {
		t.Errorf("passthrough should keep snapshot verbatim; got %q", snap)
	}
	if got.Out["fallback"] != false {
		t.Errorf("passthrough should not flag fallback")
	}
	if got.Out["compacted_snapshots"] != 1 {
		t.Errorf("compacted_snapshots = %v, want 1", got.Out["compacted_snapshots"])
	}
}

func TestCompact_FallbackKeepsNewest_BudgetEnforced(t *testing.T) {
	// Multiple bulky snapshots, no provider → deterministic fallback.
	// Newest must survive; total must fit budget.
	snapshots := []string{
		strings.Repeat("OLDEST ", 100),
		strings.Repeat("MIDDLE ", 100),
		strings.Repeat("NEWEST FACT X ", 30), // small enough that it fits
	}
	h := newCompactHandler(CompactConfig{}) // nil provider
	got, err := h(context.Background(), map[string]any{
		"snapshots":  snapshots,
		"max_tokens": 200,
		"intent":     "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snap, _ := got.Out["snapshot"].(string)
	tok, _ := got.Out["snapshot_tokens"].(int)
	if tok > 200 {
		t.Errorf("snapshot_tokens=%d > 200; fallback did not enforce budget", tok)
	}
	if !strings.Contains(snap, "NEWEST FACT X") {
		t.Errorf("fallback should preserve newest snapshot; got %q", snap)
	}
	if got.Out["fallback"] != true {
		t.Errorf("fallback path should flag fallback=true")
	}
	if got.Out["compacted_snapshots"] != 3 {
		t.Errorf("compacted_snapshots = %v, want 3", got.Out["compacted_snapshots"])
	}
}

func TestCompact_LLMPath_ClampsToMaxTokens(t *testing.T) {
	// Model ignores budget → handler clamps.
	long := strings.Repeat("HUGE ", 500)
	p := &scriptedProvider{response: long, available: true}
	h := newCompactHandler(CompactConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"snapshots":  []string{"a", "b", "c"},
		"max_tokens": 100,
		"intent":     "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tok, _ := got.Out["snapshot_tokens"].(int)
	if tok > 100 {
		t.Errorf("snapshot_tokens=%d > 100; LLM-path clamp not enforced", tok)
	}
	if got.Out["fallback"] != false {
		t.Errorf("successful LLM path should not flag fallback")
	}
}

func TestCompact_JournalHookFires(t *testing.T) {
	calls := 0
	var seenCompactedCount int
	hook := func(_ string, _ int, compactedCount int, _ bool) {
		calls++
		seenCompactedCount = compactedCount
	}
	h := newCompactHandler(CompactConfig{JournalWrite: hook})
	_, err := h(context.Background(), map[string]any{
		"snapshots":  []string{"a", "b", "c"},
		"max_tokens": 50,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if calls != 1 {
		t.Errorf("journal hook fired %d times, want 1", calls)
	}
	if seenCompactedCount != 3 {
		t.Errorf("journal hook saw compactedCount=%d, want 3", seenCompactedCount)
	}
}

func TestCompact_EmptySnapshots_Errors(t *testing.T) {
	h := newCompactHandler(CompactConfig{})
	_, err := h(context.Background(), map[string]any{
		"snapshots":  []string{},
		"max_tokens": 200,
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error for empty snapshots")
	}
}

func TestCompact_RequiresMaxTokens(t *testing.T) {
	h := newCompactHandler(CompactConfig{})
	_, err := h(context.Background(), map[string]any{
		"snapshots":  []string{"x"},
		"max_tokens": 0,
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error for max_tokens<=0")
	}
}

func TestCompact_TemplateLoads(t *testing.T) {
	resetTemplateCache()
	t.Cleanup(resetTemplateCache)
	if _, err := LoadTemplate("attend_compact"); err != nil {
		t.Errorf("LoadTemplate: %v", err)
	}
}
