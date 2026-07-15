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
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/dereksantos/cortex/internal/fslock"
)

// progressEvent is the SSE "progress" event payload: one per tool call,
// rendered from loop.go's existing progressLine (the same text the REPL's
// live Progress sink already shows).
type progressEvent struct {
	Line string `json:"line"`
}

// thinkingEvent is the SSE "thinking" event payload: Active=true when the
// model starts deliberating (its first reasoning delta), Active=false once
// its answer content starts (or the call ends without any). Never carries
// the reasoning text itself — a served (quiet) session has nowhere to print
// a live ticker, so this is the web UI's only signal that something is
// happening instead of dead air (see CortexSession.onThinking, streaming.go's
// sendQuietObserved).
type thinkingEvent struct {
	Active bool `json:"active"`
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
// the same live-session lookup (mgr.GetOrResume, transparently resuming from
// the on-disk transcript when this serve process doesn't already hold id
// live — serve_session.go, serve_turn.go's handleTurn doc) and
// turn-serializing mutex as handleTurn — a second concurrent turn on the
// SAME session blocks behind this one's mutex exactly as the plain
// POST .../turn endpoint does — but streams each Progress line as an SSE
// "progress" event while the turn runs, then a final "result" event (or
// "error" on failure). A transcript that genuinely doesn't exist is a 404;
// another process holding its lock is a 409.
func handleTurnStream(mgr *SessionManager) http.HandlerFunc {
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

		// Wired only for the duration of this request: ms.cs.send() (quiet
		// path, streaming.go) calls this on the reasoning/content transition
		// so the web UI sees a "thinking" event instead of dead air while the
		// model deliberates. Cleared unconditionally after so a later request
		// against the same live session never fires into this request's
		// closed response writer.
		ms.cs.onThinking = func(active bool) {
			_ = sseEvent(w, flusher, "thinking", thinkingEvent{Active: active})
		}
		defer func() { ms.cs.onThinking = nil }()

		result, err := ms.cs.TurnWithProgress(r.Context(), body.Input, progress)
		if err != nil {
			_ = sseEvent(w, flusher, "error", map[string]string{"error": err.Error()})
			return
		}
		// Explicit field-by-field construction — see serve_turn.go's handleTurn
		// for why this isn't a bare type conversion (TurnResult.StopReason,
		// M6.4).
		_ = sseEvent(w, flusher, "result", turnResponse{Reply: result.Reply, Interrupted: result.Interrupted})
	}
}
