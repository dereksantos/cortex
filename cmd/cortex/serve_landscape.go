// serve_landscape.go — M4.2c1: GET /api/landscape, the read-only half of
// the landscape/models slice GOAL.md §6 M4.2 bundles together (split here
// into M4.2c1 landscape / M4.2c2 models — STATE.md). Deliberately calls
// scan.go's own resolveScanRoots/buildScanReport rather than inventing a
// parallel scan path — `cortex serve`'s landscape view and `cortex scan`'s
// CLI report can never drift on what roots or caps mean.
package main

import (
	"errors"
	"net/http"
)

// handleLandscape serves GET /api/landscape: the same ScanReport `cortex
// scan --json` prints, built from the persisted scan.roots config key
// under configPath (GOAL.md D3 — never a blind $HOME sweep) and homeDir
// (harness/runtime probes). Neither `cortex scan`'s --root flag nor
// --register apply here — this is a read-only view over whatever roots
// are already persisted; configuring roots is M1.7's greeting flow.
// ErrNoScanRoots surfaces as 412 Precondition Failed: the client needs to
// finish onboarding (answer where the code lives) before this endpoint has
// anything to report, distinct from a 500 the client can't act on.
func handleLandscape(configPath, homeDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roots, err := resolveScanRoots(configPath, "")
		if err != nil {
			if errors.Is(err, ErrNoScanRoots) {
				http.Error(w, err.Error(), http.StatusPreconditionFailed)
				return
			}
			http.Error(w, "failed to resolve scan roots: "+err.Error(), http.StatusInternalServerError)
			return
		}
		report, err := buildScanReport(homeDir, roots, defaultScanCaps)
		if err != nil {
			http.Error(w, "failed to build scan report: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}
