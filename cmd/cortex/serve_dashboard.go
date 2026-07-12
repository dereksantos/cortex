// serve_dashboard.go — M5.3b: wires M5.2a's buildDashboardViewModel into
// the HTTP surface as GET /api/dashboard, so the dashboard screen's JS has
// a single endpoint returning exactly the view-model it renders (GOAL.md
// §6 M5.3: "the four screens render those view-models"). M4.2 didn't add
// this route — dashboardViewModel (registry + sessions + git change
// status) postdates M4.2's endpoint set; GET /api/projects (M4.2a) remains
// the plain registry listing other consumers use.
package main

import (
	"net/http"

	"github.com/dereksantos/cortex/internal/registry"
)

// handleDashboard serves GET /api/dashboard: the full project dashboard
// view-model (buildDashboardViewModel, webui_dashboard.go) as JSON.
func handleDashboard(reg registry.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vm, err := buildDashboardViewModel(reg)
		if err != nil {
			http.Error(w, "failed to build dashboard: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	}
}
