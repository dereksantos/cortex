// Package llm — model capability inference.
//
// Endpoints that expose capability labels (Lemonade does, via the
// labels[] field on /v1/models) get those labels passed through
// verbatim. Endpoints that don't (raw Ollama, OpenAI proper, most
// hosted proxies) get labels inferred from the model id via the
// pattern table here. The two paths merge in EffectiveLabels.
//
// Phase 4 substrate. Role-map recommendation (Slice D) consumes
// EffectiveLabels to pick a model for each role.

package llm

import "strings"

// Capability labels are the union of what real endpoints emit (Lemonade
// uses these) and what the recommender consumes. Listed here as
// constants so consumers don't depend on stringly-typed magic.
//
// Specialty tags (`<base>:specialist`) mark a model as *trained
// primarily* for the named task — xLAM-1.5B for tool-calling,
// DeepSeek-Coder for code, o-series for reasoning. The convention:
// a specialty tag always implies the base tag, enforced when labels
// are assigned (here) and when an endpoint advertises them
// (passthrough in EffectiveLabels). Per-node routing
// (docs/per-node-routing-plan.md) relies on the distinction:
// `Requires: []string{CapToolCallingSpecialist, CapToolCalling}` means
// "prefer a specialist; accept any tool-caller as fallback."
const (
	CapCoding           = "coding"
	CapCodingSpecialist = "coding:specialist"

	CapToolCalling           = "tool-calling"
	CapToolCallingSpecialist = "tool-calling:specialist"

	CapReasoning           = "reasoning"
	CapReasoningSpecialist = "reasoning:specialist"

	CapEmbedding = "embeddings"
	CapReranking = "reranking"
	CapVision    = "vision"
)

// EffectiveLabels returns a model's capability tags, preferring labels
// exposed by the endpoint and falling back to ID-pattern inference.
// Endpoints that already emit good labels (Lemonade) pass through; bare
// Ollama listings get inferred labels so the role-map recommender can
// route them anyway.
func EffectiveLabels(m CompatModel) []string {
	if len(m.Labels) > 0 {
		return m.Labels
	}
	return InferCapabilities(m.ID)
}

// InferCapabilities returns capability tags for a model id based on
// naming conventions. Tolerant of registry prefixes (e.g.
// "anthropic/claude-haiku-4.5", "qwen/qwen-2.5-coder-32b") and tag
// suffixes (e.g. "qwen2.5-coder:7b"). Empty when no pattern matches —
// caller can decide whether to treat that as "unknown" or "default
// chat".
//
// The table is intentionally conservative: a missing label is better
// than a wrong one (a "coding" tag on a generic chat model would
// route real coding work to the wrong model).
func InferCapabilities(modelID string) []string {
	id := normalizeModelID(modelID)
	var labels []string

	switch {
	case strings.Contains(id, "embed"):
		// Pure embedder. Don't tag as coding/reasoning even if
		// the parent family is known for those.
		labels = append(labels, CapEmbedding)
		return labels
	case strings.Contains(id, "rerank"):
		labels = append(labels, CapReranking)
		return labels
	}

	// Tool-calling specialists — models trained primarily for
	// function-call JSON emission. These are reliable JSON emitters
	// at small sizes and are the engine per-node routing leans on for
	// `decide.tool_call` / `decide.next`. Additive: a "xlam-coder"
	// hybrid (hypothetical) would correctly pick up both specialty
	// tags via the coder check below.
	if strings.Contains(id, "xlam") ||
		strings.Contains(id, "phi-3-mini-tools") || strings.Contains(id, "phi3-mini-tools") ||
		strings.Contains(id, "hermes-tool") || strings.Contains(id, "hermes-function") ||
		strings.Contains(id, "functionary") {
		labels = append(labels, CapToolCalling, CapToolCallingSpecialist)
	}

	// Coding-specialized families. Coder-trained models are coding
	// specialists by virtue of training intent. They're also generally
	// tool-capable, but NOT tool-calling specialists — purpose-built
	// JSON emitters (above) beat them on `decide.tool_call`.
	if strings.Contains(id, "coder") || strings.Contains(id, "codestral") || strings.Contains(id, "codegemma") || strings.Contains(id, "deepseek-coder") {
		labels = append(labels, CapCoding, CapCodingSpecialist, CapToolCalling)
	}

	// Reasoning families. Some overlap with coding (Qwen3-14B for
	// example is reasoning but not coder-specialized). The o-series
	// is split out as a reasoning *specialist* — OpenAI's chain-of-
	// thought-trained line; gpt-4/5 are generally capable but not
	// specialty reasoners.
	switch {
	case strings.Contains(id, "qwen3") && !hasLabel(labels, CapCoding):
		labels = append(labels, CapReasoning, CapToolCalling)
	case strings.Contains(id, "claude"):
		// Frontier chat; supports reasoning + tool-calling broadly.
		labels = append(labels, CapReasoning, CapToolCalling)
	case strings.Contains(id, "o1") || strings.Contains(id, "o3") || strings.Contains(id, "o4"):
		labels = append(labels, CapReasoning, CapReasoningSpecialist, CapToolCalling)
	case strings.Contains(id, "gpt-4") || strings.Contains(id, "gpt-5"):
		labels = append(labels, CapReasoning, CapToolCalling)
	case strings.Contains(id, "llama-3") || strings.Contains(id, "llama3"):
		// Llama 3.1+ supports tool-calling per Meta's spec.
		labels = append(labels, CapToolCalling)
	case strings.Contains(id, "mistral") || strings.Contains(id, "mixtral"):
		labels = append(labels, CapToolCalling)
	case strings.Contains(id, "gemma") && !strings.Contains(id, "codegemma"):
		// Plain Gemma chat (not the coder variant); tool-calling
		// support is patchy across sizes.
		// Leave labels as-is — caller treats empty as "default chat".
	}

	// Vision suffix.
	if strings.Contains(id, "vision") || strings.Contains(id, "-vl-") || strings.Contains(id, "llava") {
		labels = append(labels, CapVision)
	}

	return labels
}

// normalizeModelID lowercases and strips common registry prefixes +
// tag suffixes so the pattern table doesn't have to enumerate every
// shape ("anthropic/claude-haiku-4.5" vs "claude-haiku-4.5").
func normalizeModelID(id string) string {
	s := strings.ToLower(id)
	// Strip registry prefix (chars before "/") if present.
	if i := strings.Index(s, "/"); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	// Strip tag suffix (chars after ":") if present — Ollama
	// convention: "qwen2.5-coder:7b".
	if i := strings.Index(s, ":"); i > 0 {
		s = s[:i]
	}
	return s
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// HasCapability returns true when m's effective labels include cap.
// Convenience for role-map recommendation: HasCapability(m, CapCoding).
func HasCapability(m CompatModel, cap string) bool {
	for _, l := range EffectiveLabels(m) {
		if l == cap {
			return true
		}
	}
	return false
}

// ContextClass is a coarse capability bucket the harness uses to
// allocate salience budgets: tighter caps + more fan-out for small
// models that can't synthesize big chunks; looser caps + fewer nodes
// for large models that hold more in working memory. See
// docs/salience-budgets.md Phase-3 Slice 1.
type ContextClass int

const (
	// ContextSmall fits ≤7B parameter models OR endpoints with
	// max_context_window ≤ 8192. These need narrow tool surfaces and
	// per-call outputs <= ~250 tokens to maintain coherence.
	ContextSmall ContextClass = iota
	// ContextMedium covers 7-30B models and 16-32k contexts. Default
	// when no signal — most local serves land here.
	ContextMedium
	// ContextLarge covers 30B+ local + frontier hosted models with
	// 32k+ contexts. These tolerate (and benefit from) larger chunks
	// per node so a synthesis turn sees rich, uncompressed evidence.
	ContextLarge
)

// String renders the class for log lines / prompts.
func (c ContextClass) String() string {
	switch c {
	case ContextSmall:
		return "small"
	case ContextLarge:
		return "large"
	default:
		return "medium"
	}
}

// InferContextClass picks a coarse capability bucket for a model id
// based on naming conventions + the optional endpoint-provided context
// window. Priority order:
//
//  1. Frontier hosted families (claude / gpt-4+ / o-series / sonnet /
//     opus) → ContextLarge.
//  2. Explicit parameter-count tag (e.g. "qwen2.5-coder:1.5b",
//     "qwen3-coder-30b-a3b") → bucketed by size.
//  3. ctxWindow hint (when > 0): ≥32k → Large, ≥16k → Medium, else
//     Small.
//  4. Fallback → ContextMedium.
//
// Inference is conservative: an unknown id with no ctxWindow signal
// returns ContextMedium rather than guessing wrong on either edge.
func InferContextClass(modelID string, ctxWindow int) ContextClass {
	id := normalizeModelID(modelID)

	// Frontier hosted models — assume large regardless of size tag.
	switch {
	case strings.Contains(id, "claude"),
		strings.Contains(id, "sonnet"),
		strings.Contains(id, "opus"),
		strings.Contains(id, "gpt-4"),
		strings.Contains(id, "gpt-5"),
		strings.Contains(id, "o1"),
		strings.Contains(id, "o3"),
		strings.Contains(id, "o4"):
		return ContextLarge
	}

	// Parameter-count tag inference. parseParamCount is called on the
	// RAW id so Ollama's tag suffix (qwen2.5-coder:1.5b) is preserved
	// — the normalized form already stripped it. Lossy on the
	// sub-billion edge but adequate for the small/medium/large split.
	if size := parseParamCount(modelID); size > 0 {
		switch {
		case size <= 7:
			return ContextSmall
		case size >= 30:
			return ContextLarge
		default:
			return ContextMedium
		}
	}

	// Endpoint-provided context-window hint as a secondary signal.
	switch {
	case ctxWindow >= 32768:
		return ContextLarge
	case ctxWindow > 0 && ctxWindow < 8192:
		return ContextSmall
	}

	return ContextMedium
}

// SalienceCapForClass returns the recommended per-tool-call salience
// cap for a model in this context class. Sized so an 8-turn agent
// loop's compressed transcript fits comfortably inside the class's
// typical context window with room for system prompt + reasoning.
//
// When a calibration snapshot has been applied via
// SetSalienceCapOverride (Phase 3 Slice 5), the override wins —
// callers see the calibrated value. Falls back to the static
// 200/500/1500 defaults from Phase 3 Slice 1 when no override is
// configured.
func SalienceCapForClass(c ContextClass) int {
	if v, ok := lookupSalienceOverride(c); ok {
		return v
	}
	switch c {
	case ContextSmall:
		return 200
	case ContextLarge:
		return 1500
	default:
		return 500
	}
}

// salienceCapOverride holds the per-class caps loaded from the
// salience calibration snapshot. Nil means "no override; use the
// static defaults". Mutated by SetSalienceCapOverride at process
// startup (the REPL loads .cortex/calibration/salience.json once
// per session) and read by SalienceCapForClass on every lookup.
//
// Package-level state is justified here because the calibration is
// a process-wide knob (every REPL turn, every harness call, every
// trace row should see the same cap) and the alternative would be
// threading a *SalienceCalibration through dozens of call sites
// that already don't take any state.
var salienceCapOverride map[ContextClass]int

// SetSalienceCapOverride configures the per-class cap overrides
// SalienceCapForClass returns. Pass a nil or empty map to clear and
// revert to the static defaults. Used by the REPL at session start
// after loading .cortex/calibration/salience.json.
//
// Not safe for concurrent use with SalienceCapForClass — call this
// at startup before any tool calls go out.
func SetSalienceCapOverride(perClass map[ContextClass]int) {
	if len(perClass) == 0 {
		salienceCapOverride = nil
		return
	}
	cp := make(map[ContextClass]int, len(perClass))
	for k, v := range perClass {
		if v > 0 {
			cp[k] = v
		}
	}
	salienceCapOverride = cp
}

func lookupSalienceOverride(c ContextClass) (int, bool) {
	if salienceCapOverride == nil {
		return 0, false
	}
	v, ok := salienceCapOverride[c]
	return v, ok
}
