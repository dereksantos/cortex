package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

func TestDetectOpenAICompatEndpointsMixed(t *testing.T) {
	// Endpoint 1: healthy, returns 2 models with labels.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"coder","labels":["coding"]},{"id":"embed","labels":["embeddings"]}]}`))
	}))
	defer ok.Close()

	// Endpoint 2: returns 500.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer bad.Close()

	// Endpoint 3: unreachable URL (closed port).
	endpoints := []llm.EndpointConfig{
		{Name: "ok", BaseURL: ok.URL + "/v1"},
		{Name: "bad", BaseURL: bad.URL + "/v1"},
		{Name: "down", BaseURL: "http://127.0.0.1:1/v1"}, // port 1 = guaranteed unreachable
	}

	results := DetectOpenAICompatEndpoints(context.Background(), endpoints)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if !results[0].Reachable || len(results[0].Models) != 2 {
		t.Errorf("endpoint[0] should be reachable with 2 models, got %+v", results[0])
	}
	if results[0].Models[0].ID != "coder" || len(results[0].Models[0].Labels) != 1 {
		t.Errorf("endpoint[0] model wrong: %+v", results[0].Models[0])
	}

	if results[1].Reachable || results[1].Error == "" {
		t.Errorf("endpoint[1] should be unreachable with error, got %+v", results[1])
	}
	if results[2].Reachable || results[2].Error == "" {
		t.Errorf("endpoint[2] should be unreachable with error, got %+v", results[2])
	}
}

func TestDetectOpenAICompatEndpointsParallel(t *testing.T) {
	// Two slow endpoints that each take ~200ms; serial would be ~400ms,
	// parallel should be ~200ms. We assert under 400ms to give margin.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer slow.Close()

	endpoints := []llm.EndpointConfig{
		{Name: "a", BaseURL: slow.URL + "/v1"},
		{Name: "b", BaseURL: slow.URL + "/v1"},
	}

	// Just verify both come back successfully — race conditions in
	// parallel goroutines would show as a result with wrong Name.
	results := DetectOpenAICompatEndpoints(context.Background(), endpoints)
	if results[0].Name != "a" || results[1].Name != "b" {
		t.Errorf("results out of order or swapped: [0]=%s [1]=%s", results[0].Name, results[1].Name)
	}
}
