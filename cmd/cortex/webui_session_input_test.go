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

// TestSessionScreenComposerHasImageAttachmentFields is issue #218 step 6: the
// composer must offer BOTH attachment kinds (a file picker and an image-URL
// text field), collect them into the turn/stream body, and show a visible
// attachment line. Same convention as the tests above — no JS engine here, so
// assertions are over the embedded source text.
//
// They read attach.js because that is where the picker lives (rendered into
// the composer by renderComposerAttachments, which app.js calls); asserting
// against app.js alone would pass even if the picker were never wired in, so
// the pair is checked together: the elements in attach.js, the call and the
// body field in app.js.
func TestSessionScreenComposerHasImageAttachmentFields(t *testing.T) {
	attachSrc := readWebUIJS(t, "attach.js")
	appSrc := readWebUIJS(t, "app.js")

	t.Run("the picker elements exist", func(t *testing.T) {
		for _, want := range []struct {
			needle string
			why    string
		}{
			{`type: "file"`, "attach.js offers no file picker input"},
			{`turn-attach-file`, "attach.js's file picker has no stable id"},
			{`turn-attach-url`, "attach.js offers no image-URL text field"},
			{`turn-attach-line`, "attach.js renders no visible attachment line"},
		} {
			if !strings.Contains(attachSrc, want.needle) {
				t.Error(want.why)
			}
		}
	})

	t.Run("a chosen file becomes a base64 data URI", func(t *testing.T) {
		if !strings.Contains(attachSrc, "readAsDataURL") {
			t.Error("attach.js never reads the chosen file as a data: URI")
		}
		if !strings.Contains(attachSrc, ";base64,") {
			t.Error("attach.js does not check the base64 payload it read")
		}
	})

	t.Run("the composer wires the picker in and sends both fields", func(t *testing.T) {
		if !strings.Contains(appSrc, "renderComposerAttachments(") {
			t.Error("app.js's composer never renders the attachment picker")
		}
		if !strings.Contains(appSrc, "attachments") {
			t.Error("app.js never puts attachments in the turn/stream body")
		}
		// Both shapes the server accepts (serve_attachments.go): posted bytes
		// for a browser-picked file, a URL the server fetches.
		if !strings.Contains(attachSrc, "data:") || !strings.Contains(attachSrc, "url:") {
			t.Error("attach.js does not build both the {data} and {url} attachment shapes")
		}
	})

	t.Run("a rejected file is reported in the status, not sent", func(t *testing.T) {
		// The rejection must reach the composer's status element AND stop the
		// send: collect() returns an error and app.js returns on it.
		if !strings.Contains(attachSrc, "status.textContent") {
			t.Error("attach.js reports nothing to the composer status for a rejected file")
		}
		if !strings.Contains(attachSrc, "is not a supported image") {
			t.Error("attach.js does not reject an unsupported file type")
		}
		if !strings.Contains(attachSrc, "image limit") {
			t.Error("attach.js does not reject an over-cap file")
		}
		// app.js must bail on the collector's error instead of posting.
		if !strings.Contains(appSrc, "collected.error") {
			t.Error("app.js sends the turn without checking the attachment collector's error")
		}
		if idx := strings.Index(appSrc, "collected.error"); idx > 0 {
			window := appSrc[idx:]
			if len(window) > 400 {
				window = window[:400]
			}
			if !strings.Contains(window, "return") {
				t.Error("app.js notes the attachment error but does not stop the send")
			}
		}
	})

	t.Run("a sent attachment does not ride onto the next turn", func(t *testing.T) {
		if !strings.Contains(attachSrc, "clear: function") {
			t.Error("attach.js offers no way to drop a sent attachment")
		}
		if !strings.Contains(appSrc, "attach.clear()") {
			t.Error("app.js never clears the pending attachment after a send")
		}
	})

	t.Run("the picker is loaded by the page", func(t *testing.T) {
		html, err := fs.ReadFile(webUIFS(), "index.html")
		if err != nil {
			t.Fatalf("ReadFile(index.html): %v", err)
		}
		if !strings.Contains(string(html), "/attach.js") {
			t.Error("index.html never loads attach.js, so the composer could never use it")
		}
	})
}

func readWebUIJS(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(webUIFS(), name)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", name, err)
	}
	return string(data)
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
