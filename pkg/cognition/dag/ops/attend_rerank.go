package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// RerankConfig wires the handler to a provider at registration time.
type RerankConfig struct {
	Provider llm.Provider
}

// rerankResponse is the parser target for the model's JSON output.
type rerankResponse struct {
	Ranking []string `json:"ranking"`
	Reason  string   `json:"reason"`
}

// RerankSpec returns the NodeSpec for attend.rerank.
func RerankSpec(cfg RerankConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "rerank",
		Description: "rerank N candidates by relevance to a query via a micro-LLM call (or score fallback)",
		Inputs: []dag.ParamSpec{
			{Name: "query", Type: "string", Required: true},
			{Name: "candidates", Type: "[]cognition.Result", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "reranked", Type: "[]cognition.Result"},
			{Name: "reason", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    rerankCostHint,
		Handler: NewRerankHandler(cfg),
	}
}

// rerankCostHint — calibrated 2026-05-18 against OpenRouter Haiku 4.5
// via the calibrate_test.go probe: single call with 3 candidates ≈
// 18,862ms wall / 315 tokens (in+out). The wall time is dominated by
// the OpenRouter round-trip — network + queueing + model inference —
// not the model's own inference latency. Set 22000ms / 400 tok for
// ~15% headroom. The 24× gap vs my pre-calibration guess
// (800ms / 250 tok) is documented in docs/eval-journal.md
// "Stage 2 cost recalibration".
var rerankCostHint = dag.Cost{LatencyMS: 22000, Tokens: 400}

// NewRerankHandler returns a dag.Handler for attend.rerank.
//
// Inputs:
//   - query (string)                       — required
//   - candidates ([]cognition.Result | []any) — required; non-empty
//
// Outputs:
//   - reranked ([]cognition.Result) — candidates in new order
//   - reason (string)                — model's 1-line rationale
//   - fallback (bool)                — true when score-based fallback ran
//
// Self-modulates: low budget OR no provider OR LLM error OR malformed
// output → fall back to the candidate's existing Score field (the
// reflex/mechanical scoring upstream). When no candidate has a Score,
// the order is preserved as given.
func NewRerankHandler(cfg RerankConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		query := readString(in, "query")
		if query == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.rerank: 'query' (string) is required")
		}
		candidates, cerr := readResultSlice(in, "candidates")
		if cerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("attend.rerank: %w", cerr)
		}
		if len(candidates) == 0 {
			return dag.NodeResult{
				Out:          map[string]any{"reranked": candidates, "reason": "no candidates", "fallback": false},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		// Fallback when LLM not viable.
		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			reranked := scoreFallbackRerank(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"reranked": reranked,
					"reason":   "score fallback (no LLM)",
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("attend_rerank")
		if terr != nil {
			reranked := scoreFallbackRerank(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"reranked": reranked,
					"reason":   fmt.Sprintf("template load failed: %v", terr),
					"fallback": true,
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
			}, fmt.Errorf("attend.rerank: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			reranked := scoreFallbackRerank(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"reranked": reranked,
					"reason":   fmt.Sprintf("llm error: %v", gerr),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, perr := parseRerankResponse(resp)
		if perr != nil {
			reranked := scoreFallbackRerank(candidates)
			return dag.NodeResult{
				Out: map[string]any{
					"reranked": reranked,
					"reason":   fmt.Sprintf("parse error: %v", perr),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		reranked := applyRanking(candidates, parsed.Ranking)
		return dag.NodeResult{
			Out: map[string]any{
				"reranked": reranked,
				"reason":   parsed.Reason,
				"fallback": false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// readResultSlice extracts []cognition.Result from in[key], accepting
// either the typed slice or YAML-decoded []any of maps.
func readResultSlice(in map[string]any, key string) ([]cognition.Result, error) {
	v, ok := in[key]
	if !ok {
		return nil, fmt.Errorf("input %q is required", key)
	}
	switch x := v.(type) {
	case []cognition.Result:
		return x, nil
	case []any:
		out := make([]cognition.Result, 0, len(x))
		for i, e := range x {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: not a map", key, i)
			}
			var r cognition.Result
			if s, ok := m["id"].(string); ok {
				r.ID = s
			}
			if s, ok := m["content"].(string); ok {
				r.Content = s
			}
			if s, ok := m["category"].(string); ok {
				r.Category = s
			}
			switch s := m["score"].(type) {
			case float64:
				r.Score = s
			case int:
				r.Score = float64(s)
			case int64:
				r.Score = float64(s)
			}
			out = append(out, r)
		}
		return out, nil
	}
	return nil, fmt.Errorf("input %q has unsupported type %T", key, v)
}

// formatCandidatesForPrompt renders candidates as a
// "ID [category]: content" list the LLM can consume. Category is
// included because rerank scenarios encode a category preference
// (decision > constraint > pattern); without it the model can't honor
// the preference. Trims content to 200 chars per row so a 10-row
// prompt stays comfortably under typical input budgets.
func formatCandidatesForPrompt(cs []cognition.Result) string {
	var sb strings.Builder
	for _, c := range cs {
		content := c.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		category := c.Category
		if category == "" {
			category = "unknown"
		}
		fmt.Fprintf(&sb, "%s [%s]: %s\n", c.ID, category, content)
	}
	return sb.String()
}

// parseRerankResponse extracts the {"ranking": [...]} JSON envelope.
func parseRerankResponse(resp string) (rerankResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return rerankResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed rerankResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return rerankResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	if len(parsed.Ranking) == 0 {
		return rerankResponse{}, fmt.Errorf("ranking is empty")
	}
	return parsed, nil
}

// applyRanking reorders candidates per the ID list. Candidates whose
// IDs don't appear in the ranking are appended in original order
// (preserve everything; let downstream filter).
func applyRanking(candidates []cognition.Result, ranking []string) []cognition.Result {
	byID := make(map[string]cognition.Result, len(candidates))
	original := make([]string, 0, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
		original = append(original, c.ID)
	}
	seen := make(map[string]bool, len(ranking))
	out := make([]cognition.Result, 0, len(candidates))
	for _, id := range ranking {
		if c, ok := byID[id]; ok && !seen[id] {
			out = append(out, c)
			seen[id] = true
		}
	}
	for _, id := range original {
		if !seen[id] {
			out = append(out, byID[id])
			seen[id] = true
		}
	}
	return out
}

// scoreFallbackRerank sorts candidates by their existing Score field
// (DESC). Stable — preserves input order for ties. Returns a copy.
func scoreFallbackRerank(candidates []cognition.Result) []cognition.Result {
	out := make([]cognition.Result, len(candidates))
	copy(out, candidates)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
