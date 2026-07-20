// serve_status.go — GET /api/status (design item 4): the API half of
// `cortex serve status`. The CLI half (reading serve.pid for pid/port/
// uptime when the API is unreachable, and rendering this endpoint's body
// into a compact human summary) is serve_stop.go's runServeStatusCLI.
package main

import (
	"net/http"
	"time"

	"github.com/dereksantos/cortex/internal/loops"
)

// statusLoopView is one loop's slice of GET /api/status's body — just
// enough for a compact human summary, not the full spec+run-history
// GET /api/loops already returns (serve_loops.go).
type statusLoopView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	NextRun string `json:"next_run,omitempty"`
}

// statusResponse is GET /api/status's JSON body:
// {uptime_seconds, live_sessions, loops: [{name, enabled, next_run}]}.
type statusResponse struct {
	UptimeSeconds int64            `json:"uptime_seconds"`
	LiveSessions  int              `json:"live_sessions"`
	Loops         []statusLoopView `json:"loops"`
}

// handleStatus serves GET /api/status. startedAt is runServeCLI's own
// in-process start instant — the same value written to serve.pid, so
// uptime here and in the pid file never disagree. Reuses
// buildLoopsViewModel's NextRunAt computation (webui_loops.go) for the
// loops slice, so this endpoint never disagrees with GET /api/loops or the
// scheduler itself about when a loop next fires.
func handleStatus(startedAt time.Time, mgr *SessionManager, loopsStore loops.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vm, err := buildLoopsViewModel(loopsStore)
		if err != nil {
			http.Error(w, "failed to build loops view: "+err.Error(), http.StatusInternalServerError)
			return
		}
		loopViews := make([]statusLoopView, 0, len(vm.Loops))
		for _, l := range vm.Loops {
			loopViews = append(loopViews, statusLoopView{Name: l.Name, Enabled: l.Enabled, NextRun: l.NextRun})
		}
		writeJSON(w, http.StatusOK, statusResponse{
			UptimeSeconds: int64(time.Since(startedAt).Seconds()),
			LiveSessions:  len(mgr.List()),
			Loops:         loopViews,
		})
	}
}
