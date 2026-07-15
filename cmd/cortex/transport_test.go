package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
