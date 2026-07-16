package main

import "github.com/dereksantos/cortex/pkg/llm"

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
