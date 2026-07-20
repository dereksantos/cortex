// serve_memory.go — the dashboard MEMORY screen's HTTP surface (docs/
// cross-source-learning.md piece 4): GET /api/memory (list, tier-tagged),
// GET /api/memory/note (one note's full body), DELETE /api/memory/note (the
// human correction loop, routed to the real Store.Forget path — the same
// removal memory_forget uses, cmd/cortex/tool_deps.go's MemoryForget).
// Every route here sits under the existing "/api/..." prefix, so
// hostOriginMiddleware (serve.go) already gates it — no new auth wiring.
package main

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/registry"
)

// errUnsupportedTier / errProjectRequired are resolveTierStore's own
// sentinel errors, mapped to 400 by the handlers below — distinguishing
// "the request was malformed" from a registry lookup failure (mapped to
// 404) or any other resolution failure (mapped to 500).
var (
	errUnsupportedTier = errors.New("unsupported tier (want \"project\" or \"user\")")
	errProjectRequired = errors.New("tier=project requires ?project=<name>")
)

// resolveTierStore resolves the memory.Store for an explicit tier query
// value — scopeProject or scopeUser (tool_deps.go's own constants, reused
// here so the wire vocabulary matches the memory tools' scope argument
// exactly). Deliberately never falls back or searches both tiers: piece
// 4's DELETE contract is "never cross-tier", and GET /api/memory/note's
// contract is "one explicit tier" — both read this same resolver.
func resolveTierStore(reg registry.Registry, tier, project string) (*memory.Store, error) {
	switch tier {
	case scopeUser:
		return userMemoryStore()
	case scopeProject:
		if project == "" {
			return nil, errProjectRequired
		}
		return projectMemoryStore(reg, project)
	default:
		return nil, errUnsupportedTier
	}
}

// writeTierResolutionError maps resolveTierStore's failure modes to the
// right HTTP status: a malformed request (bad/missing tier, missing
// project) is 400, an unregistered project is 404 (matching every other
// reg.Lookup call site in this package), anything else is a genuine
// server-side failure (500).
func writeTierResolutionError(w http.ResponseWriter, project string, err error) {
	switch {
	case errors.Is(err, errUnsupportedTier), errors.Is(err, errProjectRequired):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, registry.ErrProjectNotFound):
		http.Error(w, "project not registered: "+project, http.StatusNotFound)
	default:
		http.Error(w, "failed to resolve memory store: "+err.Error(), http.StatusInternalServerError)
	}
}

// handleListMemory serves GET /api/memory?project=<name>: the named
// project's project-tier notes plus every user-tier note, tier-tagged
// (webui_memory.go's buildMemoryViewModel) — names/hooks/timestamps only,
// no bodies, matching the landscape screen's names-only privacy stance.
func handleListMemory(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		if project == "" {
			http.Error(w, "GET /api/memory requires ?project=<name>", http.StatusBadRequest)
			return
		}
		vm, err := buildMemoryViewModel(reg, project)
		if err != nil {
			writeTierResolutionError(w, project, err)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	}
}

// memoryNoteDetail is GET /api/memory/note's response body: one note's full
// text, with its tier/name/project echoed back so the client doesn't need
// to remember what it asked for.
type memoryNoteDetail struct {
	Tier    string `json:"tier"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Updated string `json:"updated,omitempty"`
	Body    string `json:"body"`
}

// noteUpdated best-effort looks up name's last-updated instant from the
// store's own List() (which already parses the note's frontmatter) — Read
// itself strips frontmatter entirely, so this is the only way to recover
// the timestamp without re-reading the raw file. A miss (e.g. a race with a
// concurrent Forget) just omits the field; it's a display nicety, not load
// bearing.
func noteUpdated(store *memory.Store, name string) string {
	metas, err := store.List()
	if err != nil {
		return ""
	}
	for _, m := range metas {
		if m.Name == name {
			if m.Updated.IsZero() {
				return ""
			}
			return m.Updated.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// handleGetMemoryNote serves GET /api/memory/note?tier=&name=&project=: one
// note's full body from the exact tier requested (never both, unlike
// MemoryRead's unscoped shadowing) — a miss is a 404, matching the
// filesystem-backed Store.Read's os.ErrNotExist contract.
func handleGetMemoryNote(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tier := r.URL.Query().Get("tier")
		name := r.URL.Query().Get("name")
		project := r.URL.Query().Get("project")
		if name == "" {
			http.Error(w, "GET /api/memory/note requires ?name=<note>", http.StatusBadRequest)
			return
		}
		store, err := resolveTierStore(reg, tier, project)
		if err != nil {
			writeTierResolutionError(w, project, err)
			return
		}
		body, err := store.Read(name)
		if os.IsNotExist(err) {
			http.Error(w, "no note named \""+name+"\" in "+tier+" memory", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "failed to read note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, memoryNoteDetail{
			Tier:    tier,
			Name:    name,
			Project: project,
			Updated: noteUpdated(store, name),
			Body:    body,
		})
	}
}

// deleteMemoryNoteResponse is DELETE /api/memory/note's response body.
type deleteMemoryNoteResponse struct {
	Tier    string `json:"tier"`
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

// handleDeleteMemoryNote serves DELETE /api/memory/note?tier=&name=&project=:
// the human correction loop, routed to the exact same Store.Forget path
// MemoryForget (tool_deps.go) uses — resolveTierStore never searches both
// tiers, so a delete can never remove the wrong tier's note by accident. A
// miss (the note was already gone) is a 404, matching Store.Forget's own
// "not found" signal (removed=false, no error) rather than silently
// succeeding on nothing.
func handleDeleteMemoryNote(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tier := r.URL.Query().Get("tier")
		name := r.URL.Query().Get("name")
		project := r.URL.Query().Get("project")
		if name == "" {
			http.Error(w, "DELETE /api/memory/note requires ?name=<note>", http.StatusBadRequest)
			return
		}
		store, err := resolveTierStore(reg, tier, project)
		if err != nil {
			writeTierResolutionError(w, project, err)
			return
		}
		removed, err := store.Forget(name)
		if err != nil {
			http.Error(w, "failed to forget note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !removed {
			http.Error(w, "no note named \""+name+"\" in "+tier+" memory", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, deleteMemoryNoteResponse{Tier: tier, Name: name, Removed: true})
	}
}
