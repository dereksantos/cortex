package processor

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

func setupTestProcessor(t *testing.T) (*Processor, *config.Config, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "cortex-processor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0o755); err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create db dir: %v", err)
	}

	cfg := &config.Config{
		ContextDir:  tempDir,
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "mistral:7b",
	}
	store, err := storage.New(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	processor := New(cfg, store)
	cleanup := func() {
		processor.Stop()
		store.Close()
		os.RemoveAll(tempDir)
	}
	return processor, cfg, cleanup
}

// appendCaptureEvent writes one capture.event entry to the project's
// journal/capture/ directory. Test helper for driving the processor.
func appendCaptureEvent(t *testing.T, contextDir string, ev *events.Event) {
	t.Helper()
	classDir := filepath.Join(contextDir, "journal", "capture")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	defer w.Close()

	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if _, err := w.Append(&journal.Entry{
		Type:    "capture.event",
		V:       1,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestNew(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	if processor == nil {
		t.Fatal("expected non-nil processor")
	}
	if processor.cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if processor.storage == nil {
		t.Fatal("expected non-nil storage")
	}
	if processor.registry == nil {
		t.Fatal("expected non-nil registry")
	}
	if len(processor.indexers) != 2 {
		t.Errorf("expected 2 default indexers (capture, observation), got %d", len(processor.indexers))
	}
}

func TestProcessor_StartStop(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	t.Run("starts successfully", func(t *testing.T) {
		if err := processor.Start(); err != nil {
			t.Fatalf("failed to start processor: %v", err)
		}
		if !processor.running.Load() {
			t.Error("processor should be running after Start")
		}
	})

	t.Run("prevents double start", func(t *testing.T) {
		if err := processor.Start(); err == nil {
			t.Error("expected error when starting already running processor")
		}
	})

	t.Run("stops successfully", func(t *testing.T) {
		processor.Stop()
		if processor.running.Load() {
			t.Error("processor should not be running after Stop")
		}
	})

	t.Run("can restart after stop", func(t *testing.T) {
		if err := processor.Start(); err != nil {
			t.Fatalf("failed to restart processor: %v", err)
		}
		processor.Stop()
	})
}

func TestProcessor_SetEventCallback(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	called := false
	processor.SetEventCallback(func(evs []*events.Event) {
		called = true
	})
	if processor.eventCallback == nil {
		t.Error("expected eventCallback to be set")
	}
	processor.eventCallback([]*events.Event{})
	if !called {
		t.Error("expected callback to be called")
	}
}

func TestProcessor_RunBatchProjectsCaptureEvents(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	// Write 3 capture.event entries to the journal.
	for i := 0; i < 3; i++ {
		ev := &events.Event{
			ID:        "proj-test-" + string(rune('a'+i)),
			Source:    events.SourceClaude,
			EventType: events.EventToolUse,
			Timestamp: time.Now(),
			ToolName:  "Edit",
			Context:   events.EventContext{ProjectPath: "/test"},
		}
		appendCaptureEvent(t, cfg.ContextDir, ev)
	}

	// Capture the events the cognition callback receives.
	var got []*events.Event
	processor.SetEventCallback(func(evs []*events.Event) {
		got = append(got, evs...)
	})

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 3 {
		t.Errorf("projected = %d, want 3", n)
	}
	if len(got) != 3 {
		t.Errorf("callback received %d events, want 3", len(got))
	}

	// Each event should be in SQLite now.
	for i := 0; i < 3; i++ {
		id := "proj-test-" + string(rune('a'+i))
		ev, err := processor.storage.GetEvent(id)
		if err != nil {
			t.Errorf("GetEvent %s: %v", id, err)
			continue
		}
		if ev == nil {
			t.Errorf("event %s missing from storage", id)
		}
	}
}

func TestProcessor_RunBatchIdempotent(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	ev := &events.Event{
		ID:        "idempotent-test",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	appendCaptureEvent(t, cfg.ContextDir, ev)

	n1, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("first RunBatch: %v", err)
	}
	if n1 != 1 {
		t.Errorf("first RunBatch projected = %d, want 1", n1)
	}

	// Second run with no new entries should project 0.
	n2, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("second RunBatch: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second RunBatch projected = %d, want 0 (cursor should skip already-indexed)", n2)
	}
}

func TestProcessor_ProjectsObservationsAndDedups(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "observation")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	// Three observations:
	//   - two with same (URI, content_hash) → second is a no-op
	//   - one with different content_hash → records a new row
	e1, _ := journal.NewObservationEntry(
		journal.TypeObservationMemoryFile, "memory-md", "file:///a",
		[]byte("alpha"), 5, time.Time{})
	if _, err := w.Append(e1); err != nil {
		t.Fatalf("append e1: %v", err)
	}
	e2, _ := journal.NewObservationEntry(
		journal.TypeObservationMemoryFile, "memory-md", "file:///a",
		[]byte("alpha"), 5, time.Time{})
	if _, err := w.Append(e2); err != nil {
		t.Fatalf("append e2: %v", err)
	}
	e3, _ := journal.NewObservationEntry(
		journal.TypeObservationMemoryFile, "memory-md", "file:///a",
		[]byte("alpha-updated"), 13, time.Time{})
	if _, err := w.Append(e3); err != nil {
		t.Fatalf("append e3: %v", err)
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 3 {
		t.Errorf("indexed = %d, want 3", n)
	}

	// Dedup: storage should hold both hashes for file:///a.
	if !processor.storage.HasObservation("file:///a", journal.HashContent([]byte("alpha"))) {
		t.Error("first observation not recorded")
	}
	if !processor.storage.HasObservation("file:///a", journal.HashContent([]byte("alpha-updated"))) {
		t.Error("second observation (new hash) not recorded")
	}
	// But the duplicate e2 must not have created a second derived row.
	// We can't enumerate; instead, verify the journal has 3 entries but
	// the cursor advanced past all 3 (RunBatch returns 3 above) AND
	// re-running should add 0 more.
	n2, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("second RunBatch: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second RunBatch indexed = %d, want 0 (already at tail)", n2)
	}
}

func TestProcessor_AddJournalDirAddsIndexer(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	initialDirs := len(processor.indexers)

	otherProject, err := os.MkdirTemp("", "cortex-other-proj-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(otherProject)

	contextDir := filepath.Join(otherProject, ".cortex")
	processor.AddJournalDir(filepath.Join(contextDir, "journal", "capture"))
	if len(processor.indexers) != initialDirs+1 {
		t.Errorf("indexer count after AddJournalDir = %d, want %d",
			len(processor.indexers), initialDirs+1)
	}

	ev := &events.Event{
		ID:        "multi-project-test",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	appendCaptureEvent(t, contextDir, ev)

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n < 1 {
		t.Errorf("RunBatch projected = %d, want >= 1 (new project's journal entry)", n)
	}
}
