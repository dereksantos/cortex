package llm

import "testing"

func TestEstimateChatTokens_AccountsForToolCalls(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "x", Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":"main.go"}`}},
		}},
	}
	got := EstimateChatTokens(msgs)
	// 11 + (9 + 19)/4 + 4*2 ≈ floor(11/4)+floor(9/4)+floor(19/4)+8
	// = 2 + 2 + 4 + 8 = 16. Loose check — heuristic, not exact.
	if got < 10 || got > 40 {
		t.Errorf("EstimateChatTokens = %d, want roughly 10-40", got)
	}
}

func TestTrimChatHistory_DropsOldestUntilFits(t *testing.T) {
	bulky := func(role string, n int) ChatMessage {
		s := make([]byte, n)
		for i := range s {
			s[i] = 'x'
		}
		return ChatMessage{Role: role, Content: string(s)}
	}
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		bulky("user", 400),      // ~100 tokens
		bulky("assistant", 400), // ~100 tokens
		bulky("user", 400),      // ~100 tokens
		bulky("assistant", 400), // ~100 tokens
		{Role: "user", Content: "now"},
	}
	got, dropped := TrimChatHistory(msgs, 150, 1, 1)
	if dropped == 0 {
		t.Fatal("expected some messages to be dropped")
	}
	if EstimateChatTokens(got) > 150 {
		t.Errorf("trimmed still over budget: %d > 150", EstimateChatTokens(got))
	}
	// system + final user must survive.
	if got[0].Role != "system" || got[len(got)-1].Content != "now" {
		t.Errorf("head/tail not preserved: %+v", got)
	}
}

func TestTrimChatHistory_NoOpWhenFits(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "small"},
	}
	got, dropped := TrimChatHistory(msgs, 1_000_000, 1, 1)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if len(got) != 2 {
		t.Errorf("len(got) = %d, want 2", len(got))
	}
}

func TestTrimChatHistory_BoundsBudget(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "anything"},
	}
	got, dropped := TrimChatHistory(msgs, 0, 0, 0)
	if dropped != 0 || len(got) != 1 {
		t.Errorf("maxTokens=0 should be a no-op; got dropped=%d len=%d", dropped, len(got))
	}
}
