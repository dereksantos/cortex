package tools

import (
    "fmt"
    "strconv"
    "strings"
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

    // Parse citations to get turn ordinals
    startOrdinal, err := parseTurnOrdinal(startCitation)
    if err != nil {
        return "", fmt.Errorf("invalid start citation: %w", err)
    }

    endOrdinal, err := parseTurnOrdinal(endCitation)
    if err != nil {
        return "", fmt.Errorf("invalid end citation: %w", err)
    }

    if startOrdinal > endOrdinal {
        return "", fmt.Errorf("range_start (%d) must be <= range_end (%d)", startOrdinal, endOrdinal)
    }

    // Get the number of entries to merge
    count := endOrdinal - startOrdinal + 1
    
    // For now, return a message indicating what would happen
    // Full implementation would merge outline entries in CortexSession
    return fmt.Sprintf(
        "context_merge: would merge %d turns (ordinal %d to %d) into single entry. "+
        "Start: %s, End: %s", count, startOrdinal, endOrdinal, startCitation, endCitation), nil
}

// parseTurnOrdinal parses a citation string like "@session/20260701-143210#t12"
// and returns the turn ordinal (12 in this example).
func parseTurnOrdinal(citation string) (int, error) {
    // Expected format: @session/<session-id>#t<ordinal>
    if !strings.HasPrefix(citation, "@session/") {
        return 0, fmt.Errorf("citation must start with '@session/'")
    }
    
    rest := citation[len("@session/"):]
    
    // Split by #t
    parts := strings.Split(rest, "#t")
    if len(parts) != 2 {
        return 0, fmt.Errorf("citation must contain '#t<ordinal>'")
    }
    
    // Parse ordinal
    ordinalStr := parts[1]
    if !strings.HasPrefix(ordinalStr, "t") {
        return 0, fmt.Errorf("ordinal must start with 't'")
    }
    
    ordinal, err := strconv.Atoi(ordinalStr[1:])
    if err != nil {
        return 0, fmt.Errorf("invalid ordinal: %w", err)
    }
    
    return ordinal, nil
}
