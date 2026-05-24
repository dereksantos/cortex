package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// attend.distill — two-pass salience compression.
//
// attend.compress is one LLM call: "summarize this under N tokens
// preserving X." It works for clean inputs. It struggles when the
// source is long, mixed-relevance, or when intent fidelity matters
// — the model picks salient content and writes the surface form in
// the same step, and either side can degrade silently.
//
// attend.distill separates the two concerns into successive LLM
// calls:
//
//	pass 1 (extract): enumerate candidate facts from the source,
//	                  prioritized 1..5 by intent-relevance.
//	pass 2 (emit):    rewrite the prioritized facts as a compressed
//	                  output that fits the salience budget.
//
// More LLM activity per compression — but the "what stays" decision
// becomes an explicit, auditable artifact (the priority-tagged fact
// list) that the calibration loop can inspect. Routed to selectively:
// CompressConfig.Calibration is read at handler-build time, and the
// dispatcher uses ShouldDistill to choose between attend.compress
// (single-pass) and attend.distill (two-pass) per intent based on
// the observed fallback rate.

// distillCostHint covers two small-model calls plus a deterministic
// merge. Roughly 2× compress's cost; the trade-off is paid only when
// calibration says the intent benefits.
var distillCostHint = dag.Cost{LatencyMS: 8000, Tokens: 600}

// DistillConfig wires DistillSpec to its provider + (optional)
// calibration data. Nil Provider falls back to attend.compress's
// truncate-stub via the chained Compress handler — distill cannot
// run without an LLM.
type DistillConfig struct {
	Provider llm.Provider
}

// DistillSpec returns the NodeSpec for attend.distill. The op is a
// sibling of attend.compress: same input/output contract, different
// internal mechanic. Callers who already constructed a CompressSpec
// can swap to DistillSpec without changing the call site.
func DistillSpec(opts ...DistillConfig) dag.NodeSpec {
	var cfg DistillConfig
	if len(opts) > 0 {
		cfg = opts[0]
	}
	return dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "distill",
		Description: "compress an oversized output via a two-pass extract+emit pipeline; more LLM activity than attend.compress, more emergent salience decision",
		Inputs: []dag.ParamSpec{
			{Name: "raw", Type: "string", Required: true},
			{Name: "max_tokens", Type: "int", Required: true},
			{Name: "intent", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "compressed", Type: "string"},
			{Name: "original_tokens", Type: "int"},
			{Name: "kept_tokens", Type: "int"},
			{Name: "passes", Type: "int"},
			{Name: "facts", Type: "string"},
			{Name: "lossy", Type: "bool"},
			{Name: "fallback", Type: "bool"},
		},
		Cost:      distillCostHint,
		Exposable: false,
		Handler:   newDistillHandler(cfg),
	}
}

// newDistillHandler builds the attend.distill handler. The two-pass
// path runs when:
//
//  1. raw exceeds max_tokens (otherwise passthrough)
//  2. a provider is wired AND available
//  3. budget LatencyMS is at least minDistillLatency (a single LLM
//     call is cheap; two are not — fall back rather than half-run)
//
// Any failure in pass 1 or pass 2 falls through to attend.compress's
// truncate-stub path. The trace row carries `passes: 2` on the LLM
// path, `passes: 0` on passthrough, `passes: 1` on the fallback —
// the calibration loop reads this to attribute downstream quality.
func newDistillHandler(cfg DistillConfig) dag.Handler {
	return func(ctx context.Context, in map[string]any, budget dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		raw := readString(in, "raw")
		maxTokens := readInt(in, "max_tokens", 0)
		intent := readString(in, "intent")
		origTok := approxTokens(raw)

		// Passthrough — same contract as attend.compress.
		if maxTokens <= 0 || origTok <= maxTokens {
			return dag.NodeResult{
				Out: map[string]any{
					"compressed":      raw,
					"original_tokens": origTok,
					"kept_tokens":     origTok,
					"passes":          0,
					"facts":           "",
					"lossy":           false,
					"fallback":        false,
				},
				CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: origTok},
			}, nil
		}

		// Two-pass LLM path. Prefer the executor-resolved provider
		// (Router populated Budget.Provider — see
		// docs/per-node-routing-plan.md slice 3), falling back to
		// cfg.Provider on the synthetic / no-Router path.
		provider := budget.Provider
		if provider == nil {
			provider = cfg.Provider
		}
		if provider != nil && provider.IsAvailable() && budget.LatencyMS >= minDistillLatencyMS {
			if out, ok := llmDistill(ctx, provider, raw, intent, maxTokens, started); ok {
				return out, nil
			}
		}

		// Fallback — same truncate-stub attend.compress uses, so a
		// distill node that can't run still produces a usable output.
		marker := " […distilled-stub: " + intent + "]"
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
				"passes":          1,
				"facts":           "",
				"lossy":           true,
				"fallback":        true,
			},
			CostConsumed: dag.Cost{LatencyMS: int(time.Since(started).Milliseconds()), OutputTokens: approxTokens(kept)},
		}, nil
	}
}

// minDistillLatencyMS gates the LLM path. Two small-model calls in
// series typically land in 2-8s; under this budget the fallback
// stub keeps the dispatcher path live.
const minDistillLatencyMS = 4000

// llmDistill runs the two-pass extract → emit pipeline. Returns
// (result, true) on success; (zero, false) on any failure in either
// pass so the caller can fall through to the deterministic stub.
//
// Pass 1 prompts the model to enumerate facts as priority-tagged
// bullet lines. Pass 2 prompts the model to rewrite those facts as
// the final compressed output under the budget. Both prompts are
// inline because the template loader expects single-template-per-op
// — distill is one op with two prompts, which doesn't fit that
// cleanly. Inline strings are simpler than reshaping the loader.
func llmDistill(ctx context.Context, p llm.Provider, raw, intent string, maxTokens int, started time.Time) (dag.NodeResult, bool) {
	extract := buildDistillExtractPrompt(raw, intent, maxTokens)
	factsRaw, s1, err := p.GenerateWithStats(ctx, extract)
	if err != nil {
		return dag.NodeResult{}, false
	}
	facts := strings.TrimSpace(factsRaw)
	if facts == "" {
		return dag.NodeResult{}, false
	}

	emit := buildDistillEmitPrompt(facts, intent, maxTokens)
	compressedRaw, s2, err := p.GenerateWithStats(ctx, emit)
	if err != nil {
		return dag.NodeResult{}, false
	}
	compressed := strings.TrimSpace(compressedRaw)
	if compressed == "" {
		return dag.NodeResult{}, false
	}

	// Defensive clamp — same rationale as attend.compress.
	keptTok := approxTokens(compressed)
	if keptTok > maxTokens {
		compressed = truncateToTokens(compressed, maxTokens)
		keptTok = approxTokens(compressed)
	}
	latency := int(time.Since(started).Milliseconds())
	totalTokens := s1.TotalTokens() + s2.TotalTokens()
	return dag.NodeResult{
		Out: map[string]any{
			"compressed":      compressed,
			"original_tokens": approxTokens(raw),
			"kept_tokens":     keptTok,
			"passes":          2,
			"facts":           facts,
			"lossy":           true,
			"fallback":        false,
		},
		CostConsumed: dag.Cost{LatencyMS: latency, Tokens: totalTokens, OutputTokens: keptTok},
	}, true
}

func buildDistillExtractPrompt(raw, intent string, maxTokens int) string {
	return fmt.Sprintf(`List the concrete facts in the source below that are most relevant to the intent. Each line is one fact, prefixed by a priority digit (1 = critical, 5 = peripheral) and a hyphen.

Intent: %s

Source:
%s

Rules:
- Each fact is a short noun phrase or sentence (≤ 15 tokens). No full paragraphs.
- Prefer concrete identifiers (function/class/file names, paths, numbers, error strings, decision-bearing sentences) over generic descriptions.
- Drop boilerplate, license headers, navigation prose entirely.
- Aim for ≤ %d tokens total across all facts; cut peripheral facts if needed.
- Do NOT invent facts. If the source lacks information the intent asks about, emit one line: "1 - (no X found in source)".
- One fact per line. No prose framing, no markdown fences. Start with the first fact.`, intent, raw, maxTokens*2)
}

func buildDistillEmitPrompt(facts, intent string, maxTokens int) string {
	return fmt.Sprintf(`Rewrite the prioritized fact list below as a compressed output AT MOST %d tokens, preserving information directly relevant to the intent.

Intent: %s

Facts (priority 1 = critical, 5 = peripheral):
%s

Rules:
- Use ONLY information from the facts. Do not introduce new claims.
- Prioritize priority-1 facts; include lower-priority facts only if budget allows.
- Bullet points and abbreviated phrases beat full sentences.
- No prose framing, no markdown fences, no preamble. Start with the first piece of useful content.`, maxTokens, intent, facts)
}

// ShouldDistill reports whether the given intent benefits from the
// two-pass distill path over single-pass attend.compress, based on a
// loaded SalienceCalibration snapshot. Returns false when no
// snapshot is supplied (cold start) or when the intent has no
// recorded samples — the conservative single-pass default keeps
// per-turn cost predictable until the calibration loop has data.
//
// Heuristic: when an intent's recorded fallback_rate is at or above
// the threshold, the single-pass compressor was missing its budget
// often enough that the extra LLM pass is justified. The threshold
// (0.30) is set so a clean intent class never gets distilled; the
// switch only fires for genuinely difficult content.
func ShouldDistill(intent string, cal *dag.SalienceCalibration) bool {
	if cal == nil {
		return false
	}
	fit, ok := cal.PerIntent[intent]
	if !ok {
		return false
	}
	return fit.FallbackRate >= 0.30
}
