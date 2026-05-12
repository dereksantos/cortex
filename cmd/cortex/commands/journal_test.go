package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestJournalCommand_NoArgsPrintsUsage(t *testing.T) {
	cmd := &JournalCommand{}
	err := cmd.Execute(&Context{Args: nil})
	if err != nil {
		t.Errorf("no-args dispatch: %v (want nil — should print usage)", err)
	}
}

func TestJournalCommand_UnknownSubcommand(t *testing.T) {
	cmd := &JournalCommand{}
	err := cmd.Execute(&Context{Args: []string{"flibbertigibbet"}})
	if err == nil {
		t.Error("unknown subcommand should error")
	}
}

func TestJournalCommand_StubsReportSliceTarget(t *testing.T) {
	cmd := &JournalCommand{}
	for _, sub := range []string{"rebuild", "replay", "verify", "show", "tail", "migrate"} {
		err := cmd.Execute(&Context{Args: []string{sub}})
		if err == nil {
			t.Errorf("%s stub returned nil (expected not-implemented error)", sub)
			continue
		}
		// Error message should name the slice that lands the body.
		if !contains(err.Error(), "slice") {
			t.Errorf("%s error %q should name the implementing slice", sub, err.Error())
		}
	}
}

func TestJournalCommand_IngestProjectsJournalEntries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-journal-cmd-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}

	cfg := &config.Config{ContextDir: tempDir}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	// Write a capture.event entry directly to the journal so `journal
	// ingest` has something to project.
	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	ev := &events.Event{
		ID:        "cli-ingest-test",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	payload, _ := json.Marshal(ev)
	if _, err := w.Append(&journal.Entry{
		Type:    "capture.event",
		V:       1,
		Payload: payload,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.Close()

	cmd := &JournalCommand{}
	err = cmd.Execute(&Context{
		Config:  cfg,
		Storage: store,
		Args:    []string{"ingest"},
	})
	if err != nil {
		t.Fatalf("journal ingest: %v", err)
	}

	got, err := store.GetEvent("cli-ingest-test")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got == nil {
		t.Fatal("event not in storage after ingest")
	}
	if got.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit", got.ToolName)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
