package cognition

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
