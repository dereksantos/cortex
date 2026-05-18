package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// scriptedProvider returns canned responses, paired with stub stats so
// the handler's token-cost calc has something to work with.
type scriptedProvider struct {
	response  string
	available bool
}

func (s *scriptedProvider) Name() string      { return "scripted" }
func (s *scriptedProvider) IsAvailable() bool { return s.available }
func (s *scriptedProvider) Generate(_ context.Context, _ string) (string, error) {
	return s.response, nil
}
func (s *scriptedProvider) GenerateWithSystem(_ context.Context, _, _ string) (string, error) {
	return s.response, nil
}
func (s *scriptedProvider) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return s.response, llm.GenerationStats{InputTokens: 50, OutputTokens: 30}, nil
}

func TestExtractInsight_parsesWellFormedJSON(t *testing.T) {
	p := &scriptedProvider{
		response:  `{"insights":[{"content":"Use pgx not database/sql","category":"constraint","importance":0.8}]}`,
		available: true,
	}
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"content": "decided to use pgx for postgres after sqlx perf review",
		"source":  "decision-note",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	insights, _ := got.Out["insights"].([]Insight)
	if len(insights) != 1 || insights[0].Content == "" {
		t.Fatalf("expected 1 parsed insight, got %v", insights)
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Errorf("fallback should be false on successful parse")
	}
	if got.CostConsumed.Tokens != 80 {
		t.Errorf("expected 80 tokens consumed (50+30), got %d", got.CostConsumed.Tokens)
	}
}

func TestExtractInsight_truncatesToTwo(t *testing.T) {
	// Model emits 4 insights; handler must clip to 2.
	resp := `{"insights":[
		{"content":"first","category":"decision","importance":0.9},
		{"content":"second","category":"constraint","importance":0.7},
		{"content":"third","category":"pattern","importance":0.5},
		{"content":"fourth","category":"pattern","importance":0.3}
	]}`
	p := &scriptedProvider{response: resp, available: true}
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"content": "some content",
		"source":  "x",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	insights, _ := got.Out["insights"].([]Insight)
	if len(insights) != 2 {
		t.Errorf("expected truncation to 2, got %d", len(insights))
	}
}

func TestExtractInsight_emptyEnvelope(t *testing.T) {
	p := &scriptedProvider{response: `{"insights":[]}`, available: true}
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"content": "routine refactor with no decisions",
		"source":  "transcript",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	insights, _ := got.Out["insights"].([]Insight)
	if len(insights) != 0 {
		t.Errorf("expected 0 insights for empty envelope, got %d", len(insights))
	}
	if fb, _ := got.Out["fallback"].(bool); fb {
		t.Errorf("fallback should be false — parse succeeded even with 0 insights")
	}
}

func TestExtractInsight_malformedFallsBack(t *testing.T) {
	p := &scriptedProvider{response: `garbled not-json from a small model`, available: true}
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"content": "decided to use pgx for postgres",
		"source":  "x",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("fallback should be true when JSON parse fails")
	}
	insights, _ := got.Out["insights"].([]Insight)
	// Mechanical path should catch "decided to use" → 1 insight.
	if len(insights) == 0 {
		t.Errorf("expected mechanical fallback to surface an insight from 'decided to use' marker")
	}
}

func TestExtractInsight_lowBudgetUsesFallback(t *testing.T) {
	p := &scriptedProvider{response: `{"insights":[]}`, available: true}
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: p})
	got, err := h(context.Background(), map[string]any{
		"content": "never use redis here",
		"source":  "constraint",
	}, dag.Budget{LatencyMS: 50, Tokens: 100, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("expected fallback=true when budget below threshold")
	}
}

func TestExtractInsight_noProviderUsesFallback(t *testing.T) {
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: nil})
	got, err := h(context.Background(), map[string]any{
		"content": "avoid global state in handlers",
		"source":  "review",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if fb, _ := got.Out["fallback"].(bool); !fb {
		t.Error("expected fallback=true when no provider configured")
	}
	insights, _ := got.Out["insights"].([]Insight)
	if len(insights) == 0 {
		t.Errorf("mechanical path should catch 'avoid' marker")
	}
}

func TestExtractInsight_missingContent(t *testing.T) {
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: &scriptedProvider{available: true}})
	_, err := h(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected error when content absent")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Errorf("error should mention content; got: %v", err)
	}
}

func TestExtractInsight_specRegisters(t *testing.T) {
	reg := dag.NewRegistry()
	if err := reg.Register(ExtractInsightSpec(ExtractInsightConfig{Provider: nil})); err != nil {
		t.Fatalf("register: %v", err)
	}
	spec, err := reg.Get("maintain.extract_insight")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if spec.Cost.Tokens == 0 {
		t.Error("LLM op should declare a token cost hint > 0")
	}
}

func TestMechanicalExtractInsights_categorization(t *testing.T) {
	cases := []struct {
		content      string
		expectCat    string
		expectAny    bool
		expectAtLeast int
	}{
		{"don't use database/sql here", "constraint", true, 1},
		{"never commit secrets", "constraint", true, 1},
		{"decided to use pgx", "decision", true, 1},
		{"prefer functional options", "decision", true, 1},
		{"use chi instead of gorilla/mux", "decision", true, 1},
		{"just routine refactor", "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.content, func(t *testing.T) {
			out := mechanicalExtractInsights(tc.content)
			if tc.expectAny && len(out) < tc.expectAtLeast {
				t.Errorf("expected at least %d insights, got %d (%v)", tc.expectAtLeast, len(out), out)
			}
			if !tc.expectAny && len(out) != 0 {
				t.Errorf("expected 0 insights, got %v", out)
			}
			if tc.expectCat != "" && len(out) > 0 && out[0].Category != tc.expectCat {
				t.Errorf("expected category=%q, got %q (%+v)", tc.expectCat, out[0].Category, out[0])
			}
		})
	}
}
