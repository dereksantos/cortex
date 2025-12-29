package cognition

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DaemonState represents the current state of the Cortex daemon.
// This is used to communicate mode status to the CLI status command.
type DaemonState struct {
	Mode        string    `json:"mode"`        // "idle", "think", "dream", "reflex", "reflect", "resolve"
	Description string    `json:"description"` // e.g., "learning patterns", "exploring history"
	Updated     time.Time `json:"updated"`
	Stats       struct {
		Events   int `json:"events"`
		Insights int `json:"insights"`
	} `json:"stats"`
}

// StateWriter provides thread-safe state file writing.
type StateWriter struct {
	mu   sync.Mutex
	path string
}

// NewStateWriter creates a StateWriter for the given context directory.
func NewStateWriter(contextDir string) *StateWriter {
	return &StateWriter{
		path: filepath.Join(contextDir, "daemon_state.json"),
	}
}

// Path returns the state file path.
func (sw *StateWriter) Path() string {
	return sw.path
}

// WriteDaemonState atomically writes daemon state to the state file.
// Uses temp file + rename for atomic writes.
func (sw *StateWriter) WriteDaemonState(state *DaemonState) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Ensure state has current timestamp
	state.Updated = time.Now()

	// Marshal to JSON
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to temp file first (atomic write pattern)
	tempPath := sw.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Rename temp to actual (atomic on POSIX systems)
	if err := os.Rename(tempPath, sw.path); err != nil {
		// Clean up temp file on failure
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// WriteMode is a convenience method to update just the mode and description.
func (sw *StateWriter) WriteMode(mode, description string) error {
	state := &DaemonState{
		Mode:        mode,
		Description: description,
	}
	return sw.WriteDaemonState(state)
}

// WriteModeWithStats updates mode, description, and stats.
func (sw *StateWriter) WriteModeWithStats(mode, description string, events, insights int) error {
	state := &DaemonState{
		Mode:        mode,
		Description: description,
	}
	state.Stats.Events = events
	state.Stats.Insights = insights
	return sw.WriteDaemonState(state)
}

// ReadDaemonState reads the daemon state from the state file.
// Returns nil if the file doesn't exist or is stale (> 5 seconds old).
func ReadDaemonState(path string) (*DaemonState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // File doesn't exist, not an error
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	// Check if state is stale (> 5 seconds old)
	if time.Since(state.Updated) > 5*time.Second {
		return nil, nil // State is stale
	}

	return &state, nil
}

// GetDaemonStatePath returns the standard daemon state file path for a context directory.
func GetDaemonStatePath(contextDir string) string {
	return filepath.Join(contextDir, "daemon_state.json")
}

// TruncatePath shortens a file path for status bar display.
// Shows just the filename, or parent/filename if short enough.
// Keeps result under maxLen characters.
func TruncatePath(path string, maxLen int) string {
	if path == "" {
		return ""
	}

	// Get just the filename
	filename := filepath.Base(path)
	if len(filename) <= maxLen {
		// Try to include parent directory if it fits
		dir := filepath.Dir(path)
		if dir != "." && dir != "/" {
			parent := filepath.Base(dir)
			withParent := parent + "/" + filename
			if len(withParent) <= maxLen {
				return withParent
			}
		}
		return filename
	}

	// Filename too long, truncate with ellipsis
	if maxLen > 3 {
		return filename[:maxLen-3] + "..."
	}
	return filename[:maxLen]
}

// TruncateInsight shortens an insight description for status bar display.
// Keeps result under maxLen characters, ending with ellipsis if truncated.
func TruncateInsight(insight string, maxLen int) string {
	if len(insight) <= maxLen {
		return insight
	}

	if maxLen <= 3 {
		return insight[:maxLen]
	}

	// Find a word boundary to break at
	truncated := insight[:maxLen-3]
	lastSpace := -1
	for i := len(truncated) - 1; i >= 0; i-- {
		if truncated[i] == ' ' {
			lastSpace = i
			break
		}
	}

	// If we found a space within reasonable distance, break there
	if lastSpace > maxLen/2 {
		return truncated[:lastSpace] + "..."
	}

	return truncated + "..."
}

// RetrievalStats tracks statistics about retrieval operations.
// Written by inject-context hook, read by watch command.
type RetrievalStats struct {
	LastQuery       string    `json:"last_query"`
	LastMode        string    `json:"last_mode"`    // "fast" or "full"
	LastReflexMs    int64     `json:"last_reflex_ms"`
	LastReflectMs   int64     `json:"last_reflect_ms"`
	LastResults     int       `json:"last_results"`
	LastDecision    string    `json:"last_decision"` // "inject", "skip", "wait"
	TotalRetrievals int       `json:"total_retrievals"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RetrievalStatsWriter provides thread-safe writing of retrieval stats.
type RetrievalStatsWriter struct {
	mu   sync.Mutex
	path string
}

// NewRetrievalStatsWriter creates a writer for the given context directory.
func NewRetrievalStatsWriter(contextDir string) *RetrievalStatsWriter {
	return &RetrievalStatsWriter{
		path: filepath.Join(contextDir, "retrieval_stats.json"),
	}
}

// Path returns the stats file path.
func (w *RetrievalStatsWriter) Path() string {
	return w.path
}

// WriteStats atomically writes retrieval stats to the stats file.
func (w *RetrievalStatsWriter) WriteStats(stats *RetrievalStats) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	stats.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal retrieval stats: %w", err)
	}

	// Atomic write pattern
	tempPath := w.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, w.path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename stats file: %w", err)
	}

	return nil
}

// ReadRetrievalStats reads retrieval stats from the stats file.
// Returns nil if the file doesn't exist.
func ReadRetrievalStats(contextDir string) (*RetrievalStats, error) {
	path := filepath.Join(contextDir, "retrieval_stats.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read retrieval stats: %w", err)
	}

	var stats RetrievalStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse retrieval stats: %w", err)
	}

	return &stats, nil
}

// GetRetrievalStatsPath returns the standard retrieval stats file path.
func GetRetrievalStatsPath(contextDir string) string {
	return filepath.Join(contextDir, "retrieval_stats.json")
}

// ActivityLogEntry represents a single log entry for the watch command.
type ActivityLogEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Mode        string    `json:"mode"`        // "dream", "think", "reflex", "reflect", "resolve"
	Description string    `json:"description"`
	Query       string    `json:"query,omitempty"`
	Results     int       `json:"results,omitempty"`
	LatencyMs   int64     `json:"latency_ms,omitempty"`
}

// ActivityLogger appends activity entries to a log file.
type ActivityLogger struct {
	mu   sync.Mutex
	path string
}

// NewActivityLogger creates a logger for the given context directory.
func NewActivityLogger(contextDir string) *ActivityLogger {
	return &ActivityLogger{
		path: filepath.Join(contextDir, "activity.log"),
	}
}

// Path returns the log file path.
func (l *ActivityLogger) Path() string {
	return l.path
}

// Log appends an activity entry to the log file.
func (l *ActivityLogger) Log(entry *ActivityLogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal log entry: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(string(data) + "\n"); err != nil {
		return fmt.Errorf("failed to write log entry: %w", err)
	}

	return nil
}

// ReadRecentActivity reads the most recent N activity log entries.
func ReadRecentActivity(contextDir string, limit int) ([]ActivityLogEntry, error) {
	path := filepath.Join(contextDir, "activity.log")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read activity log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, nil
	}

	// Read from end
	entries := make([]ActivityLogEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(entries) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry ActivityLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // Skip malformed entries
		}
		entries = append(entries, entry)
	}

	return entries, nil
}
