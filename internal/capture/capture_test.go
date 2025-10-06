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
