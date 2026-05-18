// Package dag implements the per-turn DAG protocol per
// docs/dag-protocol.md — seed + grow + decay runtime where typed nodes
// spawn children under a decaying budget.
//
// This file: the Budget type. Three axes (latency_ms, tokens, depth)
// all decay as work happens. The executor enforces them per
// docs/dag-protocol.md "Budget model".
package dag

import "fmt"

// Budget tracks the remaining resources a turn DAG can consume.
// Three axes; all decay on each node call.
type Budget struct {
	LatencyMS int // milliseconds of wall time remaining
	Tokens    int // LLM token budget remaining
	Depth     int // max remaining spawn depth (decrement per spawn)
}

// Cost is what a single node call reports as consumed. The executor
// subtracts this from the running budget.
type Cost struct {
	LatencyMS int
	Tokens    int
}

// Consume subtracts c from b. Does not clamp at zero — the caller
// (executor) checks Exhausted to decide whether to stop spawning.
func (b *Budget) Consume(c Cost) {
	b.LatencyMS -= c.LatencyMS
	b.Tokens -= c.Tokens
}

// ConsumeDepth decrements depth by one (used per spawn).
func (b *Budget) ConsumeDepth() {
	b.Depth--
}

// Exhausted returns true if any axis has dropped to zero or below.
// Returns the axis name (latency_ms / tokens / depth) of the first
// axis that exhausted; empty if not exhausted.
func (b Budget) Exhausted() (bool, string) {
	if b.LatencyMS <= 0 {
		return true, "latency_ms"
	}
	if b.Tokens <= 0 {
		return true, "tokens"
	}
	if b.Depth <= 0 {
		return true, "depth"
	}
	return false, ""
}

// CanAfford reports whether the budget has headroom for an op with
// the given cost hint. Used by the executor before scheduling a
// spawned child. Soft check — handlers can also self-modulate based
// on remaining budget.
func (b Budget) CanAfford(c Cost) bool {
	if c.LatencyMS > b.LatencyMS {
		return false
	}
	if c.Tokens > b.Tokens {
		return false
	}
	return true
}

func (b Budget) String() string {
	return fmt.Sprintf("Budget{lat=%dms tok=%d depth=%d}", b.LatencyMS, b.Tokens, b.Depth)
}

// DefaultTurnBudget is the seed budget for a turn-type DAG.
// Conservative defaults per docs/dag-protocol.md "Per-DAG-type seeds".
// pkg/config will override per project.
func DefaultTurnBudget() Budget {
	return Budget{
		LatencyMS: 2000,
		Tokens:    4000,
		Depth:     10,
	}
}

// DefaultThinkBudget is the seed budget for a think-type DAG. Smaller
// latency budget (background spare cycles); fewer tokens. Real budget
// is computed by the scheduler from inverse activity level.
func DefaultThinkBudget() Budget {
	return Budget{
		LatencyMS: 500,
		Tokens:    1000,
		Depth:     8,
	}
}

// DefaultDreamBudget is the seed budget for a dream-type DAG. Larger
// latency (idle time); more tokens; deeper exploration allowed.
func DefaultDreamBudget() Budget {
	return Budget{
		LatencyMS: 30000,
		Tokens:    20000,
		Depth:     15,
	}
}

// DefaultCaptureBudget is the seed budget for a capture-type DAG.
// Tiny — capture is per-event and must not block.
func DefaultCaptureBudget() Budget {
	return Budget{
		LatencyMS: 100,
		Tokens:    500,
		Depth:     3,
	}
}
