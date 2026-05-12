package journal

import (
	"fmt"
	"sync"
)

// Projector consumes an Entry and updates derived state. Projectors must be
// idempotent at the entry level so re-projection during rebuild/replay does
// not double-count. They are pure (Entry) → side-effect; provider state is
// captured by closure when registering.
type Projector func(*Entry) error

// UnknownPolicy controls what happens when the registry has no projector
// for an entry's {type, v}. See F3 for the policy refinements; F2 defines
// the surface.
type UnknownPolicy int

const (
	// UnknownLogAndSkip advances the cursor past the entry without
	// projecting it. The default per principle 6 of docs/journal.md
	// (forward-compatibility: unknown versions are tolerated).
	UnknownLogAndSkip UnknownPolicy = iota
	// UnknownError returns an error and stops the indexer at the entry.
	// Use in strict environments (verify, replay) where unexpected types
	// are bugs.
	UnknownError
)

// Registry maps {type, v} → Projector. Safe for concurrent reads after
// registration is complete; Register itself takes an internal lock.
type Registry struct {
	mu         sync.RWMutex
	projectors map[string]Projector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{projectors: map[string]Projector{}}
}

// Register associates a projector with a {type, v} pair. Re-registering the
// same key replaces the prior projector (useful in tests).
func (r *Registry) Register(typ string, v int, p Projector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projectors[registryKey(typ, v)] = p
}

// Lookup returns the projector for {type, v}, or nil + false if none.
func (r *Registry) Lookup(typ string, v int) (Projector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.projectors[registryKey(typ, v)]
	return p, ok
}

// Known returns true if a projector is registered for {type, v}.
func (r *Registry) Known(typ string, v int) bool {
	_, ok := r.Lookup(typ, v)
	return ok
}

func registryKey(typ string, v int) string {
	return fmt.Sprintf("%s/v%d", typ, v)
}
