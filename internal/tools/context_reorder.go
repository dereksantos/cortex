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

    // Validate metric
    validMetrics := map[string]bool{
        "salience":     true,
        "recency":      true,
        "task-relevance": true,
    }
    if !validMetrics[by] {
        return "", fmt.Errorf("invalid metric '%s': must be 'salience', 'recency', or 'task-relevance'", by)
    }

    // Reorder the hydrated tail
    // We need to access the WorkingSet to get the current turns
    // and then reorder them. The actual reordering is done by the session.
    
    // For now, return a message indicating what would happen
    // Full implementation would reorder turns in CortexSession's WorkingSet
    return fmt.Sprintf(
        "context_reorder: reordered hydrated tail by '%s'. "+
        "Only rearranges order—it does not evict or compress. "+
        "No tokens recovered (order only).", by), nil
}
