package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// contextSessionEntries re-reads the just-written session transcript and
// returns every "context" entry's sample — the diagnostic record this test
// exists to prove is actually on disk, not just held in memory.
func contextSessionEntries(t *testing.T, cs *CortexSession) []contextSample {
	t.Helper()
	path := filepath.Join(sessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	var out []contextSample
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		if e.Kind == kindContext && e.Context != nil {
			out = append(out, *e.Context)
		}
	}
	return out
}

// TestTurnWritesContextSamples closes the diagnostic gap: a session's real,
// provider-billed prompt-token usage was previously only held transiently in
// cs.LastPromptTokens (or the live REPL gauge) and never persisted, so a past
// session could only be diagnosed against the estTurnTokens/TailTokens
// char/4 heuristic — never against what the model actually saw. Every model
// round-trip must now leave a "context" entry with the real usage.prompt_tokens
// alongside the estimate and the watermarks in force at that instant.
func TestTurnWritesContextSamples(t *testing.T) {
	t.Chdir(t.TempDir())
	backend := newContextEvalBackend(t) // usage.prompt_tokens=10 per scripted reply
	// Window 4000 -> tail watermarks high=2000/low=1333 (docs/context-architecture.md).
	cs := newContextEvalSession(t, backend, 4000)

	if _, err := cs.Turn(context.Background(), "hello"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	samples := contextSessionEntries(t, cs)
	if len(samples) != 1 {
		t.Fatalf("got %d context samples for one no-tool-call turn, want 1", len(samples))
	}
	s := samples[0]
	if s.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", s.Iteration)
	}
	if s.LastPromptTokens != 10 {
		t.Errorf("LastPromptTokens = %d, want 10 (the backend's scripted usage.prompt_tokens)", s.LastPromptTokens)
	}
	if s.Window != 4000 {
		t.Errorf("Window = %d, want 4000", s.Window)
	}
	if s.HighWatermark != 2000 {
		t.Errorf("HighWatermark = %d, want 2000 (window/2)", s.HighWatermark)
	}
	if s.TailTokensEst < 0 {
		t.Errorf("TailTokensEst = %d, want >= 0", s.TailTokensEst)
	}
	if s.MaxTokens <= 0 {
		t.Errorf("MaxTokens = %d, want > 0", s.MaxTokens)
	}

	// A second turn accrues a second sample with an independent iteration
	// counter (not a running total across turns).
	if _, err := cs.Turn(context.Background(), "again"); err != nil {
		t.Fatalf("Turn 2: %v", err)
	}
	samples = contextSessionEntries(t, cs)
	if len(samples) != 2 {
		t.Fatalf("got %d context samples after two turns, want 2", len(samples))
	}
	if samples[1].Iteration != 1 {
		t.Errorf("turn 2 Iteration = %d, want 1 (resets per turn)", samples[1].Iteration)
	}
}
