// M4.2b3 — POST .../turn/stream: same live-session lookup + turn-serializing
// mutex as handleTurn (serve_turn.go, M4.2b2), but streams each Progress line
// (cmd/cortex/loop.go's existing per-tool-call breadcrumb seam) as an SSE
// "progress" event while the turn is in flight, then a terminal "result"
// event carrying the same reply/interrupted shape POST .../turn returns.
// The dedicated "SSE event order and shape golden-tested" + "no
// WriteTimeout" test is M4.5's box (GOAL.md §6) — this increment only proves
// the stream exists and carries a real progress event plus a terminal result
// event.
package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

// streamTurnTestSessionFactory builds a sessionFactory whose *CortexSession
// points at a scripted two-round backend: round 1 answers with a tool call
// (bash "echo hi", the exact allowlisted-safe command
// TestTurnStopsRepeatedToolCalls already established needs no
// classifyShell/reasoner fake), round 2 answers with final content — driving
// exactly one real Progress line through the full coderDispatcher/tools.Execute
// path (not a fake Dispatch), so the SSE handler is proven against the real
// seam runLoop already exposes.
func streamTurnTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	var round int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		round++
		if round == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// sseEvents splits a raw SSE body into (event-name, data-line) pairs, one per
// blank-line-delimited block — the minimal parse needed to assert shape
// without pulling in a full SSE client.
func sseEvents(t *testing.T, body string) []struct{ event, data string } {
	t.Helper()
	var events []struct{ event, data string }
	var cur struct{ event, data string }
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.event != "" {
				events = append(events, cur)
			}
			cur = struct{ event, data string }{}
		}
	}
	if cur.event != "" {
		events = append(events, cur)
	}
	return events
}

func TestTurnStreamEndpointStreamsProgressAndResult(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, streamTurnTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
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
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	var body strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
	}
	events := sseEvents(t, body.String())

	var sawProgress, sawResult bool
	for _, ev := range events {
		switch ev.event {
		case "progress":
			var p progressEvent
			if err := json.Unmarshal([]byte(ev.data), &p); err != nil {
				t.Fatalf("progress event data %q: %v", ev.data, err)
			}
			if !strings.Contains(p.Line, "bash(echo hi)") {
				t.Errorf("progress line = %q, want it to name the bash(echo hi) call", p.Line)
			}
			sawProgress = true
		case "result":
			var r turnResponse
			if err := json.Unmarshal([]byte(ev.data), &r); err != nil {
				t.Fatalf("result event data %q: %v", ev.data, err)
			}
			if r.Reply != "ok" {
				t.Errorf("result reply = %q, want %q", r.Reply, "ok")
			}
			if r.Interrupted {
				t.Error("result Interrupted = true for a clean turn, want false")
			}
			sawResult = true
		case "error":
			t.Errorf("unexpected error event: %s", ev.data)
		}
	}
	if !sawProgress {
		t.Error("no progress event seen — the Progress seam should have fired for the bash tool call")
	}
	if !sawResult {
		t.Error("no terminal result event seen")
	}
	// The result event must be the LAST event: a consumer that stops reading
	// after "result" must not miss anything.
	if len(events) == 0 || events[len(events)-1].event != "result" {
		t.Errorf("last event = %q, want \"result\" to be terminal", events[len(events)-1].event)
	}
}

func TestTurnStreamEndpointUnknownSessionReturns404(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: t.TempDir()}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/does-not-exist/turn/stream", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestTurnStreamEndpointRequiresAuth(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, streamTurnTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn/stream", "application/json", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
