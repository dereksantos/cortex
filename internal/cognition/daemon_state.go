package cognition

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
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
func TruncatePath(p string, maxLen int) string {
	if p == "" {
		return ""
	}

	// Normalize to forward slashes for consistent cross-platform output
	p = filepath.ToSlash(p)

	// Get just the filename
	filename := path.Base(p)
	if len(filename) <= maxLen {
		// Try to include parent directory if it fits
		dir := path.Dir(p)
		if dir != "." && dir != "/" {
			parent := path.Base(dir)
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
	LastMode        string    `json:"last_mode"` // "fast" or "full"
	LastReflexMs    int64     `json:"last_reflex_ms"`
	LastReflectMs   int64     `json:"last_reflect_ms"`
	LastResolveMs   int64     `json:"last_resolve_ms"`
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

// RetrievalStatsHistoryEntry represents a single entry in the retrieval stats history.
// This is the JSONL format for historical retrieval analysis.
type RetrievalStatsHistoryEntry struct {
	Timestamp time.Time `json:"ts"`
	Query     string    `json:"query"`
	Mode      string    `json:"mode"` // "fast" or "full"
	ReflexMs  int64     `json:"reflex_ms"`
	ReflectMs int64     `json:"reflect_ms"`
	ResolveMs int64     `json:"resolve_ms"`
	Results   int       `json:"results"`
	Decision  string    `json:"decision"` // "inject", "skip", "queue"
}

// RetrievalStatsHistoryWriter provides thread-safe appending of retrieval stats history.
// Implements automatic rotation when file exceeds MaxHistorySize.
type RetrievalStatsHistoryWriter struct {
	mu   sync.Mutex
	path string
}

// MaxHistorySize is the maximum size of the history file before rotation (10MB).
const MaxHistorySize = 10 * 1024 * 1024

// NewRetrievalStatsHistoryWriter creates a history writer for the given context directory.
func NewRetrievalStatsHistoryWriter(contextDir string) *RetrievalStatsHistoryWriter {
	return &RetrievalStatsHistoryWriter{
		path: filepath.Join(contextDir, "retrieval_stats_history.jsonl"),
	}
}

// Path returns the history file path.
func (w *RetrievalStatsHistoryWriter) Path() string {
	return w.path
}

// AppendEntry appends a retrieval stats entry to the history file.
// Performs automatic rotation when file exceeds MaxHistorySize.
func (w *RetrievalStatsHistoryWriter) AppendEntry(entry *RetrievalStatsHistoryEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Check file size and rotate if needed (failure shouldn't block append)
	_ = w.maybeRotate()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal history entry: %w", err)
	}

	// Open with O_APPEND for atomic append
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open history file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(string(data) + "\n"); err != nil {
		return fmt.Errorf("failed to write history entry: %w", err)
	}

	return nil
}

// maybeRotate checks if the history file exceeds MaxHistorySize and rotates if needed.
// Rotation renames current file to .old (overwriting any existing .old file).
func (w *RetrievalStatsHistoryWriter) maybeRotate() error {
	info, err := os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet, no rotation needed
		}
		return fmt.Errorf("failed to stat history file: %w", err)
	}

	if info.Size() < MaxHistorySize {
		return nil // File is within size limit
	}

	// Rotate: rename to .old
	oldPath := w.path + ".old"
	if err := os.Rename(w.path, oldPath); err != nil {
		return fmt.Errorf("failed to rotate history file: %w", err)
	}

	return nil
}

// AppendFromStats creates a history entry from RetrievalStats and appends it.
// This is a convenience method to convert from the main stats format.
func (w *RetrievalStatsHistoryWriter) AppendFromStats(stats *RetrievalStats) error {
	entry := &RetrievalStatsHistoryEntry{
		Timestamp: stats.UpdatedAt,
		Query:     stats.LastQuery,
		Mode:      stats.LastMode,
		ReflexMs:  stats.LastReflexMs,
		ReflectMs: stats.LastReflectMs,
		ResolveMs: stats.LastResolveMs,
		Results:   stats.LastResults,
		Decision:  stats.LastDecision,
	}
	return w.AppendEntry(entry)
}

// GetRetrievalStatsHistoryPath returns the standard retrieval stats history file path.
func GetRetrievalStatsHistoryPath(contextDir string) string {
	return filepath.Join(contextDir, "retrieval_stats_history.jsonl")
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
	Mode        string    `json:"mode"` // "dream", "think", "reflex", "reflect", "resolve"
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

// LogMetric logs a metric value for a cognitive mode.
// Format: "[mode] metric: value" (e.g., "[reflect] ABR: 0.87")
func (l *ActivityLogger) LogMetric(mode string, metric string, value interface{}) error {
	desc := fmt.Sprintf("%s: %v", metric, value)
	return l.Log(&ActivityLogEntry{
		Mode:        mode,
		Description: desc,
	})
}

// LogQuery logs a reflex query with results and latency.
func (l *ActivityLogger) LogQuery(query string, results int, latencyMs int64) error {
	desc := fmt.Sprintf("query %q → %d results (%dms)", query, results, latencyMs)
	return l.Log(&ActivityLogEntry{
		Mode:        "reflex",
		Description: desc,
		Query:       query,
		Results:     results,
		LatencyMs:   latencyMs,
	})
}

// LogInsight logs an insight extraction.
func (l *ActivityLogger) LogInsight(insight string) error {
	desc := fmt.Sprintf("extracted insight: %q", insight)
	return l.Log(&ActivityLogEntry{
		Mode:        "dream",
		Description: desc,
	})
}

// LogRerank logs a reflect reranking operation with ABR.
func (l *ActivityLogger) LogRerank(abr float64) error {
	desc := fmt.Sprintf("reranked → ABR %.2f", abr)
	return l.Log(&ActivityLogEntry{
		Mode:        "reflect",
		Description: desc,
	})
}

// LogCache logs a think cache operation.
func (l *ActivityLogger) LogCache(operation string) error {
	return l.Log(&ActivityLogEntry{
		Mode:        "think",
		Description: operation,
	})
}

// LogDecision logs a Resolve decision with confidence and query context.
func (l *ActivityLogger) LogDecision(decision string, confidence float64, query string, resultCount int) error {
	desc := fmt.Sprintf("decision=%s confidence=%.2f results=%d", decision, confidence, resultCount)
	return l.Log(&ActivityLogEntry{
		Mode:        "resolve",
		Description: desc,
		Query:       query,
		Results:     resultCount,
	})
}

// LogContradiction logs a detected contradiction between insights.
// Used by Reflect mode for noise ratio analysis in evals.
func (l *ActivityLogger) LogContradiction(insight1Summary, insight2Summary, resolution string) error {
	desc := fmt.Sprintf("contradiction: %q vs %q resolved=%q", insight1Summary, insight2Summary, resolution)
	return l.Log(&ActivityLogEntry{
		Mode:        "reflect",
		Description: desc,
	})
}

// LogSessionStart logs the start of a new session with its initial prompt.
func (l *ActivityLogger) LogSessionStart(sessionID string, initialPrompt string) error {
	desc := fmt.Sprintf("started session_id=%s", sessionID)
	return l.Log(&ActivityLogEntry{
		Mode:        "session",
		Description: desc,
		Query:       initialPrompt,
	})
}

// LogSessionEnd logs the end of a session with summary statistics.
func (l *ActivityLogger) LogSessionEnd(sessionID string, eventCount int, insightCount int) error {
	desc := fmt.Sprintf("ended session_id=%s events=%d insights=%d", sessionID, eventCount, insightCount)
	return l.Log(&ActivityLogEntry{
		Mode:        "session",
		Description: desc,
	})
}

// LogError logs a hook or command error for debugging visibility.
func (l *ActivityLogger) LogError(command string, err error) error {
	desc := fmt.Sprintf("%s failed: %v", command, err)
	return l.Log(&ActivityLogEntry{
		Mode:        "error",
		Description: desc,
	})
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

// BackgroundMetrics tracks background processing state for the watch command.
// Written periodically by the daemon, read by the watch command.
type BackgroundMetrics struct {
	ThinkBudget     int       `json:"think_budget"`      // Current Think budget
	ThinkMaxBudget  int       `json:"think_max_budget"`  // Max Think budget for reference
	DreamQueueDepth int       `json:"dream_queue_depth"` // ProactiveQueue length
	DreamBudget     int       `json:"dream_budget"`      // Current Dream budget
	DreamMaxBudget  int       `json:"dream_max_budget"`  // Max Dream budget for reference
	ActivityLevel   float64   `json:"activity_level"`    // 0.0 (idle) to 1.0 (very active)
	IdleSeconds     int       `json:"idle_seconds"`      // Time since last retrieve
	CacheHitRate    float64   `json:"cache_hit_rate"`    // Think cache hit rate (0-1)
	CacheHits       int       `json:"cache_hits"`        // Total cache hits
	CacheMisses     int       `json:"cache_misses"`      // Total cache misses
	InsightsSession int       `json:"insights_session"`  // Insights discovered this session
	UpdatedAt       time.Time `json:"updated_at"`
}

// BackgroundMetricsWriter provides thread-safe writing of background metrics.
type BackgroundMetricsWriter struct {
	mu   sync.Mutex
	path string
}

// NewBackgroundMetricsWriter creates a writer for the given context directory.
func NewBackgroundMetricsWriter(contextDir string) *BackgroundMetricsWriter {
	return &BackgroundMetricsWriter{
		path: filepath.Join(contextDir, "background_metrics.json"),
	}
}

// Path returns the metrics file path.
func (w *BackgroundMetricsWriter) Path() string {
	return w.path
}

// WriteMetrics atomically writes background metrics to the metrics file.
func (w *BackgroundMetricsWriter) WriteMetrics(metrics *BackgroundMetrics) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	metrics.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal background metrics: %w", err)
	}

	// Atomic write pattern
	tempPath := w.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, w.path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename metrics file: %w", err)
	}

	return nil
}

// ReadBackgroundMetrics reads background metrics from the metrics file.
// Returns nil if the file doesn't exist or is stale (> 10 seconds old).
func ReadBackgroundMetrics(contextDir string) (*BackgroundMetrics, error) {
	path := filepath.Join(contextDir, "background_metrics.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read background metrics: %w", err)
	}

	var metrics BackgroundMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return nil, fmt.Errorf("failed to parse background metrics: %w", err)
	}

	// Check if metrics are stale (> 10 seconds old)
	if time.Since(metrics.UpdatedAt) > 10*time.Second {
		return nil, nil // Metrics are stale
	}

	return &metrics, nil
}

// GetBackgroundMetricsPath returns the standard background metrics file path.
func GetBackgroundMetricsPath(contextDir string) string {
	return filepath.Join(contextDir, "background_metrics.json")
}
