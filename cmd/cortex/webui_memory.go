// webui_memory.go — the dashboard MEMORY screen's view-model (docs/
// cross-source-learning.md piece 4). Per GOAL.md §3 P5 / CLAUDE.md's
// existing web-track convention, rendering logic lives in Go view-model
// builders under cmd/cortex/webui*.go; this file is pure data assembly, no
// HTML/JS.
//
// The screen reads BOTH memory tiers the way the model itself sees them
// (docs/memory-tools.md + docs/cross-source-learning.md piece 1):
// project-tier notes under the named project's .cortex/memory (the exact
// path EnableMemory/tool_deps.go's storeFor resolves for scope="project")
// and user-tier notes under the machine-wide store (scope="user") — the
// SAME internal/memory.Store implementation, just pointed at different
// roots, opened directly here rather than through a live *CortexSession
// (the API has no session — mirrors handleListProjectSessions/
// buildDashboardViewModel's existing reg.Lookup -> NewWorkspace pattern).
// Shadowed names (a project note and a user note sharing a name) appear as
// TWO tagged rows, never merged — open question 4's recommendation: the
// human surface should never show less than memory_search already shows
// the model.
package main

import (
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/userhome"
)

// projectMemoryStore opens the named project's project-tier memory store,
// resolving the project root the same way handleListProjectSessions/
// buildDashboardViewModel already do (reg.Lookup -> NewWorkspace) and then
// pointing memory.New at ws.ContextDir() — byte-identical to
// CortexSession.EnableMemory()'s cs.memory construction (session_runtime.go),
// so this reads the exact store the coder's memory_write/read/search/forget
// tools already write.
func projectMemoryStore(reg registry.Registry, name string) (*memory.Store, error) {
	proj, err := reg.Lookup(name)
	if err != nil {
		return nil, err
	}
	ws, err := NewWorkspace(proj.Root)
	if err != nil {
		return nil, err
	}
	return memory.New(ws.ContextDir())
}

// userMemoryStore opens the machine-wide user-tier memory store, resolved
// exactly as CortexSession.EnableMemory() resolves cs.userMemory
// (session_runtime.go: userhome.Path("memory") then memory.New(that dir)) —
// so this reads the exact store the coder's scope="user" memory tool calls
// already write, in every project on this machine.
func userMemoryStore() (*memory.Store, error) {
	dir, err := userhome.Path("memory")
	if err != nil {
		return nil, err
	}
	return memory.New(dir)
}

// memoryNoteView is one note's list-row shape: identity + a one-line hook +
// its last-updated instant, tier-tagged. No body here — matching the
// landscape screen's names-only privacy stance (docs/cross-source-
// learning.md piece 4); the full body is a separate GET /api/memory/note
// call, made only when a human actually opens a note.
type memoryNoteView struct {
	Tier    string `json:"tier"`
	Name    string `json:"name"`
	Hook    string `json:"hook"`
	Updated string `json:"updated,omitempty"`
}

// memoryViewModel is the full memory screen for one project: its own
// project-tier notes plus every user-tier note (the same two tiers
// memory_search shows the model, docs/cross-source-learning.md piece 1).
type memoryViewModel struct {
	Project string           `json:"project"`
	Notes   []memoryNoteView `json:"notes"`
}

// notesToView converts a store's NoteMeta list to tier-tagged view rows,
// preserving Store.List's own most-recently-updated-first order.
func notesToView(metas []memory.NoteMeta, tier string) []memoryNoteView {
	out := make([]memoryNoteView, 0, len(metas))
	for _, m := range metas {
		v := memoryNoteView{Tier: tier, Name: m.Name, Hook: m.Hook}
		if !m.Updated.IsZero() {
			v.Updated = m.Updated.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out
}

// buildMemoryViewModel composes the named project's project-tier notes
// (first — matches the doc's project-first tagging in MemorySearch's own
// render order) with every user-tier note. A project that fails to
// resolve (unregistered name, workspace error) fails the whole build — the
// caller (handleListMemory) maps registry.ErrProjectNotFound to 404. The
// user-tier store is best-effort: a project can be inspected even when the
// user tier is unavailable (e.g. no writable home directory), same
// "unavailable, not fatal" posture MemorySearch/MemoryRead already take.
func buildMemoryViewModel(reg registry.Registry, projectName string) (memoryViewModel, error) {
	projStore, err := projectMemoryStore(reg, projectName)
	if err != nil {
		return memoryViewModel{}, err
	}
	projMetas, err := projStore.List()
	if err != nil {
		return memoryViewModel{}, err
	}

	vm := memoryViewModel{Project: projectName, Notes: notesToView(projMetas, scopeProject)}

	if userStore, err := userMemoryStore(); err == nil {
		if userMetas, err := userStore.List(); err == nil {
			vm.Notes = append(vm.Notes, notesToView(userMetas, scopeUser)...)
		}
	}
	return vm, nil
}
