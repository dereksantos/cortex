package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// TestExecuteBulkCapture verifies the NDJSON-stdin path writes the
// expected number of journal entries and returns clean errors for
// malformed input. The journal write itself is covered by
// internal/capture; this only exercises the CLI handler.
func TestExecuteBulkCapture(t *testing.T) {
	t.Run("happy path writes all events", func(t *testing.T) {
		workdir := t.TempDir()
		var buf bytes.Buffer
		for i := 0; i < 3; i++ {
			ev := &events.Event{
				Source:     events.SourceGeneric,
				EventType:  events.EventToolUse,
				Timestamp:  time.Now(),
				ToolName:   "test_chunk",
				ToolResult: "payload-" + string(rune('a'+i)),
				Context:    events.EventContext{SessionID: "test-session"},
			}
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal event %d: %v", i, err)
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}

		if err := executeBulkCapture(workdir, &buf); err != nil {
			t.Fatalf("executeBulkCapture: %v", err)
		}

		segDir := filepath.Join(workdir, ".cortex", "journal", "capture")
		entries, err := os.ReadDir(segDir)
		if err != nil {
			t.Fatalf("read journal dir: %v", err)
		}
		var totalLines int
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(segDir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			totalLines += strings.Count(string(data), "\n")
		}
		if totalLines != 3 {
			t.Errorf("journal lines = %d, want 3", totalLines)
		}
	})

	t.Run("missing workdir errors", func(t *testing.T) {
		err := executeBulkCapture("", strings.NewReader(""))
		if err == nil {
			t.Fatal("expected error for empty workdir")
		}
		if !strings.Contains(err.Error(), "--workdir") {
			t.Errorf("error %q does not mention --workdir", err)
		}
	})

	t.Run("malformed line names line number", func(t *testing.T) {
		workdir := t.TempDir()
		ev, _ := json.Marshal(&events.Event{Source: events.SourceGeneric, EventType: events.EventToolUse})
		input := string(ev) + "\nnot-json\n"
		err := executeBulkCapture(workdir, strings.NewReader(input))
		if err == nil {
			t.Fatal("expected error for malformed second line")
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("error %q should reference line 2", err)
		}
	})

	t.Run("blank lines skipped", func(t *testing.T) {
		workdir := t.TempDir()
		ev, _ := json.Marshal(&events.Event{
			Source:    events.SourceGeneric,
			EventType: events.EventToolUse,
			ToolName:  "x",
		})
		input := string(ev) + "\n\n" + string(ev) + "\n"
		if err := executeBulkCapture(workdir, strings.NewReader(input)); err != nil {
			t.Fatalf("executeBulkCapture: %v", err)
		}
	})
}
