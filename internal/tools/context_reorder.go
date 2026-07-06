package tools

import (
    "fmt"
)

// contextReorder reorders the hydrated tail based on the given metric.
// Only rearranges order—it does not evict or compress.
func contextReorder(tc ToolCall, deps ToolDeps) (string, error) {
    by, err := tc.StringArg("by")
    if err != nil {
        return "", fmt.Errorf("by is required: %w", err)
    }

    // TODO: Implement context reorder
    return fmt.Sprintf("context_reorder placeholder for metric %s", by), nil
}
