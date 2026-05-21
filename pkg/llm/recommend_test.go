package llm

import "testing"

func TestRecommendPicksLocalCoderForCode(t *testing.T) {
	cats := []EndpointCatalog{
		{
			Name: "chatterbox", IsLocal: true,
			Models: []CompatModel{
				{ID: "Qwen3-Coder-30B-A3B-Instruct-GGUF", Labels: []string{"coding", "tool-calling"}, ContextLength: 262144},
				{ID: "Qwen3-14B-GGUF", Labels: []string{"reasoning"}, ContextLength: 40960},
			},
		},
		{
			Name: "openrouter", IsLocal: false,
			Models: []CompatModel{
				{ID: "anthropic/claude-haiku-4.5"},
			},
		},
	}
	rec := Recommend(cats)
	code, ok := rec.Choices[RoleCode]
	if !ok || code.Endpoint != "chatterbox" {
		t.Fatalf("RoleCode should pick local chatterbox coder, got %+v", code)
	}
	if code.Model != "Qwen3-Coder-30B-A3B-Instruct-GGUF" {
		t.Errorf("RoleCode model: got %q", code.Model)
	}
}

func TestRecommendFallsBackToCloudWhenNoLocalCapability(t *testing.T) {
	// Local has only an embedder; reasoning must fall back to cloud.
	cats := []EndpointCatalog{
		{
			Name: "chatterbox", IsLocal: true,
			Models: []CompatModel{
				{ID: "Qwen3-Embedding-0.6B-GGUF", Labels: []string{"embeddings"}},
			},
		},
		{
			Name: "openrouter", IsLocal: false,
			Models: []CompatModel{
				{ID: "anthropic/claude-haiku-4.5"},
			},
		},
	}
	rec := Recommend(cats)
	reason, ok := rec.Choices[RoleReason]
	if !ok || reason.Endpoint != "openrouter" {
		t.Errorf("RoleReason should fall back to openrouter, got %+v", reason)
	}
}

func TestRecommendNoCandidateLeavesRoleUnassigned(t *testing.T) {
	cats := []EndpointCatalog{
		{
			Name: "embedder-only", IsLocal: true,
			Models: []CompatModel{
				{ID: "Qwen3-Embedding-0.6B-GGUF", Labels: []string{"embeddings"}},
			},
		},
	}
	rec := Recommend(cats)
	if _, ok := rec.Choices[RoleCode]; ok {
		t.Errorf("RoleCode should be unassigned when no coding model exists")
	}
	if _, ok := rec.Choices[RoleEmbed]; !ok {
		t.Errorf("RoleEmbed should be assigned")
	}
}

func TestRecommendFastPrefersSmaller(t *testing.T) {
	cats := []EndpointCatalog{
		{
			Name: "chatterbox", IsLocal: true,
			Models: []CompatModel{
				{ID: "Qwen3-Coder-30B", Labels: []string{"coding", "tool-calling"}},
				{ID: "qwen3-8b-FLM", Labels: []string{"reasoning", "tool-calling"}},
				{ID: "qwen3-1.5b", Labels: []string{"reasoning", "tool-calling"}},
			},
		},
	}
	rec := Recommend(cats)
	fast, ok := rec.Choices[RoleFast]
	if !ok {
		t.Fatal("RoleFast should be picked")
	}
	if fast.Model != "qwen3-1.5b" {
		t.Errorf("Fast should pick smallest tool-caller, got %q", fast.Model)
	}
}

func TestRecommendCodePrefersLarger(t *testing.T) {
	cats := []EndpointCatalog{
		{
			Name: "ep", IsLocal: true,
			Models: []CompatModel{
				{ID: "qwen2.5-coder:1.5b"},
				{ID: "qwen2.5-coder:32b"},
				{ID: "qwen2.5-coder:7b"},
			},
		},
	}
	rec := Recommend(cats)
	if rec.Choices[RoleCode].Model != "qwen2.5-coder:32b" {
		t.Errorf("Code should pick largest coder, got %q", rec.Choices[RoleCode].Model)
	}
}

func TestRecommendEmbedAndRerankAreIsolated(t *testing.T) {
	cats := []EndpointCatalog{
		{
			Name: "chatterbox", IsLocal: true,
			Models: []CompatModel{
				{ID: "Qwen3-Embedding-0.6B-GGUF", Labels: []string{"embeddings"}},
				{ID: "bge-reranker-v2-m3-GGUF", Labels: []string{"reranking"}},
				{ID: "Qwen3-Coder-30B", Labels: []string{"coding", "tool-calling"}},
			},
		},
	}
	rec := Recommend(cats)
	if rec.Choices[RoleEmbed].Model != "Qwen3-Embedding-0.6B-GGUF" {
		t.Errorf("Embed: %+v", rec.Choices[RoleEmbed])
	}
	if rec.Choices[RoleRerank].Model != "bge-reranker-v2-m3-GGUF" {
		t.Errorf("Rerank: %+v", rec.Choices[RoleRerank])
	}
}

func TestParseParamCount(t *testing.T) {
	cases := []struct {
		id   string
		want int
	}{
		{"qwen2.5-coder:7b", 7},
		{"Qwen3-Coder-30B-A3B-Instruct-GGUF", 30},
		{"qwen3-1.5b", 1},
		{"llama-3.1-70b-instruct", 70},
		{"random-model-no-size", 0},
		{"some-7brand-fake", 0}, // 7b inside a word — should reject
	}
	for _, tc := range cases {
		if got := parseParamCount(tc.id); got != tc.want {
			t.Errorf("parseParamCount(%q) = %d want %d", tc.id, got, tc.want)
		}
	}
}
