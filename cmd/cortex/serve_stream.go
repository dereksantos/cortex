// serve_stream.go — M4.2b3: POST .../turn/stream fans the existing Progress
// seam (cmd/cortex/loop.go's runLoop, now reachable via
// CortexSession.TurnWithProgress, turn.go) into Server-Sent Events while a
// turn is in flight, then emits one terminal "result" event carrying the
// same reply/interrupted shape POST .../turn (handleTurn, serve_turn.go)
// returns as plain JSON. GOAL.md D6/M4.5 requires the serve http.Server set
// no WriteTimeout for a stream this long-lived to survive — newServeServer
// (serve.go) already leaves it unset by omission (confirmed by reading
// serve.go, not assumed); this increment doesn't touch that. The exact SSE
// event order/shape is golden-tested at M4.5 — this increment only proves
// the stream exists and carries a real progress event plus a terminal
// result event.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// progressEvent is the SSE "progress" event payload: one per tool call,
// rendered from loop.go's existing progressLine (the same text the REPL's
// live Progress sink already shows).
type progressEvent struct {
	Line string `json:"line"`
}

// sseEvent writes one Server-Sent Event (an "event:" line naming the type, a
// "data:" line carrying JSON, and the blank line that closes it) and flushes
// so the client sees it immediately rather than buffered behind later
// events.
func sseEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal SSE payload for event %q: %w", event, err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("failed to write SSE event %q: %w", event, err)
	}
	flusher.Flush()
	return nil
}

// handleTurnStream serves POST /api/projects/{name}/sessions/{id}/turn/stream:
// the same live-session lookup and turn-serializing mutex as handleTurn
// (serve_turn.go, M4.2b2) — an id the manager doesn't currently hold live is
// a 404, and a second concurrent turn on the SAME session blocks behind this
// one's mutex exactly as the plain POST .../turn endpoint does — but streams
// each Progress line as an SSE "progress" event while the turn runs, then a
// final "result" event (or "error" on failure).
func handleTurnStream(mgr *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ms, ok := mgr.Get(id)
		if !ok {
			http.Error(w, "session not found: "+id, http.StatusNotFound)
			return
		}
		mgr.Touch(id) // M4.7: a live request resets the idle-eviction clock

		var body turnRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ms.mu.Lock()
		defer ms.mu.Unlock()

		progress := func(line string) {
			_ = sseEvent(w, flusher, "progress", progressEvent{Line: line})
		}

		result, err := ms.cs.TurnWithProgress(r.Context(), body.Input, progress)
		if err != nil {
			_ = sseEvent(w, flusher, "error", map[string]string{"error": err.Error()})
			return
		}
		_ = sseEvent(w, flusher, "result", turnResponse(result))
	}
}
