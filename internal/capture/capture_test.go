package capture

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestCapture_CaptureEvent(t *testing.T) {
	// Create temp directory for test
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
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
		},
		ToolResult: "success",
		Context: events.EventContext{
			ProjectPath: "/test/project",
			SessionID:   "session-1",
		},
	}

	// Capture the event
	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify event file exists
	eventFile := filepath.Join(tempDir, "queue", "pending", "test-event-123.json")
	if _, err := os.Stat(eventFile); os.IsNotExist(err) {
		t.Errorf("Event file not created: %s", eventFile)
	}

	// Verify event file contents
	data, err := os.ReadFile(eventFile)
	if err != nil {
		t.Fatalf("Failed to read event file: %v", err)
	}

	parsedEvent, err := events.FromJSON(data)
	if err != nil {
		t.Fatalf("Failed to parse event file: %v", err)
	}

	if parsedEvent.ID != "test-event-123" {
		t.Errorf("Expected ID 'test-event-123', got '%s'", parsedEvent.ID)
	}

	if parsedEvent.ToolName != "Edit" {
		t.Errorf("Expected ToolName 'Edit', got '%s'", parsedEvent.ToolName)
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

	// Event that should be skipped
	skippedEvent := &events.Event{
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

	err = cap.CaptureEvent(skippedEvent)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify event file was NOT created
	eventFile := filepath.Join(tempDir, "queue", "pending", "skip-event-123.json")
	if _, err := os.Stat(eventFile); !os.IsNotExist(err) {
		t.Errorf("Event file should not exist for skipped event: %s", eventFile)
	}
}

func TestCapture_GenerateID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	// Event without ID
	event := &events.Event{
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}

	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify ID was generated
	if event.ID == "" {
		t.Error("Event ID should be generated")
	}
}

func TestGenerateEventID(t *testing.T) {
	// Generate multiple IDs
	id1 := generateEventID()
	time.Sleep(1 * time.Millisecond)
	id2 := generateEventID()

	if id1 == "" {
		t.Error("Generated ID should not be empty")
	}

	if id1 == id2 {
		t.Error("Generated IDs should be unique")
	}

	// Check format (should contain timestamp and random suffix)
	if len(id1) < 15 {
		t.Errorf("Generated ID seems too short: %s", id1)
	}
}

func TestCapture_AtomicWrite(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	event := &events.Event{
		ID:        "atomic-test",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}

	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify temp file doesn't exist (should be renamed)
	tempFile := filepath.Join(tempDir, "queue", "pending", "atomic-test.tmp")
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Error("Temp file should not exist after atomic rename")
	}

	// Verify final file exists
	finalFile := filepath.Join(tempDir, "queue", "pending", "atomic-test.json")
	if _, err := os.Stat(finalFile); os.IsNotExist(err) {
		t.Error("Final file should exist after atomic rename")
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
			event := &events.Event{
				ID:        "bash-" + cmd,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Bash",
				ToolInput: map[string]interface{}{
					"command": cmd,
				},
				ToolResult: "output",
			}

			err := cap.CaptureEvent(event)
			if err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}

			// Verify event file was NOT created (skipped)
			eventFile := filepath.Join(tempDir, "queue", "pending", "bash-"+cmd+".json")
			if _, err := os.Stat(eventFile); !os.IsNotExist(err) {
				t.Errorf("Routine command '%s' should be skipped", cmd)
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
			eventID := "bash-allowed-" + filepath.Base(cmd)
			event := &events.Event{
				ID:        eventID,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Bash",
				ToolInput: map[string]interface{}{
					"command": cmd,
				},
				ToolResult: "success",
			}

			err := cap.CaptureEvent(event)
			if err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}

			// Verify event file WAS created
			eventFile := filepath.Join(tempDir, "queue", "pending", eventID+".json")
			if _, err := os.Stat(eventFile); os.IsNotExist(err) {
				t.Errorf("Non-routine command '%s' should be captured", cmd)
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
			eventID := "pattern-test-" + filepath.Base(tt.filePath)
			event := &events.Event{
				ID:        eventID,
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Edit",
				ToolInput: map[string]interface{}{
					"file_path": tt.filePath,
				},
				ToolResult: "modified",
			}

			err := cap.CaptureEvent(event)
			if err != nil {
				t.Fatalf("CaptureEvent failed: %v", err)
			}

			eventFile := filepath.Join(tempDir, "queue", "pending", eventID+".json")
			exists := true
			if _, err := os.Stat(eventFile); os.IsNotExist(err) {
				exists = false
			}

			if tt.shouldSkip && exists {
				t.Errorf("Expected %s to be skipped", tt.filePath)
			}
			if !tt.shouldSkip && !exists {
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

	// Event with node_modules in tool result (not file path)
	event := &events.Event{
		ID:        "result-skip-test",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Grep",
		ToolInput: map[string]interface{}{
			"pattern": "TODO",
		},
		ToolResult: "Found in node_modules/pkg/file.js:10",
	}

	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Should be skipped because tool result contains skip pattern
	eventFile := filepath.Join(tempDir, "queue", "pending", "result-skip-test.json")
	if _, err := os.Stat(eventFile); !os.IsNotExist(err) {
		t.Error("Event with skip pattern in result should be skipped")
	}
}

func TestCapture_LogSlow(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	// Manually call logSlow
	cap.logSlow(100 * time.Millisecond)

	// Verify log file was created
	logFile := filepath.Join(tempDir, "logs", "capture.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	content := string(data)
	if content == "" {
		t.Error("Log file should not be empty")
	}
	if !filepath.IsAbs(logFile) {
		t.Error("Log file path should be absolute")
	}
}

func TestCapture_ConcurrentCaptures(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	// Capture multiple events concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			event := &events.Event{
				ID:        "concurrent-" + string(rune('a'+idx)),
				Source:    events.SourceClaude,
				EventType: events.EventToolUse,
				Timestamp: time.Now(),
				ToolName:  "Edit",
				ToolInput: map[string]interface{}{
					"file_path": "file.go",
				},
			}
			cap.CaptureEvent(event)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all events were captured
	files, _ := filepath.Glob(filepath.Join(tempDir, "queue", "pending", "concurrent-*.json"))
	if len(files) != 10 {
		t.Errorf("Expected 10 concurrent captures, got %d", len(files))
	}
}

func TestCapture_LogSlow_FileCreation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	// Call logSlow multiple times to verify file append
	cap.logSlow(50 * time.Millisecond)
	cap.logSlow(75 * time.Millisecond)
	cap.logSlow(100 * time.Millisecond)

	logFile := filepath.Join(tempDir, "logs", "capture.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	content := string(data)
	// Should have 3 log entries
	if len(content) < 100 {
		t.Error("Log file should contain multiple entries")
	}
}

func TestCapture_WriteToQueue_CreatesDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Use a nested path that doesn't exist
	nestedDir := filepath.Join(tempDir, "deep", "nested", "path")
	cfg := &config.Config{
		ContextDir: nestedDir,
	}

	cap := New(cfg)

	event := &events.Event{
		ID:        "nested-test",
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
	}

	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify the directory was created
	queueDir := filepath.Join(nestedDir, "queue", "pending")
	if _, err := os.Stat(queueDir); os.IsNotExist(err) {
		t.Error("Queue directory should be created")
	}

	// Verify event file exists
	eventFile := filepath.Join(queueDir, "nested-test.json")
	if _, err := os.Stat(eventFile); os.IsNotExist(err) {
		t.Error("Event file should be created")
	}
}

func TestCapture_EventWithComplexMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	event := &events.Event{
		ID:        "complex-event",
		Source:    events.SourceClaude,
		EventType: events.EventToolUse,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
			"nested": map[string]interface{}{
				"key": "value",
			},
			"array": []interface{}{"a", "b", "c"},
		},
		ToolResult: "success",
		Context: events.EventContext{
			ProjectPath: "/complex/path",
			SessionID:   "session-complex",
		},
	}

	err = cap.CaptureEvent(event)
	if err != nil {
		t.Fatalf("CaptureEvent failed: %v", err)
	}

	// Verify event file exists
	eventFile := filepath.Join(tempDir, "queue", "pending", "complex-event.json")
	if _, err := os.Stat(eventFile); os.IsNotExist(err) {
		t.Error("Event file should be created")
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

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	cap := New(cfg)

	event := &events.Event{
		Source:    events.SourceClaude,
		EventType: events.EventEdit,
		Timestamp: time.Now(),
		ToolName:  "Edit",
		ToolInput: map[string]interface{}{
			"file_path": "test.go",
		},
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event.ID = "" // Reset ID so it gets generated each time
		err := cap.CaptureEvent(event)
		if err != nil {
			b.Fatalf("CaptureEvent failed: %v", err)
		}
	}
}
