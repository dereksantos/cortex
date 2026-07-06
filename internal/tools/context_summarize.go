package tools

import (
    "fmt"
)

// contextSummarize compresses a demoted turn into a compact digest.
// Uses the existing Summarizer interface (sequential chunk-and-fold)
// and preserves citations mechanically.
func contextSummarize(tc ToolCall, deps ToolDeps) (string, error) {
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }

    // TODO: Implement context summarization
    return fmt.Sprintf("context_summarize placeholder for %s", citation), nil
}
