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

    // TODO: Implement watermark adjustment
    return fmt.Sprintf("context_adjust_watermarks placeholder: high_delta=%d, low_delta=%d", highDelta, lowDelta), nil
}
