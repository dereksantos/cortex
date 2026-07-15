// serve_thinking_test.go — item 6 of the thinking-models small wins: a
// served (quiet) session has no terminal to print a live "thinking…" ticker
// to, so the web UI otherwise sees dead air while a reasoning model
// deliberates. handleTurnStream (serve_stream.go) wires
// CortexSession.onThinking to emit an SSE "thinking" event on the
// reasoning/content transition; this proves the wiring end to end against a
// backend that actually streams reasoning_content deltas before its answer.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

// reasoningTurnTestSessionFactory scripts a single-round backend whose SSE
// response streams reasoning_content deltas before its final content — the
// case that only surfaces anything on the streaming path (a blocking
// response's Choice.Reasoning is parsed post-hoc, after the whole call
// already finished, too late for a live "started thinking" signal).
func reasoningTurnTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","reasoning_content":"Let me "}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"think about this."}}]}`,
			`{"choices":[{"delta":{"content":"Here's the answer."},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":6,"completion_tokens":9}}`,
		)))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

func TestTurnStreamEndpointEmitsThinkingStartAndStop(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, reasoningTurnTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn/stream", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		body.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	events := sseEvents(t, body.String())

	var thinkingSeq []bool
	var sawResult bool
	resultIdx, lastThinkingIdx := -1, -1
	for i, ev := range events {
		switch ev.event {
		case "thinking":
			var p thinkingEvent
			if err := json.Unmarshal([]byte(ev.data), &p); err != nil {
				t.Fatalf("thinking event data %q: %v", ev.data, err)
			}
			thinkingSeq = append(thinkingSeq, p.Active)
			lastThinkingIdx = i
		case "result":
			sawResult = true
			resultIdx = i
		}
	}

	if len(thinkingSeq) < 2 {
		t.Fatalf("got %d thinking events, want at least 2 (start, stop): %v", len(thinkingSeq), thinkingSeq)
	}
	if !thinkingSeq[0] {
		t.Errorf("first thinking event = %v, want true (deliberation started)", thinkingSeq[0])
	}
	if thinkingSeq[len(thinkingSeq)-1] {
		t.Errorf("last thinking event = %v, want false (deliberation ended before the answer)", thinkingSeq[len(thinkingSeq)-1])
	}
	if !sawResult {
		t.Fatal("no terminal result event seen")
	}
	if lastThinkingIdx > resultIdx {
		t.Errorf("a thinking event arrived after the terminal result event (index %d > %d)", lastThinkingIdx, resultIdx)
	}

	// No raw reasoning text ever rides the wire — only the on/off signal.
	if strings.Contains(body.String(), "Let me think") {
		t.Errorf("SSE stream leaked the raw reasoning text, want only the thinking on/off signal: %s", body.String())
	}
}

// TestTurnEndpointDoesNotStreamOrSignalThinking: the plain (non-SSE)
// POST .../turn endpoint never wires onThinking, so a quiet session without
// it stays on the original blocking Send — proven by a backend that only
// answers a single blocking JSON body (no SSE framing at all); if handleTurn
// accidentally started streaming, SendChat's SSE parser would see no "data:"
// lines and return an empty reply instead of erroring loudly.
func TestTurnEndpointDoesNotStreamOrSignalThinking(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	factory := func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
	mgr := NewSessionManager(reg, factory)
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result turnResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Reply != "ok" {
		t.Errorf("reply = %q, want %q (blocking Send should have parsed the plain JSON body)", result.Reply, "ok")
	}
}
