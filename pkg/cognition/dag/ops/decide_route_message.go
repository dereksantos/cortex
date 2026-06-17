// Package ops — decide.route_message.
//
// Small-LLM micro-decision that routes an incoming message against the
// session's current task: does it CONTINUE the active change, or START
// a new, distinct one? A long-lived harness (e.g. the Discord adapter)
// runs this at message ingress to decide whether to keep accumulating
// the current session or reset to a fresh one — bounding context and
// keeping one change per PR without a human flipping a switch.
//
// Fails safe: on any unavailable provider / budget / parse error the
// handler returns decision="continue" with confidence=0, so an
// uncertain call never resets a session out from under live work.
// Misroutes degrade to "keep going", never to a surprise reset.
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

// RouteMessageConfig wires the handler to a provider.
type RouteMessageConfig struct {
	Provider llm.Provider
}

// routeMessageResponse is the parser target.
type routeMessageResponse struct {
	Decision   string  `json:"decision"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

// Decision labels. DecisionContinue is the safe default returned on
// every failure path and for any unrecognized label — the caller keeps
// the current session.
const (
	DecisionContinue  = "continue"
	DecisionNewChange = "new_change"
)

// validDecisions is the allowed set; anything else normalizes to
// DecisionContinue (the fail-safe).
var validDecisions = map[string]bool{
	DecisionContinue:  true,
	DecisionNewChange: true,
}

// routeMessageCostHint — a single small-LLM classification: short
// prompt, fixed-shape ~40-token output. Same order as
// sense.classify_intent.
var routeMessageCostHint = dag.Cost{LatencyMS: 2000, Tokens: 200}

// RouteMessageSpec returns the NodeSpec for decide.route_message.
//
// Like sense.classify_intent it emits a JSON object matching a schema
// ({decision, name, confidence, why}) — structured output, not a
// function call — so it prefers a generalist tool-caller over a
// tool-call specialist via the Requires chain.
func RouteMessageSpec(cfg RouteMessageConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "route_message",
		Description: "route an incoming message: continue the current change or start a new one",
		Inputs: []dag.ParamSpec{
			{Name: "message", Type: "string", Required: true},
			{Name: "goal", Type: "string", Required: false},
		},
		Outputs: []dag.ParamSpec{
			{Name: "decision", Type: "string"},
			{Name: "name", Type: "string"},
			{Name: "confidence", Type: "float64"},
			{Name: "why", Type: "string"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      routeMessageCostHint,
		Exposable: true,
		Requires:  []string{llm.CapToolCalling},
		Handler:   NewRouteMessageHandler(cfg),
	}
}

// NewRouteMessageHandler returns the dag.Handler for
// decide.route_message.
//
// Inputs:
//   - message (string) — required; the incoming user message.
//   - goal (string)    — optional; a one-line description of the active
//     task, so the model can judge "same task" vs "different task".
//     Empty goal => no task to compare against => always continue.
//
// Outputs:
//   - decision (string)    — "continue" | "new_change"
//   - name (string)        — short slug to name a new change (when new_change)
//   - confidence (float64) — 0.0-1.0, clamped
//   - why (string)         — short rationale
//   - fallback (bool)      — true when routing couldn't be performed
//
// The fail-safe path always returns decision="continue" with
// confidence=0, so a caller thresholding on confidence treats it as
// "not confidently a new change" and keeps the current session.
func NewRouteMessageHandler(cfg RouteMessageConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		message := readString(in, "message")
		if message == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.route_message: 'message' (string) is required")
		}
		goal := readString(in, "goal")
		// No established task yet: nothing to diverge from, so the only
		// sensible decision is to continue (and let this message become
		// the goal). Saves a model call on the first turn of a session.
		if strings.TrimSpace(goal) == "" {
			return routeMessageFallback(started, "no active goal"), nil
		}

		provider := budget.Provider
		if provider == nil {
			provider = cfg.Provider
		}
		if provider == nil || !provider.IsAvailable() || !budget.CanAfford(routeMessageCostHint) {
			return routeMessageFallback(started, "provider unavailable or budget exhausted"), nil
		}

		pt, terr := LoadTemplate("decide_route_message")
		if terr != nil {
			return routeMessageFallback(started, fmt.Sprintf("template load: %v", terr)), nil
		}
		rendered, rerr := pt.Render(map[string]any{"message": message, "goal": goal})
		if rerr != nil {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.route_message: render: %w", rerr)
		}

		resp, stats, gerr := provider.GenerateWithStats(ctx, rendered)
		latency := int(time.Since(started).Milliseconds())
		if gerr != nil {
			return routeMessageFallback(started, fmt.Sprintf("llm error: %v", gerr)), nil
		}

		parsed, perr := parseRouteMessageResponse(resp)
		if perr != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"decision":   DecisionContinue,
					"name":       "",
					"confidence": 0.0,
					"why":        fmt.Sprintf("parse error: %v", perr),
					"fallback":   true,
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
			}, nil
		}

		decision := strings.ToLower(strings.TrimSpace(parsed.Decision))
		if !validDecisions[decision] {
			decision = DecisionContinue
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
				"decision":   decision,
				"name":       strings.TrimSpace(parsed.Name),
				"confidence": parsed.Confidence,
				"why":        parsed.Why,
				"fallback":   false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens()},
		}, nil
	}
}

func parseRouteMessageResponse(resp string) (routeMessageResponse, error) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return routeMessageResponse{}, fmt.Errorf("no JSON object")
	}
	var parsed routeMessageResponse
	if err := json.Unmarshal([]byte(resp[start:end+1]), &parsed); err != nil {
		return routeMessageResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return parsed, nil
}

func routeMessageFallback(started time.Time, why string) dag.NodeResult {
	return dag.NodeResult{
		Out: map[string]any{
			"decision":   DecisionContinue,
			"name":       "",
			"confidence": 0.0,
			"why":        why,
			"fallback":   true,
		},
		CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
	}
}
