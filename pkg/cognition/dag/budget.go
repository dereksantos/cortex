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
// Calibrated 2026-05-18 against OpenRouter Haiku 4.5 real-LLM
// measurements (calibrate_test.go): the 7 LLM-backed Stage 2 ops
// take 8-19s wall each, ~280-435 tokens each (315 token average).
// The Stage 2 turn chain runs 5 LLM ops sequentially (sense → embed →
// search → rerank → inject → coding_turn → extract_insight → capture)
// plus coding_turn (variable; the BIG node). For sequential walking:
//
//   5 LLM ops × 18s headroom = 90,000ms
//   + coding_turn allowance   = 30,000ms
//   + slack                   = 30,000ms
//   = 150,000ms total
//
// Stage 4 will revisit when parallel execution lands — parallel
// fan-out collapses sequential ops onto a single critical path, so
// budgets can shrink. For now, this supports the sequential chain.
//
// Token budget: 5 LLM ops × 400 tok headroom = 2,000 + coding_turn
// (~5,000 tok) + slack = 10,000.
//
// Depth 10 covers any plausible chain — the Stage 2 chain is depth 7
// (sense → embed → search → rerank → inject → coding_turn →
// extract_insight → capture, where each is the parent of the next).
//
// pkg/config will override per project.
func DefaultTurnBudget() Budget {
	return Budget{
		LatencyMS: 150000,
		Tokens:    10000,
		Depth:     10,
	}
}

// DefaultThinkBudget is the seed budget for a think-type DAG. Smaller
// latency budget (background spare cycles); fewer tokens. Real budget
// is computed by the scheduler from inverse activity level. Sized to
// support 1-2 sequential LLM ops at calibrated cost (e.g., a single
// value.score + maintain.extract_insight ≈ 7s + 15s + slack).
func DefaultThinkBudget() Budget {
	return Budget{
		LatencyMS: 30000,
		Tokens:    3000,
		Depth:     8,
	}
}

// DefaultDreamBudget is the seed budget for a dream-type DAG. Larger
// latency (idle time); more tokens; deeper exploration allowed.
// Sized to support ~10 sequential LLM ops + insight extraction at
// calibrated Haiku 4.5 cost (10 × 15s = 150s + extract overhead).
func DefaultDreamBudget() Budget {
	return Budget{
		LatencyMS: 180000,
		Tokens:    30000,
		Depth:     15,
	}
}

// DefaultCaptureBudget is the seed budget for a capture-type DAG.
// Tiny — capture is per-event and must not block. Even
// decide.should_capture (calibrated at 13s real-LLM) is too expensive
// for this budget; capture-type DAGs should run mechanical ops only,
// or self-modulate to fallbacks immediately.
func DefaultCaptureBudget() Budget {
	return Budget{
		LatencyMS: 100,
		Tokens:    500,
		Depth:     3,
	}
}
