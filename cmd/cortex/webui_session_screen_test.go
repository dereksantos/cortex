// webui_session_screen_test.go — M5.3c2: the session screen's static
// render. Following M5.3b's established testing convention (recorded in
// STATE.md's Decisions Log): this repo's stdlib-only suite has no JS
// engine, so "tested" for the hand-written JS/HTML means structural
// source-content assertions over the embedded FS, not executed-DOM
// assertions. This increment is read-only render only — the input box
// (M5.3c3) and live SSE (M5.3c4) land as later sub-items.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSessionScreenAppJSFetchesSessionEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/projects/") || !strings.Contains(src, "/sessions/") {
		t.Error("app.js does not fetch a /api/projects/{name}/sessions/{id} style path for the session screen")
	}
	if !strings.Contains(src, "Cortex web UI") {
		t.Error("app.js lost the \"Cortex web UI\" marker asserted by TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence")
	}
}

func TestSessionScreenAppJSReadsProjectAndSessionQueryParams(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `"project"`) || !strings.Contains(src, `"session"`) {
		t.Error("app.js does not read ?project=<name>&session=<id> query params for the session screen")
	}
}

func TestSessionScreenIndexHTMLHasSessionContainer(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `id="session"`) {
		t.Error("index.html does not declare a #session container for the session screen's rendered output")
	}
}
