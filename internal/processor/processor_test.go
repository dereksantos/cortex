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
	if len(processor.indexers) != 8 {
		t.Errorf("expected 8 default indexers (capture, observation, dream, reflect, resolve, think, feedback, eval), got %d", len(processor.indexers))
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

func TestProcessor_ProjectsDreamInsight(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "dream")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	e, err := journal.NewDreamInsightEntry(journal.DreamInsightPayload{
		InsightID:    "dream-test-1",
		Category:     "decision",
		Content:      "Use journal as source of truth",
		Importance:   8,
		Tags:         []string{"architecture"},
		SessionID:    "sess-1",
		SourceItemID: "memory:CLAUDE.md:Direction",
		SourceName:   "memory-md",
	})
	if err != nil {
		t.Fatalf("NewDreamInsightEntry: %v", err)
	}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("indexed = %d, want 1", n)
	}
	// Storage projection: query the insight back through storage.
	insights, err := processor.storage.GetRecentInsights(10)
	if err != nil {
		t.Fatalf("GetRecentInsights: %v", err)
	}
	var found bool
	for _, ins := range insights {
		if ins.EventID == "dream-test-1" && ins.Summary == "Use journal as source of truth" {
			found = true
			if ins.Importance != 8 {
				t.Errorf("Importance = %d, want 8", ins.Importance)
			}
			break
		}
	}
	if !found {
		t.Error("dream insight not projected to storage")
	}
}

func TestProcessor_ProjectsReflectRerankContradictions(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "reflect")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	e, err := journal.NewReflectRerankEntry(journal.ReflectRerankPayload{
		QueryText: "auth flow",
		InputIDs:  []string{"a", "b", "c"},
		RankedIDs: []string{"c", "a", "b"},
		Contradictions: []journal.ContradictionRecord{
			{IDs: []string{"a", "b"}, Reason: "use JWT vs sessions"},
			{IDs: []string{"a", "c"}, Reason: "rotation policy disagrees"},
		},
	})
	if err != nil {
		t.Fatalf("NewReflectRerankEntry: %v", err)
	}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("indexed = %d, want 1", n)
	}

	got := processor.storage.GetContradictions(10)
	if len(got) != 2 {
		t.Fatalf("contradictions = %d, want 2", len(got))
	}
	// Most-recent-first ordering. Both should reference the same journal offset.
	for _, c := range got {
		if c.JournalOffset != 1 {
			t.Errorf("JournalOffset = %d, want 1", c.JournalOffset)
		}
		if c.QueryText != "auth flow" {
			t.Errorf("QueryText = %s, want auth flow", c.QueryText)
		}
	}
}

func TestProcessor_ProjectsResolveRetrieval(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "resolve")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	// Two entries: one inject decision, one wait.
	for _, p := range []journal.ResolveRetrievalPayload{
		{QueryText: "q1", Decision: "inject", Confidence: 0.9, ResultCount: 3, InjectedIDs: []string{"a", "b", "c"}},
		{QueryText: "q2", Decision: "wait", Confidence: 0.3, ResultCount: 1},
	} {
		e, err := journal.NewResolveRetrievalEntry(p)
		if err != nil {
			t.Fatalf("build entry: %v", err)
		}
		if _, err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 2 {
		t.Errorf("indexed = %d, want 2", n)
	}

	stats := processor.storage.GetRetrievalStats()
	if stats.Total != 2 {
		t.Errorf("Total = %d, want 2", stats.Total)
	}
	if stats.ByDecision["inject"] != 1 {
		t.Errorf("ByDecision[inject] = %d, want 1", stats.ByDecision["inject"])
	}
	if stats.ByDecision["wait"] != 1 {
		t.Errorf("ByDecision[wait] = %d, want 1", stats.ByDecision["wait"])
	}

	got := processor.storage.GetRetrievals(10)
	if len(got) != 2 {
		t.Errorf("retrievals = %d, want 2", len(got))
	}
}

func TestProcessor_ProjectsThinkSessionContext(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "think")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	// Two snapshots — second supersedes the first as "latest".
	e1, _ := journal.NewThinkSessionContextEntry(journal.ThinkSessionContextPayload{
		TopicWeights:  map[string]float64{"auth": 0.6},
		RecentQueries: []string{"first q"},
		SessionID:     "sess-1",
	})
	if _, err := w.Append(e1); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	e2, _ := journal.NewThinkSessionContextEntry(journal.ThinkSessionContextPayload{
		TopicWeights:  map[string]float64{"auth": 0.8, "db": 0.4},
		RecentQueries: []string{"second q"},
		SessionID:     "sess-1",
	})
	if _, err := w.Append(e2); err != nil {
		t.Fatalf("Append e2: %v", err)
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 2 {
		t.Errorf("indexed = %d, want 2", n)
	}
	if processor.storage.SessionContextSnapshotCount() != 2 {
		t.Errorf("snapshot count = %d, want 2", processor.storage.SessionContextSnapshotCount())
	}
	latest := processor.storage.LatestSessionContext()
	if latest == nil {
		t.Fatal("LatestSessionContext returned nil")
	}
	if latest.TopicWeights["auth"] != 0.8 {
		t.Errorf("latest TopicWeights[auth] = %v, want 0.8", latest.TopicWeights["auth"])
	}
	if latest.TopicWeights["db"] != 0.4 {
		t.Errorf("latest TopicWeights[db] = %v, want 0.4", latest.TopicWeights["db"])
	}
}

func TestProcessor_ProjectsFeedback(t *testing.T) {
	processor, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "feedback")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}

	// One correction (graded ID = insight-7) followed by a retraction of
	// a different insight (graded ID = insight-8).
	e1, err := journal.NewFeedbackEntry(journal.TypeFeedbackCorrection, journal.FeedbackPayload{
		GradedID:    "insight-7",
		Note:        "use Postgres",
		Replacement: "use Postgres not SQLite",
	})
	if err != nil {
		t.Fatalf("NewFeedbackEntry corr: %v", err)
	}
	if _, err := w.Append(e1); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	e2, err := journal.NewFeedbackEntry(journal.TypeFeedbackRetraction, journal.FeedbackPayload{
		GradedID: "insight-8",
		Reason:   "user forget",
	})
	if err != nil {
		t.Fatalf("NewFeedbackEntry retr: %v", err)
	}
	if _, err := w.Append(e2); err != nil {
		t.Fatalf("Append e2: %v", err)
	}
	w.Close()

	n, err := processor.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 2 {
		t.Errorf("indexed = %d, want 2", n)
	}

	feedbacks := processor.storage.FeedbackFor("insight-7")
	if len(feedbacks) != 1 || feedbacks[0].Type != journal.TypeFeedbackCorrection {
		t.Errorf("insight-7 feedbacks = %+v, want one correction", feedbacks)
	}
	if processor.storage.IsRetracted("insight-7") {
		t.Error("insight-7 should not be retracted")
	}
	if !processor.storage.IsRetracted("insight-8") {
		t.Error("insight-8 should be retracted")
	}
}

// TestProcessor_ProjectsEvalCellResult verifies the injected
// EvalCellResultProjector callback receives each journal entry's
// payload. The processor itself owns no eval read-side state — the
// canonical projection lives in internal/eval/v2 — so the test asserts
// the callback wiring, not a downstream side-channel.
func TestProcessor_ProjectsEvalCellResult(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-processor-eval-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}

	cfg := &config.Config{ContextDir: tempDir, OllamaURL: "http://localhost:11434", OllamaModel: "mistral:7b"}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	var projected []*journal.EvalCellResultPayload
	proc := New(cfg, store, WithEvalCellResultProjector(func(p *journal.EvalCellResultPayload) error {
		projected = append(projected, p)
		return nil
	}))
	defer proc.Stop()

	classDir := filepath.Join(cfg.ContextDir, "journal", "eval")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	seed := int64(7)
	e, err := journal.NewEvalCellResultEntry(journal.EvalCellResultPayload{
		SchemaVersion:        "1.0",
		RunID:                "eval-run-1",
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		ScenarioID:           "auth-service",
		Harness:              "aider",
		Provider:             "anthropic",
		Model:                "claude-haiku-4.5",
		ContextStrategy:      "cortex",
		Seed:                 &seed,
		Temperature:          0.0,
		TokensIn:             1200,
		TokensOut:            450,
		TaskSuccess:          true,
		TaskSuccessCriterion: "all_tests_pass",
	})
	if err != nil {
		t.Fatalf("NewEvalCellResultEntry: %v", err)
	}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.Close()

	n, err := proc.RunBatch()
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("indexed = %d, want 1", n)
	}
	if len(projected) != 1 {
		t.Fatalf("projector calls = %d, want 1", len(projected))
	}
	if projected[0].RunID != "eval-run-1" {
		t.Errorf("RunID=%q want %q", projected[0].RunID, "eval-run-1")
	}
}

// TestProcessor_NoEvalProjector_SkipsEntries verifies the default
// behavior: when no projector is wired, eval entries advance the cursor
// without side effects. Rebuild resets the cursor so a later run with a
// wired projector picks up everything.
func TestProcessor_NoEvalProjector_SkipsEntries(t *testing.T) {
	proc, cfg, cleanup := setupTestProcessor(t)
	defer cleanup()

	classDir := filepath.Join(cfg.ContextDir, "journal", "eval")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("journal writer: %v", err)
	}
	e, err := journal.NewEvalCellResultEntry(journal.EvalCellResultPayload{
		RunID: "x", ScenarioID: "y",
	})
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("append: %v", err)
	}
	w.Close()

	if _, err := proc.RunBatch(); err != nil {
		t.Fatalf("RunBatch: %v", err)
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
