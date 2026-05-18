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

// ScoreConfig wires NewScoreHandler to a provider.
type ScoreConfig struct {
	Provider llm.Provider
}

// scoreResponse is the parser target for the model's JSON output.
type scoreResponse struct {
	LoadBearing bool    `json:"load_bearing"`
	Confidence  float64 `json:"confidence"`
	Why         string  `json:"why"`
}

// ScoreSpec returns the NodeSpec for value.score.
func ScoreSpec(cfg ScoreConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncValue,
		Op:          "score",
		Description: "decide if a candidate is load-bearing for the query (Y/N + why)",
		Inputs: []dag.ParamSpec{
			{Name: "query", Type: "string", Required: true},
			{Name: "candidate", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "load_bearing", Type: "bool"},
			{Name: "confidence", Type: "float64"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    scoreCostHint,
		Handler: NewScoreHandler(cfg),
	}
}

// scoreCostHint — tight Y/N output, ~30-50 tokens; 500ms p50 on Haiku
// 4.5. Set 600ms / 120 tok for headroom.
var scoreCostHint = dag.Cost{LatencyMS: 600, Tokens: 120}

// NewScoreHandler returns a dag.Handler for value.score.
//
// Inputs:
//   - query (string)     — required
//   - candidate (string) — required; the content to evaluate
//
// Outputs:
//   - load_bearing (bool)
//   - confidence (float64)
//   - why (string)
//   - fallback (bool)
//
// Fallback: keyword overlap between query and candidate.
// load_bearing=true when at least one query token appears in candidate
// (case-insensitive, length >=4); confidence=0.5 (heuristic guess).
func NewScoreHandler(cfg ScoreConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		query := readString(in, "query")
		candidate := readString(in, "candidate")
		if query == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.score: 'query' (string) is required")
		}
		if candidate == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.score: 'candidate' (string) is required")
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			lb, conf := keywordOverlapScore(query, candidate)
			return dag.NodeResult{
				Out: map[string]any{
					"load_bearing": lb,
					"confidence":   conf,
					"why":          "keyword overlap (fallback)",
					"fallback":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("value_score")
		if terr != nil {
			lb, conf := keywordOverlapScore(query, candidate)
			return dag.NodeResult{
				Out: map[string]any{
					"load_bearing": lb,
					"confidence":   conf,
					"why":          fmt.Sprintf("template load failed: %v", terr),
					"fallback":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{"query": query, "candidate": candidate})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.score: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			lb, conf := keywordOverlapScore(query, candidate)
			return dag.NodeResult{
				Out: map[string]any{
					"load_bearing": lb,
					"confidence":   conf,
					"why":          fmt.Sprintf("llm error: %v", gerr),
					"fallback":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, perr := parseScoreResponse(resp)
		if perr != nil {
			lb, conf := keywordOverlapScore(query, candidate)
			return dag.NodeResult{
				Out: map[string]any{
					"load_bearing": lb,
					"confidence":   conf,
					"why":          fmt.Sprintf("parse error: %v", perr),
					"fallback":     true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		return dag.NodeResult{
			Out: map[string]any{
				"load_bearing": parsed.LoadBearing,
				"confidence":   parsed.Confidence,
				"why":          parsed.Why,
				"fallback":     false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// parseScoreResponse extracts the score envelope.
func parseScoreResponse(resp string) (scoreResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return scoreResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed scoreResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return scoreResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	if parsed.Confidence < 0 {
		parsed.Confidence = 0
	}
	if parsed.Confidence > 1 {
		parsed.Confidence = 1
	}
	return parsed, nil
}

// keywordOverlapScore checks for any query token (len >=4) appearing
// in the candidate, case-insensitive. Returns load_bearing + heuristic
// confidence (0.5 when matched, 0.4 when not — neither is high-conf).
func keywordOverlapScore(query, candidate string) (bool, float64) {
	q := strings.ToLower(query)
	c := strings.ToLower(candidate)
	tokens := strings.FieldsFunc(q, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, tok := range tokens {
		if len(tok) >= 4 && strings.Contains(c, tok) {
			return true, 0.5
		}
	}
	return false, 0.4
}
