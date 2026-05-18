package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// PredictNextConfig wires the handler to a provider.
type PredictNextConfig struct {
	Provider llm.Provider
}

// predictNextResponse is the parser target.
type predictNextResponse struct {
	Predictions []string `json:"predictions"`
}

// PredictNextSpec returns the NodeSpec for model.predict_next.
func PredictNextSpec(cfg PredictNextConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncModel,
		Op:          "predict_next",
		Description: "predict the top 3 likely follow-up queries given recent session activity",
		Inputs: []dag.ParamSpec{
			{Name: "recent_queries", Type: "[]string", Required: false},
			{Name: "current", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "predictions", Type: "[]string"},
			{Name: "count", Type: "int"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    predictNextCostHint,
		Handler: NewPredictNextHandler(cfg),
	}
}

// predictNextCostHint — calibrated 2026-05-18 against OpenRouter
// Haiku 4.5 (calibrate_test.go probe): top-3 prediction call ≈
// 8,913ms wall / 279 tokens. Set 11000ms / 350 tok for ~15% headroom.
// 10× gap vs pre-calibration guess (850ms / 200 tok).
var predictNextCostHint = dag.Cost{LatencyMS: 11000, Tokens: 350}

// NewPredictNextHandler returns a dag.Handler for model.predict_next.
//
// Inputs:
//   - recent_queries ([]string | []any) — optional; prior session queries
//   - current (string)                  — required; the active query
//
// Outputs:
//   - predictions ([]string) — exactly 3 (truncated/padded)
//   - count (int)
//   - fallback (bool)
//
// Fallback: noun-extraction echo. Pulls 3-char+ nouns from the
// current query and synthesizes generic follow-ups ("where is the X",
// "how does X work", "what configures X"). Coarse but
// deterministic — never invents nouns the query doesn't contain.
func NewPredictNextHandler(cfg PredictNextConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		current := readString(in, "current")
		if current == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("model.predict_next: 'current' (string) is required")
		}
		recent, _ := readStringSlice(in, "recent_queries")

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			preds := nounEchoPredict(current)
			return dag.NodeResult{
				Out: map[string]any{
					"predictions": preds,
					"count":       len(preds),
					"fallback":    true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("model_predict_next")
		if terr != nil {
			preds := nounEchoPredict(current)
			return dag.NodeResult{
				Out: map[string]any{
					"predictions": preds,
					"count":       len(preds),
					"fallback":    true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		recentRendered := "(none)"
		if len(recent) > 0 {
			recentRendered = "- " + strings.Join(recent, "\n- ")
		}
		prompt, rerr := pt.Render(map[string]any{
			"recent_queries": recentRendered,
			"current":        current,
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("model.predict_next: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			preds := nounEchoPredict(current)
			return dag.NodeResult{
				Out: map[string]any{
					"predictions": preds,
					"count":       len(preds),
					"fallback":    true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, perr := parsePredictNextResponse(resp)
		if perr != nil || len(parsed.Predictions) == 0 {
			preds := nounEchoPredict(current)
			return dag.NodeResult{
				Out: map[string]any{
					"predictions": preds,
					"count":       len(preds),
					"fallback":    true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		// Truncate to top 3.
		if len(parsed.Predictions) > 3 {
			parsed.Predictions = parsed.Predictions[:3]
		}

		return dag.NodeResult{
			Out: map[string]any{
				"predictions": parsed.Predictions,
				"count":       len(parsed.Predictions),
				"fallback":    false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parsePredictNextResponse(resp string) (predictNextResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return predictNextResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed predictNextResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return predictNextResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	// Filter empty strings.
	out := make([]string, 0, len(parsed.Predictions))
	for _, p := range parsed.Predictions {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	parsed.Predictions = out
	return parsed, nil
}

// nounEchoPredict synthesizes 0–3 generic follow-ups from nouns
// extracted from the current query. Never invents — every emitted
// prediction echoes a noun from the query.
func nounEchoPredict(current string) []string {
	lower := strings.ToLower(current)
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	stopwords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "that": true,
		"this": true, "from": true, "what": true, "where": true, "how": true,
		"does": true, "into": true, "have": true, "been": true, "will": true,
	}
	nouns := []string{}
	seen := map[string]bool{}
	for _, tok := range tokens {
		if len(tok) < 4 || stopwords[tok] || seen[tok] {
			continue
		}
		seen[tok] = true
		nouns = append(nouns, tok)
		if len(nouns) >= 3 {
			break
		}
	}
	templates := []string{
		"where is the %s defined",
		"how does %s work",
		"what configures %s",
	}
	out := []string{}
	for i, n := range nouns {
		out = append(out, fmt.Sprintf(templates[i%len(templates)], n))
	}
	return out
}
