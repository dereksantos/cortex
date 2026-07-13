// webui_loops_screen_test.go — M6.7f (owner amendment A4): the loops
// screen. Renders GET /api/loops' response (loopsViewModel: every
// registered loop's spec plus run history, live since M6.7b —
// serve_loops.go) into a #loops container, plus create/enable/disable/
// run-now controls wired to M6.7b-e's endpoints (POST /api/loops, POST
// /api/loops/{name}/enable, .../disable, .../run-now). No new endpoint
// needed — the read+write surface is already complete (M6.7a-e). Same
// structural-source-content testing convention as M5.3b/c/d/e (Decisions
// Log, set at M5.3b): this stdlib-only suite has no JS engine, so
// assertions are over the embedded JS/HTML source text, not executed DOM.
// The endpoints themselves are already httptest-covered by
// serve_loops_test.go.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestLoopsScreenJSFetchesLoopsEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	if !strings.Contains(string(data), "/api/loops") {
		t.Error("loops.js does not fetch /api/loops")
	}
}

func TestLoopsScreenIndexHTMLHasLoopsContainer(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	if !strings.Contains(string(data), `id="loops"`) {
		t.Error("index.html does not declare a #loops container for the screen's rendered output")
	}
}

func TestIndexHTMLLoadsAppJSBeforeLoopsJS(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	appIdx := strings.Index(src, "app.js")
	loopsIdx := strings.Index(src, "loops.js")
	if appIdx == -1 {
		t.Fatal("index.html does not load app.js")
	}
	if loopsIdx == -1 {
		t.Fatal("index.html does not load loops.js")
	}
	if appIdx > loopsIdx {
		t.Error("index.html loads app.js after loops.js — loops.js's authToken()/apiFetch() calls need app.js already loaded")
	}
}

func TestLoopsScreenJSHasCreateForm(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `createElement("form")`) {
		t.Error("loops.js does not create a <form> element for creating a new loop")
	}
	if !strings.Contains(src, `"POST"`) {
		t.Error("loops.js does not issue a POST request")
	}
}

func TestLoopsScreenJSHasEnableDisableControls(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/enable") {
		t.Error("loops.js does not target a .../enable endpoint")
	}
	if !strings.Contains(src, "/disable") {
		t.Error("loops.js does not target a .../disable endpoint")
	}
}

func TestLoopsScreenJSHasRunNowControl(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	if !strings.Contains(string(data), "/run-now") {
		t.Error("loops.js does not target a .../run-now endpoint")
	}
}

func TestLoopsScreenJSAuthenticatesWriteRequests(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	if !strings.Contains(string(data), "Authorization") {
		t.Error("loops.js does not authenticate its write requests")
	}
}

func TestLoopsScreenJSReloadsAfterActions(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	// loadLoops() must appear more than once: once as the initial page load
	// call, once inside a write action's success path re-rendering with the
	// newly changed state (create/enable/disable/run-now all mutate what
	// the next GET /api/loops returns).
	if strings.Count(src, "loadLoops()") < 2 {
		t.Error("loops.js's loadLoops() is only called once — write actions must call it again to re-render")
	}
}
