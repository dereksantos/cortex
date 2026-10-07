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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestServeEndToEndSmokeCreateSessionTurnStreamAndTranscriptReflectsIt(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	// Issue #219: the turn receipt's files-changed fact reads the
	// workspace's git state — the root must be a git repository so
	// gitWorkspace() is true and render() emits the files-changed
	// section (even empty), making the receipt non-empty and the
	// kindNote written. A non-repo temp dir measures nothing and the
	// receipt renders "".
	git := exec.Command("git", "init", "-q")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitc := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	gitc("config", "user.email", "t@t")
	gitc("config", "user.name", "t")
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, streamTurnTestSessionFactory(t))
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	// 1. Create a session over the real HTTP surface (not mgr.Create
	// directly) — the smoke test's job is proving the wired-together
	// endpoints, not the SessionManager unit in isolation.
	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest(create): %v", err)
	}
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
	transcriptResp := doGet(t, ts.URL+"/api/projects/blog/sessions/"+created.ID)
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
			// Issue #219: the turn's measurement receipt is harness output — it
			// rides the distinct TurnResult.Receipt field and persists as a
			// transcript-only kindNote entry, so the stored assistant message
			// (and its transcript entry) holds the model's verbatim reply.
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

	// Issue #219: the turn's measurement receipt is harness output — the
	// turn ran a tool (the bash call), so the receipt is computed and
	// persisted on the session's transcript as a kindNote entry, while the
	// model's reply stays the verbatim "ok" (asserted above: the receipt
	// rides TurnResult.Receipt and the kindNote, never the assistant
	// message).
	sessionPath := filepath.Join(root, ".cortex", "sessions", created.ID+".jsonl")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read transcript file %s: %v", sessionPath, err)
	}
	sawReceiptNote := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Kind    string `json:"kind"`
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		if entry.Kind == "note" && strings.Contains(entry.Content, "turn receipt:") {
			sawReceiptNote = true
		}
	}
	if !sawReceiptNote {
		t.Errorf("no kindNote transcript entry carries the turn receipt; transcript: %s", data)
	}
}
