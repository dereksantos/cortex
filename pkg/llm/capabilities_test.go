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
