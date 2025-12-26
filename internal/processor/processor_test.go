package processor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func TestProcessor_shouldAnalyze(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	tests := []struct {
		name  string
		event *events.Event
		want  bool
	}{
		{
			name: "should skip Read operations",
			event: &events.Event{
				ToolName: "Read",
			},
			want: false,
		},
		{
			name: "should skip Grep operations",
			event: &events.Event{
				ToolName: "Grep",
			},
			want: false,
		},
		{
			name: "should analyze Edit",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "main.go",
				},
			},
			want: true,
		},
		{
			name: "should analyze Write",
			event: &events.Event{
				ToolName:  "Write",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "test.go",
				},
			},
			want: true,
		},
		{
			name: "should skip binary files (.pyc)",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "script.pyc",
				},
			},
			want: false,
		},
		{
			name: "should skip image files (.png)",
			event: &events.Event{
				ToolName:  "Write",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "logo.png",
				},
			},
			want: false,
		},
		{
			name: "should skip lock files",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "package-lock.json",
				},
			},
			want: false,
		},
		{
			name: "should analyze markdown files",
			event: &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "README.md",
				},
			},
			want: true,
		},
		{
			name: "should analyze Task events",
			event: &events.Event{
				ToolName:  "Task",
				Timestamp: time.Now(),
			},
			want: true,
		},
		{
			name: "should analyze Bash events",
			event: &events.Event{
				ToolName:  "Bash",
				Timestamp: time.Now(),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.shouldAnalyze(tt.event)
			if got != tt.want {
				t.Errorf("shouldAnalyze() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessor_Deduplication(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	now := time.Now()

	// First edit to a file
	event1 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now,
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should analyze first time
	if !p.shouldAnalyze(event1) {
		t.Error("First edit should be analyzed")
	}

	// Second edit to same file within 30 seconds
	event2 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now.Add(15 * time.Second),
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should be skipped (deduplicated)
	if p.shouldAnalyze(event2) {
		t.Error("Second edit within 30s should be skipped (deduplicated)")
	}

	// Third edit to same file after 30 seconds
	event3 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now.Add(35 * time.Second),
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
		},
	}

	// Should analyze again
	if !p.shouldAnalyze(event3) {
		t.Error("Edit after 30s should be analyzed")
	}

	// Edit to different file
	event4 := &events.Event{
		ToolName:  "Edit",
		Timestamp: now,
		ToolInput: map[string]interface{}{
			"file_path": "other.go",
		},
	}

	// Should analyze (different file)
	if !p.shouldAnalyze(event4) {
		t.Error("Edit to different file should be analyzed")
	}
}

func TestProcessor_FileExtensionFiltering(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	binaryExtensions := []string{
		".pyc", ".o", ".class", ".lock",
		".png", ".jpg", ".jpeg", ".gif", ".svg",
		".zip", ".tar", ".gz", ".pdf",
	}

	for _, ext := range binaryExtensions {
		t.Run("should skip "+ext, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "file" + ext,
				},
			}

			if p.shouldAnalyze(event) {
				t.Errorf("Files with extension %s should be skipped", ext)
			}
		})
	}

	// Test files that SHOULD be analyzed
	allowedExtensions := []string{
		".go", ".py", ".js", ".ts", ".md", ".txt",
		".json", ".yaml", ".yml", ".toml",
	}

	for _, ext := range allowedExtensions {
		t.Run("should analyze "+ext, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": "file" + ext,
				},
			}

			if !p.shouldAnalyze(event) {
				t.Errorf("Files with extension %s should be analyzed", ext)
			}
		})
	}
}

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
	if processor.lastProcessed == nil {
		t.Fatal("expected non-nil lastProcessed map")
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
		if !processor.running {
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
		if processor.running {
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

func TestProcessor_LockFileSkipping(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	lockFiles := []string{
		"package-lock.json",
		"yarn.lock",
		"Gemfile.lock",
		"poetry.lock",
		"pnpm-lock.yaml",
		"composer.lock",
	}

	for _, lockFile := range lockFiles {
		t.Run("skips "+lockFile, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": lockFile,
				},
			}

			if p.shouldAnalyze(event) {
				t.Errorf("Lock file %s should be skipped", lockFile)
			}
		})
	}
}

func TestProcessor_HelperFunctions(t *testing.T) {
	t.Run("contains finds substring", func(t *testing.T) {
		if !contains("hello world", "world") {
			t.Error("should find 'world' in 'hello world'")
		}
		if contains("hello", "world") {
			t.Error("should not find 'world' in 'hello'")
		}
		if contains("", "test") {
			t.Error("should not find substring in empty string")
		}
		// Note: contains("test", "") returns true in current implementation
		// because findSubstring finds empty string at position 0
	})

	t.Run("toLower converts correctly", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"HELLO", "hello"},
			{"Hello World", "hello world"},
			{"already lowercase", "already lowercase"},
			{"MiXeD123", "mixed123"},
			{"", ""},
		}

		for _, tt := range tests {
			result := toLower(tt.input)
			if result != tt.expected {
				t.Errorf("toLower(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		}
	})
}

func TestProcessor_NoFilePath(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	// Event without file_path in ToolInput
	event := &events.Event{
		ToolName:  "Task",
		Timestamp: time.Now(),
		ToolInput: map[string]interface{}{
			"description": "some task",
		},
	}

	// Should still analyze (Task is in allowed tools)
	if !p.shouldAnalyze(event) {
		t.Error("Task events without file_path should be analyzed")
	}
}

func TestProcessor_DeduplicationWithDifferentTools(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	now := time.Now()
	filePath := "main.go"

	// First: Edit to file
	edit := &events.Event{
		ToolName:  "Edit",
		Timestamp: now,
		ToolInput: map[string]interface{}{
			"file_path": filePath,
		},
	}
	if !p.shouldAnalyze(edit) {
		t.Error("First edit should be analyzed")
	}

	// Second: Write to same file within 30s
	write := &events.Event{
		ToolName:  "Write",
		Timestamp: now.Add(10 * time.Second),
		ToolInput: map[string]interface{}{
			"file_path": filePath,
		},
	}

	// Should be deduplicated (same file, regardless of tool)
	if p.shouldAnalyze(write) {
		t.Error("Write to same file within 30s should be deduplicated")
	}
}

func TestProcessor_CaseInsensitiveLockDetection(t *testing.T) {
	p := &Processor{
		lastProcessed: make(map[string]time.Time),
	}

	// Test case variations
	lockFiles := []string{
		"Package-Lock.json",
		"YARN.LOCK",
		"Gemfile.LOCK",
	}

	for _, lockFile := range lockFiles {
		t.Run("skips "+lockFile, func(t *testing.T) {
			event := &events.Event{
				ToolName:  "Edit",
				Timestamp: time.Now(),
				ToolInput: map[string]interface{}{
					"file_path": lockFile,
				},
			}

			if p.shouldAnalyze(event) {
				t.Errorf("Lock file %s should be skipped (case insensitive)", lockFile)
			}
		})
	}
}
