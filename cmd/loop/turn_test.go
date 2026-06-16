package main

import "testing"

// lastAssistantText is what a headless Turn caller relays back to its transport,
// so it must return the model's actual prose — the final assistant message with
// content — and skip tool-call-only (empty-content) assistant messages.
func TestLastAssistantText(t *testing.T) {
	tests := []struct {
		name string
		msgs []Message
		want string
	}{
		{
			name: "empty turn",
			msgs: nil,
			want: "",
		},
		{
			name: "single assistant answer",
			msgs: []Message{
				{Role: RoleUser, Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
			want: "hello",
		},
		{
			name: "skips tool-call-only assistant message",
			msgs: []Message{
				{Role: RoleUser, Content: "read the file"},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "1"}}}, // no content
				{Role: RoleTool, ToolCallID: "1", Content: "file body"},
				{Role: "assistant", Content: "here is what it says"},
			},
			want: "here is what it says",
		},
		{
			name: "returns the LAST assistant prose, not the first",
			msgs: []Message{
				{Role: "assistant", Content: "let me check"},
				{Role: RoleTool, ToolCallID: "1", Content: "result"},
				{Role: "assistant", Content: "final answer"},
			},
			want: "final answer",
		},
		{
			name: "whitespace-only content is not a reply",
			msgs: []Message{
				{Role: "assistant", Content: "real answer"},
				{Role: "assistant", Content: "   \n"},
			},
			want: "real answer",
		},
		{
			name: "ignores trailing tool result",
			msgs: []Message{
				{Role: "assistant", Content: "answer before tool"},
				{Role: RoleTool, ToolCallID: "9", Content: "tool output that is not a reply"},
			},
			want: "answer before tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastAssistantText(tt.msgs); got != tt.want {
				t.Errorf("lastAssistantText() = %q, want %q", got, tt.want)
			}
		})
	}
}
