// serve_session.go — M4.2b1 (GOAL.md §6 M4.2, split per STATE.md into
// M4.2b1/b2/b3): the SessionManager, an in-process map of live
// *CortexSession keyed by session id (docs/cortex-web.md Phase 4). This
// increment lands the create/resume lifecycle only; the per-session mutex
// that serializes turns ("the discord mutex generalized" — one turn at a
// time per session, different sessions concurrent) lands on managedSession
// in M4.2b2 alongside the turn handler that actually needs it (an unused
// field would fail check.sh's lint gate today). Owns no on-disk state of
// its own: every session it holds is backed by the same transcript file
// StartTranscript/ResumeTranscript already write (session.go), so a
// restarted manager (M4.6) re-derives everything from disk.
package main

import (
	"fmt"
	"sync"

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
// identity. See the file doc comment for why the per-session turn mutex
// isn't here yet.
type managedSession struct {
	cs *CortexSession
}

// ID is the session's transcript id (cs.SessionID).
func (ms *managedSession) ID() string { return ms.cs.SessionID }

// SessionManager is an in-process map of live *CortexSession keyed by
// session id (GOAL.md §3 P4 / docs/cortex-web.md Phase 4's "session
// manager"). Reuses internal/registry to resolve a project name to a root
// and applyProjectByName (project_workspace.go, M3.5) to target a freshly
// built session at it — no new resolution logic.
type SessionManager struct {
	mu         sync.Mutex
	sessions   map[string]*managedSession
	reg        registry.Registry
	newSession sessionFactory
}

// NewSessionManager constructs a SessionManager backed by reg for project
// resolution, using newSession to build each live *CortexSession before
// it's targeted at a project.
func NewSessionManager(reg registry.Registry, newSession sessionFactory) *SessionManager {
	return &SessionManager{sessions: make(map[string]*managedSession), reg: reg, newSession: newSession}
}

// Create starts a brand-new session against project's workspace and tracks
// it live. Returns registry.ErrProjectNotFound (wrapped) for an unregistered
// project name.
func (m *SessionManager) Create(project string) (*managedSession, error) {
	cs := m.newSession()
	if err := applyProjectByName(cs, m.reg, project); err != nil {
		return nil, err
	}
	cs.StartTranscript()
	if cs.SessionID == "" {
		return nil, fmt.Errorf("failed to start a new session transcript for project %q", project)
	}
	ms := &managedSession{cs: cs}
	m.mu.Lock()
	m.sessions[cs.SessionID] = ms
	m.mu.Unlock()
	return ms, nil
}

// Resume returns the already-live session for id if the manager already
// holds it (no reopening a transcript this process already has open), else
// re-hydrates it from the on-disk transcript under project's workspace via
// CortexSession.ResumeTranscript.
func (m *SessionManager) Resume(project, id string) (*managedSession, error) {
	if ms, ok := m.Get(id); ok {
		return ms, nil
	}
	cs := m.newSession()
	if err := applyProjectByName(cs, m.reg, project); err != nil {
		return nil, err
	}
	if err := cs.ResumeTranscript(id); err != nil {
		return nil, fmt.Errorf("failed to resume session %q for project %q: %w", id, project, err)
	}
	ms := &managedSession{cs: cs}
	m.mu.Lock()
	m.sessions[cs.SessionID] = ms
	m.mu.Unlock()
	return ms, nil
}

// Get returns the live session for id, if the manager currently holds one.
func (m *SessionManager) Get(id string) (*managedSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
// markdown/prompts to.
func newProductionSession() *CortexSession {
	cs := NewCortexSession()
	cs.quiet = true
	return cs
}
