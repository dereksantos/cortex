package tools

import (
	"context"
	"fmt"
	"strings"
)

// contextSummarize compresses a demoted turn into a compact digest.
// Uses the existing Summarizer interface (sequential chunk-and-fold)
// and preserves citations mechanically.
func contextSummarize(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
	// Parse arguments
	citation, err := tc.StringArg("citation")
	if err != nil {
		return "", fmt.Errorf("citation is required: %w", err)
	}

	goal, _ := tc.StringArg("goal") // optional
	if goal == "" {
		goal = "What are the key facts and decisions in this turn?"
	}

	budget, _ := tc.IntArg("budget")
	if budget <= 0 {
		budget = 512 // fallback default
	}

	// Recall the raw messages (deterministic, no LLM)
	rawDetail, err := deps.Recall(citation)
	if err != nil {
		return "", fmt.Errorf("recall failed: %w", err)
	}

	// Use SummarizeText for in-memory content
	digest, compressed, err := deps.SummarizeText(ctx, rawDetail, goal, budget)
	if err != nil {
		return "", fmt.Errorf("summarize failed: %w", err)
	}

	// MUST preserve citation (mechanical check)
	if !strings.Contains(digest, citation) {
		digest += fmt.Sprintf("\n\n[CITATION KEPT: %s]", citation)
	}

	// Format response
	return fmt.Sprintf(
		"Summarized %s into ~%d tokens (compressed: %v):\n\n%s",
		citation, budget, compressed, digest,
	), nil
}
