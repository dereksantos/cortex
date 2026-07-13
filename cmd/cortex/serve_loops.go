// serve_loops.go — M6.7b: GET /api/loops, wiring M6.7a's
// buildLoopsViewModel (webui_loops.go) into the HTTP surface. Go-only, no
// screen yet — mirrors M4.2a/M5.3c1's endpoint-first precedent (see
// STATE.md's Next Up note for this increment). Read-only: create/enable/
// disable/run-now are M6.7c/d/e.
package main

import (
	"net/http"

	"github.com/dereksantos/cortex/internal/loops"
)

// handleListLoops serves GET /api/loops: every registered loop spec plus
// its run history, via internal/loops.Store (a small interface, so this
// handler is httptest-testable with a fake — no model, no real journal
// needed unless the test actually wants run-history coverage).
func handleListLoops(store loops.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vm, err := buildLoopsViewModel(store)
		if err != nil {
			http.Error(w, "failed to build loops view: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	}
}
