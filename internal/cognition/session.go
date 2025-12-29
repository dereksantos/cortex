package cognition

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// PersistedSession represents the serializable form of SessionContext.
// Used for saving/loading session state across daemon restarts.
type PersistedSession struct {
	SessionID              string                 `json:"session_id"`
	TopicWeights           map[string]float64     `json:"topic_weights"`
	WarmCache              map[string][]byte      `json:"warm_cache"` // serialized results
	ResolvedContradictions map[string]string      `json:"resolved_contradictions"`
	LastUpdated            time.Time              `json:"last_updated"`
}

// SessionPersister handles saving and loading of session state.
type SessionPersister struct {
	path string
}

// NewSessionPersister creates a new persister with the given file path.
func NewSessionPersister(contextDir string) *SessionPersister {
	return &SessionPersister{
		path: filepath.Join(contextDir, "session.json"),
	}
}

// Path returns the path to the session file.
func (p *SessionPersister) Path() string {
	return p.path
}

// Save persists the session context to disk.
// Uses atomic write (write to temp file, then rename) for safety.
func (p *SessionPersister) Save(ctx *cognition.SessionContext) error {
	if ctx == nil {
		return fmt.Errorf("session context is nil")
	}

	// Convert to persisted form
	persisted := &PersistedSession{
		SessionID:              generateSessionID(),
		TopicWeights:           ctx.TopicWeights,
		ResolvedContradictions: ctx.ResolvedContradictions,
		LastUpdated:            time.Now(),
	}

	// Serialize WarmCache (Results to JSON bytes)
	persisted.WarmCache = make(map[string][]byte)
	for key, results := range ctx.WarmCache {
		data, err := json.Marshal(results)
		if err != nil {
			// Skip entries that fail to serialize
			continue
		}
		persisted.WarmCache[key] = data
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to temp file first (atomic write pattern)
	tempPath := p.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Rename temp to final (atomic on most filesystems)
	if err := os.Rename(tempPath, p.path); err != nil {
		os.Remove(tempPath) // Clean up temp file on failure
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// Load reads the session context from disk.
// Returns an empty session if the file doesn't exist.
func (p *SessionPersister) Load() (*cognition.SessionContext, error) {
	// Check if file exists
	if _, err := os.Stat(p.path); os.IsNotExist(err) {
		// Return empty session
		return NewEmptySessionContext(), nil
	}

	// Read file
	data, err := os.ReadFile(p.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	// Unmarshal
	var persisted PersistedSession
	if err := json.Unmarshal(data, &persisted); err != nil {
		// If corrupted, return empty session rather than error
		return NewEmptySessionContext(), nil
	}

	// Convert to SessionContext
	ctx := &cognition.SessionContext{
		TopicWeights:           persisted.TopicWeights,
		RecentQueries:          make([]cognition.Query, 0),
		RecentPrompts:          make([]string, 0),
		WarmCache:              make(map[string][]cognition.Result),
		CachedReflect:          make(map[string][]cognition.Result),
		ResolvedContradictions: persisted.ResolvedContradictions,
		LastUpdated:            persisted.LastUpdated,
	}

	// Ensure maps are initialized even if empty in persisted data
	if ctx.TopicWeights == nil {
		ctx.TopicWeights = make(map[string]float64)
	}
	if ctx.ResolvedContradictions == nil {
		ctx.ResolvedContradictions = make(map[string]string)
	}

	// Deserialize WarmCache
	for key, data := range persisted.WarmCache {
		var results []cognition.Result
		if err := json.Unmarshal(data, &results); err != nil {
			// Skip entries that fail to deserialize
			continue
		}
		ctx.WarmCache[key] = results
	}

	return ctx, nil
}

// NewEmptySessionContext creates an initialized empty session context.
func NewEmptySessionContext() *cognition.SessionContext {
	return &cognition.SessionContext{
		TopicWeights:           make(map[string]float64),
		RecentQueries:          make([]cognition.Query, 0),
		RecentPrompts:          make([]string, 0),
		WarmCache:              make(map[string][]cognition.Result),
		CachedReflect:          make(map[string][]cognition.Result),
		ResolvedContradictions: make(map[string]string),
		LastUpdated:            time.Now(),
	}
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	return fmt.Sprintf("session-%d", time.Now().UnixNano())
}

// SessionSaver provides a callback-based session saving mechanism.
// It debounces save requests to avoid excessive disk writes.
type SessionSaver struct {
	persister    *SessionPersister
	saveInterval time.Duration
	lastSave     time.Time
	dirty        bool
}

// NewSessionSaver creates a new session saver.
func NewSessionSaver(persister *SessionPersister, saveInterval time.Duration) *SessionSaver {
	return &SessionSaver{
		persister:    persister,
		saveInterval: saveInterval,
	}
}

// MarkDirty indicates that the session has changes that need saving.
func (s *SessionSaver) MarkDirty() {
	s.dirty = true
}

// MaybeSave saves the session if it's dirty and enough time has passed.
// Returns true if a save was performed.
func (s *SessionSaver) MaybeSave(ctx *cognition.SessionContext) bool {
	if !s.dirty {
		return false
	}

	if time.Since(s.lastSave) < s.saveInterval {
		return false
	}

	if err := s.persister.Save(ctx); err != nil {
		// Log error but don't fail
		return false
	}

	s.lastSave = time.Now()
	s.dirty = false
	return true
}

// ForceSave saves the session immediately, regardless of interval.
func (s *SessionSaver) ForceSave(ctx *cognition.SessionContext) error {
	if err := s.persister.Save(ctx); err != nil {
		return err
	}
	s.lastSave = time.Now()
	s.dirty = false
	return nil
}
