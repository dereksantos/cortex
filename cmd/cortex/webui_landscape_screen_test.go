// webui_landscape_screen_test.go — M5.3d: the landscape screen. Renders
// buildLandscapeViewModel (M5.2c) via GET /api/landscape (M4.2c1,
// serve_landscape.go — no new endpoint needed here) into a #landscape
// container, following the M5.3b/c screens' established fetch/render
// pattern. Kept as its own landscape.js file (not folded into app.js) per
// M5.3a's mechanical size caps — app.js is already near the 300-line
// per-file cap after M5.3c's session-screen growth, the same reasoning that
// split sse.js out at M5.3c4. Same structural-source-content testing
// convention as M5.3b/c (no JS engine in this stdlib-only suite; assertions
// are over the embedded JS/HTML source text, not executed DOM).
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestLandscapeScreenJSFetchesLandscapeEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "landscape.js")
	if err != nil {
		t.Fatalf("ReadFile(landscape.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/landscape") {
		t.Error("landscape.js does not fetch /api/landscape")
	}
}

func TestLandscapeScreenIndexHTMLHasLandscapeContainer(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `id="landscape"`) {
		t.Error("index.html does not declare a #landscape container for the screen's rendered output")
	}
}

func TestIndexHTMLLoadsAppJSBeforeLandscapeJS(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	appIdx := strings.Index(src, "app.js")
	landscapeIdx := strings.Index(src, "landscape.js")
	if appIdx == -1 {
		t.Fatal("index.html does not load app.js")
	}
	if landscapeIdx == -1 {
		t.Fatal("index.html does not load landscape.js")
	}
	if appIdx > landscapeIdx {
		t.Error("index.html loads app.js after landscape.js — landscape.js's apiFetch() calls need app.js already loaded")
	}
}

func TestLandscapeScreenJSHandlesNoRootsRefusal(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "landscape.js")
	if err != nil {
		t.Fatalf("ReadFile(landscape.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "412") {
		t.Error("landscape.js does not handle the 412 ErrNoScanRoots typed refusal GET /api/landscape returns when no scan roots are configured")
	}
}

func TestLandscapeScreenJSReportsTruncation(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "landscape.js")
	if err != nil {
		t.Fatalf("ReadFile(landscape.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "truncated") {
		t.Error("landscape.js does not surface ScanReport.truncated")
	}
}
