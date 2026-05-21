package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestRecallSummary_modelProducesGroundedAnswer(t *testing.T) {
	// No real storage in this test — handler treats nil storage as
	// "no prior context indexed", forces grounded=false even when the
	// model claims grounded=true. That's the safety invariant under
	// test here AND the proof the synthesis path runs end-to-end.
	p := &scriptedProvider{
		response:  `{"answer":"We chose pgx for the postgres layer.","grounded":true,"why":"decision present"}`,
		available: true,
	}
	var captured string
	h := NewRecallSummaryHandler(RecallSummaryConfig{
		Provider:   p,
		OnResponse: func(r string) { captured = r },
	})
	got, err := h(context.Background(), map[string]any{"prompt": "what did we pick for postgres?"}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ans, _ := got.Out["answer"].(string); ans != "We chose pgx for the postgres layer." {
		t.Errorf("got answer=%q", ans)
	}
	if captured == "" {
		t.Error("OnResponse should fire with the answer")
	}
	if g, _ := got.Out["grounded"].(bool); g {
		t.Error("with no storage matches, grounded must be forced false even if the model claims true")
	}
	if rc, _ := got.Out["results_count"].(int); rc != 0 {
		t.Errorf("expected results_count=0 with nil storage, got %d", rc)
	}
}

func TestRecallSummary_providerUnavailableUsesMechanicalFallback(t *testing.T) {
	var captured string
	h := NewRecallSummaryHandler(RecallSummaryConfig{
		Provider:   nil,
		OnResponse: func(r string) { captured = r },
	})
	got, _ := h(context.Background(), map[string]any{"prompt": "what did we pick?"}, dag.DefaultTurnBudget())
	if ans, _ := got.Out["answer"].(string); ans != mechanicalRecallReply {
		t.Errorf("expected mechanical fallback, got %q", ans)
	}
	if captured != mechanicalRecallReply {
		t.Error("OnResponse should fire even on mechanical fallback")
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true with nil provider")
	}
}

func TestRecallSummary_parseErrorFallsBack(t *testing.T) {
	p := &scriptedProvider{response: "not json", available: true}
	h := NewRecallSummaryHandler(RecallSummaryConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "x"}, dag.DefaultTurnBudget())
	if ans, _ := got.Out["answer"].(string); ans != mechanicalRecallReply {
		t.Errorf("expected mechanical fallback on parse error, got %q", ans)
	}
}

func TestRecallSummary_emptyAnswerFallsBack(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"answer":"   ","grounded":false,"why":"x"}`,
		available: true,
	}
	h := NewRecallSummaryHandler(RecallSummaryConfig{Provider: p})
	got, _ := h(context.Background(), map[string]any{"prompt": "x"}, dag.DefaultTurnBudget())
	if ans, _ := got.Out["answer"].(string); ans != mechanicalRecallReply {
		t.Errorf("expected fallback when model returns whitespace, got %q", ans)
	}
}

func TestRecallSummary_missingPrompt(t *testing.T) {
	p := &scriptedProvider{available: true}
	h := NewRecallSummaryHandler(RecallSummaryConfig{Provider: p})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Error("expected error when prompt is missing")
	}
}

func TestRecallSummarySpec(t *testing.T) {
	spec := RecallSummarySpec(RecallSummaryConfig{})
	if spec.QualifiedName() != "decide.recall_summary" {
		t.Errorf("qualified name = %q, want decide.recall_summary", spec.QualifiedName())
	}
	if !spec.Exposable {
		t.Error("recall_summary should be Exposable so decide.next can re-invoke")
	}
}

func TestRecallSummary_fitsInRecallBudget(t *testing.T) {
	// Defense-in-depth: ensure the op's cost hint fits under
	// BudgetForIntent("recall"). If someone bumps either side without
	// thinking about the other, the recall seed silently falls back to
	// the full chain instead of doing what intent-aware routing
	// promises.
	spec := RecallSummarySpec(RecallSummaryConfig{})
	if !dag.BudgetForIntent("recall").CanAfford(spec.Cost) {
		t.Errorf("decide.recall_summary Cost=%+v does not fit under BudgetForIntent(\"recall\")=%+v",
			spec.Cost, dag.BudgetForIntent("recall"))
	}
}

func TestFormatRecallContext_emptyReturnsExplicitMarker(t *testing.T) {
	got := formatRecallContext(nil)
	if !strings.Contains(got, "no prior context") {
		t.Errorf("empty match list must produce explicit \"no prior context\" marker, got %q", got)
	}
}

func TestFormatRecallContext_includesPromptAndToolEvents(t *testing.T) {
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	matches := []*events.Event{
		{
			Timestamp: now,
			EventType: events.EventUserPrompt,
			Prompt:    "decided to use pgx instead of database/sql",
		},
		{
			Timestamp:  now.Add(time.Hour),
			EventType:  events.EventToolUse,
			ToolName:   "Edit",
			ToolResult: "modified db.go",
		},
	}
	got := formatRecallContext(matches)
	if !strings.Contains(got, "pgx") {
		t.Errorf("context block must include the user prompt content, got %q", got)
	}
	if !strings.Contains(got, "Edit") {
		t.Errorf("context block must include the tool name, got %q", got)
	}
	if !strings.Contains(got, "modified db.go") {
		t.Errorf("context block must include the tool result, got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 100, "short"},
		{"abcdef", 3, "abc…"},
		{"abc", 3, "abc"},
		{"abc", 0, "abc"},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
