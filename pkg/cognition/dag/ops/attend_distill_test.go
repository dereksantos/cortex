package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// scriptedDistillProvider drives the two-pass distill path: pass 1
// returns the queued facts, pass 2 returns the queued compressed
// output. Optional failures let tests exercise the fallback paths.
type scriptedDistillProvider struct {
	facts       string
	compressed  string
	factsErr    error
	compressErr error
	available   bool
	calls       []string
}

func (s *scriptedDistillProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (s *scriptedDistillProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return "", nil
}
func (s *scriptedDistillProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	s.calls = append(s.calls, prompt)
	// Pass 1 prompt mentions "List the concrete facts"; pass 2 mentions
	// "Rewrite the prioritized fact list".
	if strings.Contains(prompt, "List the concrete facts") {
		if s.factsErr != nil {
			return "", llm.GenerationStats{}, s.factsErr
		}
		return s.facts, llm.GenerationStats{InputTokens: 50, OutputTokens: 30}, nil
	}
	if strings.Contains(prompt, "Rewrite the prioritized") {
		if s.compressErr != nil {
			return "", llm.GenerationStats{}, s.compressErr
		}
		return s.compressed, llm.GenerationStats{InputTokens: 80, OutputTokens: 40}, nil
	}
	return "", llm.GenerationStats{}, errors.New("unexpected prompt shape")
}
func (s *scriptedDistillProvider) Name() string      { return "scripted-distill" }
func (s *scriptedDistillProvider) IsAvailable() bool { return s.available }

func TestDistill_Passthrough_UnderBudget(t *testing.T) {
	spec := DistillSpec()
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        "hello",
		"max_tokens": 100,
		"intent":     "greeting",
	}, dag.Budget{LatencyMS: 10000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Out["compressed"].(string) != "hello" {
		t.Errorf("passthrough should preserve input")
	}
	if passes := res.Out["passes"].(int); passes != 0 {
		t.Errorf("passthrough should report passes=0, got %d", passes)
	}
	if fallback, _ := res.Out["fallback"].(bool); fallback {
		t.Errorf("passthrough is not a fallback")
	}
}

func TestDistill_TwoPass_LLMSuccess(t *testing.T) {
	p := &scriptedDistillProvider{
		facts:      "1 - foo writes the result\n2 - bar validates input\n3 - baz is helper",
		compressed: "foo writes result; bar validates; baz helper",
		available:  true,
	}
	spec := DistillSpec(DistillConfig{Provider: p})

	bigRaw := strings.Repeat("padding text here ", 100) // ~400 tokens > 60 budget
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 60,
		"intent":     "find writers",
	}, dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(p.calls) != 2 {
		t.Errorf("expected 2 LLM calls (extract + emit), got %d", len(p.calls))
	}
	if !strings.Contains(p.calls[0], "List the concrete facts") {
		t.Errorf("call 1 should be the extract prompt; got: %q", p.calls[0][:80])
	}
	if !strings.Contains(p.calls[1], "Rewrite the prioritized") {
		t.Errorf("call 2 should be the emit prompt; got: %q", p.calls[1][:80])
	}
	if passes := res.Out["passes"].(int); passes != 2 {
		t.Errorf("passes=%d; want 2", passes)
	}
	if !strings.Contains(res.Out["compressed"].(string), "foo writes result") {
		t.Errorf("compressed should carry the emit-pass content")
	}
	if facts := res.Out["facts"].(string); !strings.Contains(facts, "1 - foo writes") {
		t.Errorf("facts artifact missing extract-pass content; got %q", facts)
	}
	if fallback, _ := res.Out["fallback"].(bool); fallback {
		t.Errorf("two-pass success should not be a fallback")
	}
}

func TestDistill_ExtractFails_FallsBackToStub(t *testing.T) {
	p := &scriptedDistillProvider{
		factsErr:  errors.New("extract failed"),
		available: true,
	}
	spec := DistillSpec(DistillConfig{Provider: p})
	bigRaw := strings.Repeat("x ", 500)
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 50,
		"intent":     "find Xs",
	}, dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("extract failure should mark fallback=true")
	}
	if passes := res.Out["passes"].(int); passes != 1 {
		t.Errorf("fallback path should report passes=1, got %d", passes)
	}
	if !strings.Contains(res.Out["compressed"].(string), "[…distilled-stub: find Xs]") {
		t.Errorf("fallback should carry the distilled-stub marker")
	}
}

func TestDistill_EmitFails_FallsBackToStub(t *testing.T) {
	p := &scriptedDistillProvider{
		facts:       "1 - alpha",
		compressErr: errors.New("emit failed"),
		available:   true,
	}
	spec := DistillSpec(DistillConfig{Provider: p})
	bigRaw := strings.Repeat("x ", 500)
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 50,
		"intent":     "stuff",
	}, dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("emit failure should fall back")
	}
}

func TestDistill_LowBudget_SkipsLLMPath(t *testing.T) {
	// Budget below minDistillLatencyMS — the two-pass path is skipped
	// in favor of the deterministic stub.
	p := &scriptedDistillProvider{
		facts:      "1 - foo",
		compressed: "out",
		available:  true,
	}
	spec := DistillSpec(DistillConfig{Provider: p})
	bigRaw := strings.Repeat("y ", 500)
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 50,
		"intent":     "things",
	}, dag.Budget{LatencyMS: 1000, Tokens: 5000, Depth: 5}) // 1s < 4s gate
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(p.calls) != 0 {
		t.Errorf("tight-budget path should skip LLM; got %d calls", len(p.calls))
	}
	if !res.Out["fallback"].(bool) {
		t.Errorf("tight-budget should fall back")
	}
}

// TestDistill_BudgetProviderWinsOverCfg — slice 7 of
// docs/per-node-routing-plan.md: when Budget.Provider is set by the
// executor's Router (the LLM-spawned path), the handler uses it
// instead of cfg.Provider. Differentiated providers tell the test
// which one fired via the compressed output.
func TestDistill_BudgetProviderWinsOverCfg(t *testing.T) {
	cfgP := &scriptedDistillProvider{
		facts:      "1 - from cfg",
		compressed: "CFG-COMPRESSED",
		available:  true,
	}
	budgetP := &scriptedDistillProvider{
		facts:      "1 - from budget",
		compressed: "BUDGET-COMPRESSED",
		available:  true,
	}
	spec := DistillSpec(DistillConfig{Provider: cfgP})

	bigRaw := strings.Repeat("padding text here ", 100)
	b := dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 5}
	b.Provider = budgetP
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 60,
		"intent":     "find writers",
	}, b)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := res.Out["compressed"].(string)
	if !strings.Contains(got, "BUDGET-COMPRESSED") {
		t.Errorf("expected Budget.Provider response, got %q", got)
	}
	// cfgP should never have been called.
	if len(cfgP.calls) != 0 {
		t.Errorf("cfg.Provider should NOT fire when Budget.Provider is set; got %d calls", len(cfgP.calls))
	}
}

// TestDistill_LegacyFallbackWhenNoBudgetProvider — nil Budget.Provider
// keeps cfg.Provider in force (preserves the synthetic /
// pre-routing-Router behavior).
func TestDistill_LegacyFallbackWhenNoBudgetProvider(t *testing.T) {
	cfgP := &scriptedDistillProvider{
		facts:      "1 - from cfg",
		compressed: "CFG-COMPRESSED",
		available:  true,
	}
	spec := DistillSpec(DistillConfig{Provider: cfgP})

	bigRaw := strings.Repeat("padding text here ", 100)
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigRaw,
		"max_tokens": 60,
		"intent":     "find writers",
	}, dag.Budget{LatencyMS: 30000, Tokens: 5000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := res.Out["compressed"].(string)
	if !strings.Contains(got, "CFG-COMPRESSED") {
		t.Errorf("cfg.Provider must drive distillation without a Router; got %q", got)
	}
}

func TestDistill_RegisteredInDefaults(t *testing.T) {
	reg := dag.NewRegistry()
	if _, err := RegisterDefaults(reg, DefaultsConfig{}); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	if _, err := reg.Get("attend.distill"); err != nil {
		t.Errorf("attend.distill not registered: %v", err)
	}
	// Sibling op still present.
	if _, err := reg.Get("attend.compress"); err != nil {
		t.Errorf("attend.compress disappeared: %v", err)
	}
}

func TestShouldDistill_RoutesByFallbackRate(t *testing.T) {
	cal := &dag.SalienceCalibration{
		PerIntent: map[string]dag.SalienceCapFit{
			"clean-read":     {FallbackRate: 0.05, Samples: 20},
			"hard-summarize": {FallbackRate: 0.55, Samples: 12},
			"edge":           {FallbackRate: 0.30, Samples: 5},
		},
	}
	tests := []struct {
		intent string
		want   bool
	}{
		{"clean-read", false},     // below threshold
		{"hard-summarize", true},  // above threshold
		{"edge", true},            // at threshold
		{"unknown-intent", false}, // not in calibration
	}
	for _, tc := range tests {
		t.Run(tc.intent, func(t *testing.T) {
			got := ShouldDistill(tc.intent, cal)
			if got != tc.want {
				t.Errorf("ShouldDistill(%q) = %v, want %v", tc.intent, got, tc.want)
			}
		})
	}
}

func TestShouldDistill_NilCalibration_FalseEverywhere(t *testing.T) {
	if ShouldDistill("anything", nil) {
		t.Errorf("nil calibration should yield false (conservative default)")
	}
}
