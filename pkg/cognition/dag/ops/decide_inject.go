package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// InjectConfig wires the handler to a provider.
type InjectConfig struct {
	Provider llm.Provider
}

// injectResponse is the parser target.
type injectResponse struct {
	Decision   string   `json:"decision"`
	InjectIDs  []string `json:"inject_ids"`
	Confidence float64  `json:"confidence"`
	Why        string   `json:"why"`
}

// InjectSpec returns the NodeSpec for decide.inject.
func InjectSpec(cfg InjectConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "inject",
		Description: "decide whether to inject candidates now, wait for more, or queue for next hook",
		Inputs: []dag.ParamSpec{
			{Name: "query", Type: "string", Required: true},
			{Name: "candidates", Type: "[]cognition.Result", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "decision", Type: "string"}, // "inject" | "wait" | "queue"
			{Name: "inject_ids", Type: "[]string"},
			{Name: "confidence", Type: "float64"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    injectCostHint,
		Handler: NewInjectHandler(cfg),
	}
}

// injectCostHint — small decision, ~50-70 tok output, 600ms p50 Haiku
// 4.5. Set 700ms / 150 tok for headroom.
var injectCostHint = dag.Cost{LatencyMS: 700, Tokens: 150}

// NewInjectHandler returns a dag.Handler for decide.inject.
//
// Inputs:
//   - query (string)                       — required
//   - candidates ([]cognition.Result | []any) — required (may be empty)
//
// Outputs:
//   - decision (string) — "inject" | "wait" | "queue"
//   - inject_ids ([]string)
//   - confidence (float64)
//   - why (string)
//   - fallback (bool)
//
// Fallback (score-threshold heuristic, mirrors legacy resolve.go):
//
//	- any candidate Score >= 0.8 → inject (high relevance)
//	- candidates exist but max Score < 0.5 → wait (ambiguous)
//	- otherwise → queue (some signal, not strong enough to inject now)
//
// Empty candidates → wait (no signal at all).
func NewInjectHandler(cfg InjectConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		query := readString(in, "query")
		if query == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.inject: 'query' (string) is required")
		}
		candidates, cerr := readResultSlice(in, "candidates")
		if cerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.inject: %w", cerr)
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			d, ids, conf := scoreBasedInjectDecision(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   d,
					"inject_ids": ids,
					"confidence": conf,
					"why":        "score-threshold fallback",
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("decide_inject")
		if terr != nil {
			d, ids, conf := scoreBasedInjectDecision(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   d,
					"inject_ids": ids,
					"confidence": conf,
					"why":        fmt.Sprintf("template load failed: %v", terr),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{
			"query":      query,
			"candidates": formatCandidatesForPrompt(candidates),
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.inject: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			d, ids, conf := scoreBasedInjectDecision(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   d,
					"inject_ids": ids,
					"confidence": conf,
					"why":        fmt.Sprintf("llm error: %v", gerr),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, perr := parseInjectResponse(resp)
		if perr != nil {
			d, ids, conf := scoreBasedInjectDecision(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   d,
					"inject_ids": ids,
					"confidence": conf,
					"why":        fmt.Sprintf("parse error: %v", perr),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		// Sanitize: if decision != inject, force empty inject_ids; if
		// decision == inject but inject_ids is empty, downgrade to wait.
		switch parsed.Decision {
		case "inject":
			if len(parsed.InjectIDs) == 0 {
				parsed.Decision = "wait"
			}
		case "wait", "queue":
			parsed.InjectIDs = []string{}
		default:
			// Unknown decision string — fall back.
			d, ids, conf := scoreBasedInjectDecision(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   d,
					"inject_ids": ids,
					"confidence": conf,
					"why":        fmt.Sprintf("unknown decision %q from model", parsed.Decision),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		return dag.NodeResult{
			Out: map[string]any{
				"decision":   parsed.Decision,
				"inject_ids": parsed.InjectIDs,
				"confidence": parsed.Confidence,
				"why":        parsed.Why,
				"fallback":   false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseInjectResponse(resp string) (injectResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return injectResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed injectResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return injectResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	if parsed.InjectIDs == nil {
		parsed.InjectIDs = []string{}
	}
	if parsed.Confidence < 0 {
		parsed.Confidence = 0
	}
	if parsed.Confidence > 1 {
		parsed.Confidence = 1
	}
	return parsed, nil
}

// scoreBasedInjectDecision implements the fallback heuristic. Mirrors
// the project's lived-in cognition.Resolve thresholds (internal/
// cognition/resolve.go makeDecision):
//
//   - avg ≥ 0.5  → inject (confidence = avg)
//   - max ≥ 0.8  → inject anyway (high-confidence top result rescue)
//   - avg ≥ 0.3  → queue   (confidence = avg)
//   - avg ≥ 0.2  → wait    (confidence = avg)
//   - else       → wait    (confidence = 1.0 - avg; this is the
//                           legacy "Discard" path collapsed into wait,
//                           since decide.inject is 3-way not 4-way)
//
// When inject, inject_ids = all candidates whose Score ≥ 0.5 (the
// threshold needed to clear the inject avg gate). This mirrors the
// legacy "results passed to formatter as-is" semantics.
//
// Returns (decision, inject_ids, confidence).
func scoreBasedInjectDecision(candidates []cognition.Result) (string, []string, float64) {
	if len(candidates) == 0 {
		return "wait", []string{}, 0.5
	}
	maxScore, sum := 0.0, 0.0
	for _, c := range candidates {
		sum += c.Score
		if c.Score > maxScore {
			maxScore = c.Score
		}
	}
	avgScore := sum / float64(len(candidates))

	if avgScore >= 0.5 {
		ids := []string{}
		for _, c := range candidates {
			if c.Score >= 0.5 {
				ids = append(ids, c.ID)
			}
		}
		return "inject", ids, avgScore
	}
	if maxScore >= 0.8 {
		ids := []string{}
		for _, c := range candidates {
			if c.Score >= 0.5 {
				ids = append(ids, c.ID)
			}
		}
		return "inject", ids, maxScore
	}
	if avgScore >= 0.3 {
		return "queue", []string{}, avgScore
	}
	if avgScore >= 0.2 {
		return "wait", []string{}, avgScore
	}
	return "wait", []string{}, 1.0 - avgScore
}
