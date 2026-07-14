// serve_e2e_test.go — M5.4: the end-to-end smoke test GOAL.md §6 asks for
// to close M5. Drives the full path over the real HTTP surface with a
// scripted Sender and no live model: create a session, POST a turn over
// the SSE stream endpoint, observe the stream render (progress + terminal
// result frames), then GET the transcript endpoint and assert the
// rendered view-model reflects the turn just posted. Go-only/httptest — no
// browser, no JS execution (the JS side of "SSE stream renders" is already
// covered structurally by webui_session_stream_test.go; M4.5's
// serve_sse_golden_test.go already pins the wire-level frame shape). Per
// STATE.md's Next Up note, this reuses streamTurnTestSessionFactory and
// sseEvents from serve_stream_test.go rather than re-inventing a scripted
// backend or a frame parser.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestServeEndToEndSmokeCreateSessionTurnStreamAndTranscriptReflectsIt(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, streamTurnTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	// 1. Create a session over the real HTTP surface (not mgr.Create
	// directly) — the smoke test's job is proving the wired-together
	// endpoints, not the SessionManager unit in isolation.
	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest(create): %v", err)
	}
	createReq.Header.Set("Authorization", "Bearer tok")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("Do(create): %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", createResp.StatusCode)
	}
	var created createSessionResponse
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created session id is empty")
	}

	// 2. POST a turn over the SSE stream endpoint and consume the frames —
	// the scripted backend (streamTurnTestSessionFactory) answers round 1
	// with a bash("echo hi") tool call, then round 2 with final content
	// "ok", driving one real progress line before the terminal result.
	turnReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+created.ID+"/turn/stream", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest(turn/stream): %v", err)
	}
	turnReq.Header.Set("Authorization", "Bearer tok")
	turnResp, err := http.DefaultClient.Do(turnReq)
	if err != nil {
		t.Fatalf("Do(turn/stream): %v", err)
	}
	defer turnResp.Body.Close()
	if turnResp.StatusCode != http.StatusOK {
		t.Fatalf("turn/stream status = %d, want 200", turnResp.StatusCode)
	}
	var rawBody strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := turnResp.Body.Read(buf)
		rawBody.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	events := sseEvents(t, rawBody.String())

	var sawProgress, sawResult bool
	for _, ev := range events {
		switch ev.event {
		case "progress":
			sawProgress = true
		case "result":
			var r turnResponse
			if err := json.Unmarshal([]byte(ev.data), &r); err != nil {
				t.Fatalf("result event data %q: %v", ev.data, err)
			}
			if r.Reply != "ok" {
				t.Errorf("result reply = %q, want %q", r.Reply, "ok")
			}
			sawResult = true
		case "error":
			t.Errorf("unexpected error event: %s", ev.data)
		}
	}
	if !sawProgress {
		t.Fatal("SSE stream never rendered a progress event for the bash tool call")
	}
	if !sawResult {
		t.Fatal("SSE stream never rendered a terminal result event")
	}

	// 3. GET the transcript page endpoint and assert it reflects the turn
	// just posted: the user's input, the bash tool call, its tool result,
	// and the final assistant reply all appear in transcript order.
	transcriptResp := doAuthedGet(t, ts.URL+"/api/projects/blog/sessions/"+created.ID, "tok")
	defer transcriptResp.Body.Close()
	if transcriptResp.StatusCode != http.StatusOK {
		t.Fatalf("transcript status = %d, want 200", transcriptResp.StatusCode)
	}
	var vm transcriptViewModel
	if err := json.NewDecoder(transcriptResp.Body).Decode(&vm); err != nil {
		t.Fatalf("decode transcript view-model: %v", err)
	}
	if vm.SessionID != created.ID {
		t.Errorf("transcript session id = %q, want %q", vm.SessionID, created.ID)
	}

	var sawUserInput, sawBashCall, sawToolResult, sawFinalReply bool
	for _, e := range vm.Entries {
		switch {
		case e.Role == RoleUser && e.Content == "hello":
			sawUserInput = true
		case e.Role == "assistant" && len(e.ToolCalls) > 0 && e.ToolCalls[0].Name == "bash":
			sawBashCall = true
		case e.Role == RoleTool && e.ToolCallID != "":
			sawToolResult = true
		case e.Role == "assistant" && e.Content == "ok":
			sawFinalReply = true
		}
	}
	if !sawUserInput {
		t.Error("transcript view-model is missing the posted user input \"hello\"")
	}
	if !sawBashCall {
		t.Error("transcript view-model is missing the assistant's bash tool call")
	}
	if !sawToolResult {
		t.Error("transcript view-model is missing the tool result for the bash call")
	}
	if !sawFinalReply {
		t.Error("transcript view-model is missing the final assistant reply \"ok\"")
	}
}
