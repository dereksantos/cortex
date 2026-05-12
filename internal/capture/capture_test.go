package capture

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// readJournalEvents reads every capture.event entry from the journal under
// contextDir and returns the parsed events. Test helper for the journal
// cutover (replaces direct .cortex/queue/pending/*.json filesystem checks).
func readJournalEvents(t *testing.T, contextDir string) []*events.Event {
	t.Helper()
	classDir := filepath.Join(contextDir, "journal", "capture")
	r, err := journal.NewReader(classDir)
	if err != nil {
		t.Fatalf("journal reader: %v", err)
	}
	defer r.Close()

	var out []*events.Event
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("journal next: %v", err)
		}
		if e.Type != "capture.event" {
			t.Errorf("unexpected entry type in capture journal: %q", e.Type)
			continue
		}
		ev, err := events.FromJSON(e.Payload)
		if err != nil {
			t.Fatalf("parse event payload at offset %d: %v", e.Offset, err)
		}
		out = append(out, ev)
	}
	return out
}

// findCaptured returns the captured event with the given ID, or nil if
// absent.
func findCaptured(t *testing.T, contextDir, id string) *events.Event {
	t.Helper()
	for _, ev := range readJournalEvents(t, contextDir) {
		if ev.ID == id {
			return ev
		}
	}
	return nil
}

func TestCapture_CaptureEvent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{".git", "node_modules"},
	}
	cap := New(cfg)

	event := &events.Event{
		ID:        "test-event-123",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{"file_path": "test.go"},
		ToolResult: "success",
		Context: events.EventContext{
			ProjectPath: "/test/project",
			SessionID:   "session-1",
		},
	}

	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	got := findCaptured(t, tempDir, "test-event-123")
	if got == nil {
		t.Fatal("event not present in journal")
	}
	if got.ID != "test-event-123" {
		t.Errorf("ID = %q, want test-event-123", got.ID)
	}
	if got.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit", got.ToolName)
	}
}

func TestCapture_SkipPatterns(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{".git", "node_modules"},
	}
	cap := New(cfg)

	skipped := &events.Event{
		ID:        "skip-event-123",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{
			"file_path": "node_modules/package.json",
		},
		ToolResult: "success",
	}
	if err := cap.CaptureEvent(skipped); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}
	if got := findCaptured(t, tempDir, "skip-event-123"); got != nil {
		t.Error("skipped event should not be in journal")
	}
}

func TestCapture_GenerateID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)

	event := &events.Event{
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}
	if event.ID == "" {
		t.Error("Event ID should be generated")
	}
}

func TestGenerateEventID(t *testing.T) {
	id1 := generateEventID()
	time.Sleep(1 * time.Millisecond)
	id2 := generateEventID()

	if id1 == "" {
		t.Error("Generated ID should not be empty")
	}
	if id1 == id2 {
		t.Error("Generated IDs should be unique")
	}
	if len(id1) < 15 {
		t.Errorf("Generated ID seems too short: %s", id1)
	}
}

func TestCapture_NoTempFilesInJournalDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)

	event := &events.Event{
		ID:        "atomic-test",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Journal uses direct append, not write-temp-then-rename. Verify no
	// stray .tmp leftovers in the class directory.
	classDir := filepath.Join(tempDir, "journal", "capture")
	tmps, _ := filepath.Glob(filepath.Join(classDir, "*.tmp"))
	if len(tmps) != 0 {
		t.Errorf("found unexpected .tmp files in journal: %v", tmps)
	}
	if got := findCaptured(t, tempDir, "atomic-test"); got == nil {
		t.Error("event not captured")
	}
}

func TestCapture_SkipRoutineBashCommands(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{},
	}
	cap := New(cfg)

	routineCommands := []string{"ls", "pwd", "echo", "cd", "which", "date"}
	for _, cmd := range routineCommands {
		t.Run("skips "+cmd, func(t *testing.T) {
			id := "bash-" + cmd
			event := &events.Event{
				ID:        id,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Bash",
				ToolInput: map[string]interface{}{"command": cmd},
				ToolResult: "output",
			}
			if err := cap.CaptureEvent(event); err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}
			if got := findCaptured(t, tempDir, id); got != nil {
				t.Errorf("Routine command %q should be skipped", cmd)
			}
		})
	}
}

func TestCapture_AllowNonRoutineBashCommands(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{},
	}
	cap := New(cfg)

	allowedCommands := []string{"git status", "go build", "npm install", "make test"}
	for _, cmd := range allowedCommands {
		t.Run("allows "+cmd, func(t *testing.T) {
			id := "bash-allowed-" + filepath.Base(cmd)
			event := &events.Event{
				ID:        id,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Bash",
				ToolInput: map[string]interface{}{"command": cmd},
				ToolResult: "success",
			}
			if err := cap.CaptureEvent(event); err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}
			if got := findCaptured(t, tempDir, id); got == nil {
				t.Errorf("Non-routine command %q should be captured", cmd)
			}
		})
	}
}

func TestCapture_MultipleSkipPatterns(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{".git", "node_modules", "vendor", "__pycache__"},
	}
	cap := New(cfg)

	tests := []struct {
		name       string
		filePath   string
		shouldSkip bool
	}{
		{"skips .git files", ".git/config", true},
		{"skips node_modules", "node_modules/lodash/index.js", true},
		{"skips vendor", "vendor/github.com/pkg/errors/errors.go", true},
		{"skips __pycache__", "__pycache__/module.cpython-39.pyc", true},
		{"allows src files", "src/main.go", false},
		{"allows regular files", "README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "pattern-test-" + filepath.Base(tt.filePath)
			event := &events.Event{
				ID:        id,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Edit",
				ToolInput: map[string]interface{}{"file_path": tt.filePath},
				ToolResult: "modified",
			}
			if err := cap.CaptureEvent(event); err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}
			got := findCaptured(t, tempDir, id)
			if tt.shouldSkip && got != nil {
				t.Errorf("Expected %s to be skipped", tt.filePath)
			}
			if !tt.shouldSkip && got == nil {
				t.Errorf("Expected %s to be captured", tt.filePath)
			}
		})
	}
}

func TestCapture_SkipByToolResult(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir:   tempDir,
		SkipPatterns: []string{"node_modules"},
	}
	cap := New(cfg)

	event := &events.Event{
		ID:        "result-skip-test",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Grep",
		ToolInput: map[string]interface{}{"pattern": "TODO"},
		ToolResult: "Found in node_modules/pkg/file.js:10",
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}
	if got := findCaptured(t, tempDir, "result-skip-test"); got != nil {
		t.Error("Event with skip pattern in result should be skipped")
	}
}

func TestCapture_LogSlow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)
	cap.logSlow(100 * time.Millisecond)

	logFile := filepath.Join(tempDir, "logs", "capture.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	if len(data) == 0 {
		t.Error("Log file should not be empty")
	}
}

func TestCapture_ConcurrentCaptures(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)

	// 10 concurrent CaptureEvent calls from separate goroutines. Each
	// creates its own journal.Writer; the per-segment flock serializes
	// appends to the shared segment file.
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			event := &events.Event{
				ID:        "concurrent-" + string(rune('a'+idx)),
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Edit",
				ToolInput: map[string]interface{}{"file_path": "file.go"},
			}
			_ = cap.CaptureEvent(event)
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	got := readJournalEvents(t, tempDir)
	if len(got) != 10 {
		t.Errorf("Expected 10 concurrent captures, got %d", len(got))
	}
}

func TestCapture_LogSlow_FileCreation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)
	cap.logSlow(50 * time.Millisecond)
	cap.logSlow(75 * time.Millisecond)
	cap.logSlow(100 * time.Millisecond)

	logFile := filepath.Join(tempDir, "logs", "capture.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	if len(string(data)) < 100 {
		t.Error("Log file should contain multiple entries")
	}
}

func TestCapture_CreatesJournalDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	nestedDir := filepath.Join(tempDir, "deep", "nested", "path")
	cfg := &config.Config{ContextDir: nestedDir}
	cap := New(cfg)

	event := &events.Event{
		ID:        "nested-test",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	classDir := filepath.Join(nestedDir, "journal", "capture")
	if _, err := os.Stat(classDir); os.IsNotExist(err) {
		t.Error("Journal class directory should be created")
	}
	if got := findCaptured(t, nestedDir, "nested-test"); got == nil {
		t.Error("Event should be present in journal")
	}
}

func TestCapture_EventWithComplexMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)

	event := &events.Event{
		ID:        "complex-event",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
			"nested":    map[string]interface{}{"key": "value"},
			"array":     []interface{}{"a", "b", "c"},
		},
		ToolResult: "success",
		Context: events.EventContext{
			ProjectPath: "/complex/path",
			SessionID:   "session-complex",
		},
	}
	if err := cap.CaptureEvent(event); err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}
	if got := findCaptured(t, tempDir, "complex-event"); got == nil {
		t.Error("Complex event should be captured")
	}
}

func TestNew(t *testing.T) {
	cfg := &config.Config{
		ContextDir:   "/test/path",
		SkipPatterns: []string{".git"},
	}
	cap := New(cfg)
	if cap == nil {
		t.Fatal("New should return non-nil Capture")
	}
	if cap.cfg != cfg {
		t.Error("Capture should store config reference")
	}
}

func BenchmarkCapture_CaptureEvent(b *testing.B) {
	tempDir, err := os.MkdirTemp("", "cortex-bench-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{ContextDir: tempDir}
	cap := New(cfg)

	event := &events.Event{
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{"file_path": "test.go"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.ID = "" // Reset ID so it gets generated each time
		if err := cap.CaptureEvent(event); err != nil {
			b.Fatalf("CaptureEvent failed: %v", err)
		}
	}
}
