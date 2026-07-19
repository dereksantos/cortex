package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestCharsPerTokenInvariant is P2's cross-package regression guard: pkg/llm
// keeps its OWN copy of the chars-per-token heuristic (charsPerToken,
// pkg/llm/budget.go) because pkg/ must not import internal/ (CLAUDE.md's
// package-structure layering), but that copy's VALUE must still equal
// internal/cache.CharsPerToken — the one place cmd/cortex (which imports
// both) can assert the invariant named in pkg/llm/budget.go's comment. A PR
// that changes one without the other breaks this test.
func TestCharsPerTokenInvariant(t *testing.T) {
	const n = 400 // multiple of 4, large enough the per-message overhead is a clean separate term
	content := strings.Repeat("x", n)

	cacheTokens := cache.TokensOf(n)
	llmTokens := llm.EstimateChatTokens([]llm.ChatMessage{{Role: "user", Content: content}})
	// EstimateChatTokens adds a flat +4 per-message overhead on top of the
	// same content/charsPerToken division cache.TokensOf does — see
	// pkg/llm/budget.go's doc comment.
	const perMessageOverhead = 4
	if llmTokens-perMessageOverhead != cacheTokens {
		t.Errorf("llm.EstimateChatTokens(%d chars) - overhead = %d, want internal/cache.TokensOf(%d) = %d — "+
			"pkg/llm's charsPerToken has drifted from internal/cache.CharsPerToken", n, llmTokens-perMessageOverhead, n, cacheTokens)
	}
}
