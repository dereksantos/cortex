// webui_memory_screen_test.go — the memory screen (docs/cross-source-
// learning.md piece 4). Same structural-source-content testing convention
// as the loops/landscape/models screens (webui_loops_screen_test.go etc,
// set at M5.3b): this stdlib-only suite has no JS engine, so assertions
// are over the embedded JS/HTML source text, not executed DOM. The
// endpoints themselves are already httptest-covered by
// serve_memory_test.go.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestMemoryScreenJSFetchesMemoryEndpoints(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "memory.js")
	if err != nil {
		t.Fatalf("ReadFile(memory.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/memory") {
		t.Error("memory.js does not fetch /api/memory")
	}
	if !strings.Contains(src, "/api/memory/note") {
		t.Error("memory.js does not fetch /api/memory/note")
	}
	if !strings.Contains(src, "/api/projects") {
		t.Error("memory.js does not fetch /api/projects for the project picker")
	}
}

func TestMemoryScreenIndexHTMLHasMemoryContainerAndNavLink(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `id="memory"`) {
		t.Error("index.html does not declare a #memory container for the screen's rendered output")
	}
	if !strings.Contains(src, `data-screen="memory"`) {
		t.Error("index.html does not declare a memory nav link/screen section")
	}
}

func TestIndexHTMLLoadsAppJSBeforeMemoryJS(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	appIdx := strings.Index(src, "app.js")
	memIdx := strings.Index(src, "memory.js")
	if appIdx == -1 {
		t.Fatal("index.html does not load app.js")
	}
	if memIdx == -1 {
		t.Fatal("index.html does not load memory.js")
	}
	if appIdx > memIdx {
		t.Error("index.html loads app.js after memory.js — memory.js's apiFetch() calls need app.js already loaded")
	}
}

// TestMemoryScreenJSHasDeleteWithConfirmStep is the task's "delete button
// per note with a confirm step" requirement: a DELETE call must be gated
// behind some form of confirmation state, not fired on the first click.
func TestMemoryScreenJSHasDeleteWithConfirmStep(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "memory.js")
	if err != nil {
		t.Fatalf("ReadFile(memory.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `"DELETE"`) {
		t.Error("memory.js does not issue a DELETE request")
	}
	if !strings.Contains(src, "confirming") {
		t.Error("memory.js's delete control does not appear to gate on a confirm step")
	}
}

// TestMemoryScreenJSRendersWikilinksAndGatesClickability is the "[[links]]
// as plain highlighted text — clickable only if the target exists in the
// loaded set" requirement.
func TestMemoryScreenJSRendersWikilinksAndGatesClickability(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "memory.js")
	if err != nil {
		t.Fatalf("ReadFile(memory.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "wikilink") {
		t.Error("memory.js does not render a wikilink class for [[name]] tokens")
	}
	if !strings.Contains(src, "missing") {
		t.Error("memory.js does not distinguish an unresolvable wikilink target")
	}
	if strings.Contains(src, "innerHTML") {
		t.Error("memory.js must render note bodies via plain DOM writes (textContent/createTextNode), never innerHTML with response data")
	}
}

// TestMemoryScreenJSHasProjectPicker checks the project-picker requirement:
// a <select> populated from the projects listing, changing the active
// project.
func TestMemoryScreenJSHasProjectPicker(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "memory.js")
	if err != nil {
		t.Fatalf("ReadFile(memory.js): %v", err)
	}
	if !strings.Contains(string(data), `createElement("select")`) {
		t.Error("memory.js does not build a project-picker <select>")
	}
}

// TestMemoryScreenJSHasLearnActivityStrip is the recent-learn-activity
// requirement: reuse the existing loops history API (GET /api/loops),
// filtered to kind:"learn", and degrade gracefully (no invented fields —
// this only reads outcome/reason/timestamp, all of which already exist on
// loops.RunRecord).
func TestMemoryScreenJSHasLearnActivityStrip(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "memory.js")
	if err != nil {
		t.Fatalf("ReadFile(memory.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/loops") {
		t.Error("memory.js does not reuse the existing GET /api/loops history API for the learn-activity strip")
	}
	if !strings.Contains(src, `"learn"`) {
		t.Error("memory.js does not filter loop runs to kind:\"learn\"")
	}
}
