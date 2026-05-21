// Package ops — decide.clarify.
//
// Small-LLM terminal node for ambiguous prompts. sense.classify_intent
// flags the turn as `clarify` when the prompt is too vague to act on
// without a follow-up question; this op composes ONE focused question
// and ends the turn. No tools, no verifier — the next user turn is
// expected to be the answer.
//
// Fails safe: provider unavailable / parse error → return a mechanical
// fallback question ("Can you say more about what you want to do, and
// in which file?") rather than blocking the turn. The user can always
// re-phrase, but they should never be stuck.
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

// ClarifyConfig wires the handler to a provider and an optional
// response sink (the REPL uses OnResponse to land the question into
// LoopResult.Final so the standard render path is unchanged).
type ClarifyConfig struct {
	Provider   llm.Provider
	OnResponse func(reply string)
}

// clarifyResponse is the parser target.
type clarifyResponse struct {
	Question string `json:"question"`
	Why      string `json:"why"`
}

// ClarifySpec returns the NodeSpec for decide.clarify. Marked
// Exposable so decide.next can also reach for it when its plan
// notices unrecoverable ambiguity mid-chain.
func ClarifySpec(cfg ClarifyConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "clarify",
		Description: "ask one focused clarifying question for an ambiguous prompt; ends the turn",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "question", Type: "string"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      clarifyCostHint,
		Exposable: true,
		Handler:   NewClarifyHandler(cfg),
	}
}

// clarifyCostHint — single short LLM call, output is one ≤25-word
// question. Sized to fit under BudgetForIntent("clarify") (3000ms
// / 500 tok) with room for the classifier that ran first.
var clarifyCostHint = dag.Cost{LatencyMS: 2500, Tokens: 250}

// mechanicalClarifyQuestion is the fallback reply when no provider is
// available or the LLM call fails. Generic but actionable — names the
// two most-common missing dimensions (intent + target file).
const mechanicalClarifyQuestion = "Can you say more about what you want to do, and which file or area of the project it should touch?"

// NewClarifyHandler returns the dag.Handler for decide.clarify.
//
// Inputs:
//   - prompt (string) — required; the original user turn.
//
// Outputs:
//   - question (string) — the clarifying question shown to the user.
//   - why (string)      — short rationale for what was unclear.
//   - fallback (bool)   — true when the mechanical fallback fired.
//
// The question is also pushed through cfg.OnResponse so the REPL can
// capture it into LoopResult.Final without inspecting Out.
func NewClarifyHandler(cfg ClarifyConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.clarify: 'prompt' (string) is required")
		}

		if cfg.Provider == nil || !cfg.Provider.IsAvailable() || !budget.CanAfford(clarifyCostHint) {
			return clarifyMechanicalFallback(cfg, started, "provider unavailable or budget exhausted"), nil
		}

		pt, terr := LoadTemplate("decide_clarify")
		if terr != nil {
			return clarifyMechanicalFallback(cfg, started, fmt.Sprintf("template load: %v", terr)), nil
		}
		rendered, rerr := pt.Render(map[string]any{"prompt": prompt})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.clarify: render: %w", rerr)
		}

		resp, stats, gerr := cfg.Provider.GenerateWithStats(ctx, rendered)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			return clarifyMechanicalFallback(cfg, started, fmt.Sprintf("llm error: %v", gerr)), nil
		}

		parsed, perr := parseClarifyResponse(resp)
		if perr != nil || strings.TrimSpace(parsed.Question) == "" {
			out := clarifyMechanicalFallback(cfg, started, fmt.Sprintf("parse error or empty question: %v", perr))
			out.CostConsumed.Tokens = stats.TotalTokens()
			return out, nil
		}

		question := strings.TrimSpace(parsed.Question)
		if cfg.OnResponse != nil {
			cfg.OnResponse(question)
		}
		return dag.NodeResult{
			Out: map[string]any{
				"question": question,
				"why":      parsed.Why,
				"fallback": false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseClarifyResponse(resp string) (clarifyResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return clarifyResponse{}, fmt.Errorf("no JSON object")
	}
	var parsed clarifyResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return clarifyResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

// clarifyMechanicalFallback wraps the generic question, forwards it to
// OnResponse, and packages a NodeResult with fallback=true so callers
// (and traces) can tell apart fallback turns from LLM-driven ones.
func clarifyMechanicalFallback(cfg ClarifyConfig, started time.Time, why string) dag.NodeResult {
	if cfg.OnResponse != nil {
		cfg.OnResponse(mechanicalClarifyQuestion)
	}
	return dag.NodeResult{
		Out: map[string]any{
			"question": mechanicalClarifyQuestion,
			"why":      why,
			"fallback": true,
		},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}
