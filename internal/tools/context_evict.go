package tools

import (
    "fmt"
)

// contextEvict removes an outline entry by citation from the session's outline.
// The entry is already demoted (in the journal), so this only affects the hydrated
// tail and outline. Returns a confirmation message.
func contextEvict(tc ToolCall, deps ToolDeps) (string, error) {
    citation, err := tc.StringArg("citation")
    if err != nil {
        return "", fmt.Errorf("citation is required: %w", err)
    }

    // Check if deps implements outline modification methods
    var outlineMod OutlineModifier
    if deps != nil {
        outlineMod = deps
    }
    
    // Remove the entry
    found := outlineMod.RemoveOutlineEntry(citation)

    if !found {
        return fmt.Sprintf("outline entry %q not found (already evicted or invalid citation)", citation), nil
    }

    return fmt.Sprintf("evicted outline entry %q. Outline now has %d entries.", citation, outlineMod.OutlineLen()), nil
}
