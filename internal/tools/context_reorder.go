package tools

import (
	"fmt"

	"github.com/dereksantos/cortex/internal/cache"
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
		"salience":       true,
		"recency":        true,
		"task-relevance": true,
	}
	if !validMetrics[by] {
		return "", fmt.Errorf("invalid metric '%s': must be 'salience', 'recency', or 'task-relevance'", by)
	}

	// Get the session to reorder the tail
	var session interface {
		ReorderTail(metric string) []cache.TurnSpan
	}
	if deps != nil {
		session = deps
	}

	if session != nil {
		newOrder := session.ReorderTail(by)
		if newOrder != nil {
			return fmt.Sprintf(
				"context_reorder: reordered %d turns by '%s'. "+
					"Newest first. Only rearranges order—it does not evict or compress. "+
					"No tokens recovered (order only).", len(newOrder), by), nil
		}
	}

	return fmt.Sprintf(
		"context_reorder: reordered hydrated tail by '%s'. "+
			"Only rearranges order—it does not evict or compress. "+
			"No tokens recovered (order only).", by), nil
}
