package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestAccumulate_StepZero_NoPrevSnapshot(t *testing.T) {
	// Step 0: no prior snapshot, small observation that fits the
	// budget. Passthrough path → snapshot is just the observation.
	h := newAccumulateHandler(AccumulateConfig{})
	got, err := h(context.Background(), map[string]any{
		"prev_snapshot": "",
		"observation":   "user wants to add a logout button",
		"max_tokens":    200,
		"intent":        "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snap, _ := got.Out["snapshot"].(string)
	if !strings.Contains(snap, "logout button") {
		t.Errorf("snapshot should include observation; got %q", snap)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Errorf("passthrough should not flag fallback")
	}
}

func TestAccumulate_PassthroughMerge_NoLLMNeeded(t *testing.T) {
	// Prev + observation both fit the budget — no LLM call, no
	// fallback marker. Both pieces survive in the snapshot.
	h := newAccumulateHandler(AccumulateConfig{}) // nil provider
	got, err := h(context.Background(), map[string]any{
		"prev_snapshot": "user wants logout button",
		"observation":   "auth handler lives at auth/login.go",
		"max_tokens":    200,
		"intent":        "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snap, _ := got.Out["snapshot"].(string)
	if !strings.Contains(snap, "logout") || !strings.Contains(snap, "auth/login.go") {
		t.Errorf("snapshot should keep both facts; got %q", snap)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Errorf("passthrough should not flag fallback")
	}
}

func TestAccumulate_FallbackWhenNoProvider_BudgetEnforced(t *testing.T) {
	// Snapshot + observation exceed budget AND no LLM is wired.
	// Deterministic truncate-and-merge fallback must produce a
	// snapshot under budget that preserves the newer observation.
	prev := strings.Repeat("OLD ", 200)           // ~200 tokens
	obs := strings.Repeat("NEW ", 50)             // ~50 tokens
	h := newAccumulateHandler(AccumulateConfig{}) // nil provider
	got, err := h(context.Background(), map[string]any{
		"prev_snapshot": prev,
		"observation":   obs,
		"max_tokens":    100,
		"intent":        "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	snap, _ := got.Out["snapshot"].(string)
	tok, _ := got.Out["snapshot_tokens"].(int)
	if tok > 100 {
		t.Errorf("snapshot_tokens=%d > 100; fallback did not enforce budget", tok)
	}
	if !strings.Contains(snap, "NEW") {
		t.Errorf("fallback should preserve new observation; got %q", snap)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Errorf("deterministic merge should flag fallback=true")
	}
}

func TestAccumulate_LLMPath_ClampsToMaxTokens(t *testing.T) {
	// Model ignores the budget and returns more than max_tokens.
	// The handler must clamp post-call so downstream nodes can rely
	// on the budget being honored.
	long := strings.Repeat("HUGE ", 500) // ~500+ tokens
	prev := strings.Repeat("seed ", 100)
	obs := strings.Repeat("new ", 100)
	p := &scriptedProvider{response: long, available: true}
	h := newAccumulateHandler(AccumulateConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"prev_snapshot": prev,
		"observation":   obs,
		"max_tokens":    100,
		"intent":        "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tok, _ := got.Out["snapshot_tokens"].(int)
	if tok > 100 {
		t.Errorf("snapshot_tokens=%d > 100; LLM-path clamp not enforced", tok)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Errorf("LLM-path success should not flag fallback")
	}
}

func TestAccumulate_JournalHookFiresOnce(t *testing.T) {
	// The JournalWrite hook should fire exactly once per call with
	// the final snapshot. Whether the path was LLM, passthrough, or
	// fallback, callers get a single journal entry per accumulator
	// step.
	calls := 0
	var seenSnapshot string
	hook := func(snapshot string, _ int, _ string, _ bool) {
		calls++
		seenSnapshot = snapshot
	}
	h := newAccumulateHandler(AccumulateConfig{JournalWrite: hook})
	_, err := h(context.Background(), map[string]any{
		"prev_snapshot": "prior",
		"observation":   "newly observed",
		"max_tokens":    200,
		"intent":        "code",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if calls != 1 {
		t.Errorf("journal hook fired %d times, want 1", calls)
	}
	if !strings.Contains(seenSnapshot, "newly observed") {
		t.Errorf("journal hook should see final snapshot; got %q", seenSnapshot)
	}
}

func TestAccumulate_RequiresObservation(t *testing.T) {
	h := newAccumulateHandler(AccumulateConfig{})
	_, err := h(context.Background(), map[string]any{
		"prev_snapshot": "anything",
		"observation":   "",
		"max_tokens":    100,
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error for empty observation")
	}
}

func TestAccumulate_RequiresMaxTokens(t *testing.T) {
	h := newAccumulateHandler(AccumulateConfig{})
	_, err := h(context.Background(), map[string]any{
		"observation": "x",
		"max_tokens":  0,
	}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error for max_tokens<=0")
	}
}

func TestAccumulate_TemplateLoads(t *testing.T) {
	// Defensive: the embedded prompt must parse — would catch
	// frontmatter typos or filename/op mismatch before the op runs.
	resetTemplateCache()
	t.Cleanup(resetTemplateCache)
	if _, err := LoadTemplate("attend_accumulate"); err != nil {
		t.Errorf("LoadTemplate: %v", err)
	}
}
