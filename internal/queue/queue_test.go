package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func setupTestQueue(t *testing.T) (*Manager, *config.Config, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "cortex-queue-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Create directory structure
	dirs := []string{
		filepath.Join(tempDir, "queue", "pending"),
		filepath.Join(tempDir, "queue", "processing"),
		filepath.Join(tempDir, "queue", "processed"),
		filepath.Join(tempDir, "db"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			os.RemoveAll(tempDir)
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	store, err := storage.New(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	manager := New(cfg, store)

	cleanup := func() {
		store.Close()
		os.RemoveAll(tempDir)
	}

	return manager, cfg, cleanup
}

func createTestEventFile(t *testing.T, dir, id string) {
	t.Helper()

	event := &events.Event{
		ID:         id,
		Source:     events.SourceClaude,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "Edit",
		ToolInput:  map[string]interface{}{"file_path": "/test/file.go"},
		ToolResult: "modified file",
		Context: events.EventContext{
			ProjectPath: "/test",
			SessionID:   "test-session",
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	filePath := filepath.Join(dir, id+".json")
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatalf("failed to write event file: %v", err)
	}
}

func TestNew(t *testing.T) {
	manager, _, cleanup := setupTestQueue(t)
	defer cleanup()

	if manager == nil {
		t.Fatal("expected non-nil manager")
	}
	if manager.cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if manager.storage == nil {
		t.Fatal("expected non-nil storage")
	}
}

func TestGetPendingCount(t *testing.T) {
	manager, cfg, cleanup := setupTestQueue(t)
	defer cleanup()

	pendingDir := filepath.Join(cfg.ContextDir, "queue", "pending")

	t.Run("returns zero for empty queue", func(t *testing.T) {
		count, err := manager.GetPendingCount()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0 pending, got %d", count)
		}
	})

	t.Run("counts pending files", func(t *testing.T) {
		// Create some pending files
		createTestEventFile(t, pendingDir, "event-1")
		createTestEventFile(t, pendingDir, "event-2")
		createTestEventFile(t, pendingDir, "event-3")

		count, err := manager.GetPendingCount()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 3 {
			t.Errorf("expected 3 pending, got %d", count)
		}
	})

	t.Run("ignores non-json files", func(t *testing.T) {
		// Create a non-json file
		nonJsonPath := filepath.Join(pendingDir, "not-an-event.txt")
		os.WriteFile(nonJsonPath, []byte("test"), 0644)

		count, err := manager.GetPendingCount()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should still be 3 from previous test (only counts .json)
		if count != 3 {
			t.Errorf("expected 3 pending (ignoring non-json), got %d", count)
		}
	})
}

func TestProcessPending(t *testing.T) {
	t.Run("processes pending events", func(t *testing.T) {
		manager, cfg, cleanup := setupTestQueue(t)
		defer cleanup()

		pendingDir := filepath.Join(cfg.ContextDir, "queue", "pending")
		processedDir := filepath.Join(cfg.ContextDir, "queue", "processed")

		// Create pending events
		createTestEventFile(t, pendingDir, "process-event-1")
		createTestEventFile(t, pendingDir, "process-event-2")

		processed, err := manager.ProcessPending()
		if err != nil {
			t.Fatalf("failed to process pending: %v", err)
		}

		if processed != 2 {
			t.Errorf("expected 2 processed, got %d", processed)
		}

		// Check pending is empty
		pendingCount, _ := manager.GetPendingCount()
		if pendingCount != 0 {
			t.Errorf("expected 0 pending after processing, got %d", pendingCount)
		}

		// Check processed directory has files
		processedFiles, _ := filepath.Glob(filepath.Join(processedDir, "*.json"))
		if len(processedFiles) != 2 {
			t.Errorf("expected 2 processed files, got %d", len(processedFiles))
		}
	})

	t.Run("handles empty queue", func(t *testing.T) {
		manager, _, cleanup := setupTestQueue(t)
		defer cleanup()

		processed, err := manager.ProcessPending()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if processed != 0 {
			t.Errorf("expected 0 processed for empty queue, got %d", processed)
		}
	})

	t.Run("skips invalid JSON files", func(t *testing.T) {
		manager, cfg, cleanup := setupTestQueue(t)
		defer cleanup()

		pendingDir := filepath.Join(cfg.ContextDir, "queue", "pending")

		// Create valid event
		createTestEventFile(t, pendingDir, "valid-event")

		// Create invalid JSON file
		invalidPath := filepath.Join(pendingDir, "invalid.json")
		os.WriteFile(invalidPath, []byte("not valid json"), 0644)

		processed, err := manager.ProcessPending()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should only process the valid one
		if processed != 1 {
			t.Errorf("expected 1 processed (skipping invalid), got %d", processed)
		}

		// Invalid file should still be in pending (moved back)
		pendingFiles, _ := filepath.Glob(filepath.Join(pendingDir, "*.json"))
		if len(pendingFiles) != 1 {
			t.Errorf("expected invalid file to remain in pending, got %d files", len(pendingFiles))
		}
	})

	t.Run("stores events in database", func(t *testing.T) {
		manager, cfg, cleanup := setupTestQueue(t)
		defer cleanup()

		pendingDir := filepath.Join(cfg.ContextDir, "queue", "pending")
		createTestEventFile(t, pendingDir, "db-test-event")

		_, err := manager.ProcessPending()
		if err != nil {
			t.Fatalf("failed to process: %v", err)
		}

		// Verify event is in storage
		event, err := manager.storage.GetEvent("db-test-event")
		if err != nil {
			t.Fatalf("failed to get event from storage: %v", err)
		}
		if event.ID != "db-test-event" {
			t.Errorf("expected event ID 'db-test-event', got %q", event.ID)
		}
	})
}

func TestCleanProcessed(t *testing.T) {
	manager, cfg, cleanup := setupTestQueue(t)
	defer cleanup()

	processedDir := filepath.Join(cfg.ContextDir, "queue", "processed")

	t.Run("removes processed files", func(t *testing.T) {
		// Create some processed files
		for i := 0; i < 3; i++ {
			filePath := filepath.Join(processedDir, "processed-"+string(rune('a'+i))+".json")
			os.WriteFile(filePath, []byte("{}"), 0644)
		}

		// Verify files exist
		files, _ := filepath.Glob(filepath.Join(processedDir, "*.json"))
		if len(files) != 3 {
			t.Fatalf("expected 3 files before clean, got %d", len(files))
		}

		err := manager.CleanProcessed()
		if err != nil {
			t.Fatalf("failed to clean processed: %v", err)
		}

		// Verify files are gone
		files, _ = filepath.Glob(filepath.Join(processedDir, "*.json"))
		if len(files) != 0 {
			t.Errorf("expected 0 files after clean, got %d", len(files))
		}
	})

	t.Run("handles empty processed directory", func(t *testing.T) {
		err := manager.CleanProcessed()
		if err != nil {
			t.Fatalf("unexpected error for empty directory: %v", err)
		}
	})
}

func TestProcessPendingIdempotent(t *testing.T) {
	manager, cfg, cleanup := setupTestQueue(t)
	defer cleanup()

	pendingDir := filepath.Join(cfg.ContextDir, "queue", "pending")
	createTestEventFile(t, pendingDir, "idempotent-event")

	// Process once
	processed1, _ := manager.ProcessPending()
	if processed1 != 1 {
		t.Fatalf("expected 1 processed first time, got %d", processed1)
	}

	// Process again - should be 0 (already processed)
	processed2, _ := manager.ProcessPending()
	if processed2 != 0 {
		t.Errorf("expected 0 processed second time, got %d", processed2)
	}
}
