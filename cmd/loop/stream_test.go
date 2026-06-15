package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseHandler serves a fixed SSE body for any request path.
func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}
}

// sseBody wraps each event JSON as a `data:` SSE frame and appends the
// terminating [DONE]. Used by Resolve tests to drive the streaming path.
func sseBody(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestSendStreamAssemblesResponse(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Reading "}}]}`,
		`data: {"choices":[{"delta":{"content":"the file."}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := httptest.NewServer(sseHandler(body))
	defer srv.Close()

	req := &AgentRequest{Model: "coder", BaseURL: srv.URL}
	var got strings.Builder
	res, err := req.SendStream(context.Background(), func(s string) { got.WriteString(s) }, nil)
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if len(res.Choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(res.Choices))
	}
	msg := res.Choices[0].Message
	if msg.Role != "assistant" || msg.Content != "Reading the file." {
		t.Errorf("message = %+v, want assistant prose", msg)
	}
	if got.String() != "Reading the file." {
		t.Errorf("streamed deltas = %q, want full prose", got.String())
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"go.mod"}` {
		t.Errorf("tool call = %+v, want read_file(go.mod)", tc)
	}
	if res.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", res.Choices[0].FinishReason)
	}
	if res.Usage.PromptTokens != 42 || res.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want in=42 out=7", res.Usage)
	}
}

func TestStreamPrinterSuppressesToolMarkup(t *testing.T) {
	tests := []struct {
		name      string
		fragments []string
		want      string // visible output, excluding the gutter prefix
	}{
		{
			name:      "plain prose prints whole",
			fragments: []string{"Hello", " there"},
			want:      "Hello there",
		},
		{
			name:      "marker split across chunks is held back",
			fragments: []string{"Let me look. <to", "ol_call>{\"name\":\"bash\"}</tool_call>"},
			want:      "Let me look. ",
		},
		{
			name:      "marker at start prints nothing",
			fragments: []string{"<tool_call>", "{\"name\":\"x\"}", "</tool_call>"},
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			p := &streamPrinter{out: &buf} // nil spinner: no terminal control
			for _, f := range tt.fragments {
				p.onContent(f)
			}
			began := p.finish()

			out := buf.String()
			if strings.Contains(out, toolMarker) {
				t.Errorf("output leaked tool markup: %q", out)
			}
			// Strip the gutter prefix (everything up to the double-space) when
			// anything was printed, then compare the prose body.
			if began {
				if i := strings.Index(out, "  "); i >= 0 {
					out = strings.TrimSuffix(out[i+2:], "\n")
				}
			}
			if out != tt.want {
				t.Errorf("visible = %q, want %q", out, tt.want)
			}
			if began != (tt.want != "") {
				t.Errorf("began = %v, want %v", began, tt.want != "")
			}
		})
	}
}
