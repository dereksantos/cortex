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

// chat_template_kwargs rides on the wire only when configured — the
// default request must stay universally OpenAI-compatible (some hosted
// endpoints reject unknown fields).
func TestOpenAICompatChatTemplateKwargs(t *testing.T) {
	tests := []struct {
		name   string
		kwargs map[string]any
	}{
		{"sent when configured", map[string]any{"enable_thinking": false}},
		{"omitted when nil", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				got, present := raw["chat_template_kwargs"]
				if tt.kwargs == nil {
					if present {
						t.Errorf("chat_template_kwargs should be omitted, got %s", got)
					}
				} else if !present || !strings.Contains(string(got), `"enable_thinking":false`) {
					t.Errorf("chat_template_kwargs missing or wrong: present=%v body=%s", present, got)
				}
				_ = json.NewEncoder(w).Encode(compatResponse{
					Choices: []compatChoice{{Message: compatMessage{Role: "assistant", Content: "ok"}}},
				})
			}))
			defer srv.Close()

			c := NewOpenAICompatClient(EndpointConfig{Name: "test", BaseURL: srv.URL + "/v1", ChatTemplateKwargs: tt.kwargs})
			c.SetModel("m")

			if _, err := c.Generate(context.Background(), "hello"); err != nil {
				t.Fatalf("generate: %v", err)
			}
			if _, _, err := c.GenerateWithTools(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, nil, ""); err != nil {
				t.Fatalf("generate with tools: %v", err)
			}
		})
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

// TestOpenAICompatNestedLemonadeError pins the unwrap path that
// surfaces lemonade-server's actual llama-server message instead of the
// useless "llama-server request failed" wrapper. Lemonade returns the
// real reason nested at error.details.response.error.message, with the
// outer HTTP status set to 200 even though the inner code is 400.
func TestOpenAICompatNestedLemonadeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"llama-server request failed","type":"backend_error","details":{"status_code":400,"response":{"error":{"code":400,"message":"request (4921 tokens) exceeds the available context size (4096 tokens), try increasing it","type":"exceed_context_size_error"}}}}}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(EndpointConfig{Name: "chatterbox", BaseURL: srv.URL + "/v1"})
	c.SetModel("m")
	_, err := c.Generate(context.Background(), "p")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "llama-server request failed") {
		t.Errorf("expected wrapper message preserved, got %v", err)
	}
	if !strings.Contains(err.Error(), "exceeds the available context size") {
		t.Errorf("expected nested cause surfaced, got %v", err)
	}
}

func TestOpenAICompatBaseURLTrailingSlashTrimmed(t *testing.T) {
	c := NewOpenAICompatClient(EndpointConfig{Name: "t", BaseURL: "http://example.invalid/v1/"})
	if c.BaseURL() != "http://example.invalid/v1" {
		t.Errorf("trailing slash not trimmed: %q", c.BaseURL())
	}
}
