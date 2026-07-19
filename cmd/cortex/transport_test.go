package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockingServer returns a test server that replies with the given raw JSON
// body to any request (the blocking chat-completions shape).
func blockingServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestChoiceReasoningParsedFromBlockingResponse covers item 1(c)'s blocking-
// path parity: reasoning_content/reasoning and a leading <think> fence are
// both pulled off the wire into Choice.Reasoning, mirroring what the
// streaming path already does per-delta.
func TestChoiceReasoningParsedFromBlockingResponse(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantContent string
		wantReason  string
	}{
		{
			name:        "reasoning_content field, no fence",
			body:        `{"choices":[{"message":{"role":"assistant","content":"Answer.","reasoning_content":"Let me think."}}]}`,
			wantContent: "Answer.",
			wantReason:  "Let me think.",
		},
		{
			name:        "reasoning alias field (OpenRouter)",
			body:        `{"choices":[{"message":{"role":"assistant","content":"Answer.","reasoning":"Let me think."}}]}`,
			wantContent: "Answer.",
			wantReason:  "Let me think.",
		},
		{
			name:        "leading think fence in content, no reasoning_content field",
			body:        `{"choices":[{"message":{"role":"assistant","content":"<think>chain of thought</think>Answer."}}]}`,
			wantContent: "Answer.",
			wantReason:  "chain of thought",
		},
		{
			name:        "unclosed fence (max-tokens clamp): empty content, all reasoning",
			body:        `{"choices":[{"message":{"role":"assistant","content":"<think>never closes"}}]}`,
			wantContent: "",
			wantReason:  "never closes",
		},
		{
			name:        "no fence, no reasoning field: passthrough",
			body:        `{"choices":[{"message":{"role":"assistant","content":"Plain answer."}}]}`,
			wantContent: "Plain answer.",
			wantReason:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := blockingServer(t, tt.body)
			defer srv.Close()

			res, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if len(res.Choices) != 1 {
				t.Fatalf("got %d choices, want 1", len(res.Choices))
			}
			ch := res.Choices[0]
			if ch.Message.Content != tt.wantContent {
				t.Errorf("Message.Content = %q, want %q", ch.Message.Content, tt.wantContent)
			}
			if ch.Reasoning != tt.wantReason {
				t.Errorf("Choice.Reasoning = %q, want %q", ch.Reasoning, tt.wantReason)
			}
		})
	}
}

// TestUsageReasoningTokens covers item 2's blocking-path parity: a reasoning
// model's completion_tokens_details.reasoning_tokens usage sub-field,
// mirroring the existing prompt_tokens_details.cached_tokens handling.
func TestUsageReasoningTokens(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantReasoning int
	}{
		{
			name:          "reasoning_tokens reported",
			body:          `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":37}}}`,
			wantReasoning: 37,
		},
		{
			name:          "completion_tokens_details absent: zero",
			body:          `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":50}}`,
			wantReasoning: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := blockingServer(t, tt.body)
			defer srv.Close()

			res, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if got := res.Usage.ReasoningTokens(); got != tt.wantReasoning {
				t.Errorf("Usage.ReasoningTokens() = %d, want %d", got, tt.wantReasoning)
			}
		})
	}
}

// TestSendStreamReasoningTokenUsage proves the streaming path's assembled
// AgentResponse carries the reasoning-token usage split through
// assembleStreamResponse.
func TestSendStreamReasoningTokenUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":37}}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	srv := httptest.NewServer(sseHandler(body))
	defer srv.Close()

	req := &AgentRequest{Model: "m", BaseURL: srv.URL}
	res, err := req.SendStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if got := res.Usage.ReasoningTokens(); got != 37 {
		t.Errorf("Usage.ReasoningTokens() = %d, want 37", got)
	}
}

// TestChoiceReasoningNeverRoundTrips is the design-constraint test: whatever
// reasoning the blocking path parses off the wire must never come back out
// when the stored Message is marshaled again — not as fence bytes left in
// Content, not as a reasoning field, since Message carries none.
func TestChoiceReasoningNeverRoundTrips(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"<think>secret chain of thought</think>Answer.","reasoning_content":"also secret"}}]}`
	srv := blockingServer(t, body)
	defer srv.Close()

	res, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	msg := res.Choices[0].Message

	out, err := json.Marshal(&msg)
	if err != nil {
		t.Fatalf("marshal Message: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "secret") {
		t.Errorf("marshaled Message leaked reasoning bytes: %s", got)
	}
	if strings.Contains(got, "<think>") || strings.Contains(got, "</think>") {
		t.Errorf("marshaled Message leaked a think fence: %s", got)
	}
	if strings.Contains(got, "reasoning") {
		t.Errorf("marshaled Message carries a reasoning field: %s", got)
	}
	if msg.Content != "Answer." {
		t.Errorf("Message.Content = %q, want %q (fence stripped)", msg.Content, "Answer.")
	}
}

// quickRetries shrinks the retry backoff for the duration of a test.
func quickRetries(t *testing.T) {
	t.Helper()
	saved := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = saved })
}

const okResponse = `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1}}`

func TestSendRetriesTransientErrors(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(okResponse))
	}))
	defer srv.Close()

	req := &AgentRequest{Model: "m", BaseURL: srv.URL}
	res, err := req.Send(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("server saw %d calls, want 3 (two 503s then success)", calls)
	}
	if res.Choices[0].Message.Content != "ok" {
		t.Errorf("unexpected response content %q", res.Choices[0].Message.Content)
	}
}

// TestSendPerturbsTemperatureOnRetry locks the peg-500 escape: the first attempt
// goes at temperature 0 (deterministic); a retry after a 5xx bumps the temperature
// so the model can escape a deterministic generation the proxy can't parse.
func TestSendPerturbsTemperatureOnRetry(t *testing.T) {
	quickRetries(t)
	var temps []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Temperature float64 `json:"temperature"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		temps = append(temps, body.Temperature)
		if len(temps) < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(okResponse))
	}))
	defer srv.Close()

	if _, err := (&AgentRequest{Model: "m", BaseURL: srv.URL, Temperature: defaultTemperature}).Send(context.Background()); err != nil {
		t.Fatalf("expected success on the perturbed retry, got %v", err)
	}
	if len(temps) < 2 {
		t.Fatalf("want at least 2 attempts, got %d", len(temps))
	}
	if temps[0] != defaultTemperature {
		t.Errorf("first attempt temp = %v, want default %v", temps[0], defaultTemperature)
	}
	if temps[1] <= temps[0] {
		t.Errorf("retry temp = %v, want > first attempt %v (perturbed to escape the 500)", temps[1], temps[0])
	}
}

func TestSendGivesUpAfterMaxAttempts(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != maxSendAttempts {
		t.Errorf("server saw %d calls, want %d", calls, maxSendAttempts)
	}
}

// A 4xx means the request itself is wrong (e.g. context overflow) — retrying
// can't fix it and would just burn time, so exactly one attempt is made.
func TestSendDoesNotRetryClientErrors(t *testing.T) {
	quickRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("context size (32768 tokens)"))
	}))
	defer srv.Close()

	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(context.Background())
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1 (no retry on 4xx)", calls)
	}
	// The error must preserve the provider's message — study's window
	// self-calibration parses it.
	if !strings.Contains(err.Error(), "context size (32768 tokens)") {
		t.Errorf("error should carry the response body, got %q", err)
	}
}

func TestSendHonorsContextCancel(t *testing.T) {
	quickRetries(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // hold the request open until the test ends
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).Send(ctx)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Send took %v after cancel; should return promptly", elapsed)
	}
}

// TestSendStreamHonorsContextCancel proves that SendStream respects context
// cancellation during an in-flight SSE stream. This is the streaming path
// that was missing cancellation support.
func TestSendStreamHonorsContextCancel(t *testing.T) {
	quickRetries(t)
	// Track when client disconnects
	clientDisconnected := make(chan struct{}, 1)
	// Server that sends one SSE chunk then waits to see if client disconnects
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
		w.(http.Flusher).Flush()
		// Wait to see if client disconnects
		select {
		case <-clientDisconnected:
			t.Logf("Server: client disconnected")
		case <-time.After(2 * time.Second):
			t.Logf("Server: no disconnect within 2s")
		}
	}))
	defer func() {
		select {
		case clientDisconnected <- struct{}{}:
		default:
		}
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	t.Logf("Starting SendStream with %v timeout", 50*time.Millisecond)
	_, err := (&AgentRequest{Model: "m", BaseURL: srv.URL}).SendStream(ctx, nil, nil)
	elapsed := time.Since(start)
	t.Logf("SendStream returned after %v with err=%v", elapsed, err)

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("SendStream took %v after cancel; should return promptly", elapsed)
	}
}
