package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatEmbedder_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "embedder" {
			t.Errorf("model = %q, want embedder", req.Model)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
			},
			"model": "embedder",
		})
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(EndpointConfig{Name: "t", BaseURL: srv.URL}, "embedder")
	if !e.IsEmbeddingAvailable() {
		t.Fatal("IsEmbeddingAvailable = false, want true")
	}
	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Errorf("vec = %v, want [0.1 0.2 0.3]", vec)
	}
}

func TestOpenAICompatEmbedder_BatchOrdering(t *testing.T) {
	// Return data out of index order to prove we re-sort by Index.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{2}, "index": 1},
				{"embedding": []float32{0}, "index": 0},
			},
		})
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(EndpointConfig{BaseURL: srv.URL}, "embedder")
	vecs, err := e.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0 || vecs[1][0] != 2 {
		t.Errorf("ordering wrong: %v", vecs)
	}
}

func TestOpenAICompatEmbedder_CountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1}, "index": 0}},
		})
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(EndpointConfig{BaseURL: srv.URL}, "embedder")
	if _, err := e.EmbedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("expected error on count mismatch, got nil")
	}
}

func TestOpenAICompatEmbedder_NotConfigured(t *testing.T) {
	e := NewOpenAICompatEmbedder(EndpointConfig{}, "embedder")
	if e.IsEmbeddingAvailable() {
		t.Error("IsEmbeddingAvailable = true with empty base URL, want false")
	}
}
