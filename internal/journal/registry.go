// Forward-compatibility contract for the registry:
//
//  1. Unknown type — the entry's type has no projectors registered at any
//     version. Indexer treats per UnknownPolicy. Typical cause: an older
//     build reads a journal written by a newer build with a new entry
//     class. We log and skip so the older build does not crash on data
//     it cannot interpret. The cursor advances; if the newer build runs
//     later, it can re-project by rebuild.
//
//  2. Known type, unsupported version — the type has projectors at one
//     or more versions, but not this entry's V. Same policy applies, but
//     logged distinctly so it is visible during debugging that a schema
//     evolved and a migrator or new projector is needed.
//
// Schema migration (e.g. registering a v1→v2 transform that feeds the v2
// projector) is intentionally deferred — it lands when we have a real
// second version of any entry type to migrate. See docs/journal.md.
package journal

import (
	"fmt"
	"sort"
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

// HasType returns true if any version of typ has a projector registered.
// Used to distinguish "unknown type" (no versions at all) from "known type,
// unsupported version" (some versions registered, but not this V).
func (r *Registry) HasType(typ string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefix := typ + "/v"
	for k := range r.projectors {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Versions returns the registered versions for typ, sorted ascending.
// Returns nil if no versions are registered.
func (r *Registry) Versions(typ string) []int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefix := typ + "/v"
	var versions []int
	for k := range r.projectors {
		if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(k[len(prefix):], "%d", &v); err == nil {
			versions = append(versions, v)
		}
	}
	sort.Ints(versions)
	return versions
}

func registryKey(typ string, v int) string {
	return fmt.Sprintf("%s/v%d", typ, v)
}
