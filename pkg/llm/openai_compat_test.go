package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("authorization header should be empty when no apiKey is set, got %q", r.Header.Get("Authorization"))
		}
		var body compatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "qwen-coder-test" {
			t.Errorf("model: got %q want qwen-coder-test", body.Model)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" {
			t.Errorf("expected system+user messages, got %+v", body.Messages)
		}
		_ = json.NewEncoder(w).Encode(compatResponse{
			Choices: []compatChoice{{Message: compatMessage{Role: "assistant", Content: "hi"}}},
			Usage:   compatUsage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1"})
	c.SetModel("qwen-coder-test")

	out, err := c.GenerateWithSystem(context.Background(), "hello", "you are helpful")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out != "hi" {
		t.Errorf("output: got %q want hi", out)
	}
}

func TestOpenAICompatStatsRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(compatResponse{
			Choices: []compatChoice{{Message: compatMessage{Content: "ok"}}},
			Usage:   compatUsage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1"})
	c.SetModel("m")
	_, stats, err := c.GenerateWithStats(context.Background(), "hi")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if stats.InputTokens != 5 || stats.OutputTokens != 1 {
		t.Errorf("stats: got %+v want {5 1}", stats)
	}
}

func TestOpenAICompatRequiresModel(t *testing.T) {
	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: "http://example.invalid/v1"})
	_, err := c.Generate(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "model not set") {
		t.Errorf("expected 'model not set' error, got %v", err)
	}
}

func TestOpenAICompatAuthHeaderWhenKeySet(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(compatResponse{
			Choices: []compatChoice{{Message: compatMessage{Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1", APIKey: "secret"})
	c.SetModel("m")
	if _, err := c.Generate(context.Background(), "p"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got != "Bearer secret" {
		t.Errorf("auth header: got %q want 'Bearer secret'", got)
	}
}

func TestOpenAICompatListModelsWithLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"coder","labels":["coding","tool-calling"],"max_context_window":262144},
				{"id":"embedder","labels":["embeddings"],"max_context_window":32768},
				{"id":"plain","max_context_window":4096}
			]
		}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1"})
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if models[0].ID != "coder" || len(models[0].Labels) != 2 || models[0].ContextLength != 262144 {
		t.Errorf("coder entry wrong: %+v", models[0])
	}
	if models[2].ID != "plain" || len(models[2].Labels) != 0 {
		t.Errorf("plain entry should have no labels: %+v", models[2])
	}
}

func TestOpenAICompatNonOKBodySurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(compatResponse{Error: &compatErr{Message: "model not found"}})
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1"})
	c.SetModel("m")
	_, err := c.Generate(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("expected error to surface server message, got %v", err)
	}
}

func TestOpenAICompatBaseURLTrailingSlashTrimmed(t *testing.T) {
	c := NewOpenAICompatClient(EndpointConfig{Name: "t", BaseURL: "http://example.invalid/v1/"})
	if c.BaseURL() != "http://example.invalid/v1" {
		t.Errorf("trailing slash not trimmed: %q", c.BaseURL())
	}
}
