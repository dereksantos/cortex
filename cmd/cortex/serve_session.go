// serve_session.go — M4.2b1 (GOAL.md §6 M4.2, split per STATE.md into
// M4.2b1/b2/b3): the SessionManager, an in-process map of live
// *CortexSession keyed by session id (docs/cortex-web.md Phase 4).
// managedSession's per-session turn-serializing mutex ("the discord mutex
// generalized" — one turn at a time per session, different sessions
// concurrent) is used by handleTurn (serve_turn.go, M4.2b2). Owns no on-disk
// state of its own: every session it holds is backed by the same transcript
// file StartTranscript/ResumeTranscript already write (session.go), so a
// restarted manager (M4.6) re-derives everything from disk.
package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/registry"
)

// sessionFactory builds a fresh *CortexSession with no project targeting yet
// (applyProjectByName retargets it) — the seam that makes SessionManager
// httptest-testable without a model: production wires NewCortexSession
// (real config/backend resolution, same as runTurnCLI/discord.go already
// do); tests inject a hermetic hand-built session pointed at a scripted
// httptest backend (the pattern greeting_test.go/serve_routes_test.go
// established).
type sessionFactory func() *CortexSession

// managedSession pairs a live *CortexSession with its manager-tracked
// identity and the mutex that serializes turns on it (GOAL.md §3 P4: "one
// turn at a time per session, different sessions concurrent" — the discord
// mutex generalized). Held by handleTurn (serve_turn.go, M4.2b2) for the
// duration of session.Turn.
type managedSession struct {
	cs          *CortexSession
	mu          sync.Mutex
	lastTouched time.Time
}

// ID is the session's transcript id (cs.SessionID).
func (ms *managedSession) ID() string { return ms.cs.SessionID }

// SessionManager is an in-process map of live *CortexSession keyed by
// session id (GOAL.md §3 P4 / docs/cortex-web.md Phase 4's "session
// manager"). Reuses internal/registry to resolve a project name to a root
// and applyProjectByName (project_workspace.go, M3.5) to target a freshly
// built session at it — no new resolution logic.
type SessionManager struct {
	mu          sync.Mutex
	sessions    map[string]*managedSession
	reg         registry.Registry
	newSession  sessionFactory
	idleTimeout time.Duration    // 0 (the zero value) disables eviction — opt-in via SetIdleTimeout
	now         func() time.Time // overridden by SetClock in tests; defaults to time.Now
}

// NewSessionManager constructs a SessionManager backed by reg for project
// resolution, using newSession to build each live *CortexSession before
// it's targeted at a project. Idle eviction (M4.7) is disabled by default —
// call SetIdleTimeout to enable it.
func NewSessionManager(reg registry.Registry, newSession sessionFactory) *SessionManager {
	return &SessionManager{
		sessions:   make(map[string]*managedSession),
		reg:        reg,
		newSession: newSession,
		now:        time.Now,
	}
}

// SetIdleTimeout arms eviction: a session untouched (no Create/Resume/Touch)
// for at least d is dropped from the live map the next time it's looked up
// via Get or Resume, falling back to the on-disk transcript exactly like a
// restart (M4.6) does. d <= 0 disables eviction (the default).
func (m *SessionManager) SetIdleTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleTimeout = d
}

// SetClock overrides the manager's notion of "now" for eviction checks —
// tests inject a fakeClock.Now so idle-eviction assertions never sleep.
func (m *SessionManager) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// Touch resets id's idle clock. Called by handleTurn/handleTurnStream
// (serve_turn.go, serve_stream.go) on every request against a live session,
// so a session under continuous use is never evicted mid-conversation.
// No-op if id isn't currently live.
func (m *SessionManager) Touch(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ms, ok := m.sessions[id]; ok {
		ms.lastTouched = m.now()
	}
}

// evictIfIdleLocked drops id from the live map if it's idle-timed-out.
// Caller must hold m.mu.
func (m *SessionManager) evictIfIdleLocked(id string) {
	if m.idleTimeout <= 0 {
		return
	}
	ms, ok := m.sessions[id]
	if !ok {
		return
	}
	if m.now().Sub(ms.lastTouched) < m.idleTimeout {
		return
	}
	ms.cs.Close()
	delete(m.sessions, id)
}

// Create starts a brand-new session against project's workspace and tracks
// it live. Returns registry.ErrProjectNotFound (wrapped) for an unregistered
// project name. project == "" skips project targeting entirely, leaving
// newSession()'s own default in place — the same no-op semantics
// applyProjectFlag (project_workspace.go) already gives the CLI's
// --project-less path; the Discord adapter (discord.go, docs/cortex-web.md
// Phase 7) is this seam's other caller and has no project selection UI yet.
func (m *SessionManager) Create(project string) (*managedSession, error) {
	cs := m.newSession()
	if project != "" {
		if err := applyProjectByName(cs, m.reg, project); err != nil {
			return nil, err
		}
	}
	cs.StartTranscript()
	if cs.SessionID == "" {
		return nil, fmt.Errorf("failed to start a new session transcript for project %q", project)
	}
	m.mu.Lock()
	ms := &managedSession{cs: cs, lastTouched: m.now()}
	m.sessions[cs.SessionID] = ms
	m.mu.Unlock()
	return ms, nil
}

// Resume returns the already-live session for id if the manager already
// holds it (no reopening a transcript this process already has open), else
// re-hydrates it from the on-disk transcript under project's workspace via
// CortexSession.ResumeTranscript — the same path an idle-evicted session
// (M4.7) falls back to, since Get evicts before reporting "not held".
func (m *SessionManager) Resume(project, id string) (*managedSession, error) {
	if ms, ok := m.Get(id); ok {
		return ms, nil
	}
	cs := m.newSession()
	if project != "" {
		if err := applyProjectByName(cs, m.reg, project); err != nil {
			return nil, err
		}
	}
	if err := cs.ResumeTranscript(id); err != nil {
		return nil, fmt.Errorf("failed to resume session %q for project %q: %w", id, project, err)
	}
	m.mu.Lock()
	ms := &managedSession{cs: cs, lastTouched: m.now()}
	m.sessions[cs.SessionID] = ms
	m.mu.Unlock()
	return ms, nil
}

// GetOrResume returns id's live session if the manager already holds it,
// else transparently resumes it from the on-disk transcript under project
// (Resume) — the same fallback M4.7's idle-eviction already promises ("a
// subsequent request re-hydrates from the transcript"), extended to cover a
// session id this serve process's manager never held live at all (e.g. a
// browser client that navigates straight to an old session URL after a
// restart or against a fresh serve process — the turn/turn-stream handlers,
// serve_turn.go/serve_stream.go, used to 404 this case instead of resuming
// it). Callers distinguish the resulting error with errors.Is: a wrapped
// os.ErrNotExist means the transcript genuinely doesn't exist (404); a
// wrapped fslock.ErrBusy means another process currently holds it open
// (409); anything else (e.g. an unregistered project) is a 500.
func (m *SessionManager) GetOrResume(project, id string) (*managedSession, error) {
	if ms, ok := m.Get(id); ok {
		return ms, nil
	}
	return m.Resume(project, id)
}

// Get returns the live session for id, if the manager currently holds one
// and it hasn't idle-timed-out (M4.7's eviction check runs here, lazily, so
// no background goroutine/ticker is needed).
func (m *SessionManager) Get(id string) (*managedSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictIfIdleLocked(id)
	ms, ok := m.sessions[id]
	return ms, ok
}

// List returns the ids of every session currently live in the manager, in
// no particular order.
func (m *SessionManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// newProductionSession is the real (non-test) sessionFactory: identical
// construction to runTurnCLI/discord.go's own NewCortexSession() usage,
// with output quieted since a server has no interactive terminal to render
// markdown/prompts to. Also wires memory + capture (EnableMemory), matching
// cli.go/main.go/discord.go's own production sessions — every serve/web-UI
// turn now gets memory tools, the turn-start index injection, and a
// captureTurn record, the same as every other adapter (docs/memory-tools.md,
// docs/cross-source-learning.md's "Code-reality check").
func newProductionSession() *CortexSession {
	cs := NewCortexSession()
	cs.quiet = true
	cs.EnableMemory()
	return cs
}
