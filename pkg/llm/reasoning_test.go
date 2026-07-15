package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// mustJSON marshals s as a JSON string literal for embedding in a hand-built
// SSE frame.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestStripLeadingThinkFence exercises the non-streaming (blocking-path)
// fence stripper directly.
func TestStripLeadingThinkFence(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantContent string
		wantReason  string
	}{
		{
			name:        "no fence: passthrough",
			in:          "Hello world, no think here.",
			wantContent: "Hello world, no think here.",
			wantReason:  "",
		},
		{
			name:        "closed fence at the start",
			in:          "<think>let me reason</think>Answer.",
			wantContent: "Answer.",
			wantReason:  "let me reason",
		},
		{
			name:        "fence-only completion",
			in:          "<think>only reasoning</think>",
			wantContent: "",
			wantReason:  "only reasoning",
		},
		{
			name:        "unclosed fence (max-tokens clamp)",
			in:          "<think>never closes",
			wantContent: "",
			wantReason:  "never closes",
		},
		{
			name:        "think tag mid-answer after real prose is left in content",
			in:          "Here is my answer. <think>not a leading fence</think>",
			wantContent: "Here is my answer. <think>not a leading fence</think>",
			wantReason:  "",
		},
		{
			name:        "empty string",
			in:          "",
			wantContent: "",
			wantReason:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotContent, gotReason := StripLeadingThinkFence(tt.in)
			if gotContent != tt.wantContent || gotReason != tt.wantReason {
				t.Errorf("StripLeadingThinkFence(%q) = (%q, %q), want (%q, %q)",
					tt.in, gotContent, gotReason, tt.wantContent, tt.wantReason)
			}
		})
	}
}

// TestThinkFenceFilter_Feed drives the streaming filter directly, one call per
// fragment, covering a fence split arbitrarily across feed() calls.
func TestThinkFenceFilter_Feed(t *testing.T) {
	tests := []struct {
		name        string
		fragments   []string
		wantContent string
		wantReason  string
	}{
		{
			name:        "fence split across many fragments",
			fragments:   []string{"<thi", "nk>reasoning ", "here</thi", "nk>An", "swer"},
			wantContent: "Answer",
			wantReason:  "reasoning here",
		},
		{
			name:        "unclosed fence, drained by flush",
			fragments:   []string{"<think>", "chain of thought", " that never closes"},
			wantContent: "",
			wantReason:  "chain of thought that never closes",
		},
		{
			name:        "fence-only completion",
			fragments:   []string{"<think>only reasoning</think>"},
			wantContent: "",
			wantReason:  "only reasoning",
		},
		{
			name:        "no fence: passthrough",
			fragments:   []string{"Hello ", "world."},
			wantContent: "Hello world.",
			wantReason:  "",
		},
		{
			name:        "single-byte fragments split the tag maximally",
			fragments:   strings.Split("<think>hi</think>bye", ""),
			wantContent: "bye",
			wantReason:  "hi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f thinkFenceFilter
			var content, reasoning strings.Builder
			for _, frag := range tt.fragments {
				c, r := f.feed(frag)
				content.WriteString(c)
				reasoning.WriteString(r)
			}
			c, r := f.flush()
			content.WriteString(c)
			reasoning.WriteString(r)
			if content.String() != tt.wantContent {
				t.Errorf("content = %q, want %q", content.String(), tt.wantContent)
			}
			if reasoning.String() != tt.wantReason {
				t.Errorf("reasoning = %q, want %q", reasoning.String(), tt.wantReason)
			}
		})
	}
}

// TestStreamChat_ThinkFence proves the fence filter is correctly wired into
// StreamChat's SSE assembly: a server that puts chain-of-thought inline in
// the content channel (raw llama.cpp / Ollama R1-style, no reasoning_content
// field at all) still yields separated content/reasoning.
func TestStreamChat_ThinkFence(t *testing.T) {
	tests := []struct {
		name        string
		fragments   []string // each becomes one SSE content-delta frame
		wantContent string
		wantReason  string
	}{
		{
			name:        "fence split across SSE chunk boundaries",
			fragments:   []string{"<thi", "nk>reasoning here</thi", "nk>Answer"},
			wantContent: "Answer",
			wantReason:  "reasoning here",
		},
		{
			name:        "unclosed fence at stream end (max-tokens clamp signature)",
			fragments:   []string{"<think>chain of thought that never closes"},
			wantContent: "",
			wantReason:  "chain of thought that never closes",
		},
		{
			name:        "fence-only completion yields empty content",
			fragments:   []string{"<think>only reasoning</think>"},
			wantContent: "",
			wantReason:  "only reasoning",
		},
		{
			name:        "no fence: passthrough",
			fragments:   []string{"Hello world, no think here."},
			wantContent: "Hello world, no think here.",
			wantReason:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var frames []string
			for _, f := range tt.fragments {
				frames = append(frames, `data: {"choices":[{"delta":{"content":`+mustJSON(f)+`}}]}`)
			}
			frames = append(frames, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`, ``)
			body := strings.Join(frames, "\n\n")

			srv := sseServer(t, http.StatusOK, body)
			defer srv.Close()

			var content, reasoning strings.Builder
			res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`),
				func(s string) { content.WriteString(s) },
				func(s string) { reasoning.WriteString(s) })
			if err != nil {
				t.Fatalf("StreamChat: %v", err)
			}
			if res.Content != tt.wantContent || content.String() != tt.wantContent {
				t.Errorf("content = %q / %q, want %q", res.Content, content.String(), tt.wantContent)
			}
			if res.Reasoning != tt.wantReason || reasoning.String() != tt.wantReason {
				t.Errorf("reasoning = %q / %q, want %q", res.Reasoning, reasoning.String(), tt.wantReason)
			}
		})
	}
}

// TestStreamChat_ReasoningAliasField proves OpenRouter's "reasoning" delta
// field (as opposed to "reasoning_content") is accumulated into the same
// reasoning trace.
func TestStreamChat_ReasoningAliasField(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","reasoning":"Let me "}}]}`,
		`data: {"choices":[{"delta":{"reasoning":"think."}}]}`,
		`data: {"choices":[{"delta":{"content":"Answer."},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := sseServer(t, http.StatusOK, body)
	defer srv.Close()

	res, err := StreamChat(context.Background(), srv.Client(), srv.URL, "", []byte(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if res.Reasoning != "Let me think." {
		t.Errorf("reasoning = %q, want %q", res.Reasoning, "Let me think.")
	}
	if res.Content != "Answer." {
		t.Errorf("content = %q, want %q", res.Content, "Answer.")
	}
}
