// serve_transcript.go — M5.3c1: wires M5.2b's buildTranscriptViewModel into
// the HTTP surface as GET /api/projects/{name}/sessions/{id}, so the session
// screen's JS has a single endpoint returning exactly the view-model it
// renders (GOAL.md §6 M5.3: "the four screens render those view-models").
// Mirrors serve_dashboard.go's (M5.3b) thin-handler-over-pure-builder shape;
// resolving name+id to a transcript path follows handleListProjectSessions'
// (serve_routes.go, M4.2a) reg.Lookup -> NewWorkspace -> SessionsDir()
// pattern, then joins "<id>.jsonl" the same way session.go's own
// StartTranscript/ResumeTranscript do.
package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dereksantos/cortex/internal/registry"
)

// handleGetSession serves GET /api/projects/{name}/sessions/{id}: the full
// session transcript view-model (buildTranscriptViewModel,
// webui_transcript.go) as JSON. Deliberately independent of SessionManager —
// like handleListProjectSessions, this reads the on-disk transcript directly
// and works whether or not the manager currently holds the session live.
func handleGetSession(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		id := r.PathValue("id")

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

		path := filepath.Join(ws.SessionsDir(), id+".jsonl")
		vm, err := buildTranscriptViewModel(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "session not found: "+id, http.StatusNotFound)
				return
			}
			http.Error(w, "failed to build transcript: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	}
}
