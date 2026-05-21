package ops

import (
	"context"
	"strings"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// CompressSpec returns the NodeSpec for attend.compress — the salience-
// budget enforcement op from docs/salience-budgets.md.
//
// Phase 1 is a passthrough stub: if the raw input fits the
// max_tokens budget the op returns it verbatim; if not, it truncates
// to fit and appends a "[...]" marker. No LLM call yet — the goal of
// the stub is to prove the wiring (trace columns, executor post-handler
// hook, calibration logging) before the compressor quality matters.
//
// Phase 2 replaces this handler with a real small-model Reflect-style
// compression call. The signature stays the same so the executor's
// post-handler hook doesn't need to change.
//
// Token accounting: Phase 1 uses a 4-char-per-token approximation. Good
// enough for stub behavior; Phase 2 will tokenize properly via the
// provider's tokenizer.
func CompressSpec() dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "compress",
		Description: "compress an oversized output to fit a salience budget, preserving intent-relevant content",
		Inputs: []dag.ParamSpec{
			{Name: "raw", Type: "string", Required: true},
			{Name: "max_tokens", Type: "int", Required: true},
			{Name: "intent", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "compressed", Type: "string"},
			{Name: "original_tokens", Type: "int"},
			{Name: "kept_tokens", Type: "int"},
			{Name: "lossy", Type: "bool"},
		},
		// Stub costs are tiny because no LLM call runs. Phase 2 will
		// raise these to match real compressor latency/tokens, at which
		// point pre-spawn CanAfford checks start governing whether a
		// compression is affordable on a near-exhausted budget.
		Cost:      dag.Cost{LatencyMS: 5, Tokens: 0},
		Exposable: false,
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			raw := readString(in, "raw")
			maxTokens := readInt(in, "max_tokens", 0)
			origTok := approxTokens(raw)

			// max_tokens=0 means "no cap" — passthrough untouched.
			if maxTokens <= 0 || origTok <= maxTokens {
				return dag.NodeResult{
					Out: map[string]any{
						"compressed":      raw,
						"original_tokens": origTok,
						"kept_tokens":     origTok,
						"lossy":           false,
					},
					CostConsumed: dag.Cost{LatencyMS: 5, OutputTokens: origTok},
				}, nil
			}

			// Truncation marker carries a hint that this is a stub
			// compression, not real salience extraction. Phase 2 swaps
			// this for an LLM-driven Reflect-style summary keyed off
			// the intent string.
			marker := " […compressed-stub: " + readString(in, "intent") + "]"
			markerTok := approxTokens(marker)
			budget := maxTokens - markerTok
			if budget < 1 {
				budget = 1
			}
			kept := truncateToTokens(raw, budget) + marker

			return dag.NodeResult{
				Out: map[string]any{
					"compressed":      kept,
					"original_tokens": origTok,
					"kept_tokens":     approxTokens(kept),
					"lossy":           true,
				},
				CostConsumed: dag.Cost{LatencyMS: 5, OutputTokens: approxTokens(kept)},
			}, nil
		},
	}
}

// approxTokens returns a coarse token count for s using a 4-char rule.
// Faster + dependency-free than calling out to a tokenizer; off by
// 10-25% for English source code but consistent. Phase 2 swaps this
// for the provider's tokenizer.
func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		return 1
	}
	return n
}

// truncateToTokens cuts s at the byte length corresponding to maxTok
// tokens (under approxTokens), respecting UTF-8 boundaries by
// trimming any trailing partial rune. The intent is "fast and
// deterministic"; real salience-aware truncation comes in Phase 2.
func truncateToTokens(s string, maxTok int) string {
	if maxTok <= 0 {
		return ""
	}
	maxBytes := maxTok * 4
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	// Trim trailing partial rune so we never emit invalid UTF-8.
	for len(cut) > 0 && !isASCIIOrFullRuneEnd(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " \t\n")
}

// isASCIIOrFullRuneEnd reports whether s ends on an ASCII byte or a
// completed UTF-8 sequence — i.e. cutting here doesn't strand a partial
// rune. UTF-8 continuation bytes have the high bits 10xxxxxx; a string
// that ends on one means the rune is incomplete.
func isASCIIOrFullRuneEnd(s string) bool {
	if s == "" {
		return true
	}
	last := s[len(s)-1]
	// Single-byte rune (high bit clear) is always a complete rune end.
	if last&0x80 == 0 {
		return true
	}
	// High bits 11xxxxxx → start of a multi-byte sequence; cutting
	// here strands a partial rune. 10xxxxxx → continuation byte;
	// same problem.
	return false
}
