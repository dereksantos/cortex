package tools

import (
    "fmt"
)

// contextMerge merges consecutive demoted turns into a single outline entry.
// Deterministic (no LLM) and preserves citations.
func contextMerge(tc ToolCall, deps ToolDeps) (string, error) {
    startCitation, err := tc.StringArg("range_start")
    if err != nil {
        return "", fmt.Errorf("range_start is required: %w", err)
    }

    endCitation, err := tc.StringArg("range_end")
    if err != nil {
        return "", fmt.Errorf("range_end is required: %w", err)
    }

    // TODO: Implement context merge
    return fmt.Sprintf("context_merge placeholder for range %s to %s", startCitation, endCitation), nil
}
