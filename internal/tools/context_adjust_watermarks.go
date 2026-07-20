package tools

import (
	"fmt"
)

// contextAdjustWatermarks adjusts the working set watermarks by the given
// deltas. The deltas are bounded (±W/4) by the session's validator and the
// working set itself; the lowWM <= highWM invariant is enforced there too.
func contextAdjustWatermarks(tc ToolCall, deps ToolDeps) (string, error) {
	highDelta, _ := tc.IntArg("high_delta")
	lowDelta, _ := tc.IntArg("low_delta")

	printToolAction(deps, fmt.Sprintf("context_adjust_watermarks(high_delta=%d, low_delta=%d)", highDelta, lowDelta))

	oldHigh, oldLow, newHigh, newLow, err := deps.AdjustWatermarks(highDelta, lowDelta)
	if err != nil {
		return "", fmt.Errorf("adjust watermarks failed: %w", err)
	}
	return fmt.Sprintf(
		"adjusted watermarks: high %d→%d, low %d→%d tokens. Demotion now triggers above %d and drains to %d.",
		oldHigh, newHigh, oldLow, newLow, newHigh, newLow), nil
}
