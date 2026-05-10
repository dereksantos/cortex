package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// happyResponse is what OpenRouter returned to our 2026-05-10 probe on
// gpt-oss-20b:free, trimmed to the fields we depend on.
const happyResponse = `{
  "id": "gen-test-1",
  "model": "openai/gpt-oss-20b:free",
  "provider": "OpenInference",
  "choices": [
    {
      "message": {"role": "assistant", "content": "ok"},
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 5,
    "total_tokens": 17,
    "cost": 0.0001234
  }
}`

func newTestClient(t *testing.T, serverURL string) *OpenRouterClient {
	t.Helper()
	t.Setenv("OPEN_ROUTER_API_KEY", "sk-or-fake-test")
	c := NewOpenRouterClient(nil)
	c.SetAPIURL(serverURL)
	return c
}

func TestOpenRouterIdentity(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "sk-or-fake")
	c := NewOpenRouterClient(nil)
	if got, want := c.Name(), "openrouter"; got != want {
		t.Errorf("Name=%q, want %q", got, want)
	}
	if !c.IsAvailable() {
		t.Error("IsAvailable should be true when key is set")
	}
}

func TestOpenRouterIsAvailableNoKey(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "")
	c := NewOpenRouterClient(nil)
	if c.IsAvailable() {
		t.Error("IsAvailable should be false when key is unset")
	}
}

func TestOpenRouterModelDefaultAndOverride(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "sk-or-fake")
	t.Setenv("OPEN_ROUTER_MODEL", "")
	c := NewOpenRouterClient(nil)
	if c.Model() != openrouterDefaultModel {
		t.Errorf("default model = %q, want %q", c.Model(), openrouterDefaultModel)
	}
	c.SetModel("anthropic/claude-haiku-4.5")
	if c.Model() != "anthropic/claude-haiku-4.5" {
		t.Errorf("after SetModel: %q", c.Model())
	}

	t.Setenv("OPEN_ROUTER_MODEL", "qwen/qwen3-coder")
	c2 := NewOpenRouterClient(nil)
	if c2.Model() != "qwen/qwen3-coder" {
		t.Errorf("env-driven model = %q, want qwen/qwen3-coder", c2.Model())
	}
}

func TestOpenRouterGenerateHappyPath(t *testing.T) {
	var (
		gotAuth    string
		gotReferer string
		gotTitle   string
		gotBody    orRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("server: bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(happyResponse))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	c.SetModel("openai/gpt-oss-20b:free")

	out, stats, err := c.GenerateWithStats(context.Background(), "say ok")
	if err != nil {
		t.Fatalf("GenerateWithStats: %v", err)
	}

	// Output + stats
	if out != "ok" {
		t.Errorf("out=%q want %q", out, "ok")
	}
	if stats.InputTokens != 12 || stats.OutputTokens != 5 {
		t.Errorf("stats=%+v want input=12 output=5", stats)
	}

	// Cost + provider exposed via accessor
	if c.LastCostUSD() != 0.0001234 {
		t.Errorf("LastCostUSD=%v want 0.0001234", c.LastCostUSD())
	}
	if c.LastProvider() != "OpenInference" {
		t.Errorf("LastProvider=%q want OpenInference", c.LastProvider())
	}

	// Auth + attribution headers
	if gotAuth != "Bearer sk-or-fake-test" {
		t.Errorf("Authorization=%q", gotAuth)
	}
	if gotReferer != openrouterReferer {
		t.Errorf("HTTP-Referer=%q want %q", gotReferer, openrouterReferer)
	}
	if gotTitle != openrouterTitle {
		t.Errorf("X-Title=%q want %q", gotTitle, openrouterTitle)
	}

	// Request body shape — model passed verbatim, usage.include must be true
	if gotBody.Model != "openai/gpt-oss-20b:free" {
		t.Errorf("request.model=%q", gotBody.Model)
	}
	if !gotBody.Usage.Include {
		t.Error("request.usage.include must be true to surface cost in response")
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" {
		t.Errorf("messages=%+v", gotBody.Messages)
	}
}

func TestOpenRouterGenerateWithSystem(t *testing.T) {
	var gotBody orRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)

	out, err := c.GenerateWithSystem(context.Background(), "user msg", "be terse")
	if err != nil {
		t.Fatalf("GenerateWithSystem: %v", err)
	}
	if out != "hi" {
		t.Errorf("out=%q", out)
	}
	if len(gotBody.Messages) != 2 {
		t.Fatalf("want 2 messages (system+user), got %d: %+v", len(gotBody.Messages), gotBody.Messages)
	}
	if gotBody.Messages[0].Role != "system" || gotBody.Messages[0].Content != "be terse" {
		t.Errorf("system message: %+v", gotBody.Messages[0])
	}
	if gotBody.Messages[1].Role != "user" || gotBody.Messages[1].Content != "user msg" {
		t.Errorf("user message: %+v", gotBody.Messages[1])
	}
}

func TestOpenRouterErrorResponseSurfacesProviderMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{
            "error": {
                "code": 429,
                "message": "Provider returned error",
                "metadata": {
                    "raw": "qwen3-coder:free is rate-limited",
                    "provider_name": "Venice",
                    "retry_after_seconds": 22
                }
            }
        }`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)

	_, _, err := c.GenerateWithStats(context.Background(), "x")
	if err == nil {
		t.Fatal("want error from 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should include status: %v", err)
	}
	if !strings.Contains(err.Error(), "Provider returned error") {
		t.Errorf("error should include OpenRouter message: %v", err)
	}
}

func TestOpenRouterMissingKey(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "")
	c := NewOpenRouterClient(nil)

	_, _, err := c.GenerateWithStats(context.Background(), "x")
	if err == nil {
		t.Fatal("want error when key is unset, got nil")
	}
	if !strings.Contains(err.Error(), "OPEN_ROUTER_API_KEY") {
		t.Errorf("error should mention env var: %v", err)
	}
}

func TestOpenRouterEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"usage":{}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)

	_, _, err := c.GenerateWithStats(context.Background(), "x")
	if err == nil {
		t.Fatal("want error when no choices, got nil")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error should mention empty choices: %v", err)
	}
}

func TestOpenRouterMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)

	_, _, err := c.GenerateWithStats(context.Background(), "x")
	if err == nil {
		t.Fatal("want decode error, got nil")
	}
}

// Build-time interface satisfaction check: OpenRouterClient implements Provider.
var _ Provider = (*OpenRouterClient)(nil)
