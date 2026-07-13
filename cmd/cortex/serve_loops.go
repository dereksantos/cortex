// serve_loops.go — M6.7b: GET /api/loops, wiring M6.7a's
// buildLoopsViewModel (webui_loops.go) into the HTTP surface. Go-only, no
// screen yet — mirrors M4.2a/M5.3c1's endpoint-first precedent (see
// STATE.md's Next Up note for this increment). M6.7c adds POST /api/loops
// (create), the write half — mirrors M4.2c2b1's PUT /api/models/{role}
// pattern: decode a JSON body into internal/loops.Spec, call Store.Save,
// map its typed loops.ErrCadenceTooLow to 400 (vs a generic 500 for any
// other failure), then read the persisted spec back via Store.Lookup so
// the response reflects exactly what landed, not just what was requested.
// M6.7d adds POST /api/loops/{name}/enable and .../disable, the single-
// field-toggle pair — combined into one item + one handler factory since
// both routes are the same Lookup-flip-Save shape, mirroring M4.2c2b1's
// combined-writes precedent. Run-now (M6.7e) is a separate item.
package main

import (
	"encoding/json"
	"errors"
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

// handleCreateLoop serves POST /api/loops: creates a new loop spec (or
// overwrites the existing one with the same name, per FileStore.Save's
// upsert-by-name semantics). A cadence below the 15-minute floor (D11) is
// surfaced as a typed 400 via loops.ErrCadenceTooLow, distinguishing "the
// request was invalid" from "something broke" (500).
func handleCreateLoop(store loops.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var spec loops.Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, "failed to decode request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := store.Save(spec); err != nil {
			if errors.Is(err, loops.ErrCadenceTooLow) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to save loop: "+err.Error(), http.StatusInternalServerError)
			return
		}

		saved, err := store.Lookup(spec.Name)
		if err != nil {
			http.Error(w, "failed to read back saved loop: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	}
}

// handleSetLoopEnabled serves POST /api/loops/{name}/enable and
// .../disable (via the enabled parameter this factory closes over): looks
// up the named spec, flips Enabled, and re-saves via Store.Save — an
// upsert-by-name full rewrite, so every other field survives unchanged.
// An unknown name maps loops.ErrLoopNotFound to 404, matching the
// registry-lookup precedent (e.g. handleListProjectSessions).
func handleSetLoopEnabled(store loops.Store, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		spec, err := store.Lookup(name)
		if err != nil {
			if errors.Is(err, loops.ErrLoopNotFound) {
				http.Error(w, "loop not registered: "+name, http.StatusNotFound)
				return
			}
			http.Error(w, "failed to look up loop: "+err.Error(), http.StatusInternalServerError)
			return
		}

		spec.Enabled = enabled
		if err := store.Save(spec); err != nil {
			http.Error(w, "failed to save loop: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, spec)
	}
}
