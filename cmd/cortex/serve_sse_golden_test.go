// serve_sse_golden_test.go — M4.5: the SSE event order/shape golden test
// GOAL.md §6 asks for. M4.2b3 (serve_stream_test.go) already proved the
// stream carries a real progress event plus a terminal result event for a
// SINGLE tool call; this increment pins the exact byte-for-byte frame shape
// across a scripted MULTI-step turn (two tool calls, not one) so event
// ORDER — not just presence — is provably asserted, following the
// "golden-pinned = literal string in a test" convention (greetingPrompt,
// renderScanReport) rather than inventing an on-disk golden-file mechanism.
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

// twoStepStreamTestSessionFactory scripts a three-round backend: two rounds
// each answer with a distinct bash tool call, the third answers with final
// content — driving TWO real Progress lines (in order) through the full
// coderDispatcher/tools.Execute path before the terminal result, the exact
// multi-step shape M4.2b3's single-tool-call factory didn't exercise.
//
// SSE (not blocking JSON) — see streamTurnTestSessionFactory's doc
// (serve_stream_test.go) for why every model call in a served session now
// streams even in quiet mode.
func twoStepStreamTestSessionFactory(t *testing.T) sessionFactory {
	t.Helper()
	var round int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		round++
		w.Header().Set("Content-Type", "text/event-stream")
		switch round {
		case 1:
			_, _ = w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			)))
		case 2:
			_, _ = w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo bye\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			)))
		default:
			_, _ = w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":6,"completion_tokens":3}}`,
			)))
		}
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

func TestTurnStreamEndpointGoldenFramesForMultiStepTurn(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, twoStepStreamTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet())))
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	// Byte-for-byte golden: two ordered progress frames (bash(echo hi) then
	// bash(echo bye)) followed by the terminal result frame — nothing before
	// the first event, nothing after the last.
	want := "event: progress\ndata: {\"line\":\"  ▸ bash(echo hi)\"}\n\n" +
		"event: progress\ndata: {\"line\":\"  ▸ bash(echo bye)\"}\n\n" +
		"event: result\ndata: {\"reply\":\"done\",\"interrupted\":false}\n\n"
	if string(body) != want {
		t.Errorf("SSE stream body =\n%q\nwant\n%q", body, want)
	}
}
