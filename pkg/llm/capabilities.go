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
const (
	CapCoding      = "coding"
	CapToolCalling = "tool-calling"
	CapReasoning   = "reasoning"
	CapEmbedding   = "embeddings"
	CapReranking   = "reranking"
	CapVision      = "vision"
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

	// Coding-specialized families.
	if strings.Contains(id, "coder") || strings.Contains(id, "codestral") || strings.Contains(id, "codegemma") || strings.Contains(id, "deepseek-coder") {
		labels = append(labels, CapCoding, CapToolCalling)
	}

	// Reasoning families. Some overlap with coding (Qwen3-14B for
	// example is reasoning but not coder-specialized).
	switch {
	case strings.Contains(id, "qwen3") && !hasLabel(labels, CapCoding):
		labels = append(labels, CapReasoning, CapToolCalling)
	case strings.Contains(id, "claude"):
		// Frontier chat; supports reasoning + tool-calling broadly.
		labels = append(labels, CapReasoning, CapToolCalling)
	case strings.Contains(id, "gpt-4") || strings.Contains(id, "gpt-5") || strings.Contains(id, "o1") || strings.Contains(id, "o3") || strings.Contains(id, "o4"):
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
