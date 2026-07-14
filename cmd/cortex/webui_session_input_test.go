// webui_session_input_test.go — M5.3c3: the session screen's input box. A
// text field + submit button posting a new turn via POST
// /api/projects/{name}/sessions/{id}/turn (serve_turn.go, live since
// M4.2b2), then re-rendering the transcript by calling loadSession() again.
// Same structural testing convention as M5.3c2 (Decisions Log, set at
// M5.3b): this stdlib-only suite has no JS engine, so assertions are over
// the embedded JS source text, not executed DOM. The endpoint itself is
// already httptest-covered by serve_turn_test.go.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSessionScreenAppJSPostsTurnOnSubmit(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/turn") {
		t.Error("app.js does not reference the .../turn endpoint")
	}
	if !strings.Contains(src, `"POST"`) {
		t.Error("app.js does not issue a POST request for submitting a turn")
	}
	if !strings.Contains(src, `addEventListener("submit"`) {
		t.Error("app.js does not wire a submit handler for the turn input form")
	}
}

func TestSessionScreenAppJSCreatesInputAndSubmitElements(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `createElement("input")`) {
		t.Error("app.js does not create a text input element for the turn box")
	}
	if !strings.Contains(src, `createElement("button")`) {
		t.Error("app.js does not create a submit button for the turn box")
	}
}

func TestSessionScreenAppJSReRendersAfterTurnSubmit(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	// loadSession() must appear more than once: once as the initial page
	// load call, once inside the turn-submit success handler re-rendering
	// the transcript with the newly posted turn.
	if strings.Count(src, "loadSession()") < 2 {
		t.Error("app.js's loadSession() is only called once — the turn-submit handler must call it again to re-render")
	}
}
