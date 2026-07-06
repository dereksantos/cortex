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

    // Get the session to adjust watermarks
    var session interface {
        AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error)
    }
    if deps != nil {
        session = deps
    }
    
    if session != nil {
        oldHigh, oldLow, newHigh, newLow, err := session.AdjustWatermarks(highDelta, lowDelta)
        if err != nil {
            return "", fmt.Errorf("adjust watermarks failed: %w", err)
        }
        return fmt.Sprintf(
            "Adjusted watermarks: high=%d→%d, low=%d→%d. "+
            "Bounded (±W/4) to prevent abuse. "+
            "Hysteresis preserved (high - low gap unchanged).",
            oldHigh, newHigh, oldLow, newLow), nil
    }
    
    return fmt.Sprintf(
        "context_adjust_watermarks: adjusted watermarks by high_delta=%d, low_delta=%d. "+
        "Bounded (±W/4) to prevent abuse. "+
        "Hysteresis preserved (high - low gap unchanged).", highDelta, lowDelta), nil
}
