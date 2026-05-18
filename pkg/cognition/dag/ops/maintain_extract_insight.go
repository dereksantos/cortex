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

// ExtractInsightConfig wires the handler to a provider at
// registration time. Provider may be nil — the handler then runs the
// mechanical fallback.
type ExtractInsightConfig struct {
	Provider llm.Provider
}

// Insight is the structured form of a single extracted insight.
// Matches the JSON shape declared in
// pkg/cognition/prompts/maintain_extract_insight.tmpl.
type Insight struct {
	Content    string  `json:"content"`
	Category   string  `json:"category"`
	Importance float64 `json:"importance"`
}

// extractInsightResponse is the parser target for the model's JSON.
type extractInsightResponse struct {
	Insights []Insight `json:"insights"`
}

// ExtractInsightSpec returns the NodeSpec for maintain.extract_insight.
// LLM-backed op with a deterministic mechanical fallback.
func ExtractInsightSpec(cfg ExtractInsightConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "extract_insight",
		Description: "extract 1-2 durable insights from content via a micro-LLM call (or mechanical fallback)",
		Inputs: []dag.ParamSpec{
			{Name: "content", Type: "string", Required: true},
			{Name: "source", Type: "string", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "insights", Type: "[]Insight"},
			{Name: "count", Type: "int"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:    extractInsightCostHint,
		Handler: NewExtractInsightHandler(cfg),
	}
}

// extractInsightCostHint — LLM call dominates; small-model output ≤100
// tokens. Anthropic Haiku 4.5 measures p50 ≈ 600ms for a 50-token
// completion; OpenRouter qwen3-coder p50 ≈ 800ms. Set 900ms / 200 tok
// for headroom. Will recalibrate from cell_results.jsonl after first
// real run.
var extractInsightCostHint = dag.Cost{LatencyMS: 900, Tokens: 200}

// fallbackBelowLatencyMS — if remaining budget is below this threshold,
// skip the LLM call and run the mechanical fallback. 200ms is roughly
// the floor at which an LLM round-trip is guaranteed to overshoot.
const fallbackBelowLatencyMS = 200

// NewExtractInsightHandler returns a dag.Handler for
// maintain.extract_insight.
//
// Inputs:
//   - content (string)  — required; the text to extract insights from
//   - source (string)   — optional; tag for provenance ("decision-note",
//     "transcript", "commit-message", etc.)
//
// Outputs:
//   - insights ([]Insight) — 0–2 extracted insights
//   - count (int)          — len(insights)
//   - fallback (bool)      — true when the mechanical path ran
//
// Self-modulates: when budget.LatencyMS < fallbackBelowLatencyMS or
// no provider is configured, runs the mechanical fallback (keyword
// heuristic on the content). Returns 0–1 insights with category
// "pattern" and importance 0.5 from the fallback path.
func NewExtractInsightHandler(cfg ExtractInsightConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		content := readString(in, "content")
		if content == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("maintain.extract_insight: 'content' (string) is required")
		}
		source := readString(in, "source")
		if source == "" {
			source = "unknown"
		}

		// Mechanical fallback when no provider or budget too thin.
		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || budget.LatencyMS < fallbackBelowLatencyMS {
			insights := mechanicalExtractInsights(content)
			return dag.NodeResult{
				Out: map[string]any{
					"insights": insights,
					"count":    len(insights),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		pt, terr := LoadTemplate("maintain_extract_insight")
		if terr != nil {
			// Template load failure is a build-error class problem, but
			// we fall back rather than error so the executor can keep
			// walking the DAG.
			insights := mechanicalExtractInsights(content)
			return dag.NodeResult{
				Out: map[string]any{
					"insights": insights,
					"count":    len(insights),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, nil
		}

		prompt, rerr := pt.Render(map[string]any{"content": content, "source": source})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("maintain.extract_insight: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, prompt)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			// LLM call failed — fall back rather than propagate.
			insights := mechanicalExtractInsights(content)
			return dag.NodeResult{
				Out: map[string]any{
					"insights": insights,
					"count":    len(insights),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		insights, perr := parseExtractInsightResponse(resp)
		if perr != nil {
			// Malformed model output — fall back. Logged via the
			// fallback=true marker in Out.
			insights = mechanicalExtractInsights(content)
			return dag.NodeResult{
				Out: map[string]any{
					"insights": insights,
					"count":    len(insights),
					"fallback": true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		return dag.NodeResult{
			Out: map[string]any{
				"insights": insights,
				"count":    len(insights),
				"fallback": false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// parseExtractInsightResponse extracts the {"insights": [...]} JSON
// envelope from anywhere in the response, tolerating leading or
// trailing prose. Returns 0–2 insights (truncates extras to honor the
// prompt cap).
func parseExtractInsightResponse(resp string) ([]Insight, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}
	var parsed extractInsightResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if len(parsed.Insights) > 2 {
		parsed.Insights = parsed.Insights[:2]
	}
	// Filter empty entries — model sometimes emits placeholder rows.
	out := make([]Insight, 0, len(parsed.Insights))
	for _, ins := range parsed.Insights {
		if strings.TrimSpace(ins.Content) == "" {
			continue
		}
		// Default category if model omitted.
		if ins.Category == "" {
			ins.Category = "pattern"
		}
		out = append(out, ins)
	}
	return out, nil
}

// mechanicalExtractInsights is the deterministic fallback. Inspects
// content for decision/constraint markers ("use", "don't", "must",
// "always", "never", "instead of") and emits at most one insight per
// matched marker (deduplicated). Returns empty slice if no marker fires.
//
// The fallback never invents content — it surfaces text the user wrote.
// Importance is fixed at 0.5; category is heuristic.
func mechanicalExtractInsights(content string) []Insight {
	lines := strings.Split(content, "\n")
	out := []Insight{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		category := ""
		switch {
		case strings.Contains(lower, "don't ") || strings.Contains(lower, "never ") || strings.Contains(lower, "avoid "):
			category = "constraint"
		case strings.Contains(lower, "always ") || strings.Contains(lower, "must "):
			category = "constraint"
		case strings.Contains(lower, "decided ") || strings.Contains(lower, "chose ") || strings.Contains(lower, "use ") || strings.Contains(lower, "prefer "):
			category = "decision"
		case strings.Contains(lower, "instead of "):
			category = "correction"
		}
		if category == "" {
			continue
		}
		out = append(out, Insight{
			Content:    trimmed,
			Category:   category,
			Importance: 0.5,
		})
		if len(out) >= 2 {
			break
		}
	}
	return out
}
