package cognition

import (
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/events"
)

// SessionTracker tracks session lifecycle for the watch view.
// It detects session starts, updates last actions, and maintains
// the session index cache for fast watch refresh.
type SessionTracker struct {
	storage        *storage.Storage
	indexWriter    *SessionIndexWriter
	activityLogger *ActivityLogger

	mu            sync.Mutex
	knownSessions map[string]bool // Track sessions we've seen this daemon run
}

// NewSessionTracker creates a new session tracker.
func NewSessionTracker(store *storage.Storage, contextDir string) *SessionTracker {
	return &SessionTracker{
		storage:        store,
		indexWriter:    NewSessionIndexWriter(contextDir),
		activityLogger: NewActivityLogger(contextDir),
		knownSessions:  make(map[string]bool),
	}
}

// OnEvent processes an event for session tracking.
// Call this from the router for every event.
func (t *SessionTracker) OnEvent(event *events.Event) {
	if event == nil || event.Context.SessionID == "" {
		return
	}

	sessionID := event.Context.SessionID

	t.mu.Lock()
	isNew := !t.knownSessions[sessionID]
	t.knownSessions[sessionID] = true
	t.mu.Unlock()

	// Determine initial prompt (only for user_prompt events on new sessions)
	var initialPrompt string
	if isNew && event.EventType == events.EventUserPrompt && event.Prompt != "" {
		initialPrompt = event.Prompt
	}

	// Determine last action description
	lastAction := t.describeAction(event)

	// Determine project path
	projectPath := event.Context.ProjectPath

	if isNew {
		// New session - create with initial prompt
		t.storage.CreateOrUpdateSession(sessionID, initialPrompt, lastAction, projectPath)

		// Log session start to activity log
		if t.activityLogger != nil {
			t.activityLogger.LogSessionStart(sessionID, initialPrompt)
		}
	} else {
		// Existing session - just update last action
		t.storage.UpdateSessionLastAction(sessionID, lastAction)
	}

	// Handle session end (EventStop)
	if event.EventType == events.EventStop {
		t.onSessionEnd(sessionID)
	}

	// Update the session index cache
	t.updateIndex()
}

// onSessionEnd handles logging when a session ends.
func (t *SessionTracker) onSessionEnd(sessionID string) {
	if t.activityLogger == nil {
		return
	}

	// Get session metadata to retrieve event count
	sess, err := t.storage.GetSessionByID(sessionID)
	if err != nil {
		// Log with zero counts if we can't get session data
		t.activityLogger.LogSessionEnd(sessionID, 0, 0)
		return
	}

	eventCount := 0
	if sess != nil {
		eventCount = sess.EventCount
	}

	// Note: insightCount is not tracked per-session currently, so we pass 0
	// In the future, this could be enhanced to track insights per session
	t.activityLogger.LogSessionEnd(sessionID, eventCount, 0)
}

// describeAction creates a human-readable description of the event action
func (t *SessionTracker) describeAction(event *events.Event) string {
	switch event.EventType {
	case events.EventUserPrompt:
		prompt := event.Prompt
		if len(prompt) > 40 {
			prompt = prompt[:40] + "..."
		}
		return "Prompt: " + prompt
	case events.EventToolUse:
		if event.ToolName != "" {
			return event.ToolName
		}
		return "Tool use"
	case events.EventEdit:
		return "Edit"
	case events.EventSearch:
		return "Search"
	case events.EventStop:
		return "Session ended"
	default:
		return string(event.EventType)
	}
}

// updateIndex refreshes the session index cache file
func (t *SessionTracker) updateIndex() {
	sessions, err := t.storage.GetRecentSessions(10) // Cache more than we show
	if err != nil {
		return
	}

	entries := make([]SessionIndexEntry, 0, len(sessions))
	for _, sess := range sessions {
		entries = append(entries, SessionIndexEntry{
			SessionID:     sess.SessionID,
			StartedAt:     sess.StartedAt,
			InitialPrompt: sess.InitialPrompt,
			LastAction:    sess.LastAction,
			LastActionAt:  sess.LastActionAt,
			EventCount:    sess.EventCount,
			ProjectPath:   sess.ProjectPath,
		})
	}

	t.indexWriter.Write(entries) // Debounced internally
}

// ForceUpdateIndex forces an immediate index update (bypasses debounce)
func (t *SessionTracker) ForceUpdateIndex() {
	sessions, err := t.storage.GetRecentSessions(10)
	if err != nil {
		return
	}

	entries := make([]SessionIndexEntry, 0, len(sessions))
	for _, sess := range sessions {
		entries = append(entries, SessionIndexEntry{
			SessionID:     sess.SessionID,
			StartedAt:     sess.StartedAt,
			InitialPrompt: sess.InitialPrompt,
			LastAction:    sess.LastAction,
			LastActionAt:  sess.LastActionAt,
			EventCount:    sess.EventCount,
			ProjectPath:   sess.ProjectPath,
		})
	}

	t.indexWriter.ForceWrite(entries)
}

// GetRecentSessions returns recent sessions from the database
func (t *SessionTracker) GetRecentSessions(limit int) ([]*storage.SessionMetadata, error) {
	return t.storage.GetRecentSessions(limit)
}

// GetSessionEvents returns events for a specific session (for expanded view)
func (t *SessionTracker) GetSessionEvents(sessionID string, limit int) ([]*events.Event, error) {
	return t.storage.GetSessionEvents(sessionID, limit)
}

// Clear clears the known sessions (useful for testing or daemon restart)
func (t *SessionTracker) Clear() {
	t.mu.Lock()
	t.knownSessions = make(map[string]bool)
	t.mu.Unlock()
}

// SessionCount returns the number of active sessions tracked this daemon run
func (t *SessionTracker) SessionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.knownSessions)
}

// LastUpdated returns when the session index was last written
func (t *SessionTracker) LastUpdated() time.Time {
	return t.indexWriter.lastWrite
}
