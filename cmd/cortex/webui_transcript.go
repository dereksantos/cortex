// webui_transcript.go — M5.2b: the session transcript view-model (GOAL.md §6
// M5.2, split into M5.2a/b/c/d per STATE.md; GOAL.md §2 names
// cmd/cortex/webui*.go as home for the web UI's Go view-model builders).
// Per GOAL.md §3 P5, rendering logic lives in golden-tested Go view-models
// — this file is pure data assembly, no HTML/JS.
//
// The transcript is "transcript render" (docs/cortex-web.md Phase 5): every
// message of a real session JSONL file (session.go's loadSession, the same
// reader ResumeTranscript/listSessions already use), in order, with
// tool-call/tool-result turns represented alongside plain role/content ones.
package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// transcriptToolCall is one tool invocation carried by an assistant entry.
type transcriptToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// transcriptEntry is one rendered turn in the session transcript.
type transcriptEntry struct {
	Turn       int                  `json:"turn"`
	Role       string               `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []transcriptToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

// transcriptViewModel is the full session transcript screen.
type transcriptViewModel struct {
	SessionID string            `json:"session_id"`
	Entries   []transcriptEntry `json:"entries"`
}

// buildTranscriptViewModel reads a real session transcript file and renders
// it into entries in transcript order. The seed system-prompt message
// (always index 0 of a freshly created session, per session.go's
// write-on-create loop) is intentionally omitted: it is harness
// instructions, never a displayed conversation turn — the same convention
// display.go's REPL renderer already follows (gutter() has no RoleSystem
// case, so the seed system message never prints there either).
func buildTranscriptViewModel(path string) (transcriptViewModel, error) {
	msgs, turns, _, err := loadSession(path)
	if err != nil {
		return transcriptViewModel{}, fmt.Errorf("failed to load session %s: %w", path, err)
	}

	vm := transcriptViewModel{
		SessionID: sessionIDFromPath(path),
		Entries:   []transcriptEntry{},
	}
	for i, m := range msgs {
		if m.Role == RoleSystem {
			continue
		}
		turn := 0
		if i < len(turns) {
			turn = turns[i]
		}
		e := transcriptEntry{
			Turn:       turn,
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			e.ToolCalls = append(e.ToolCalls, transcriptToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		vm.Entries = append(vm.Entries, e)
	}
	return vm, nil
}

// sessionIDFromPath derives a session id from its transcript file path
// (the basename with the .jsonl extension stripped) — the same convention
// latestSessionID/listSessions already use.
func sessionIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}
