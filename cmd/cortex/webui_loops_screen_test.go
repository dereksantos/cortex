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

// TestLoopsScreenJSCadenceHintReflectsFiveMinuteFloor is D11's floor
// tuning's UI hint half: the create form's interval hint must say the new
// 5-minute floor, not the retired 15-minute one.
func TestLoopsScreenJSCadenceHintReflectsFiveMinuteFloor(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	if strings.Contains(src, "min 15") {
		t.Error("loops.js still says \"min 15\" — the cadence floor moved to 5 minutes")
	}
	if !strings.Contains(src, "min 5") {
		t.Error("loops.js does not say \"min 5\" — the create form's interval hint must reflect the new floor")
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

// TestLoopsScreenJSHasNextRunColumn is D11's self-pacing tuning's read
// side: the loops table must surface next_run/next_reason somewhere in its
// render, not just fetch them.
func TestLoopsScreenJSHasNextRunColumn(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "next_run") {
		t.Error("loops.js does not reference next_run — the loops screen must show the derived next-run instant")
	}
	if !strings.Contains(src, "next_reason") {
		t.Error("loops.js does not reference next_reason")
	}
}

// TestLoopsScreenJSChipsDisabledTerminalStates is D11's three-strike/DONE
// tuning's read side: a disabled loop's chip must distinguish "done" from
// "3 strikes" from a plain human-disabled loop, per disabled_reason.
func TestLoopsScreenJSChipsDisabledTerminalStates(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "loops.js")
	if err != nil {
		t.Fatalf("ReadFile(loops.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "disabled_reason") {
		t.Error("loops.js does not reference disabled_reason")
	}
	if !strings.Contains(src, "3 strikes") {
		t.Error("loops.js does not render a \"3 strikes\" chip for the auto-disable case")
	}
	if !strings.Contains(src, `"done"`) {
		t.Error("loops.js does not render a \"done\" chip for the DONE terminal state")
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
