package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestResolveEmbedder_CloudConfig proves the free-cloud-backup path: pointing
// models.embed at an OpenAI-compatible /embeddings endpoint (e.g. Cloudflare
// Workers AI bge-large) resolves to a working embedder with no new code — the
// same OpenAICompatEmbedder the fleet uses, just a different base URL + key.
func TestResolveEmbedder_CloudConfig(t *testing.T) {
	var gotPath, gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2}, "index": 0}},
		})
	}))
	defer srv.Close()

	t.Setenv("FAKE_CLOUD_TOKEN", "tok-123")
	cs := &CortexSession{
		Config: &Config{
			Models: map[string]ModelSpec{
				roleEmbed: {Endpoint: srv.URL, Model: "@cf/baai/bge-large-en-v1.5", KeyEnv: "FAKE_CLOUD_TOKEN"},
			},
		},
	}

	e := cs.resolveEmbedder()
	if e == nil {
		t.Fatal("resolveEmbedder returned nil for a configured cloud embed role")
	}
	if _, ok := e.(*llm.OpenAICompatEmbedder); !ok {
		t.Fatalf("expected *OpenAICompatEmbedder, got %T", e)
	}
	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 2 {
		t.Errorf("vec len = %d, want 2", len(vec))
	}
	if gotPath != "/v1/embeddings" {
		t.Errorf("path = %q, want /v1/embeddings", gotPath)
	}
	if gotModel != "@cf/baai/bge-large-en-v1.5" {
		t.Errorf("model = %q", gotModel)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth = %q, want Bearer tok-123 (key_env not resolved)", gotAuth)
	}
}

// TestResolveEmbedder_LocalDefaultDisabled confirms that with no fleet/config
// embedder and the local embedder opted out, resolveEmbedder yields nil (Reflex
// then uses text search) rather than downloading a model.
func TestResolveEmbedder_LocalDefaultDisabled(t *testing.T) {
	t.Setenv("CORTEX_LOCAL_EMBED", "0")
	cs := &CortexSession{Config: &Config{}}
	if e := cs.resolveEmbedder(); e != nil {
		t.Fatalf("expected nil embedder when local disabled and no fleet, got %T", e)
	}
}
