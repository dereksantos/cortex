// webui_dashboard_screen_test.go — M5.3b: the dashboard screen's fetch/
// render logic. GOAL.md §6 M5.3 ("the four screens render those
// view-models") plus its Testing posture note in STATE.md's Next Up:
// this repo's stdlib-only test suite has no JS engine, so "tested" for the
// hand-written JS/HTML means structural source-content assertions over the
// embedded FS (the expected fetch call and DOM container exist in the
// shipped bytes) — not executed-DOM assertions. Recorded as a Decisions Log
// entry alongside this increment; M5.3c/d/e inherit the same convention.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestDashboardScreenAppJSFetchesDashboardEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/dashboard") {
		t.Error("app.js does not fetch /api/dashboard")
	}
	if !strings.Contains(src, "Cortex web UI") {
		t.Error("app.js lost the \"Cortex web UI\" marker asserted by TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence")
	}
}

func TestDashboardScreenIndexHTMLHasDashboardContainer(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `id="dashboard"`) {
		t.Error("index.html does not declare a #dashboard container for the screen's rendered output")
	}
}
