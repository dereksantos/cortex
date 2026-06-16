package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer returns a test server that writes the given raw SSE body with a 200
// status and the streaming content type.
func sseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestStreamChat_ContentAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":", "}}]}`,
		`data: {"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := sseServer(t, http.StatusOK, body)
	defer srv.Close()

	var deltas []string
	res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`), func(s string) {
		deltas = append(deltas, s)
	}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if res.Content != "Hello, world" {
		t.Errorf("content = %q, want %q", res.Content, "Hello, world")
	}
	if got := strings.Join(deltas, "|"); got != "Hello|, |world" {
		t.Errorf("deltas = %q, want %q", got, "Hello|, |world")
	}
	if res.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", res.FinishReason)
	}
	if res.Stats.InputTokens != 12 || res.Stats.OutputTokens != 3 {
		t.Errorf("stats = %+v, want in=12 out=3", res.Stats)
	}
}

func TestStreamChat_ReasoningSeparateFromContent(t *testing.T) {
	// Always-thinking models stream reasoning_content before content. The two
	// streams must stay separate: reasoning to onReasoning, answer to onContent.
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"Let me "}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"think."}}]}`,
		`data: {"choices":[{"delta":{"content":"Answer"}}]}`,
		`data: {"choices":[{"delta":{"content":"."},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := sseServer(t, http.StatusOK, body)
	defer srv.Close()

	var content, reasoning strings.Builder
	res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`),
		func(s string) { content.WriteString(s) },
		func(s string) { reasoning.WriteString(s) })
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if content.String() != "Answer." || res.Content != "Answer." {
		t.Errorf("content = %q / %q, want %q", content.String(), res.Content, "Answer.")
	}
	if reasoning.String() != "Let me think." || res.Reasoning != "Let me think." {
		t.Errorf("reasoning = %q / %q, want %q", reasoning.String(), res.Reasoning, "Let me think.")
	}
}

func TestStreamChat_ToolCallReassembly(t *testing.T) {
	// Arguments arrive in fragments across chunks, keyed by index; id/name come
	// once. Two distinct tool calls (indexes 0 and 1) interleave-free here.
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go.mod\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := sseServer(t, http.StatusOK, body)
	defer srv.Close()

	res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	a := res.ToolCalls[0]
	if a.ID != "call_a" || a.Name != "read_file" || a.Arguments != `{"path":"go.mod"}` {
		t.Errorf("tool[0] = %+v, want id=call_a name=read_file args={\"path\":\"go.mod\"}", a)
	}
	b := res.ToolCalls[1]
	if b.ID != "call_b" || b.Name != "bash" || b.Arguments != `{"command":"ls"}` {
		t.Errorf("tool[1] = %+v, want id=call_b name=bash args={\"command\":\"ls\"}", b)
	}
}

func TestStreamChat_ServerError(t *testing.T) {
	// Non-200 with a compat error body surfaces the unwrapped message.
	srv := sseServer(t, http.StatusBadRequest, `{"error":{"message":"bad things happened"}}`)
	defer srv.Close()

	_, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`), nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad things happened") {
		t.Errorf("error = %q, want it to contain the server message", err.Error())
	}
}

func TestStreamChatSendsAttributionAndCost(t *testing.T) {
	var ref, title string
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"cost":0.0009}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref = r.Header.Get("HTTP-Referer")
		title = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if ref == "" || title == "" {
		t.Errorf("attribution not sent (HTTP-Referer=%q X-Title=%q)", ref, title)
	}
	if res.Stats.CostUSD != 0.0009 {
		t.Errorf("cost = %v, want 0.0009", res.Stats.CostUSD)
	}
}

func TestSetAttribution(t *testing.T) {
	h := http.Header{}
	SetAttribution(h)
	if h.Get("HTTP-Referer") == "" || h.Get("X-Title") == "" {
		t.Errorf("SetAttribution did not set both headers: %v", h)
	}
}

func TestParseSSEData(t *testing.T) {
	tests := []struct {
		line     string
		wantData string
		wantOK   bool
	}{
		{"data: {\"x\":1}\n", `{"x":1}`, true},
		{"data:{\"x\":1}", `{"x":1}`, true}, // no space after colon
		{"data: [DONE]", "[DONE]", true},
		{": comment line", "", false},
		{"", "", false},
		{"event: message", "", false},
	}
	for _, tt := range tests {
		got, ok := parseSSEData(tt.line)
		if ok != tt.wantOK || got != tt.wantData {
			t.Errorf("parseSSEData(%q) = (%q,%v), want (%q,%v)", tt.line, got, ok, tt.wantData, tt.wantOK)
		}
	}
}
