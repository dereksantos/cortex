// Package ops — sense.classify_intent.
//
// Small-LLM micro-decision that routes each turn before any heavy
// work. Classifies the user's prompt into one of six intents
// (greeting | recall | clarify | code | review | meta); the REPL
// uses the result to (a) pick a per-intent seed budget via
// dag.BudgetForIntent and (b) choose the seed shape — trivial
// intents bypass the full agent loop in favor of cheaper terminal
// nodes (e.g. act.passthrough for greetings).
//
// Fails safe: on any unavailable provider / budget / parse error the
// handler returns intent="code" with confidence=0 so downstream
// defaults to the existing full pipeline. Misroutes degrade to
// today's behavior, never block the turn.
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

// ClassifyIntentConfig wires the handler to a provider.
type ClassifyIntentConfig struct {
	Provider llm.Provider
}

// classifyIntentResponse is the parser target.
type classifyIntentResponse struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

// ClassifyIntentSpec returns the NodeSpec for sense.classify_intent.
//
// Requires declares the capability preference chain the executor's
// Router uses to pick the per-node provider. Intent classification is
// well within any tool-calling-capable chat model's range, so this
// node asks for CapToolCalling without a specialist preference — the
// picker's "generalist prefers larger" tiebreaker picks the most
// capable available model, matching today's session-default behavior.
func ClassifyIntentSpec(cfg ClassifyIntentConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "classify_intent",
		Description: "classify user prompt as greeting | recall | clarify | code | review | meta",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
		},
		Outputs: []dag.ParamSpec{
			{Name: "intent", Type: "string"},
			{Name: "confidence", Type: "float64"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      classifyIntentCostHint,
		Exposable: true,
		Requires:  []string{llm.CapToolCalling},
		Handler:   NewClassifyIntentHandler(cfg),
	}
}

// classifyIntentCostHint — sized for a single small-LLM classification.
// Lighter than decide.should_capture (16s/350tok) because the prompt
// is shorter and the output is fixed-shape ~30 tokens. Will tighten
// after the first calibration pass against a real provider.
var classifyIntentCostHint = dag.Cost{LatencyMS: 2000, Tokens: 200}

// validIntents is the allowed set the prompt asks the model to choose
// from. Anything else gets normalized to IntentCode (the safe-default
// behavior — route to the full pipeline).
var validIntents = map[string]bool{
	"greeting": true,
	"recall":   true,
	"clarify":  true,
	"code":     true,
	"review":   true,
	"meta":     true,
}

// IntentCode is the safe-default intent label returned on every
// failure path (no provider, parse error, unknown label). Routes
// downstream to the full coding pipeline unchanged from pre-intent
// behavior.
const IntentCode = "code"

// NewClassifyIntentHandler returns the dag.Handler for
// sense.classify_intent.
//
// Inputs:
//   - prompt (string) — required; the user's raw turn text.
//
// Outputs:
//   - intent (string)      — one of greeting | recall | clarify | code | review | meta
//   - confidence (float64) — 0.0-1.0, clamped
//   - why (string)         — short rationale (≤8 words from the model)
//   - fallback (bool)      — true when classification couldn't be performed
//
// Fallback path always returns intent=IntentCode with confidence=0 so
// callers thresholding on confidence will treat it as "not confidently
// classified" and run the full pipeline.
func NewClassifyIntentHandler(cfg ClassifyIntentConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.classify_intent: 'prompt' (string) is required")
		}

		// Prefer the executor-resolved provider (Router populated
		// Budget.Provider from this node's Requires chain — see
		// docs/per-node-routing-plan.md slice 3). Fall back to
		// cfg.Provider when no Router is wired.
		provider := budget.Provider
		if provider == nil {
			provider = cfg.Provider
		}
		if provider == nil || !provider.IsAvailable() || !budget.CanAfford(classifyIntentCostHint) {
			return classifyIntentFallback(started, "provider unavailable or budget exhausted"), nil
		}

		pt, terr := LoadTemplate("sense_classify_intent")
		if terr != nil {
			return classifyIntentFallback(started, fmt.Sprintf("template load: %v", terr)), nil
		}
		rendered, rerr := pt.Render(map[string]any{"prompt": prompt})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("sense.classify_intent: render: %w", rerr)
		}

		resp, stats, gerr := provider.GenerateWithStats(ctx, rendered)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			return classifyIntentFallback(started, fmt.Sprintf("llm error: %v", gerr)), nil
		}

		parsed, perr := parseClassifyIntentResponse(resp)
		if perr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"intent":     IntentCode,
					"confidence": 0.0,
					"why":        fmt.Sprintf("parse error: %v", perr),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		intent := strings.ToLower(strings.TrimSpace(parsed.Intent))
		if !validIntents[intent] {
			intent = IntentCode
			parsed.Confidence = 0.0
		}
		if parsed.Confidence < 0 {
			parsed.Confidence = 0
		}
		if parsed.Confidence > 1 {
			parsed.Confidence = 1
		}

		return dag.NodeResult{
			Out: map[string]any{
				"intent":     intent,
				"confidence": parsed.Confidence,
				"why":        parsed.Why,
				"fallback":   false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseClassifyIntentResponse(resp string) (classifyIntentResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return classifyIntentResponse{}, fmt.Errorf("no JSON object")
	}
	var parsed classifyIntentResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return classifyIntentResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

func classifyIntentFallback(started time.Time, why string) dag.NodeResult {
	return dag.NodeResult{
		Out: map[string]any{
			"intent":     IntentCode,
			"confidence": 0.0,
			"why":        why,
			"fallback":   true,
		},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}
