package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestRecallFromResult(t *testing.T) {
	ts := time.Date(2026, 5, 10, 18, 4, 0, 0, time.UTC)
	r := cognition.Result{
		ID:        "result-1",
		Content:   "Use pgx not database/sql for new code paths.",
		Category:  "decision",
		Score:     0.87,
		Source:    events.Source("claude_code"),
		Timestamp: ts,
		Tags:      []string{"database", "pgx"},
	}

	entry := recallFromResult(r)

	if entry.ID != "result-1" {
		t.Errorf("ID = %q, want result-1", entry.ID)
	}
	if entry.Content != r.Content {
		t.Errorf("Content = %q, want %q", entry.Content, r.Content)
	}
	if entry.Score != 0.87 {
		t.Errorf("Score = %v, want 0.87", entry.Score)
	}
	if entry.CapturedAt != "2026-05-10T18:04:00Z" {
		t.Errorf("CapturedAt = %q, want 2026-05-10T18:04:00Z", entry.CapturedAt)
	}
	if len(entry.Tags) != 2 || entry.Tags[0] != "database" {
		t.Errorf("Tags = %v, want [database pgx]", entry.Tags)
	}
	if entry.Category != "decision" {
		t.Errorf("Category = %q, want decision", entry.Category)
	}
	if entry.Source != "claude_code" {
		t.Errorf("Source = %q, want claude_code", entry.Source)
	}
}

func TestRecallFromEvent(t *testing.T) {
	ts := time.Date(2026, 5, 10, 18, 4, 0, 0, time.UTC)
	tests := []struct {
		name        string
		event       *events.Event
		wantContent string
	}{
		{
			name: "tool_result takes precedence",
			event: &events.Event{
				ID:         "ev-1",
				Source:     events.Source("claude_code"),
				EventType:  events.EventToolUse,
				Timestamp:  ts,
				ToolName:   "Edit",
				ToolResult: "Replaced 3 occurrences",
				Prompt:     "unused",
			},
			wantContent: "Replaced 3 occurrences",
		},
		{
			name: "falls back to tool_name when result empty",
			event: &events.Event{
				ID:        "ev-2",
				Source:    events.Source("claude_code"),
				EventType: events.EventToolUse,
				Timestamp: ts,
				ToolName:  "Read",
			},
			wantContent: "Read",
		},
		{
			name: "falls back to prompt for user_prompt events",
			event: &events.Event{
				ID:        "ev-3",
				Source:    events.Source("claude_code"),
				EventType: events.EventUserPrompt,
				Timestamp: ts,
				Prompt:    "how does auth work",
			},
			wantContent: "how does auth work",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := recallFromEvent(tc.event)
			if entry.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", entry.Content, tc.wantContent)
			}
			if entry.ID != tc.event.ID {
				t.Errorf("ID = %q, want %q", entry.ID, tc.event.ID)
			}
			if entry.Score != 0 {
				t.Errorf("Score = %v, want 0 (events have no score)", entry.Score)
			}
			if entry.CapturedAt != "2026-05-10T18:04:00Z" {
				t.Errorf("CapturedAt = %q, want 2026-05-10T18:04:00Z", entry.CapturedAt)
			}
			if entry.Category != string(tc.event.EventType) {
				t.Errorf("Category = %q, want %q", entry.Category, string(tc.event.EventType))
			}
		})
	}
}

func TestEmitRecallJSON_NonEmpty(t *testing.T) {
	entries := []recallEntry{
		{
			ID:         "result-1",
			Content:    "decision A",
			Score:      0.9,
			CapturedAt: "2026-05-10T18:04:00Z",
			Tags:       []string{"x"},
			Category:   "decision",
			Source:     "claude_code",
		},
	}

	var buf bytes.Buffer
	if err := emitRecallJSON(&buf, entries); err != nil {
		t.Fatalf("emitRecallJSON returned error: %v", err)
	}

	// Validate it's a parseable JSON array of the expected shape.
	var decoded []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output not valid JSON array: %v\noutput: %s", err, buf.String())
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded length = %d, want 1", len(decoded))
	}
	got := decoded[0]
	requiredKeys := []string{"id", "content", "score", "captured_at"}
	for _, k := range requiredKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("required key %q missing from JSON output", k)
		}
	}
	if got["id"] != "result-1" {
		t.Errorf("id = %v, want result-1", got["id"])
	}
	if got["captured_at"] != "2026-05-10T18:04:00Z" {
		t.Errorf("captured_at = %v, want 2026-05-10T18:04:00Z", got["captured_at"])
	}
}

func TestEmitRecallJSON_EmptyIsArrayNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := emitRecallJSON(&buf, nil); err != nil {
		t.Fatalf("emitRecallJSON(nil) returned error: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if out != "[]" {
		t.Errorf("empty output = %q, want [] (downstream consumers depend on always-an-array)", out)
	}

	buf.Reset()
	if err := emitRecallJSON(&buf, []recallEntry{}); err != nil {
		t.Fatalf("emitRecallJSON([]) returned error: %v", err)
	}
	out = strings.TrimSpace(buf.String())
	if out != "[]" {
		t.Errorf("empty-slice output = %q, want []", out)
	}
}

func TestEmitRecallJSON_OmitsEmptyOptionalFields(t *testing.T) {
	// An entry with empty Tags / Category / Source should not
	// emit those keys (they have `omitempty`). The required
	// keys must always be present.
	entries := []recallEntry{
		{
			ID:         "result-2",
			Content:    "bare entry",
			Score:      0.5,
			CapturedAt: "2026-05-10T18:04:00Z",
		},
	}

	var buf bytes.Buffer
	if err := emitRecallJSON(&buf, entries); err != nil {
		t.Fatalf("emitRecallJSON returned error: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	got := decoded[0]
	for _, k := range []string{"tags", "category", "source"} {
		if _, ok := got[k]; ok {
			t.Errorf("optional key %q should be omitted when empty, got %v", k, got[k])
		}
	}
	for _, k := range []string{"id", "content", "score", "captured_at"} {
		if _, ok := got[k]; !ok {
			t.Errorf("required key %q must always be present, even for sparse entries", k)
		}
	}
}
