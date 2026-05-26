// Package dag implements the per-turn DAG protocol per
// docs/dag-protocol.md — seed + grow + decay runtime where typed nodes
// spawn children under a decaying budget.
//
// This file: the Budget type. Three axes (latency_ms, tokens, depth)
// all decay as work happens. The executor enforces them per
// docs/dag-protocol.md "Budget model".
package dag

import (
	"fmt"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Budget tracks the remaining resources a turn DAG can consume.
// Four axes (latency_ms, tokens, depth, output_tokens) decay as work
// happens. MaxContextTokens is a fifth field that DOES NOT decay —
// it's the active model's context window, the same value for every
// node in the turn. Handlers read it to size their own prompts
// (e.g., attend.accumulate uses it to derive a per-snapshot cap;
// decide.next uses it to bias toward narrower spawns when small).
//
// Zero on MaxContextTokens means "unknown / unbounded" — handlers
// keep their pre-MaxContextTokens behavior (e.g., calibrated
// defaults). Set by the seed budget when the harness knows the
// model's n_ctx (either from the probe cache or learned-from-
// overflow via internal/harness/loop.go).
type Budget struct {
	LatencyMS    int // milliseconds of wall time remaining
	Tokens       int // LLM token budget remaining
	Depth        int // max remaining spawn depth (decrement per spawn)
	OutputTokens int // tokens nodes may still deposit into turn state

	// MaxContextTokens is the model's n_ctx — the cap on a single
	// node's prompt size, NOT a consumable. Same value across the
	// turn; handlers consult it when deciding how much context to
	// build / fold / include. 0 = unknown; treat as unbounded.
	MaxContextTokens int

	// Provider is the per-node LLM provider the executor's Router
	// resolved at spawn time (from NodeSpec.Attrs["model"] override,
	// then NodeSpec.Requires chain via Registry.PickForCapabilities,
	// then session default). Nil when no router is wired or when no
	// resolution path produced a provider — handlers fall back to
	// their own configured provider (cfg.Provider). Per
	// docs/per-node-routing-plan.md "Executor wiring (Option A)".
	Provider llm.Provider

	// Intent is the classified session intent ("code" / "review" /
	// "recall" / "meta" / "greeting" / "clarify") set by the REPL after
	// sense.classify_intent runs. Handlers that need to shape work by
	// intent (e.g. the per-deposit emission budget for chunked tool
	// outputs via EmittedTokensCap) read it here. Empty preserves
	// pre-intent behavior — callers without classification keep today's
	// shape.
	Intent string

	// Scope is the free-form description emitted by sense.estimate_scope
	// for this turn — e.g. "whole-project audit comparing README claims
	// against implementation across a 600-file Go repo". The planner
	// (decide.next) reads this and shapes its plan accordingly: a wide
	// audit warrants many narrow reads; a pinpoint question warrants
	// one focused read. Empty preserves pre-estimator behavior.
	Scope string
}

// Cost is what a single node call reports as consumed. The executor
// subtracts this from the running budget. OutputTokens tracks what the
// node *left behind* in turn state (deposited into Out) — distinct
// from Tokens, which tracks LLM tokens spent producing it.
type Cost struct {
	LatencyMS    int
	Tokens       int
	OutputTokens int
}

// Consume subtracts c from b. Does not clamp at zero — the caller
// (executor) checks Exhausted to decide whether to stop spawning.
func (b *Budget) Consume(c Cost) {
	b.LatencyMS -= c.LatencyMS
	b.Tokens -= c.Tokens
	b.OutputTokens -= c.OutputTokens
}

// ConsumeDepth decrements depth by one (used per spawn).
func (b *Budget) ConsumeDepth() {
	b.Depth--
}

// Exhausted returns true if any axis has dropped to zero or below.
// Returns the axis name (latency_ms / tokens / depth / output_tokens)
// of the first axis that exhausted; empty if not exhausted.
//
// OutputTokens is checked only when the seed budget enabled it
// (initial value > 0). A zero seed means salience budgets are not in
// play for this turn and the axis is treated as unlimited — keeps
// pre-salience-budgets callers behaving exactly as before.
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
	// OutputTokens is opt-in: the axis only enforces when the seed
	// budget set it. Treating "0 means unlimited" rather than "0
	// means exhausted" keeps callers that don't yet allocate this
	// axis from spuriously exhausting on turn one.
	return false, ""
}

// CanAfford reports whether the budget has headroom for an op with
// the given cost hint. Used by the executor before scheduling a
// spawned child. Soft check — handlers can also self-modulate based
// on remaining budget.
//
// The OutputTokens dimension is checked only when the cost actually
// claims output. A node that doesn't deposit (e.g. a no-op steering
// call) doesn't have to fit under the output budget.
func (b Budget) CanAfford(c Cost) bool {
	if c.LatencyMS > b.LatencyMS {
		return false
	}
	if c.Tokens > b.Tokens {
		return false
	}
	if c.OutputTokens > 0 && c.OutputTokens > b.OutputTokens {
		return false
	}
	return true
}

func (b Budget) String() string {
	return fmt.Sprintf("Budget{lat=%dms tok=%d depth=%d out=%d ctx=%d}",
		b.LatencyMS, b.Tokens, b.Depth, b.OutputTokens, b.MaxContextTokens)
}

// WithMaxContextTokens returns a copy of b with MaxContextTokens
// set. Lets callers layer the model's n_ctx onto a per-intent seed
// budget without re-typing the rest:
//
//	budget := dag.BudgetForIntent(intent).WithMaxContextTokens(nctx)
//
// 0 keeps the field "unknown"; handlers fall back to their
// pre-MaxContextTokens behavior.
func (b Budget) WithMaxContextTokens(n int) Budget {
	if n < 0 {
		n = 0
	}
	b.MaxContextTokens = n
	return b
}

// EmittedTokensCap returns the per-deposit emission budget for chunked
// tool output, sized by classified intent and clamped to a fraction of
// the model's context window.
//
//   - code (or empty / unknown): 4000 tokens. Focused on the immediate
//     edit context — small enough that the accumulator can carry
//     several reads side-by-side without the synthesizer's prompt
//     blowing past the window.
//   - review / recall / meta: 16000 tokens. Read-heavy intents need
//     to see whole files; 16K covers ~90% of real source files end-to-
//     end so the agent doesn't loop on the first-N-chunks slice.
//
// Hard ceiling: 30% of MaxContextTokens (when known). Even on a small-
// context model, no single deposit may claim more than ~⅓ of the
// window — that leaves room for the system prompt, prior accumulator
// snapshots, and the response. Returns 0 when no policy applies
// (callers fall back to the legacy MaxEmittedChunks=8 cap).
func (b Budget) EmittedTokensCap() int {
	var cap int
	switch b.Intent {
	case "review", "recall", "meta":
		cap = 16000
	default:
		cap = 4000
	}
	if b.MaxContextTokens > 0 {
		ceiling := (b.MaxContextTokens * 30) / 100
		if ceiling > 0 && cap > ceiling {
			cap = ceiling
		}
	}
	return cap
}

// PromptBudget returns a recommended per-node prompt-token cap
// derived from MaxContextTokens. The 70% factor leaves headroom
// for: the system prompt + the response tokens (e.g. up to
// MaxOutputBudget for micro-LLMs) + tokenizer drift (the 4-char
// estimator is off by ~10–25%). Handlers use this as the default
// cap for their own prompt-building when MaxContextTokens is set.
//
// Returns 0 when MaxContextTokens is unknown — caller falls back to
// its calibrated default (e.g., attend.accumulate's max_tokens
// input).
func (b Budget) PromptBudget() int {
	if b.MaxContextTokens <= 0 {
		return 0
	}
	return (b.MaxContextTokens * 70) / 100
}

// DefaultTurnBudget is the seed budget for a turn-type DAG.
// Calibrated 2026-05-18 against OpenRouter Haiku 4.5 real-LLM
// measurements (calibrate_test.go): the 7 LLM-backed Stage 2 ops
// take 8-19s wall each, ~280-435 tokens each (315 token average).
// The Stage 2 turn chain runs 5 LLM ops sequentially (sense → embed →
// search → rerank → inject → coding_turn → extract_insight → capture)
// plus coding_turn (variable; the BIG node). For sequential walking:
//
//	5 LLM ops × 18s headroom = 90,000ms
//	+ coding_turn allowance   = 30,000ms
//	+ slack                   = 30,000ms
//	= 150,000ms total
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
		LatencyMS:    150000,
		Tokens:       10000,
		Depth:        10,
		OutputTokens: 8000, // ~half a 16k ctx window — see docs/salience-budgets.md
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

// BudgetForIntent returns the per-intent seed budget the REPL applies
// once sense.classify_intent has decided the SHAPE of the turn. Cheap
// shapes (greeting / clarify) get a small budget so a misroute can't
// blow the turn open; recall gets a middling budget for search +
// synthesis; code / review / meta stay at the calibrated full budget.
// Unknown intents fall back to DefaultTurnBudget — fail safe, never
// under-budget.
//
// Defense-in-depth: greeting / clarify budgets are intentionally too
// small to spawn decide.coding_turn (calibrated at 15s / 2000 tok).
// If the classifier mis-routes a code request as a greeting, the
// budget gate refuses the spawn rather than letting the trivial-intent
// turn balloon.
//
// Sized 2026-05-21 against the Haiku 4.5 calibration that drives
// DefaultTurnBudget (sense.scan_project_boundaries 2s; LLM ops 15-18s
// each at ~300-400 tok):
//   - greeting: act.passthrough is mechanical (~10ms / 0 tok). 2000ms
//     / 300 tok leaves enough headroom for the classifier itself plus
//     one cheap follow-up if needed; well under coding_turn cost.
//   - clarify: similar floor — one short LLM round to compose the
//     question; no tools.
//   - recall: vector_search + a small synthesis turn fits under 20s
//     / 3k tok with room for one re-decide.
//   - review: read-heavy; allow several tool_call reads + one
//     synthesis pass.
//   - meta: REPL/config queries — a small synthesis call.
//   - code: full DefaultTurnBudget; today's behavior.
func BudgetForIntent(intent string) Budget {
	var b Budget
	switch intent {
	case "greeting":
		b = Budget{LatencyMS: 2000, Tokens: 300, Depth: 3, OutputTokens: 500}
	case "clarify":
		b = Budget{LatencyMS: 3000, Tokens: 500, Depth: 3, OutputTokens: 600}
	case "recall":
		b = Budget{LatencyMS: 20000, Tokens: 3000, Depth: 5, OutputTokens: 2000}
	case "review":
		// Sized for multi-hop synthesis: grep → identify file → read
		// → answer commonly burns one synthesizer's worth of tokens
		// PER hop (~5-6k tokens in + small out at typical model sizes).
		// The earlier 5k cap couldn't even finish a single-hop synth
		// with a chunked grep output, so synth-mode follow-up spawns
		// got refused as budget_exceeded the moment the first synth
		// returned NEED_MORE. 15k accommodates ~2 hops at the calibrated
		// shape; latency bumped in proportion. See dagnode/coding_turn.go
		// synthesize-mode + maxHopDepth.
		b = Budget{LatencyMS: 120000, Tokens: 15000, Depth: 8, OutputTokens: 4000}
	case "meta":
		b = Budget{LatencyMS: 10000, Tokens: 2000, Depth: 4, OutputTokens: 1500}
	case "code":
		b = DefaultTurnBudget()
	default:
		b = DefaultTurnBudget()
	}
	b.Intent = intent
	return b
}
