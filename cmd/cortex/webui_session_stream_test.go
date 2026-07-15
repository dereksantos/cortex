// webui_session_stream_test.go — M5.3c4: live SSE progress for the session
// screen's turn submission. Swaps the M5.3c3 plain POST .../turn +
// wait-for-one-JSON-response flow for POST .../turn/stream
// (serve_stream.go, live since M4.2b3) consumed via fetch + a streamed
// ReadableStream reader (chosen over EventSource — EventSource is GET-only
// and can't carry the turn's input text in a request body; see this
// increment's Decisions Log entry for the full reasoning), rendering
// "progress"/"result"/"error" SSE frames as they arrive. Same structural
// testing convention as M5.3b/c1/c2/c3 (recorded in STATE.md's Decisions
// Log): this stdlib-only suite has no JS engine, so assertions are over the
// embedded JS source text, not executed DOM. The stream endpoint itself is
// already httptest- and golden-covered (serve_stream_test.go,
// serve_sse_golden_test.go).
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSessionScreenAppJSPostsToTurnStreamEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/turn/stream") {
		t.Error("app.js does not POST to the .../turn/stream endpoint")
	}
}

func TestSSEJSParsesEventAndDataFrames(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "sse.js")
	if err != nil {
		t.Fatalf("ReadFile(sse.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "getReader") {
		t.Error("sse.js does not read the fetch response body via a streaming reader (getReader) — EventSource can't carry a POST body")
	}
	if !strings.Contains(src, `"\n\n"`) {
		t.Error("sse.js does not split on the SSE frame boundary (\\n\\n)")
	}
	if !strings.Contains(src, "event: ") || !strings.Contains(src, "data: ") {
		t.Error("sse.js does not parse \"event: \"/\"data: \" lines out of each SSE frame")
	}
	if !strings.Contains(src, "JSON.parse") {
		t.Error("sse.js does not JSON-decode each frame's data payload")
	}
}

func TestSessionScreenAppJSHandlesProgressResultAndErrorEvents(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	for _, event := range []string{"thinking", "progress", "result", "error"} {
		if !strings.Contains(src, event+":") {
			t.Errorf("app.js does not register a %q SSE event handler", event)
		}
	}
}

func TestSessionScreenAppJSStillRerendersAfterTurnCompletes(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "app.js")
	if err != nil {
		t.Fatalf("ReadFile(app.js): %v", err)
	}
	src := string(data)
	if strings.Count(src, "loadSession()") < 2 {
		t.Error("app.js's loadSession() is only called once — the terminal \"result\" event handler must still re-render the transcript")
	}
}

func TestIndexHTMLLoadsSSEScriptBeforeAppJS(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	sseIdx := strings.Index(src, "sse.js")
	appIdx := strings.Index(src, "app.js")
	if sseIdx == -1 {
		t.Fatal("index.html does not load sse.js")
	}
	if appIdx == -1 {
		t.Fatal("index.html does not load app.js")
	}
	if sseIdx > appIdx {
		t.Error("index.html loads sse.js after app.js — app.js's streamSSE() call needs sse.js already loaded")
	}
}
