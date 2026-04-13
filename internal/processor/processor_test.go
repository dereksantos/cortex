package processor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/queue"
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
		ContextDir:  tempDir,
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "mistral:7b",
	}

	store, err := storage.New(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	queueMgr := queue.New(cfg, store)
	processor := New(cfg, store, queueMgr)

	cleanup := func() {
		processor.Stop()
		store.Close()
		os.RemoveAll(tempDir)
	}

	return processor, cfg, cleanup
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
	if processor.queue == nil {
		t.Fatal("expected non-nil queue")
	}
}

func TestProcessor_StartStop(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	t.Run("starts successfully", func(t *testing.T) {
		err := processor.Start()
		if err != nil {
			t.Fatalf("failed to start processor: %v", err)
		}
		if !processor.running.Load() {
			t.Error("processor should be running after Start")
		}
	})

	t.Run("prevents double start", func(t *testing.T) {
		err := processor.Start()
		if err == nil {
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
		err := processor.Start()
		if err != nil {
			t.Fatalf("failed to restart processor: %v", err)
		}
		processor.Stop()
	})
}

func TestProcessor_SetEventCallback(t *testing.T) {
	processor, _, cleanup := setupTestProcessor(t)
	defer cleanup()

	called := false
	processor.SetEventCallback(func(events []*events.Event) {
		called = true
	})

	if processor.eventCallback == nil {
		t.Error("expected eventCallback to be set")
	}

	// Simulate callback invocation
	processor.eventCallback([]*events.Event{})
	if !called {
		t.Error("expected callback to be called")
	}
}
