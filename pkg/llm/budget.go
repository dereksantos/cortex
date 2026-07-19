package llm

// EstimateChatTokens returns an approximate token count for a slice
// of ChatMessage values, using the 4-char-per-token heuristic that
// the rest of Cortex agrees on (mirrors internal/harness/dagnode's
// approxTokens). Used by the harness loop to budget a prompt
// against a model's context window without depending on a tokenizer.
//
// The estimate is intentionally conservative — it counts content +
// tool-call names + tool-call arguments, with a small per-message
// overhead for role + delimiter tokens. Off by ~10% in either
// direction depending on the actual tokenizer; that's fine because
// the caller uses a safety margin (~85% of n_ctx) for the trigger
// threshold.
func EstimateChatTokens(msgs []ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
		for _, c := range m.ToolCalls {
			total += estimateTokens(c.Function.Name)
			total += estimateTokens(c.Function.Arguments)
		}
		// Role + role/name boundary tokens. Empirically 3–5 tokens
		// per message on cl100k-style tokenizers; 4 is a fine middle.
		total += 4
	}
	return total
}

// charsPerToken is pkg/llm's OWN copy of the rough bytes-per-token heuristic
// — internal/cache.CharsPerToken (internal/cache/workingset.go) is the
// authoritative constant everywhere else in the repo (P2 dedup:
// docs/completion-roadmap.md's audit found this value triplicated at
// internal/cache, cmd/cortex/summarize.go, and here). cmd/cortex's copy was
// collapsed onto internal/cache.CharsPerToken directly; this one can't be:
// pkg/ is the project's public-API layer and internal/ is private
// implementation (CLAUDE.md's "Package structure"), so pkg/llm importing
// internal/cache — even though Go's internal-package rule permits it,
// since both share the module root — would cross that intentional layering
// boundary. INVARIANT: keep this equal to internal/cache.CharsPerToken; a
// PR changing one without the other is a bug.
const charsPerToken = 4

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / charsPerToken
	if n == 0 {
		return 1
	}
	return n
}

// TrimChatHistory drops messages from a [keepHead, len-keepTail)
// window — oldest first — until the total estimated token count
// fits under maxTokens. Returns the trimmed slice and how many
// messages were removed.
//
// keepHead protects the system prompt (and anything else the
// caller wants preserved at the start). keepTail protects the
// current user message + any in-flight assistant/tool sequence at
// the end (dropping a tool_result while keeping its tool_call
// produces an invalid request).
//
// Returns the input slice unchanged when it already fits, or when
// keepHead+keepTail covers everything.
func TrimChatHistory(msgs []ChatMessage, maxTokens, keepHead, keepTail int) ([]ChatMessage, int) {
	if maxTokens <= 0 || len(msgs) == 0 {
		return msgs, 0
	}
	if EstimateChatTokens(msgs) <= maxTokens {
		return msgs, 0
	}
	if keepHead < 0 {
		keepHead = 0
	}
	if keepTail < 0 {
		keepTail = 0
	}
	if keepHead+keepTail >= len(msgs) {
		return msgs, 0
	}

	dropped := 0
	for keepHead+dropped < len(msgs)-keepTail {
		// Build the trimmed view without copying every iteration —
		// for the small history sizes this loop touches (single
		// digits typically), allocation is fine.
		trimmed := make([]ChatMessage, 0, len(msgs)-1)
		trimmed = append(trimmed, msgs[:keepHead]...)
		trimmed = append(trimmed, msgs[keepHead+dropped+1:]...)
		dropped++
		if EstimateChatTokens(trimmed) <= maxTokens {
			return trimmed, dropped
		}
	}

	// Couldn't fit even after dropping everything in the trimmable
	// range. Return the maximally-trimmed slice; the caller can
	// surface the failure with a more useful error than "couldn't
	// trim."
	out := make([]ChatMessage, 0, keepHead+keepTail)
	out = append(out, msgs[:keepHead]...)
	out = append(out, msgs[len(msgs)-keepTail:]...)
	return out, len(msgs) - len(out)
}
