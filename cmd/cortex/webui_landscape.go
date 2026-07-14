// webui_landscape.go — M5.2c: the landscape report view-model (GOAL.md §6
// M5.2, split into M5.2a/b/c/d per STATE.md; GOAL.md §2 names
// cmd/cortex/webui*.go as home for the web UI's Go view-model builders).
// Per GOAL.md §3 P5, rendering logic lives in golden-tested Go view-models
// — this file is pure data assembly, no HTML/JS.
//
// The landscape report is "Phase 2 report, rendered" (docs/cortex-web.md
// Phase 5) — the identical ScanReport shape `cortex scan --json` prints and
// GET /api/landscape (M4.2c1, serve_landscape.go) already returns. Unlike
// the dashboard (M5.2a, which composes registry+sessions+git status into a
// new shape) or the transcript (M5.2b, which flattens JSONL messages into
// display entries), ScanReport's fields (Roots, Tools, Runtimes, Projects,
// Truncated) are already exactly what a landscape screen renders — names
// and paths only, per M2's content-non-leak invariant, and nothing more —
// so the view-model here is ScanReport itself; a distinct wrapper struct
// would carry no field a real screen needs and no test could tell apart
// from a plain pass-through (GOAL.md §1's "no test would notice reverting"
// anti-pattern).
package main

// buildLandscapeViewModel resolves the persisted scan roots and runs the
// same scan handleLandscape's HTTP handler does — factored out here so the
// view-model has one Go-level entry point testable without httptest, and
// handleLandscape delegates to it so the API and the eventual JS screen can
// never drift on what "the landscape" means (GOAL.md pillar 3: reuse the
// seam, one write/read path). Propagates ErrNoScanRoots verbatim (via
// resolveScanRoots) for the same "typed refusal, not a blind $HOME sweep"
// reason handleLandscape surfaces as 412.
func buildLandscapeViewModel(configPath, homeDir string) (ScanReport, error) {
	roots, err := resolveScanRoots(configPath, "")
	if err != nil {
		return ScanReport{}, err
	}
	return buildScanReport(homeDir, roots, defaultScanCaps)
}
