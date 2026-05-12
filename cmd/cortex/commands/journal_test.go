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
	for _, sub := range []string{"show", "tail"} {
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

func TestJournalCommand_RebuildReplaysCaptureJournal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-rebuild-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	// Write 3 capture.event entries to the journal AND store them in
	// storage so we have both sides of derived state populated. Then
	// blow away storage and verify rebuild repopulates it from journal
	// alone.
	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	for i, id := range []string{"r-a", "r-b", "r-c"} {
		ev := &events.Event{
			ID:        id,
			Source:    events.SourceClaude,
			EventType: events.EventToolUse,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			ToolName:  "Edit",
		}
		payload, _ := json.Marshal(ev)
		if _, err := w.Append(&journal.Entry{Type: "capture.event", V: 1, Payload: payload}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		// Also store in storage directly — simulates the indexer having
		// already projected these.
		if err := store.StoreEvent(ev); err != nil {
			t.Fatalf("StoreEvent: %v", err)
		}
	}
	w.Close()

	// Sanity: storage has 3 events.
	for _, id := range []string{"r-a", "r-b", "r-c"} {
		if _, err := store.GetEvent(id); err != nil {
			t.Fatalf("pre-rebuild GetEvent %s: %v", id, err)
		}
	}

	cmd := &JournalCommand{}
	err = cmd.Execute(&Context{Config: cfg, Storage: store, Args: []string{"rebuild"}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	// After rebuild, all 3 events should be back in storage.
	for _, id := range []string{"r-a", "r-b", "r-c"} {
		got, err := store.GetEvent(id)
		if err != nil {
			t.Errorf("post-rebuild GetEvent %s: %v", id, err)
		}
		if got == nil {
			t.Errorf("event %s missing after rebuild", id)
		}
	}

	store.Close()
}

func TestJournalCommand_RebuildWalksFullDAG(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-rebuild-dag-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	// Seed entries across multiple writer-classes.
	writeOne := func(class string, entry *journal.Entry) {
		w, err := journal.NewWriter(journal.WriterOpts{
			ClassDir: filepath.Join(tempDir, "journal", class),
			Fsync:    journal.FsyncPerBatch,
		})
		if err != nil {
			t.Fatalf("writer for %s: %v", class, err)
		}
		if _, err := w.Append(entry); err != nil {
			t.Fatalf("append %s: %v", class, err)
		}
		w.Close()
	}

	// capture
	ev := &events.Event{
		ID:        "dag-event",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	evJSON, _ := json.Marshal(ev)
	writeOne("capture", &journal.Entry{Type: "capture.event", V: 1, Payload: evJSON})

	// observation
	obs, _ := journal.NewObservationEntry(
		journal.TypeObservationMemoryFile, "memory-md", "file:///x",
		[]byte("hello"), 5, time.Time{})
	writeOne("observation", obs)

	// reflect rerank with a contradiction
	rerank, _ := journal.NewReflectRerankEntry(journal.ReflectRerankPayload{
		QueryText: "q",
		InputIDs:  []string{"a", "b"},
		RankedIDs: []string{"b", "a"},
		Contradictions: []journal.ContradictionRecord{
			{IDs: []string{"a", "b"}, Reason: "conflict"},
		},
	})
	writeOne("reflect", rerank)

	// resolve retrieval
	retr, _ := journal.NewResolveRetrievalEntry(journal.ResolveRetrievalPayload{
		QueryText:   "q",
		Decision:    "inject",
		Confidence:  0.9,
		ResultCount: 2,
	})
	writeOne("resolve", retr)

	// Run rebuild — should truncate + replay everything.
	cmd := &JournalCommand{}
	if err := cmd.Execute(&Context{Config: cfg, Storage: store, Args: []string{"rebuild"}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	// Verify each writer-class's derived state is populated.
	if _, err := store.GetEvent("dag-event"); err != nil {
		t.Errorf("capture event missing after rebuild: %v", err)
	}
	if !store.HasObservation("file:///x", journal.HashContent([]byte("hello"))) {
		t.Error("observation missing after rebuild")
	}
	if got := store.GetContradictions(10); len(got) != 1 {
		t.Errorf("contradictions = %d, want 1", len(got))
	}
	if got := store.GetRetrievals(10); len(got) != 1 {
		t.Errorf("retrievals = %d, want 1", len(got))
	}
	store.Close()
}

func TestJournalCommand_ReplayWalksRange(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-replay-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	for i := 0; i < 5; i++ {
		ev := &events.Event{ID: "replay-" + string(rune('a'+i)), Source: events.SourceClaude, Timestamp: time.Now()}
		data, _ := json.Marshal(ev)
		if _, err := w.Append(&journal.Entry{Type: "capture.event", V: 1, Payload: data}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}

	// Replay all entries (no range).
	if err := cmd.Execute(&Context{Config: cfg, Args: []string{"replay"}}); err != nil {
		t.Errorf("replay all: %v", err)
	}
	// Replay a sub-range.
	if err := cmd.Execute(&Context{
		Config: cfg,
		Args:   []string{"replay", "--from-offset=2", "--to-offset=4"},
	}); err != nil {
		t.Errorf("replay sub-range: %v", err)
	}
	// Flag parsing for config-overrides (no-op for now).
	if err := cmd.Execute(&Context{
		Config: cfg,
		Args:   []string{"replay", "--config-overrides=model=claude-haiku"},
	}); err != nil {
		t.Errorf("replay with config-overrides: %v", err)
	}
}

func TestJournalCommand_VerifyHealthyJournal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-verify-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	for i := 0; i < 3; i++ {
		ev := &events.Event{ID: "v-" + string(rune('a'+i)), Source: events.SourceClaude, Timestamp: time.Now()}
		data, _ := json.Marshal(ev)
		if _, err := w.Append(&journal.Entry{Type: "capture.event", V: 1, Payload: data}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}
	if err := cmd.Execute(&Context{Config: cfg, Args: []string{"verify"}}); err != nil {
		t.Errorf("verify on healthy journal: %v", err)
	}
}

func TestJournalCommand_VerifyDetectsCursorPastTail(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-verify-bad-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	classDir := filepath.Join(tempDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	ev := &events.Event{ID: "v-1", Source: events.SourceClaude, Timestamp: time.Now()}
	data, _ := json.Marshal(ev)
	w.Append(&journal.Entry{Type: "capture.event", V: 1, Payload: data})
	w.Close()

	// Corrupt cursor: set past the tail.
	if err := journal.OpenCursor(classDir).Set(99); err != nil {
		t.Fatalf("Set cursor: %v", err)
	}

	cfg := &config.Config{ContextDir: tempDir}
	cmd := &JournalCommand{}
	if err := cmd.Execute(&Context{Config: cfg, Args: []string{"verify"}}); err == nil {
		t.Error("verify should fail when cursor past tail")
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
