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

func TestBackgroundMetrics_WriteAndRead(t *testing.T) {
	// Create temp directory
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create metrics writer
	writer := NewBackgroundMetricsWriter(tempDir)

	// Test writing metrics
	metrics := &BackgroundMetrics{
		ThinkBudget:     3,
		ThinkMaxBudget:  5,
		DreamQueueDepth: 2,
		DreamBudget:     15,
		DreamMaxBudget:  20,
		ActivityLevel:   0.65,
		IdleSeconds:     45,
		CacheHitRate:    0.75,
		CacheHits:       30,
		CacheMisses:     10,
		InsightsSession: 5,
	}

	if err := writer.WriteMetrics(metrics); err != nil {
		t.Fatalf("WriteMetrics failed: %v", err)
	}

	// Read back
	readMetrics, err := ReadBackgroundMetrics(tempDir)
	if err != nil {
		t.Fatalf("ReadBackgroundMetrics failed: %v", err)
	}
	if readMetrics == nil {
		t.Fatal("expected metrics, got nil")
	}

	// Verify all fields
	if readMetrics.ThinkBudget != 3 {
		t.Errorf("expected ThinkBudget 3, got %d", readMetrics.ThinkBudget)
	}
	if readMetrics.ThinkMaxBudget != 5 {
		t.Errorf("expected ThinkMaxBudget 5, got %d", readMetrics.ThinkMaxBudget)
	}
	if readMetrics.DreamQueueDepth != 2 {
		t.Errorf("expected DreamQueueDepth 2, got %d", readMetrics.DreamQueueDepth)
	}
	if readMetrics.DreamBudget != 15 {
		t.Errorf("expected DreamBudget 15, got %d", readMetrics.DreamBudget)
	}
	if readMetrics.DreamMaxBudget != 20 {
		t.Errorf("expected DreamMaxBudget 20, got %d", readMetrics.DreamMaxBudget)
	}
	if readMetrics.ActivityLevel != 0.65 {
		t.Errorf("expected ActivityLevel 0.65, got %f", readMetrics.ActivityLevel)
	}
	if readMetrics.IdleSeconds != 45 {
		t.Errorf("expected IdleSeconds 45, got %d", readMetrics.IdleSeconds)
	}
	if readMetrics.CacheHitRate != 0.75 {
		t.Errorf("expected CacheHitRate 0.75, got %f", readMetrics.CacheHitRate)
	}
	if readMetrics.CacheHits != 30 {
		t.Errorf("expected CacheHits 30, got %d", readMetrics.CacheHits)
	}
	if readMetrics.CacheMisses != 10 {
		t.Errorf("expected CacheMisses 10, got %d", readMetrics.CacheMisses)
	}
	if readMetrics.InsightsSession != 5 {
		t.Errorf("expected InsightsSession 5, got %d", readMetrics.InsightsSession)
	}
}

func TestBackgroundMetrics_StaleCheck(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Write metrics directly with old timestamp
	metricsPath := filepath.Join(tempDir, "background_metrics.json")
	oldMetrics := `{
		"think_budget": 3,
		"dream_budget": 15,
		"activity_level": 0.5,
		"idle_seconds": 30,
		"cache_hit_rate": 0.5,
		"updated_at": "2020-01-01T00:00:00Z"
	}`
	if err := os.WriteFile(metricsPath, []byte(oldMetrics), 0644); err != nil {
		t.Fatalf("failed to write stale metrics: %v", err)
	}

	// Read should return nil for stale metrics
	metrics, err := ReadBackgroundMetrics(tempDir)
	if err != nil {
		t.Fatalf("ReadBackgroundMetrics failed: %v", err)
	}
	if metrics != nil {
		t.Error("expected nil for stale metrics, got non-nil")
	}
}

func TestBackgroundMetrics_MissingFile(t *testing.T) {
	// Non-existent directory should return nil, no error
	metrics, err := ReadBackgroundMetrics("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if metrics != nil {
		t.Error("expected nil for missing file")
	}
}

func TestBackgroundMetrics_UpdatedTimestamp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	writer := NewBackgroundMetricsWriter(tempDir)

	before := time.Now()
	if err := writer.WriteMetrics(&BackgroundMetrics{
		ThinkBudget: 3,
		DreamBudget: 15,
	}); err != nil {
		t.Fatalf("WriteMetrics failed: %v", err)
	}
	after := time.Now()

	metrics, err := ReadBackgroundMetrics(tempDir)
	if err != nil {
		t.Fatalf("ReadBackgroundMetrics failed: %v", err)
	}
	if metrics == nil {
		t.Fatal("expected metrics, got nil")
	}

	// Verify timestamp is between before and after
	if metrics.UpdatedAt.Before(before) || metrics.UpdatedAt.After(after) {
		t.Errorf("timestamp %v not in expected range [%v, %v]", metrics.UpdatedAt, before, after)
	}
}

func TestGetBackgroundMetricsPath(t *testing.T) {
	path := GetBackgroundMetricsPath("/some/context/dir")
	expected := filepath.Join("/some/context/dir", "background_metrics.json")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestBackgroundMetricsWriter_Path(t *testing.T) {
	writer := NewBackgroundMetricsWriter("/test/dir")
	expected := filepath.Join("/test/dir", "background_metrics.json")
	if writer.Path() != expected {
		t.Errorf("expected %s, got %s", expected, writer.Path())
	}
}

func TestRetrievalStatsHistoryWriter_AppendEntry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	writer := NewRetrievalStatsHistoryWriter(tempDir)

	// Append first entry
	entry1 := &RetrievalStatsHistoryEntry{
		Timestamp: time.Now(),
		Query:     "auth flow",
		Mode:      "fast",
		ReflexMs:  12,
		ReflectMs: 0,
		ResolveMs: 5,
		Results:   3,
		Decision:  "inject",
	}
	if err := writer.AppendEntry(entry1); err != nil {
		t.Fatalf("AppendEntry failed: %v", err)
	}

	// Append second entry
	entry2 := &RetrievalStatsHistoryEntry{
		Timestamp: time.Now(),
		Query:     "database schema",
		Mode:      "full",
		ReflexMs:  15,
		ReflectMs: 180,
		ResolveMs: 8,
		Results:   5,
		Decision:  "inject",
	}
	if err := writer.AppendEntry(entry2); err != nil {
		t.Fatalf("AppendEntry failed: %v", err)
	}

	// Read file and verify it contains both entries as separate lines
	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("failed to read history file: %v", err)
	}

	lines := splitLines(string(data))
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
}

func TestRetrievalStatsHistoryWriter_AppendFromStats(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	writer := NewRetrievalStatsHistoryWriter(tempDir)

	stats := &RetrievalStats{
		LastQuery:     "test query",
		LastMode:      "fast",
		LastReflexMs:  10,
		LastReflectMs: 0,
		LastResults:   2,
		LastDecision:  "skip",
		UpdatedAt:     time.Now(),
	}

	if err := writer.AppendFromStats(stats); err != nil {
		t.Fatalf("AppendFromStats failed: %v", err)
	}

	// Verify file exists and has content
	data, err := os.ReadFile(writer.Path())
	if err != nil {
		t.Fatalf("failed to read history file: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected non-empty history file")
	}
}

func TestRetrievalStatsHistoryWriter_Rotation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	writer := NewRetrievalStatsHistoryWriter(tempDir)

	// Create a file that exceeds MaxHistorySize
	// We'll create a smaller test by writing directly
	historyPath := writer.Path()
	largeData := make([]byte, MaxHistorySize+1)
	for i := range largeData {
		largeData[i] = 'x'
	}
	if err := os.WriteFile(historyPath, largeData, 0644); err != nil {
		t.Fatalf("failed to create large file: %v", err)
	}

	// Append an entry - this should trigger rotation
	entry := &RetrievalStatsHistoryEntry{
		Timestamp: time.Now(),
		Query:     "test",
		Mode:      "fast",
		ReflexMs:  10,
		Results:   1,
		Decision:  "inject",
	}
	if err := writer.AppendEntry(entry); err != nil {
		t.Fatalf("AppendEntry failed: %v", err)
	}

	// Verify .old file exists
	oldPath := historyPath + ".old"
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Error("expected .old file to exist after rotation")
	}

	// Verify new history file is small (just the new entry)
	info, err := os.Stat(historyPath)
	if err != nil {
		t.Fatalf("failed to stat history file: %v", err)
	}
	if info.Size() >= MaxHistorySize {
		t.Errorf("expected history file to be small after rotation, got %d bytes", info.Size())
	}
}

func TestRetrievalStatsHistoryWriter_Path(t *testing.T) {
	writer := NewRetrievalStatsHistoryWriter("/test/dir")
	expected := filepath.Join("/test/dir", "retrieval_stats_history.jsonl")
	if writer.Path() != expected {
		t.Errorf("expected %s, got %s", expected, writer.Path())
	}
}

func TestGetRetrievalStatsHistoryPath(t *testing.T) {
	path := GetRetrievalStatsHistoryPath("/some/context/dir")
	expected := filepath.Join("/some/context/dir", "retrieval_stats_history.jsonl")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

// splitLines splits a string into non-empty lines
func splitLines(s string) []string {
	var lines []string
	for _, line := range []byte(s) {
		if line == '\n' {
			continue
		}
	}
	// Simple split by newline
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				lines = append(lines, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
