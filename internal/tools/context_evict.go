package tools

import (
	"fmt"
)

// contextEvict removes an outline entry by citation from the session's outline.
// The turn is already demoted (its raw messages stay in the transcript, still
// reachable via recall), so eviction only shrinks the outline zone.
func contextEvict(tc ToolCall, deps ToolDeps) (string, error) {
	citation, err := tc.StringArg("citation")
	if err != nil {
		return "", fmt.Errorf("citation is required: %w", err)
	}

	printToolAction(deps, fmt.Sprintf("context_evict(%s)", citation))

	if !deps.RemoveOutlineEntry(citation) {
		return fmt.Sprintf("outline entry %q not found (already evicted or invalid citation)", citation), nil
	}
	return fmt.Sprintf("evicted outline entry %q. Outline now has %d entries.", citation, deps.OutlineLen()), nil
}
