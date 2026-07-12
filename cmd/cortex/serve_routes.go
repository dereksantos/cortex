// serve_routes.go — M4.2a: the first slice of `cortex serve`'s real endpoint
// surface (GOAL.md §6 M4.2, split into M4.2a/b/c per STATE.md). Read-only:
// list registered projects and peek at a project's session transcripts.
// Deliberately does NOT construct a live *CortexSession or take any lock —
// that's M4.2b's SessionManager (create/resume/turn/SSE).
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/dereksantos/cortex/internal/registry"
)

// projectSessionsLimit bounds how many sessions the listing endpoint
// returns per project (newest first, matching listSessions' own ordering).
const projectSessionsLimit = 50

// sessionSummary is the serve-facing JSON shape for one session's listing
// row. Deliberately a distinct type from sessionInfo (session.go) rather
// than exporting/JSON-tagging that REPL-internal type directly — keeps the
// wire shape (snake_case, stable) independent of the REPL's own field
// names.
type sessionSummary struct {
	ID       string    `json:"id"`
	ModTime  time.Time `json:"mod_time"`
	Messages int       `json:"messages"`
	First    string    `json:"first"`
}

// writeJSON marshals v to w with the given status code, setting the JSON
// content type. Errors marshaling are not expected for the plain structs
// these handlers emit; if json.Marshal ever fails here it's a programming
// error, not a request-input problem, so it's logged to the response as a
// 500 rather than silently swallowed.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// handleListProjects serves GET /api/projects: every registered project,
// from the shared internal/registry.Registry (small-interface, so this
// handler is httptest-testable with a fake registry, no model needed).
func handleListProjects(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projects, err := reg.List()
		if err != nil {
			http.Error(w, "failed to list projects: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if projects == nil {
			projects = []registry.Project{}
		}
		writeJSON(w, http.StatusOK, projects)
	}
}

// handleListProjectSessions serves GET /api/projects/{name}/sessions: a
// read-only peek at the named project's session transcripts (newest
// first), reusing the existing listSessions helper against the project's
// resolved Workspace.SessionsDir() — no live *CortexSession, no fslock
// acquisition (that begins at M4.2b's create/resume).
func handleListProjectSessions(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		proj, err := reg.Lookup(name)
		if err != nil {
			if errors.Is(err, registry.ErrProjectNotFound) {
				http.Error(w, "project not registered: "+name, http.StatusNotFound)
				return
			}
			http.Error(w, "failed to look up project: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ws, err := NewWorkspace(proj.Root)
		if err != nil {
			http.Error(w, "failed to resolve project workspace: "+err.Error(), http.StatusInternalServerError)
			return
		}
		infos, err := listSessions(ws.SessionsDir(), projectSessionsLimit)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// No sessions directory yet is not an error from the API's
				// perspective — an unstarted project simply has none.
				writeJSON(w, http.StatusOK, []sessionSummary{})
				return
			}
			http.Error(w, "failed to list sessions: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]sessionSummary, len(infos))
		for i, info := range infos {
			out[i] = sessionSummary(info)
		}
		writeJSON(w, http.StatusOK, out)
	}
}
