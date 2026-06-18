// Package ops — sense.estimate_scope.
//
// Reads the user's prompt + the classified intent + an optional project-
// size signal and emits a free-form "scope" description plus integer
// budget axes (tokens / latency_ms / depth). The REPL uses the budget
// numbers to seed the executor instead of the table-driven BudgetForIntent
// fallback — so a "does the README match the implementation?" prompt
// against a 600-file project gets a much larger envelope than a
// "what does function X return?" prompt against the same project.
//
// Scope is intentionally free-form (no enum) — it's an emergent
// description the reasoner produces from the prompt + signal, not a
// taxonomy that ages with the codebase. See memory:
// project-multi-hop-via-spawn for why scope-from-table failed audit-class
// prompts.
//
// Fails safe: on any unavailable provider / parse error / out-of-range
// number the handler returns zero estimates with fallback=true; the REPL
// then falls back to BudgetForIntent as the floor.
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

// EstimateScopeConfig wires the handler to a provider.
type EstimateScopeConfig struct {
	Provider llm.Provider
}

// estimateScopeResponse is the parser target. Fields are loose-typed
// (float64 for ints so JSON-emitted "5000" or "5000.0" both parse)
// and clamped post-parse by the handler.
type estimateScopeResponse struct {
	Scope           string  `json:"scope"`
	BudgetTokens    float64 `json:"budget_tokens"`
	BudgetLatencyMS float64 `json:"budget_latency_ms"`
	BudgetDepth     float64 `json:"budget_depth"`
	Reasoning       string  `json:"reasoning"`
}

// EstimateScopeSpec returns the NodeSpec for sense.estimate_scope.
//
// Requires CapReasoning then CapToolCalling — this is a JSON-output
// reasoning task, not a function-call. Routing to a tool-call specialist
// (xLAM et al.) fails the same way it does for sense.classify_intent.
func EstimateScopeSpec(cfg EstimateScopeConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "estimate_scope",
		Description: "estimate how much work this turn implies; emit free-form scope + concrete budget axes (tokens / latency_ms / depth) to seed the executor",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
			{Name: "intent", Type: "string"},
			{Name: "project_signal", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "scope", Type: "string"},
			{Name: "budget_tokens", Type: "int"},
			{Name: "budget_latency_ms", Type: "int"},
			{Name: "budget_depth", Type: "int"},
			{Name: "reasoning", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      estimateScopeCostHint,
		Exposable: true,
		Requires:  []string{llm.CapReasoning, llm.CapToolCalling},
		Handler:   NewEstimateScopeHandler(cfg),
	}
}

// estimateScopeCostHint — one small-LLM reasoning call. Slightly larger
// than classify_intent because the model has to reason about magnitude
// rather than picking from a taxonomy.
var estimateScopeCostHint = dag.Cost{LatencyMS: 4000, Tokens: 400}

// Bounds applied after parse so a confused / overflowing emission can't
// destabilize the executor. The lower bounds match the floors documented
// in the template; the upper bounds catch obvious nonsense (a model
// asking for 10M tokens or a 10000-depth chain).
const (
	minBudgetTokens    = 3000
	maxBudgetTokens    = 200000
	minBudgetLatencyMS = 30000
	maxBudgetLatencyMS = 1800000 // 30 min wall clock — generous ceiling
	minBudgetDepth     = 5
	maxBudgetDepth     = 50
)

// NewEstimateScopeHandler returns the dag.Handler for sense.estimate_scope.
//
// Inputs:
//   - prompt (string)         — required; the user's raw turn text.
//   - intent (string)         — optional; from classify_intent. Defaults to "" (unknown).
//   - project_signal (string) — optional; free-form one-line summary of project size.
//
// Outputs (always non-empty, even on fallback so downstream code can
// read defensively):
//   - scope             — free-form description; "unknown (fallback)" on failure
//   - budget_tokens     — clamped int
//   - budget_latency_ms — clamped int
//   - budget_depth      — clamped int
//   - reasoning         — short explanation (or fallback reason)
//   - fallback          — true when estimation failed; the REPL should
//     then ignore the numbers and use BudgetForIntent
//     as the floor
func NewEstimateScopeHandler(cfg EstimateScopeConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.estimate_scope: 'prompt' (string) is required")
		}
		intent := readString(in, "intent")
		if intent == "" {
			intent = "unknown"
		}
		signal := readString(in, "project_signal")
		if signal == "" {
			signal = "unknown — treat as medium"
		}

		// Prefer the executor-resolved provider (Router populated
		// Budget.Provider from this node's Requires chain), fall back
		// to cfg.Provider when no Router is wired.
		provider := budget.Provider
		if provider == nil {
			provider = cfg.Provider
		}
		if provider == nil || !provider.IsAvailable() || !budget.CanAfford(estimateScopeCostHint) {
			return estimateScopeFallback(started, "provider unavailable or budget exhausted"), nil
		}

		pt, terr := LoadTemplate("sense_estimate_scope")
		if terr != nil {
			return estimateScopeFallback(started, fmt.Sprintf("template load: %v", terr)), nil
		}
		rendered, rerr := pt.Render(map[string]any{
			"prompt":         prompt,
			"intent":         intent,
			"project_signal": signal,
		})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.estimate_scope: render: %w", rerr)
		}

		resp, stats, gerr := provider.GenerateWithStats(ctx, rendered)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			return estimateScopeFallback(started, fmt.Sprintf("llm error: %v", gerr)), nil
		}

		parsed, perr := parseEstimateScopeResponse(resp)
		if perr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"scope":             "unknown (parse error)",
					"budget_tokens":     0,
					"budget_latency_ms": 0,
					"budget_depth":      0,
					"reasoning":         fmt.Sprintf("parse error: %v", perr),
					"fallback":          true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		tokens := clampInt(int(parsed.BudgetTokens), minBudgetTokens, maxBudgetTokens)
		latencyMS := clampInt(int(parsed.BudgetLatencyMS), minBudgetLatencyMS, maxBudgetLatencyMS)
		depth := clampInt(int(parsed.BudgetDepth), minBudgetDepth, maxBudgetDepth)

		scope := strings.TrimSpace(parsed.Scope)
		if scope == "" {
			scope = "unspecified"
		}
		reasoning := strings.TrimSpace(parsed.Reasoning)

		return dag.NodeResult{
			Out: map[string]any{
				"scope":             scope,
				"budget_tokens":     tokens,
				"budget_latency_ms": latencyMS,
				"budget_depth":      depth,
				"reasoning":         reasoning,
				"fallback":          false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

// clampInt bounds v to [lo, hi]. Used to keep a confused emission from
// destabilizing the executor (e.g. a model asking for 10M tokens).
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseEstimateScopeResponse(resp string) (estimateScopeResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return estimateScopeResponse{}, fmt.Errorf("no JSON object")
	}
	var parsed estimateScopeResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return estimateScopeResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

func estimateScopeFallback(started time.Time, why string) dag.NodeResult {
	return dag.NodeResult{
		Out: map[string]any{
			"scope":             "unknown (fallback)",
			"budget_tokens":     0,
			"budget_latency_ms": 0,
			"budget_depth":      0,
			"reasoning":         why,
			"fallback":          true,
		},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}
