// Package ops — decide.next.
//
// The dynamic-DAG steering op. Each REPL turn starts with a tiny seed
// (sense.prompt → decide.next), and decide.next inspects the user's
// prompt + accumulated turn state to decide which op to spawn next.
// After certain actions (notably search) it spawns a follow-up
// decide.next so the next step can be re-evaluated in light of what
// just happened. When the answer is "done", spawn is empty and the
// executor terminates that branch.
//
// This is the seed-and-grow design from the dag package doc made
// concrete for the REPL: the same seed produces visibly different
// trees per prompt (conversational vs code vs search-augmented), and
// budget decay is handled entirely by the executor.
//
// V0 scope: four arms (code | search | converse | done), a single
// LLM classifier call with rule-based fallback, no per-step model
// routing. Per-step routing via model.route is a clean later add.
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

// NextAction is the arm decide.next chose for the next step.
type NextAction string

const (
	NextActionCode     NextAction = "code"
	NextActionSearch   NextAction = "search"
	NextActionConverse NextAction = "converse"
	NextActionDone     NextAction = "done"
)

// NextConfig wires NewNextHandler. Provider is optional — without
// it, the handler falls back to a rule-based classifier (the trivial
// "code if not greeting" heuristic).
type NextConfig struct {
	Provider llm.Provider
	// MaxLatencyMS caps the classifier call. Defaults to 1500 because
	// the goal is decide.next is cheap enough to run multiple times
	// per turn — a 5s classification call defeats the purpose.
	MaxLatencyMS int
}

// NextSpec returns the dag.NodeSpec for decide.next.
func NextSpec(cfg NextConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "next",
		Description: "pick the next op to spawn: code | search | converse | done",
		Inputs: []dag.ParamSpec{
			{Name: "prompt", Type: "string", Required: true},
			{Name: "already_searched", Type: "bool"},
			{Name: "history_summary", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "next_action", Type: "string"},
			{Name: "reasoning", Type: "string"},
		},
		Cost:    nextCostHint,
		Handler: NewNextHandler(cfg),
	}
}

// nextCostHint — sized for a small classifier call (~150 tokens out,
// ~500ms wall on Haiku-class, ~200ms on local-Ollama).
var nextCostHint = dag.Cost{LatencyMS: 1500, Tokens: 200}

const nextPrompt = `You are picking the next action for a coding assistant in the middle of one user-prompt turn.

The user said:
"""
{{PROMPT}}
"""

{{CONTEXT}}

Choose ONE of these next actions:
- code: the user is asking for code work — read/write/run something
- search: a relevant prior decision, file, or capture probably exists; search before acting (DO NOT pick this if already_searched is true)
- converse: the user wants prose — a question answered, an explanation, an analysis. No tool calls needed.
- done: the prior action(s) addressed the request; stop

Respond with ONLY a JSON object:
{"next_action": "code|search|converse|done", "reasoning": "<one short sentence>"}

No prose before or after the JSON. No markdown fences.`

type nextResponse struct {
	NextAction string `json:"next_action"`
	Reasoning  string `json:"reasoning"`
}

// NewNextHandler returns the dag.Handler for decide.next.
//
// The handler reads `prompt` (required), `already_searched` (bool —
// the follow-up decide.next after a search sets this so the LLM
// doesn't pick search again), and `history_summary` (optional short
// hint about prior turns).
//
// Spawn semantics (returned in NodeResult.Spawn):
//   - code     → spawn decide.coding_turn (forwards prompt + workdir)
//   - search   → spawn remember.vector_search → decide.next (with
//     already_searched=true so the re-decision can't loop)
//   - converse → spawn decide.coding_turn (the LLM responds in prose
//     per the REPL system prompt — no tool calls)
//   - done     → empty Spawn (executor terminates this branch)
//
// The handler does not register decide.coding_turn or
// remember.vector_search itself — the caller is expected to register
// them on the same dag.Registry before running the executor.
func NewNextHandler(cfg NextConfig) dag.Handler {
	maxLatency := cfg.MaxLatencyMS
	if maxLatency <= 0 {
		maxLatency = 1500
	}

	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		prompt := readString(in, "prompt")
		if prompt == "" {
			return dag.NodeResult{
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds())},
			}, fmt.Errorf("decide.next: 'prompt' (string) is required")
		}
		alreadySearched, _ := in["already_searched"].(bool)
		historySummary := readString(in, "history_summary")

		var action NextAction
		var reasoning string

		if cfg.Provider == nil || !budget.CanAfford(nextCostHint) {
			action = ruleBasedNextAction(prompt, alreadySearched)
			reasoning = "fallback: " + string(action) + " (no provider or budget exhausted)"
		} else {
			classifyCtx, cancel := context.WithTimeout(ctx, time.Duration(maxLatency)*time.Millisecond)
			defer cancel()
			action, reasoning = classifyNextAction(classifyCtx, cfg.Provider, prompt, alreadySearched, historySummary)
		}

		latency := int(time.Since(started).Milliseconds())
		spawn := buildNextSpawn(action, prompt)

		return dag.NodeResult{
			Out: map[string]any{
				"next_action": string(action),
				"reasoning":   reasoning,
			},
			Spawn:        spawn,
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: estimateTokens(reasoning)},
		}, nil
	}
}

// classifyNextAction asks the provider to pick the next action.
// Falls back to rule-based on any error so the chain never stalls on
// a flaky classifier.
func classifyNextAction(ctx context.Context, p llm.Provider, prompt string, alreadySearched bool, historySummary string) (NextAction, string) {
	contextBlock := buildNextContextBlock(alreadySearched, historySummary)
	systemPrompt := strings.ReplaceAll(strings.ReplaceAll(nextPrompt,
		"{{PROMPT}}", prompt),
		"{{CONTEXT}}", contextBlock)

	respText, err := p.GenerateWithSystem(ctx, "Pick the next action.", systemPrompt)
	if err != nil {
		fallback := ruleBasedNextAction(prompt, alreadySearched)
		return fallback, "fallback: " + string(fallback) + " (classifier error: " + err.Error() + ")"
	}

	var parsed nextResponse
	if perr := parseNextResponse(respText, &parsed); perr != nil {
		fallback := ruleBasedNextAction(prompt, alreadySearched)
		return fallback, "fallback: " + string(fallback) + " (parse error: " + perr.Error() + ")"
	}

	action := normalizeNextAction(parsed.NextAction, alreadySearched)
	return action, parsed.Reasoning
}

// buildNextContextBlock assembles the optional "Recent state" block
// the LLM sees. Kept compact — small models lose discipline when
// system prompts grow.
func buildNextContextBlock(alreadySearched bool, historySummary string) string {
	var b strings.Builder
	if alreadySearched {
		b.WriteString("already_searched: true (a prior step already ran remember.vector_search this turn)\n")
	}
	if historySummary != "" {
		b.WriteString("history: ")
		b.WriteString(historySummary)
		b.WriteString("\n")
	}
	return b.String()
}

// normalizeNextAction coerces the model's free-text answer into one
// of the four valid arms. Unknown values default to "code" (the
// dominant REPL case). Search is suppressed when already_searched.
func normalizeNextAction(raw string, alreadySearched bool) NextAction {
	a := NextAction(strings.ToLower(strings.TrimSpace(raw)))
	switch a {
	case NextActionCode, NextActionConverse, NextActionDone:
		return a
	case NextActionSearch:
		if alreadySearched {
			return NextActionCode
		}
		return NextActionSearch
	default:
		return NextActionCode
	}
}

// ruleBasedNextAction is the no-provider / over-budget fallback.
// Trivial but useful: greetings and very short prompts read as
// conversational; everything else defaults to code (the dominant
// REPL case). Search is never auto-picked without a classifier — it
// adds latency for no payoff when we can't tell whether it's worth it.
func ruleBasedNextAction(prompt string, alreadySearched bool) NextAction {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return NextActionDone
	}
	lower := strings.ToLower(trimmed)
	if isShortConversational(lower) {
		return NextActionConverse
	}
	_ = alreadySearched
	return NextActionCode
}

// isShortConversational covers the most common "REPL is a chat
// window" cases without an LLM. Conservative — false negatives
// just default to code, which is a reasonable miss.
func isShortConversational(lower string) bool {
	if len(lower) <= 3 {
		return true
	}
	greetings := []string{"hi", "hello", "hey", "thanks", "thank you", "?", "ok", "yes", "no", "what?", "huh"}
	for _, g := range greetings {
		if lower == g || strings.HasPrefix(lower, g+" ") || strings.HasPrefix(lower, g+",") {
			return true
		}
	}
	return false
}

// buildNextSpawn constructs the Spawn slice for the chosen action.
// IDs are auto-assigned by the executor. Attrs are kept minimal —
// downstream ops read what they need from their own registered Inputs.
func buildNextSpawn(action NextAction, prompt string) []dag.NodeSpec {
	switch action {
	case NextActionCode, NextActionConverse:
		return []dag.NodeSpec{{
			Function: dag.FuncDecide, Op: "coding_turn",
			Attrs: map[string]any{"prompt": prompt},
		}}
	case NextActionSearch:
		return []dag.NodeSpec{
			{
				Function: dag.FuncRemember, Op: "vector_search",
				Attrs: map[string]any{"prompt": prompt, "query": prompt, "limit": 5},
			},
			{
				Function: dag.FuncDecide, Op: "next",
				Attrs: map[string]any{"prompt": prompt, "already_searched": true},
			},
		}
	case NextActionDone:
		return nil
	default:
		return nil
	}
}

// parseNextResponse extracts the JSON object from the classifier's
// raw response. Tolerates leading/trailing whitespace and optional
// markdown fences.
func parseNextResponse(raw string, out *nextResponse) error {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		nl := strings.IndexByte(s, '\n')
		if nl > 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	return json.Unmarshal([]byte(s), out)
}
