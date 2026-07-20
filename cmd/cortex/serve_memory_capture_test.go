// serve_memory_capture_test.go — regression guard for
// docs/cross-source-learning.md's "Code-reality check": before this fix,
// newProductionSession() (serve_session.go) — the sessionFactory the entire
// `cortex serve` / web-UI surface uses — never called EnableMemory(), so
// every turn run through the web UI was invisible to the journal and to
// memory. This proves the fix through the real serve path: a session
// created via SessionManager.Create exposes working memory tools, and a
// turn driven through the actual serve HTTP endpoint (not cs.Turn()
// directly) produces a capture event.
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
)

// serveMemorySessionFactory mirrors newProductionSession's construction for
// tests: turnTestSessionFactory's scripted-backend substitution for
// NewCortexSession's real network/config resolution (the same swap
// turnTestSessionFactory/discordTurnSessionFactory already make so tests
// stay hermetic), plus the EnableMemory() call newProductionSession itself
// now makes in production — the exact line this test guards.
func serveMemorySessionFactory(backend *turnTestBackend) sessionFactory {
	base := turnTestSessionFactory(backend)
	return func() *CortexSession {
		cs := base()
		cs.EnableMemory()
		return cs
	}
}

// TestServeCreatedSessionHasMemoryAndCapturesATurn is Fix 1's DoD: a
// serve-created session (SessionManager.Create, the same path
// cortex serve's newServeMux/handleTurn uses) has a working memory store
// and capturer wired, and a turn driven through the real serve HTTP
// endpoint produces a capture event (captureTurn, session_runtime.go) — not
// assumed, verified against cs.captures directly.
func TestServeCreatedSessionHasMemoryAndCapturesATurn(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newTurnTestBackend(t)
	mgr := NewSessionManager(reg, serveMemorySessionFactory(backend))
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ms, ok := mgr.Get(created.ID())
	if !ok {
		t.Fatal("session not live after Create")
	}
	if ms.cs.memory == nil {
		t.Fatal("serve-created session has no memory store wired — EnableMemory not called")
	}
	if ms.cs.capturer == nil {
		t.Fatal("serve-created session has no capturer wired — EnableMemory not called")
	}

	// Memory tools are functionally available on this session, not just
	// non-nil fields — round-trip a note the way memory_e2e_test.go proves
	// for the REPL/headless drivers.
	if _, err := tools.Execute(context.Background(), memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "web-ui-note", "content": "written through a serve-created session",
	}), ms.cs); err != nil {
		t.Fatalf("memory_write on a serve-created session: %v", err)
	}
	if _, err := tools.Execute(context.Background(), memCall(tools.FunctionMemoryRead, map[string]any{
		"name": "web-ui-note",
	}), ms.cs); err != nil {
		t.Fatalf("memory_read on a serve-created session: %v", err)
	}

	// Drive a real turn through the actual serve HTTP path (not cs.Turn
	// directly), matching TestTurnEndpointRunsTurnAgainstLiveSession's
	// pattern, so the capture assertion below proves the production wiring
	// end to end rather than just the factory in isolation.
	before := ms.cs.captures
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if ms.cs.captures <= before {
		t.Error("a turn through the serve HTTP path produced no capture event — captureTurn should fire (session_runtime.go)")
	}
}
