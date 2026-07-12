// serve_turn.go — M4.2b2: POST .../turn runs session.Turn against a live
// *managedSession, serialized by managedSession's own mutex (GOAL.md §3 P4:
// "one turn at a time per session, different sessions concurrent" — the
// discord mutex generalized, see serve_session.go's managedSession.mu). SSE
// progress streaming is M4.2b3; this increment returns the turn's final
// result as plain JSON.
package main

import (
	"encoding/json"
	"net/http"
)

// turnRequest is the JSON body for POST
// /api/projects/{name}/sessions/{id}/turn.
type turnRequest struct {
	Input string `json:"input"`
}

// turnResponse is the wire shape POST .../turn returns.
type turnResponse struct {
	Reply       string `json:"reply"`
	Interrupted bool   `json:"interrupted"`
}

// handleTurn serves POST /api/projects/{name}/sessions/{id}/turn: runs
// session.Turn against the live *managedSession the SessionManager tracks
// for id, holding that session's mutex for the duration so a second
// concurrent turn on the SAME session serializes behind it instead of
// racing cs.Request.Messages (GOAL.md §3 P4). An id the manager doesn't
// currently hold live (never created/resumed on this process) is a 404 —
// this endpoint does not implicitly resume from disk; POST .../sessions
// with {"resume": "<id>"} (M4.2b1) does that.
func handleTurn(mgr *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ms, ok := mgr.Get(id)
		if !ok {
			http.Error(w, "session not found: "+id, http.StatusNotFound)
			return
		}

		var body turnRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		ms.mu.Lock()
		defer ms.mu.Unlock()

		result, err := ms.cs.Turn(r.Context(), body.Input)
		if err != nil {
			http.Error(w, "turn failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, turnResponse(result))
	}
}
