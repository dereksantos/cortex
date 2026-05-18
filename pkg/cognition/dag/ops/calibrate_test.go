//go:build calibrate
// +build calibrate

// Build-tag-guarded calibration probe. Exercises each LLM-backed op
// once with a representative input and reports observed CostConsumed
// (latency_ms + tokens) plus output preview. Used to set the
// dag.NodeSpec.Cost hints to realistic values from real model
// measurements rather than vendor-doc headroom estimates.
//
// Usage:
//
//	go test -tags=calibrate ./pkg/cognition/dag/ops/ -run TestCalibrate -v
//
// Requires the OpenRouter key in the macOS keychain (entry
// "cortex-openrouter") or $OPEN_ROUTER_API_KEY. Skips if no key.

package ops

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

func newCalibrationProvider(t *testing.T) llm.Provider {
	t.Helper()
	provider, source, err := llm.NewLLMClient(nil)
	if err != nil || provider == nil || !provider.IsAvailable() {
		t.Skipf("no LLM provider available (set OPEN_ROUTER_API_KEY or use keychain cortex-openrouter): %v", err)
	}
	t.Logf("provider resolved via %s", source)
	return provider
}

func sampleResults() []cognition.Result {
	return []cognition.Result{
		{ID: "auth_decision", Content: "Decision: Use JWT with HS256 and 24h expiry", Category: "decision", Score: 0.7},
		{ID: "auth_module", Content: "Auth module wraps login and session validation", Category: "pattern", Score: 0.6},
		{ID: "jwt_handler", Content: "JWT validation including expiry and signature checks", Category: "pattern", Score: 0.65},
	}
}

func TestCalibrate_attendRerank(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewRerankHandler(RerankConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"query":      "how does JWT token expiry work in this project?",
		"candidates": sampleResults(),
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] attend.rerank: wall=%dms cost=%dms/%dtok fallback=%v reason=%q\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens, res.Out["fallback"], res.Out["reason"])
}

func TestCalibrate_valueScore(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewScoreHandler(ScoreConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"query":     "how does JWT token expiry work?",
		"candidate": "JWT validation including expiry and signature checks",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] value.score: wall=%dms cost=%dms/%dtok fallback=%v lb=%v conf=%v\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], res.Out["load_bearing"], res.Out["confidence"])
}

func TestCalibrate_valueDetectContradiction(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewDetectContradictionHandler(DetectContradictionConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"candidate": "Use Redis for session storage — fast and battle-tested",
		"priors": []cognition.Result{
			{ID: "p1", Content: "Use postgres for all persistent data"},
			{ID: "p2", Content: "Avoid Redis — adds infrastructure overhead we don't need"},
			{ID: "p3", Content: "Sessions live in the JWT itself, not server-side"},
		},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] value.detect_contradiction: wall=%dms cost=%dms/%dtok fallback=%v conflicts=%v with=%v\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], res.Out["conflicts"], res.Out["conflicts_with"])
}

func TestCalibrate_decideInject(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewInjectHandler(InjectConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"query":      "how does authentication work?",
		"candidates": sampleResults(),
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] decide.inject: wall=%dms cost=%dms/%dtok fallback=%v decision=%v conf=%v\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], res.Out["decision"], res.Out["confidence"])
}

func TestCalibrate_decideShouldCapture(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewShouldCaptureHandler(ShouldCaptureConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"event": "Decided to use pgx instead of database/sql for all postgres queries after benchmark review showed 2x throughput",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] decide.should_capture: wall=%dms cost=%dms/%dtok fallback=%v capture=%v tag=%v\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], res.Out["capture"], res.Out["tag"])
}

func TestCalibrate_modelPredictNext(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewPredictNextHandler(PredictNextConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"current":        "how does authentication work?",
		"recent_queries": []string{"where is auth.go", "show me the login handler"},
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	fmt.Printf("[CALIBRATE] model.predict_next: wall=%dms cost=%dms/%dtok fallback=%v predictions=%v\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], res.Out["predictions"])
}

func TestCalibrate_maintainExtractInsight(t *testing.T) {
	provider := newCalibrationProvider(t)
	h := NewExtractInsightHandler(ExtractInsightConfig{Provider: provider})
	start := time.Now()
	res, err := h(context.Background(), map[string]any{
		"content": "After benchmarking, decided to use pgx instead of database/sql for postgres queries. Pgx is 2x faster and supports COPY natively. Rejected sqlx because of marshalling overhead.",
		"source":  "decision-note",
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	wall := time.Since(start)
	insights, _ := res.Out["insights"].([]Insight)
	fmt.Printf("[CALIBRATE] maintain.extract_insight: wall=%dms cost=%dms/%dtok fallback=%v insights=%d\n",
		wall.Milliseconds(), res.CostConsumed.LatencyMS, res.CostConsumed.Tokens,
		res.Out["fallback"], len(insights))
	for i, ins := range insights {
		fmt.Printf("  [%d] %s (%s, %.2f)\n", i, ins.Content, ins.Category, ins.Importance)
	}
}
