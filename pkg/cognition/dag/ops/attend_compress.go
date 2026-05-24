package ops

import (
	"context"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CompressConfig wires CompressSpec to a provider. Nil Provider falls
// back to the truncate-stub path (Phase 1 behavior) so call sites that
// haven't been upgraded still work.
type CompressConfig struct {
	Provider llm.Provider
}

// compressCostHint — a small-model salience-extraction call. The
// prompt is short, the model only needs to read the source and emit a
// compressed version; no JSON parsing or tool semantics. Budget for
// ~4s wall (room for a 1B local model with a few KB of source) and
// 300 tokens (the rendered prompt + an output that fits the requested
// max_tokens). Self-modulates: when budget can't afford this, falls
// back to the truncate-stub immediately.
var compressCostHint = dag.Cost{LatencyMS: 4000, Tokens: 300}

// CompressSpec returns the NodeSpec for attend.compress — the
// salience-budget enforcement op from docs/salience-budgets.md.
//
// When a Provider is wired (Phase 2), the handler renders the
// attend_compress.tmpl prompt and asks a small model to extract
// intent-relevant content under the requested token budget.
//
// When no Provider is wired or the budget can't afford the LLM call
// (Phase 1 fallback), the handler truncates to fit and appends a
// "[…compressed-stub: <intent>]" marker. The fallback is lossy but
// deterministic — the dispatcher path keeps working in tests + at
// startup before a provider is configured.
func CompressSpec(opts ...CompressConfig) dag.NodeSpec {
	var cfg CompressConfig
	if len(opts) > 0 {
		cfg = opts[0]
	}
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
			{Name: "fallback", Type: "bool"},
		},
		Cost:      compressCostHint,
		Exposable: false,
		Handler:   newCompressHandler(cfg),
	}
}

// newCompressHandler builds the attend.compress handler. Path
// selection per call:
//
//   - Under budget (raw ≤ max_tokens) → passthrough; no LLM call.
//   - Provider available + budget headroom → LLM compression via
//     attend_compress.tmpl; reports `fallback: false`.
//   - No provider OR low budget OR LLM error → truncate-stub with
//     intent-tagged marker; reports `fallback: true`. The dispatcher
//     path keeps working even when the small model is unreachable.
func newCompressHandler(cfg CompressConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		raw := readString(in, "raw")
		maxTokens := readInt(in, "max_tokens", 0)
		intent := readString(in, "intent")
		origTok := approxTokens(raw)

		// Passthrough when under budget. No LLM, no truncation.
		if maxTokens <= 0 || origTok <= maxTokens {
			return dag.NodeResult{
				Out: map[string]any{
					"compressed":      raw,
					"original_tokens": origTok,
					"kept_tokens":     origTok,
					"lossy":           false,
					"fallback":        false,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: origTok},
			}, nil
		}

		// LLM compression path. Provider unavailable or budget too
		// tight → fall through to the deterministic truncate-stub.
		//
		// Prefer the executor-resolved provider (Router populated
		// Budget.Provider — see docs/per-node-routing-plan.md slice 3).
		// Fall back to cfg.Provider when no Router is wired OR when
		// invoked synthetically by applySalienceCompression in
		// salience.go, which deliberately passes a clean Budget so
		// the configured compressor model stays in force on that path.
		provider := budget.Provider
		if provider == nil {
			provider = cfg.Provider
		}
		if provider != nil && provider.IsAvailable() && budget.LatencyMS >= fallbackBelowLatencyMS {
			if out, ok := llmCompress(ctx, provider, raw, intent, maxTokens, started); ok {
				return out, nil
			}
			// LLM path errored — fall through to truncate-stub.
		}

		// Truncate-stub fallback. Deterministic, no LLM cost.
		marker := " […compressed-stub: " + intent + "]"
		markerTok := approxTokens(marker)
		bytesBudget := maxTokens - markerTok
		if bytesBudget < 1 {
			bytesBudget = 1
		}
		kept := truncateToTokens(raw, bytesBudget) + marker
		return dag.NodeResult{
			Out: map[string]any{
				"compressed":      kept,
				"original_tokens": origTok,
				"kept_tokens":     approxTokens(kept),
				"lossy":           true,
				"fallback":        true,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: approxTokens(kept)},
		}, nil
	}
}

// llmCompress invokes the salience compressor via Provider and the
// attend_compress.tmpl prompt. Returns (result, true) on success;
// (zero, false) on any error so the caller can fall through to the
// truncate-stub. The post-call validation pass clamps the result to
// the requested budget — defensive against models that ignore the
// "AT MOST N tokens" instruction.
func llmCompress(ctx context.Context, p llm.Provider, raw, intent string, maxTokens int, started time.Time) (dag.NodeResult, bool) {
	pt, terr := LoadTemplate("attend_compress")
	if terr != nil {
		return dag.NodeResult{}, false
	}
	prompt, rerr := pt.Render(map[string]any{
		"raw":        raw,
		"intent":     intent,
		"max_tokens": maxTokens,
	})
	if rerr != nil {
		return dag.NodeResult{}, false
	}
	resp, stats, gerr := p.GenerateWithStats(ctx, prompt)
	if gerr != nil {
		return dag.NodeResult{}, false
	}
	compressed := strings.TrimSpace(resp)
	if compressed == "" {
		return dag.NodeResult{}, false
	}
	// Defensive clamp: a model that ignored the budget gets truncated
	// here. We keep the LLM's salience-driven prefix and drop the
	// overflow — the calibration loop will see consistently-high
	// kept_tokens for this intent class and surface the model as
	// budget-blind.
	keptTok := approxTokens(compressed)
	if keptTok > maxTokens {
		compressed = truncateToTokens(compressed, maxTokens)
		keptTok = approxTokens(compressed)
	}
	latency := int(time.Since(started).Milliseconds())
	return dag.NodeResult{
		Out: map[string]any{
			"compressed":      compressed,
			"original_tokens": approxTokens(raw),
			"kept_tokens":     keptTok,
			"lossy":           true,
			"fallback":        false,
		},
		CostConsumed: dag.Cost{LatencyMS: latency, Tokens: stats.TotalTokens(), OutputTokens: keptTok},
	}, true
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
// deterministic"; real salience-aware truncation comes via the
// LLM path above.
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
