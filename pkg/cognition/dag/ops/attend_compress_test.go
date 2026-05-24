package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// TestCompress_Passthrough pins the under-budget path: input that fits
// returns verbatim, lossy=false, output token count matches input.
func TestCompress_Passthrough(t *testing.T) {
	spec := CompressSpec()
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        "hello world",
		"max_tokens": 100,
		"intent":     "greeting",
	}, dag.Budget{LatencyMS: 1000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["compressed"].(string); got != "hello world" {
		t.Errorf("passthrough should preserve input; got %q", got)
	}
	if lossy, _ := res.Out["lossy"].(bool); lossy {
		t.Errorf("passthrough should not be lossy")
	}
}

// TestCompress_NoCap pins max_tokens=0 = no-cap semantics. A node that
// gets a SalienceContract with no MaxOutputTokens must passthrough so
// callers can attach an intent for observability without enforcing.
func TestCompress_NoCap(t *testing.T) {
	spec := CompressSpec()
	bigInput := strings.Repeat("x", 4000)
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigInput,
		"max_tokens": 0,
		"intent":     "preserve everything",
	}, dag.Budget{LatencyMS: 1000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := res.Out["compressed"].(string); got != bigInput {
		t.Errorf("max_tokens=0 should passthrough verbatim")
	}
}

// TestCompress_Truncates pins the over-budget path: input larger than
// max_tokens gets truncated, lossy=true, the marker carries the intent
// (so the calibration loop can attribute a downstream failure to the
// specific compression decision).
func TestCompress_Truncates(t *testing.T) {
	spec := CompressSpec()
	bigInput := strings.Repeat("abcd", 500) // ~2000 chars ~ 500 tokens under 4-char rule
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigInput,
		"max_tokens": 50,
		"intent":     "find TODOs",
	}, dag.Budget{LatencyMS: 1000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := res.Out["compressed"].(string)
	if !strings.Contains(got, "[…compressed-stub: find TODOs]") {
		t.Errorf("truncation marker missing intent; got %q", got)
	}
	if len(got) >= len(bigInput) {
		t.Errorf("expected truncation; got %d chars (input %d)", len(got), len(bigInput))
	}
	if lossy, _ := res.Out["lossy"].(bool); !lossy {
		t.Errorf("over-budget truncation must flag lossy=true")
	}
	// The OutputTokens cost must reflect what was actually kept, not
	// the original size — that's what the budget axis tracks.
	if res.CostConsumed.OutputTokens >= 500 {
		t.Errorf("CostConsumed.OutputTokens should be near max_tokens, got %d", res.CostConsumed.OutputTokens)
	}
}

// TestCompress_BudgetProviderWinsOverCfg — slice 7 of
// docs/per-node-routing-plan.md: when an LLM-spawned attend.compress
// arrives with Budget.Provider set by the executor's Router, the
// handler uses it instead of cfg.Provider. The synthetic
// salience-compression path (applySalienceCompression) deliberately
// passes a clean Budget so cfg.Provider stays in force there — this
// test exercises the LLM-spawned path. Different response strings
// per provider let the test attribute the actual call.
func TestCompress_BudgetProviderWinsOverCfg(t *testing.T) {
	cfgP := &scriptedProvider{response: "FROM-CFG-COMPRESSED", available: true}
	budgetP := &scriptedProvider{response: "FROM-BUDGET-COMPRESSED", available: true}

	spec := CompressSpec(CompressConfig{Provider: cfgP})
	bigInput := strings.Repeat("abcd", 500)

	b := dag.Budget{LatencyMS: 60000, Tokens: 4000, Depth: 5}
	b.Provider = budgetP
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigInput,
		"max_tokens": 50,
		"intent":     "find TODOs",
	}, b)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := res.Out["compressed"].(string)
	if !strings.Contains(got, "FROM-BUDGET-COMPRESSED") {
		t.Errorf("expected Budget.Provider response in compressed output, got %q", got)
	}
	if strings.Contains(got, "FROM-CFG-COMPRESSED") {
		t.Errorf("cfg.Provider response leaked into output: %q", got)
	}
}

// TestCompress_LegacyFallbackWhenNoBudgetProvider — pins the
// synthetic-path behavior (and any callers without a Router wired):
// nil Budget.Provider keeps cfg.Provider in force, preserving
// pre-routing behavior exactly.
func TestCompress_LegacyFallbackWhenNoBudgetProvider(t *testing.T) {
	cfgP := &scriptedProvider{response: "FROM-CFG-COMPRESSED", available: true}
	spec := CompressSpec(CompressConfig{Provider: cfgP})
	bigInput := strings.Repeat("abcd", 500)

	// No Provider set on Budget — synthetic-path shape.
	res, err := spec.Handler(context.Background(), map[string]any{
		"raw":        bigInput,
		"max_tokens": 50,
		"intent":     "find TODOs",
	}, dag.Budget{LatencyMS: 60000, Tokens: 4000, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := res.Out["compressed"].(string)
	if !strings.Contains(got, "FROM-CFG-COMPRESSED") {
		t.Errorf("cfg.Provider must drive compression on synthetic / no-Router path; got %q", got)
	}
}

// TestCompress_RegisteredInDefaults pins discoverability — every
// RegisterDefaults caller gets the compressor for free.
func TestCompress_RegisteredInDefaults(t *testing.T) {
	reg := dag.NewRegistry()
	if _, err := RegisterDefaults(reg, DefaultsConfig{}); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	if _, err := reg.Get("attend.compress"); err != nil {
		t.Fatalf("attend.compress not registered: %v", err)
	}
}
