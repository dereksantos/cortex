package llm

import (
	"reflect"
	"sort"
	"testing"
)

func TestInferCapabilitiesCoderFamilies(t *testing.T) {
	cases := []struct {
		id   string
		want []string
	}{
		{"qwen2.5-coder:7b", []string{CapCoding, CapToolCalling}},
		{"qwen/qwen-2.5-coder-32b", []string{CapCoding, CapToolCalling}},
		{"mistralai/codestral-22b", []string{CapCoding, CapToolCalling}},
		{"google/codegemma-7b", []string{CapCoding, CapToolCalling}},
		{"deepseek/deepseek-coder-v2", []string{CapCoding, CapToolCalling}},
		{"Qwen3-Coder-30B-A3B-Instruct-GGUF", []string{CapCoding, CapToolCalling}},
	}
	for _, tc := range cases {
		got := InferCapabilities(tc.id)
		sort.Strings(got)
		sort.Strings(tc.want)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.id, got, tc.want)
		}
	}
}

func TestInferCapabilitiesEmbeddingAndRerank(t *testing.T) {
	cases := []struct {
		id   string
		want []string
	}{
		{"Qwen3-Embedding-0.6B-GGUF", []string{CapEmbedding}},
		{"nomic-embed-text", []string{CapEmbedding}},
		{"text-embedding-3-large", []string{CapEmbedding}},
		{"bge-reranker-v2-m3-GGUF", []string{CapReranking}},
		{"cohere/rerank-english-v3.0", []string{CapReranking}},
	}
	for _, tc := range cases {
		got := InferCapabilities(tc.id)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.id, got, tc.want)
		}
	}
}

func TestInferCapabilitiesEmbedderDoesNotPickUpFamilyCoding(t *testing.T) {
	// Qwen3-Embedding shouldn't get the Qwen3 reasoning/tool-calling tags
	// — embedders are pure dense retrieval, never tool callers.
	got := InferCapabilities("Qwen3-Embedding-0.6B-GGUF")
	for _, label := range got {
		if label == CapReasoning || label == CapToolCalling || label == CapCoding {
			t.Errorf("embedder picked up non-embedder cap: %v", got)
		}
	}
}

func TestInferCapabilitiesReasoningFamilies(t *testing.T) {
	cases := []struct {
		id   string
		want []string
	}{
		{"anthropic/claude-haiku-4.5", []string{CapReasoning, CapToolCalling}},
		{"claude-3-5-sonnet", []string{CapReasoning, CapToolCalling}},
		{"openai/gpt-4o", []string{CapReasoning, CapToolCalling}},
		{"openai/o3-mini", []string{CapReasoning, CapToolCalling}},
		{"Qwen3-14B-GGUF", []string{CapReasoning, CapToolCalling}},
	}
	for _, tc := range cases {
		got := InferCapabilities(tc.id)
		sort.Strings(got)
		sort.Strings(tc.want)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.id, got, tc.want)
		}
	}
}

func TestInferCapabilitiesToolCallingOnlyFamilies(t *testing.T) {
	cases := []struct {
		id   string
		want []string
	}{
		{"llama-3.1-70b-instruct", []string{CapToolCalling}},
		{"meta-llama/llama3-8b", []string{CapToolCalling}},
		{"mistral-7b-instruct", []string{CapToolCalling}},
		{"mixtral-8x7b", []string{CapToolCalling}},
	}
	for _, tc := range cases {
		got := InferCapabilities(tc.id)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.id, got, tc.want)
		}
	}
}

func TestInferCapabilitiesVisionTag(t *testing.T) {
	cases := []struct {
		id           string
		expectVision bool
	}{
		{"llava-1.5", true},
		{"qwen2-vl-72b", true},
		{"gpt-4o-vision", true},
		{"qwen3-14b", false},
	}
	for _, tc := range cases {
		got := InferCapabilities(tc.id)
		hasVision := false
		for _, l := range got {
			if l == CapVision {
				hasVision = true
				break
			}
		}
		if hasVision != tc.expectVision {
			t.Errorf("%s: vision=%v want %v (got %v)", tc.id, hasVision, tc.expectVision, got)
		}
	}
}

// TestInferContextClass pins the Phase-3 Slice 1 capability bucketing.
// Frontier hosted families dominate over size tags; explicit size tags
// dominate over ctxWindow hints; ctxWindow is the secondary signal;
// unknown defaults to medium.
func TestInferContextClass(t *testing.T) {
	tests := []struct {
		id        string
		ctxWindow int
		want      ContextClass
	}{
		// Frontier hosted families → Large regardless of suffix.
		{"anthropic/claude-haiku-4.5", 0, ContextLarge},
		{"claude-sonnet-4.5", 0, ContextLarge},
		{"openai/gpt-5-mini", 0, ContextLarge},
		{"o4-mini", 0, ContextLarge},
		// Parameter-count tag dominates ctxWindow hint.
		{"qwen2.5-coder:1.5b", 262144, ContextSmall},
		{"qwen2.5-coder:7b", 262144, ContextSmall},
		{"qwen3-coder-30b-a3b", 0, ContextLarge},
		{"llama3.1:13b", 0, ContextMedium},
		{"llama3.1:70b", 0, ContextLarge},
		// ctxWindow signal when no size tag.
		{"unnamed-experimental-model", 32768, ContextLarge},
		{"unnamed-experimental-model", 16384, ContextMedium},
		{"tiny-3000-token-thing", 3000, ContextSmall},
		// Fallback — neither signal → Medium.
		{"unknown-model", 0, ContextMedium},
	}
	for _, tc := range tests {
		got := InferContextClass(tc.id, tc.ctxWindow)
		if got != tc.want {
			t.Errorf("%s (ctx=%d): got %s, want %s", tc.id, tc.ctxWindow, got, tc.want)
		}
	}
}

// TestSalienceCapForClass pins the default caps the REPL uses to size
// per-tool-call output budgets. Small → tight (favoring fan-out);
// large → loose (one big synthesis chunk fits).
func TestSalienceCapForClass(t *testing.T) {
	if SalienceCapForClass(ContextSmall) >= SalienceCapForClass(ContextMedium) {
		t.Error("small cap should be tighter than medium")
	}
	if SalienceCapForClass(ContextLarge) <= SalienceCapForClass(ContextMedium) {
		t.Error("large cap should be looser than medium")
	}
}

func TestInferCapabilitiesUnknownReturnsEmpty(t *testing.T) {
	got := InferCapabilities("some-random-experimental-model-xyz")
	if len(got) != 0 {
		t.Errorf("unknown model should return no tags, got %v", got)
	}
}

func TestEffectiveLabelsPrefersEndpointLabels(t *testing.T) {
	// Endpoint-supplied labels override inference, even if inference
	// would have produced different/more tags.
	m := CompatModel{
		ID:     "qwen2.5-coder:7b",
		Labels: []string{"custom-only"},
	}
	got := EffectiveLabels(m)
	if !reflect.DeepEqual(got, []string{"custom-only"}) {
		t.Errorf("endpoint labels should win, got %v", got)
	}
}

func TestEffectiveLabelsFallsBackToInference(t *testing.T) {
	// No endpoint labels — uses InferCapabilities.
	m := CompatModel{ID: "qwen2.5-coder:7b"}
	got := EffectiveLabels(m)
	sort.Strings(got)
	want := []string{CapCoding, CapToolCalling}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inference fallback: got %v want %v", got, want)
	}
}

func TestHasCapability(t *testing.T) {
	m := CompatModel{ID: "qwen3-14b"}
	if !HasCapability(m, CapToolCalling) {
		t.Errorf("expected tool-calling for qwen3-14b")
	}
	if HasCapability(m, CapEmbedding) {
		t.Errorf("did not expect embedding for qwen3-14b")
	}
}
