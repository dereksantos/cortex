package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// stubProvider returns a canned response, regardless of prompt.
type stubProvider struct {
	resp        string
	available   bool
	totalTokens int
}

func (s *stubProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return s.resp, nil
}
func (s *stubProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return s.resp, nil
}
func (s *stubProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	return s.resp, llm.GenerationStats{InputTokens: 0, OutputTokens: s.totalTokens}, nil
}
func (s *stubProvider) IsAvailable() bool { return s.available }
func (s *stubProvider) Name() string      { return "stub" }

func TestExtractOverview_HappyPath(t *testing.T) {
	canned := `Sure, here's the JSON: {"role":"source","summary":"HTTP router with middleware","exports":["NewRouter","Use","Handler"],"dependencies":["net/http","context"],"importance":0.8}`
	spec := ExtractOverviewSpec(ExtractOverviewConfig{Provider: &stubProvider{resp: canned, available: true}})
	res, err := spec.Handler(context.Background(),
		map[string]any{
			"content":        "package main\n\nfunc NewRouter() {}\n",
			"source":         "bootstrap:test:abc",
			"lang_hint":      "go",
			"file_role_hint": "source",
		},
		dag.Budget{LatencyMS: 60000, Tokens: 1000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ov, ok := res.Out["overview"].(Overview)
	if !ok {
		t.Fatalf("overview missing or wrong type: %T", res.Out["overview"])
	}
	if ov.Role != "source" {
		t.Errorf("Role = %q, want source", ov.Role)
	}
	if ov.Summary != "HTTP router with middleware" {
		t.Errorf("Summary = %q", ov.Summary)
	}
	if len(ov.Exports) != 3 || ov.Exports[0] != "NewRouter" {
		t.Errorf("Exports = %v", ov.Exports)
	}
	if ov.Importance != 0.8 {
		t.Errorf("Importance = %v", ov.Importance)
	}
	if fb := res.Out["fallback"].(bool); fb {
		t.Error("fallback=true on happy-path provider call")
	}
}

func TestExtractOverview_MechanicalFallback_NilProvider(t *testing.T) {
	spec := ExtractOverviewSpec(ExtractOverviewConfig{Provider: nil})
	res, err := spec.Handler(context.Background(),
		map[string]any{
			"content":   "# project\n\nA tiny example.\n",
			"source":    "bootstrap:README.md:xyz",
			"lang_hint": "md",
		},
		dag.Budget{LatencyMS: 60000, Tokens: 1000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Error("fallback=false with nil provider")
	}
	ov := res.Out["overview"].(Overview)
	if ov.Summary != "# project" {
		t.Errorf("Summary = %q, want '# project' (first non-blank line)", ov.Summary)
	}
	if ov.Role != "doc" {
		t.Errorf("Role = %q, want 'doc' for md lang_hint", ov.Role)
	}
	if ov.Importance != 0.5 {
		t.Errorf("Importance = %v, want 0.5", ov.Importance)
	}
}

func TestExtractOverview_MechanicalFallback_ThinBudget(t *testing.T) {
	spec := ExtractOverviewSpec(ExtractOverviewConfig{Provider: &stubProvider{available: true, resp: "{}"}})
	res, err := spec.Handler(context.Background(),
		map[string]any{
			"content":   "alpha\nbravo\n",
			"source":    "src",
			"lang_hint": "go",
		},
		dag.Budget{LatencyMS: 50, Tokens: 1000}) // below fallbackBelowLatencyMS
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Error("expected fallback=true on thin budget")
	}
}

func TestExtractOverview_MalformedJSON_Fallback(t *testing.T) {
	spec := ExtractOverviewSpec(ExtractOverviewConfig{Provider: &stubProvider{resp: "not even json", available: true}})
	res, err := spec.Handler(context.Background(),
		map[string]any{"content": "hello\n", "source": "x", "lang_hint": "txt"},
		dag.Budget{LatencyMS: 60000, Tokens: 1000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Error("expected fallback=true on malformed model output")
	}
	ov := res.Out["overview"].(Overview)
	if ov.Summary != "hello" {
		t.Errorf("Summary = %q", ov.Summary)
	}
}

func TestExtractOverview_MissingContentErrors(t *testing.T) {
	spec := ExtractOverviewSpec(ExtractOverviewConfig{})
	_, err := spec.Handler(context.Background(), map[string]any{}, dag.Budget{LatencyMS: 1000})
	if err == nil {
		t.Error("expected error on missing content")
	}
}

func TestExtractOverview_ImportanceClampedAndListsCapped(t *testing.T) {
	canned := `{"role":"source","summary":"x","exports":["a","b","c","d","e","f","g"],"dependencies":[],"importance":2.5}`
	spec := ExtractOverviewSpec(ExtractOverviewConfig{Provider: &stubProvider{resp: canned, available: true}})
	res, err := spec.Handler(context.Background(),
		map[string]any{"content": "y", "source": "z"},
		dag.Budget{LatencyMS: 60000})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ov := res.Out["overview"].(Overview)
	if ov.Importance != 1.0 {
		t.Errorf("Importance = %v, want clamped to 1.0", ov.Importance)
	}
	if len(ov.Exports) != 5 {
		t.Errorf("Exports len = %d, want 5 (capped)", len(ov.Exports))
	}
}

func TestExtractOverview_TemplateLoads(t *testing.T) {
	resetTemplateCache()
	pt, err := LoadTemplate("maintain_extract_overview")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	if pt.Meta.MaxOutputTokens > MaxOutputBudget {
		t.Errorf("template MaxOutputTokens=%d exceeds cap %d", pt.Meta.MaxOutputTokens, MaxOutputBudget)
	}
	// Render with the four declared vars.
	rendered, err := pt.Render(map[string]any{
		"content":        "package main\n",
		"source":         "src",
		"lang_hint":      "go",
		"file_role_hint": "source",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "package main") {
		t.Error("rendered prompt missing content")
	}
}
