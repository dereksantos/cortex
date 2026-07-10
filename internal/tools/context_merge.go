package tools

import (
	"fmt"
)

// contextMerge folds a contiguous run of outline entries into a single entry.
// Deterministic (no LLM). The merged entry carries one spanning citation that
// still resolves every original message via recall, so the fold is lossless.
func contextMerge(tc ToolCall, deps ToolDeps) (string, error) {
	startCitation, err := tc.StringArg("range_start")
	if err != nil {
		return "", fmt.Errorf("range_start is required: %w", err)
	}
	endCitation, err := tc.StringArg("range_end")
	if err != nil {
		return "", fmt.Errorf("range_end is required: %w", err)
	}

	merged, err := deps.MergeOutlineEntries(startCitation, endCitation)
	if err != nil {
		return "", fmt.Errorf("merge failed: %w", err)
	}
	return fmt.Sprintf(
		"merged the outline entries from %s through %s into one entry. Its spanning citation %s resolves every original message via recall. Outline now has %d entries.",
		startCitation, endCitation, merged, deps.OutlineLen()), nil
}
