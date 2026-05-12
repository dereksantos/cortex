package commands

import (
	"encoding/json"
	"io"
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
	for _, sub := range []string{"rebuild", "replay", "verify", "show", "tail"} {
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

func TestJournalCommand_MigrateFromQueue(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-migrate-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	queueDir := filepath.Join(tempDir, "queue", "processed")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("mkdir queue: %v", err)
	}

	// Write 3 fake "processed/" queue files, named by event ID so they
	// sort chronologically.
	ids := []string{"20260101-100000-aaaa", "20260101-100001-bbbb", "20260101-100002-cccc"}
	for _, id := range ids {
		ev := &events.Event{
			ID:        id,
			Source:    events.SourceClaude,
			EventType: events.EventToolUse,
			Timestamp: time.Now(),
			ToolName:  "Edit",
		}
		data, _ := json.Marshal(ev)
		if err := os.WriteFile(filepath.Join(queueDir, id+".json"), data, 0o644); err != nil {
			t.Fatalf("write queue file: %v", err)
		}
	}

	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}
	if err := cmd.Execute(&Context{Config: cfg, Args: []string{"migrate"}}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify journal has 3 entries in chronological order.
	r, err := journal.NewReader(filepath.Join(tempDir, "journal", "capture"))
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer r.Close()
	var got []string
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		ev, err := events.FromJSON(e.Payload)
		if err != nil {
			t.Fatalf("parse payload: %v", err)
		}
		got = append(got, ev.ID)
	}
	if len(got) != 3 {
		t.Fatalf("journal entries = %d, want 3", len(got))
	}
	for i, id := range ids {
		if got[i] != id {
			t.Errorf("entry %d: got %s, want %s (chronological order)", i, got[i], id)
		}
	}

	// Old queue files must still be present — C6 handles cleanup.
	leftover, _ := filepath.Glob(filepath.Join(queueDir, "*.json"))
	if len(leftover) != 3 {
		t.Errorf("queue files left = %d, want 3 (migrate must not delete)", len(leftover))
	}
}

func TestJournalCommand_MigrateRefusesIfJournalNonEmpty(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-migrate-noforce-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Pre-populate the journal with one entry.
	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	data, _ := json.Marshal(&events.Event{ID: "preexisting"})
	if _, err := w.Append(&journal.Entry{Type: "capture.event", V: 1, Payload: data}); err != nil {
		t.Fatalf("append: %v", err)
	}
	w.Close()

	// Queue dir with one file.
	queueDir := filepath.Join(tempDir, "queue", "processed")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("mkdir queue: %v", err)
	}
	qdata, _ := json.Marshal(&events.Event{ID: "from-queue"})
	if err := os.WriteFile(filepath.Join(queueDir, "from-queue.json"), qdata, 0o644); err != nil {
		t.Fatalf("write queue file: %v", err)
	}

	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}
	err = cmd.Execute(&Context{Config: cfg, Args: []string{"migrate"}})
	if err == nil {
		t.Fatal("migrate succeeded; want refusal because journal non-empty")
	}
	if !contains(err.Error(), "already has entries") {
		t.Errorf("error = %q, want contains 'already has entries'", err.Error())
	}

	// With --force the migration appends.
	if err := cmd.Execute(&Context{
		Config: cfg,
		Args:   []string{"migrate", "--force"},
	}); err != nil {
		t.Fatalf("migrate --force: %v", err)
	}
}

func TestJournalCommand_MigrateHandlesMissingQueueDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-migrate-empty-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}
	if err := cmd.Execute(&Context{Config: cfg, Args: []string{"migrate"}}); err != nil {
		t.Errorf("migrate on empty project: %v", err)
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
