package cognition

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaemonState_WriteAndRead(t *testing.T) {
	// Create temp directory
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create state writer
	sw := NewStateWriter(tempDir)

	// Test writing mode
	if err := sw.WriteMode("think", "learning patterns"); err != nil {
		t.Fatalf("WriteMode failed: %v", err)
	}

	// Read back
	state, err := ReadDaemonState(sw.Path())
	if err != nil {
		t.Fatalf("ReadDaemonState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected state, got nil")
	}
	if state.Mode != "think" {
		t.Errorf("expected mode 'think', got '%s'", state.Mode)
	}
	if state.Description != "learning patterns" {
		t.Errorf("expected description 'learning patterns', got '%s'", state.Description)
	}
}

func TestDaemonState_WriteModeWithStats(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sw := NewStateWriter(tempDir)

	// Write with stats
	if err := sw.WriteModeWithStats("idle", "", 100, 25); err != nil {
		t.Fatalf("WriteModeWithStats failed: %v", err)
	}

	// Read back
	state, err := ReadDaemonState(sw.Path())
	if err != nil {
		t.Fatalf("ReadDaemonState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected state, got nil")
	}
	if state.Stats.Events != 100 {
		t.Errorf("expected 100 events, got %d", state.Stats.Events)
	}
	if state.Stats.Insights != 25 {
		t.Errorf("expected 25 insights, got %d", state.Stats.Insights)
	}
}

func TestDaemonState_StaleCheck(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Write state directly with old timestamp
	statePath := filepath.Join(tempDir, "daemon_state.json")
	oldState := `{
		"mode": "think",
		"description": "old state",
		"updated": "2020-01-01T00:00:00Z",
		"stats": {"events": 10, "insights": 5}
	}`
	if err := os.WriteFile(statePath, []byte(oldState), 0644); err != nil {
		t.Fatalf("failed to write stale state: %v", err)
	}

	// Read should return nil for stale state
	state, err := ReadDaemonState(statePath)
	if err != nil {
		t.Fatalf("ReadDaemonState failed: %v", err)
	}
	if state != nil {
		t.Error("expected nil for stale state, got non-nil")
	}
}

func TestDaemonState_MissingFile(t *testing.T) {
	// Non-existent file should return nil, no error
	state, err := ReadDaemonState("/nonexistent/path/daemon_state.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if state != nil {
		t.Error("expected nil for missing file")
	}
}

func TestDaemonState_UpdatedTimestamp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sw := NewStateWriter(tempDir)

	before := time.Now()
	if err := sw.WriteMode("dream", "exploring"); err != nil {
		t.Fatalf("WriteMode failed: %v", err)
	}
	after := time.Now()

	state, err := ReadDaemonState(sw.Path())
	if err != nil {
		t.Fatalf("ReadDaemonState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected state, got nil")
	}

	// Verify timestamp is between before and after
	if state.Updated.Before(before) || state.Updated.After(after) {
		t.Errorf("timestamp %v not in expected range [%v, %v]", state.Updated, before, after)
	}
}

func TestGetDaemonStatePath(t *testing.T) {
	path := GetDaemonStatePath("/some/context/dir")
	expected := filepath.Join("/some/context/dir", "daemon_state.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestTruncatePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		maxLen   int
		expected string
	}{
		{
			name:     "empty path",
			path:     "",
			maxLen:   20,
			expected: "",
		},
		{
			name:     "short filename fits",
			path:     "/some/long/path/to/file.go",
			maxLen:   20,
			expected: "to/file.go",
		},
		{
			name:     "filename only when parent too long",
			path:     "/some/verylongdirectoryname/file.go",
			maxLen:   10,
			expected: "file.go",
		},
		{
			name:     "truncate long filename",
			path:     "/path/to/very_long_filename_that_exceeds_limit.go",
			maxLen:   20,
			expected: "very_long_filenam...",
		},
		{
			name:     "root path",
			path:     "/file.go",
			maxLen:   20,
			expected: "file.go",
		},
		{
			name:     "parent and filename fit exactly",
			path:     "/foo/bar/test.go",
			maxLen:   12,
			expected: "bar/test.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncatePath(tt.path, tt.maxLen)
			if result != tt.expected {
				t.Errorf("TruncatePath(%q, %d) = %q, want %q", tt.path, tt.maxLen, result, tt.expected)
			}
			if len(result) > tt.maxLen {
				t.Errorf("TruncatePath(%q, %d) result length %d exceeds maxLen %d", tt.path, tt.maxLen, len(result), tt.maxLen)
			}
		})
	}
}

func TestTruncateInsight(t *testing.T) {
	tests := []struct {
		name     string
		insight  string
		maxLen   int
		expected string
	}{
		{
			name:     "short insight fits",
			insight:  "Use Zustand for state",
			maxLen:   30,
			expected: "Use Zustand for state",
		},
		{
			name:     "truncate at word boundary",
			insight:  "Always use Zustand for state management in React apps",
			maxLen:   30,
			expected: "Always use Zustand for...",
		},
		{
			name:     "truncate long single word",
			insight:  "Supercalifragilisticexpialidocious is a long word",
			maxLen:   20,
			expected: "Supercalifragilis...",
		},
		{
			name:     "exact length",
			insight:  "Exact length text",
			maxLen:   17,
			expected: "Exact length text",
		},
		{
			name:     "very short max",
			insight:  "Test",
			maxLen:   3,
			expected: "Tes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateInsight(tt.insight, tt.maxLen)
			if result != tt.expected {
				t.Errorf("TruncateInsight(%q, %d) = %q, want %q", tt.insight, tt.maxLen, result, tt.expected)
			}
			if len(result) > tt.maxLen {
				t.Errorf("TruncateInsight(%q, %d) result length %d exceeds maxLen %d", tt.insight, tt.maxLen, len(result), tt.maxLen)
			}
		})
	}
}
