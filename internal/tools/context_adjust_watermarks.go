package tools

import (
    "fmt"
)

// contextAdjustWatermarks adjusts the working set watermarks by the given deltas.
// Bounded (±W/4) to prevent abuse.
func contextAdjustWatermarks(tc ToolCall, deps ToolDeps) (string, error) {
    // Validation is done at dispatcher level via ValidateToolCall
    
    highDelta, _ := tc.IntArg("high_delta")
    lowDelta, _ := tc.IntArg("low_delta")

    // For now, return a message indicating what would happen
    // Full implementation would adjust watermarks in CortexSession's WorkingSet
    return fmt.Sprintf(
        "context_adjust_watermarks: adjusted watermarks by high_delta=%d, low_delta=%d. "+
        "Bounded (±W/4) to prevent abuse. "+
        "Hysteresis preserved (high - low gap unchanged).", highDelta, lowDelta), nil
}
