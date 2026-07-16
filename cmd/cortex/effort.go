package main

import (
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// effort.go wires docs/thinking-models.md's Effort vocabulary (pkg/llm/effort.go)
// into the composition root: which dialect a session speaks, and how a
// resolved Effort lands on an *AgentRequest's wire fields.

// dialectFor picks the provider dialect a session speaks: OpenRouter's
// request-body `reasoning` object, or everyone else's chat_template_kwargs
// (docs/thinking-models.md "Open decisions" — only these two are reachable
// from cmd/cortex today).
func dialectFor(openRouter bool) llm.Dialect {
	if openRouter {
		return llm.DialectOpenRouter
	}
	return llm.DialectTemplateKwargs
}

// applyEffort resolves effort into req's wire fields for dialect, and
// records both the dialect and the resolved Effort back onto req (json:"-",
// wire-invisible) so a later engine mutation (P4's salvage-effort-off, P5's
// stuck-escalation) can recompute the SAME dialect rather than guessing it
// from which wire field happens to be non-nil — an OpenRouter request with
// effort "on" leaves Reasoning nil too (see llm.Translate), so "is Reasoning
// set" is not a reliable dialect signal.
func applyEffort(req *AgentRequest, dialect llm.Dialect, effort llm.Effort) {
	req.Dialect = dialect
	req.Effort = effort
	req.ChatTemplateKwargs, req.Reasoning = llm.Translate(dialect, effort)
}

// deliberationClampReasoningFraction is the ~80% threshold
// docs/thinking-models.md §4 sets for "the reasoning trace consumed most of
// the completion."
const deliberationClampReasoningFraction = 0.8

// deliberationClamped reports whether res looks like a reasoning model that
// burned its whole completion budget deliberating and never got to (or
// barely got to) the visible answer — the max-tokens-clamp deliberation
// signature docs/thinking-models.md §4 describes: the completion hit the
// length ceiling AND either the server-reported reasoning/completion split
// shows reasoning dominated, or — when the server never reports that split —
// the visible content is empty while a reasoning trace is present.
func deliberationClamped(res *AgentResponse) bool {
	if res == nil || len(res.Choices) == 0 {
		return false
	}
	c := res.Choices[0]
	if c.FinishReason != "length" {
		return false
	}
	if out := res.Usage.CompletionTokens; out > 0 {
		if rt := res.Usage.ReasoningTokens(); rt > 0 {
			return float64(rt) >= deliberationClampReasoningFraction*float64(out)
		}
	}
	return strings.TrimSpace(c.Message.Content) == "" && strings.TrimSpace(c.Reasoning) != ""
}

// disableEffortForSend mutates req's effort wire fields to OFF for exactly
// the next send, returning a restore func — the same one-shot pattern
// stuckJitterTemp already uses for temperature. Used by the salvage re-asks
// (docs/thinking-models.md §4): a re-ask that already showed the model
// burning its whole budget on deliberation must not repeat that.
func disableEffortForSend(req *AgentRequest) func() {
	prevKwargs, prevReasoning, prevEffort := req.ChatTemplateKwargs, req.Reasoning, req.Effort
	applyEffort(req, req.Dialect, llm.Effort{Level: llm.EffortOff})
	return func() {
		req.ChatTemplateKwargs, req.Reasoning, req.Effort = prevKwargs, prevReasoning, prevEffort
	}
}
