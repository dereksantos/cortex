// serve_turn.go — M4.2b2: POST .../turn runs session.Turn against a live
// *managedSession, serialized by managedSession's own mutex (GOAL.md §3 P4:
// "one turn at a time per session, different sessions concurrent" — the
// discord mutex generalized, see serve_session.go's managedSession.mu). SSE
// progress streaming is M4.2b3; this increment returns the turn's final
// result as plain JSON.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/dereksantos/cortex/internal/fslock"
)

// turnRequest is the JSON body for POST
// /api/projects/{name}/sessions/{id}/turn.
type turnRequest struct {
	Input string `json:"input"`
	// Attachments (#218) are images to carry on this turn, each named by a
	// workspace-relative `path` or an http(s) `url` — never by posted bytes.
	// Resolved by resolveTurnAttachments (serve_attachments.go), which is the
	// same routine the SSE endpoint uses, so the two cannot disagree about
	// what is attachable.
	Attachments []TurnAttachment `json:"attachments,omitempty"`
}

// turnResponse is the wire shape POST .../turn returns.
type turnResponse struct {
	Reply       string `json:"reply"`
	Interrupted bool   `json:"interrupted"`
}

// decodeTurnRequest reads a turn body and resolves its attachments, answering
// the HTTP status itself and returning ok=false when it did. Both turn
// endpoints go through here so the decoding and the attachment rules live in
// exactly one place (the issue's "no duplicated decoding") — a body shape or
// a confinement rule changed for one endpoint cannot miss the other.
//
// The two checks are ordered decode-then-resolve, and both are 400: a
// malformed body has no attachments to interpret, and an attachment refusal
// must be answered BEFORE the turn is claimed under the session mutex, so a
// rejected request never serializes behind (or displaces) a running turn.
func decodeTurnRequest(w http.ResponseWriter, r *http.Request, cs *CortexSession) (turnRequest, []TurnImage, bool) {
	var body turnRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
		return body, nil, false
	}
	images, attErr := resolveTurnAttachments(r.Context(), cs, body.Attachments)
	if attErr != nil {
		http.Error(w, attErr.Error(), attachmentHTTPStatus(attErr))
		return body, nil, false
	}
	return body, images, true
}

// handleTurn serves POST /api/projects/{name}/sessions/{id}/turn: runs
// session.Turn against the live *managedSession the SessionManager tracks
// for id, holding that session's mutex for the duration so a second
// concurrent turn on the SAME session serializes behind it instead of
// racing cs.Request.Messages (GOAL.md §3 P4). An id the manager doesn't
// currently hold live is transparently resumed from its on-disk transcript
// (SessionManager.GetOrResume, serve_session.go) — the same rehydrate path
// M4.7 idle-eviction already relies on, extended to cover a session this
// serve process never had live at all (e.g. a browser client navigating
// straight to an old session URL). Only a transcript that genuinely doesn't
// exist on disk is a 404; another process holding the transcript's lock is
// a 409.
func handleTurn(mgr *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ms, err := mgr.GetOrResume(r.PathValue("name"), id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "session not found: "+id, http.StatusNotFound)
				return
			}
			if errors.Is(err, fslock.ErrBusy) {
				http.Error(w, "session busy: "+err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "failed to resume session: "+err.Error(), http.StatusInternalServerError)
			return
		}
		mgr.Touch(id) // M4.7: a live request resets the idle-eviction clock

		body, images, ok := decodeTurnRequest(w, r, ms.cs)
		if !ok {
			return
		}

		ms.mu.Lock()
		defer ms.mu.Unlock()

		result, err := ms.cs.TurnWithAttachments(r.Context(), body.Input, images...)
		if err != nil {
			http.Error(w, "turn failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Explicit field-by-field construction (not a type conversion): TurnResult
		// carries StopReason (M6.4) that turnResponse's wire shape deliberately
		// does not expose — a direct conversion would require identical
		// underlying struct types.
		writeJSON(w, http.StatusOK, turnResponse{Reply: result.Reply, Interrupted: result.Interrupted})
	}
}
