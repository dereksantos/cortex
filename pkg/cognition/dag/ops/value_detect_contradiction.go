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

// DetectContradictionConfig wires the handler to a provider.
type DetectContradictionConfig struct {
	Provider llm.Provider
}

// detectContradictionResponse is the parser target.
type detectContradictionResponse struct {
	Conflicts     bool     `json:"conflicts"`
	ConflictsWith []string `json:"conflicts_with"`
	Why           string   `json:"why"`
}

// DetectContradictionSpec returns the NodeSpec for
// value.detect_contradiction.
func DetectContradictionSpec(cfg DetectContradictionConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncValue,
		Op:          "detect_contradiction",
		Description: "detect whether the candidate conflicts with any prior (Y/N + which ones)",
		Inputs: []dag.ParamSpec{
			{Name: "candidate", Type: "string", Required: true},
			{Name: "priors", Type: "[]cognition.Result", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "conflicts", Type: "bool"},
			{Name: "conflicts_with", Type: "[]string"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    detectContradictionCostHint,
		Handler: NewDetectContradictionHandler(cfg),
	}
}

// detectContradictionCostHint — ~50-80 token output; 700ms p50 on
// Haiku 4.5 with ~5 priors. Set 850ms / 180 tok for headroom.
var detectContradictionCostHint = dag.Cost{LatencyMS: 850, Tokens: 180}

// NewDetectContradictionHandler returns a dag.Handler for
// value.detect_contradiction.
//
// Inputs:
//   - candidate (string)                — required
//   - priors ([]cognition.Result | []any) — required; may be empty
//
// Outputs:
//   - conflicts (bool)
//   - conflicts_with ([]string)
//   - why (string)
//   - fallback (bool)
//
// Fallback: opposing-keyword pair detection — if candidate contains
// "use X" / "prefer X" and a prior contains "avoid X" / "don't use X"
// for the same noun, flag conflict. Coarse but deterministic.
func NewDetectContradictionHandler(cfg DetectContradictionConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		candidate := readString(in, "candidate")
		if candidate == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.detect_contradiction: 'candidate' (string) is required")
		}
		priors, perr := readResultSlice(in, "priors")
		if perr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.detect_contradiction: %w", perr)
		}
		if len(priors) == 0 {
			return dag.NodeResult{
				Out: map[string]any{
					"conflicts":      false,
					"conflicts_with": []string{},
					"why":            "no priors to compare",
					"fallback":       false,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			conflicts, ids := opposingKeywordContradiction(candidate, priors)
			return dag.NodeResult{
				Out: map[string]any{
					"conflicts":      conflicts,
					"conflicts_with": ids,
					"why":            "keyword-pair fallback",
					"fallback":       true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("value_detect_contradiction")
		if terr != nil {
			conflicts, ids := opposingKeywordContradiction(candidate, priors)
			return dag.NodeResult{
				Out: map[string]any{
					"conflicts":      conflicts,
					"conflicts_with": ids,
					"why":            fmt.Sprintf("template load failed: %v", terr),
					"fallback":       true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{
			"candidate": candidate,
			"priors":    formatCandidatesForPrompt(priors),
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("value.detect_contradiction: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			conflicts, ids := opposingKeywordContradiction(candidate, priors)
			return dag.NodeResult{
				Out: map[string]any{
					"conflicts":      conflicts,
					"conflicts_with": ids,
					"why":            fmt.Sprintf("llm error: %v", gerr),
					"fallback":       true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		parsed, pErr := parseDetectContradictionResponse(resp)
		if pErr != nil {
			conflicts, ids := opposingKeywordContradiction(candidate, priors)
			return dag.NodeResult{
				Out: map[string]any{
					"conflicts":      conflicts,
					"conflicts_with": ids,
					"why":            fmt.Sprintf("parse error: %v", pErr),
					"fallback":       true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		// Sanitize: model sometimes claims conflicts but emits empty list,
		// or vice versa. Treat the list as authoritative.
		parsed.Conflicts = len(parsed.ConflictsWith) > 0

		return dag.NodeResult{
			Out: map[string]any{
				"conflicts":      parsed.Conflicts,
				"conflicts_with": parsed.ConflictsWith,
				"why":            parsed.Why,
				"fallback":       false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseDetectContradictionResponse(resp string) (detectContradictionResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return detectContradictionResponse{}, fmt.Errorf("no JSON object found")
	}
	var parsed detectContradictionResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return detectContradictionResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	if parsed.ConflictsWith == nil {
		parsed.ConflictsWith = []string{}
	}
	return parsed, nil
}

// opposingKeywordContradiction is the mechanical fallback. Scans
// candidate for "use X" / "prefer X" markers and priors for "avoid X"
// / "don't use X" / "never X" markers on overlapping nouns (where
// "noun" = the next word after the marker). Coarse — false-negative
// rate is high — but never invents conflicts. Returns the prior IDs
// that triggered.
func opposingKeywordContradiction(candidate string, priors []cognition.Result) (bool, []string) {
	candUseTerms := extractMarkerTerms(strings.ToLower(candidate), []string{"use ", "prefer ", "choose "})
	candAvoidTerms := extractMarkerTerms(strings.ToLower(candidate), []string{"avoid ", "don't use ", "never use ", "never "})

	hits := []string{}
	for _, p := range priors {
		pl := strings.ToLower(p.Content)
		pUse := extractMarkerTerms(pl, []string{"use ", "prefer ", "choose "})
		pAvoid := extractMarkerTerms(pl, []string{"avoid ", "don't use ", "never use ", "never "})

		// candidate uses X, prior avoids X → conflict
		if intersect(candUseTerms, pAvoid) {
			hits = append(hits, p.ID)
			continue
		}
		// candidate avoids X, prior uses X → conflict
		if intersect(candAvoidTerms, pUse) {
			hits = append(hits, p.ID)
		}
	}
	return len(hits) > 0, hits
}

// extractMarkerTerms returns the lowercase word following each marker.
// Words shorter than 3 chars are skipped (too noisy).
func extractMarkerTerms(text string, markers []string) []string {
	out := []string{}
	for _, m := range markers {
		idx := 0
		for {
			rel := strings.Index(text[idx:], m)
			if rel < 0 {
				break
			}
			start := idx + rel + len(m)
			end := start
			for end < len(text) && isWordChar(text[end]) {
				end++
			}
			if end-start >= 3 {
				out = append(out, text[start:end])
			}
			idx = end
			if idx >= len(text) {
				break
			}
		}
	}
	return out
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '/'
}

func intersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if set[s] {
			return true
		}
	}
	return false
}
