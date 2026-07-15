// M4.2b2 — POST .../turn: runs session.Turn against a live *managedSession,
// serialized by managedSession's own mutex (GOAL.md §3 P4). SSE (M4.2b3) is
// not exercised here — the endpoint returns the turn's final JSON result.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/registry"
)

// turnTestBackend is a scripted Sender seam (same style as
// greetingTestBackend, greeting_test.go) that answers every chat completion
// with a fixed assistant reply, while tracking concurrent in-flight requests
// so a test can prove the per-session mutex actually serializes overlapping
// turns rather than just asserting a single request's shape.
type turnTestBackend struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	count   int
	srv     *httptest.Server
}

func newTurnTestBackend(t *testing.T) *turnTestBackend {
	t.Helper()
	b := &turnTestBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.active++
		b.count++
		if b.active > b.maxSeen {
			b.maxSeen = b.active
		}
		b.mu.Unlock()

		// Give a genuine race a real window to land in: without the
		// per-session mutex, two turns fired concurrently on the same
		// session would both be mid-flight here at once.
		time.Sleep(20 * time.Millisecond)

		b.mu.Lock()
		b.active--
		b.mu.Unlock()

		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *turnTestBackend) requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

func (b *turnTestBackend) maxConcurrent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxSeen
}

// turnTestSessionFactory builds a sessionFactory whose *CortexSession points
// at backend — the same hand-built-CortexSession-plus-BaseURL pattern
// greeting_test.go's newGreetingTestSession established, but as a
// SessionManager sessionFactory (mgr.Create/Resume call StartTranscript and
// applyProjectByName themselves; the factory just needs to hand back a bare
// session wired to the scripted backend).
func turnTestSessionFactory(backend *turnTestBackend) sessionFactory {
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = backend.srv.URL
		return cs
	}
}

func TestTurnEndpointRunsTurnAgainstLiveSession(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newTurnTestBackend(t)
	mgr := NewSessionManager(reg, turnTestSessionFactory(backend))
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
	var got turnResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reply != "ok" {
		t.Errorf("reply = %q, want %q", got.Reply, "ok")
	}
	if got.Interrupted {
		t.Error("Interrupted = true for a clean turn, want false")
	}
	if backend.requests() != 1 {
		t.Errorf("backend saw %d requests, want 1", backend.requests())
	}
}

// TestTurnEndpointResumesSessionNotLiveOnThisManager is the regression test
// for the "Failed to send: POST turn/stream: 404" bug found by live browser
// testing: navigating the web UI straight to an on-disk session URL (e.g.
// after a `cortex serve` restart, or any request landing on a process that
// never created/resumed this id itself) 404'd instead of transparently
// resuming — even though M4.7's idle-eviction design already promises "a
// subsequent request re-hydrates from the transcript" and GET
// .../sessions/{id} (serve_transcript.go) already reads on-disk sessions
// fine. Reproduces the gap directly: a session is created on one
// SessionManager, its transcript lock released (simulating that process
// going away — the same technique
// TestSetModelBindingSessionScopeSetsLiveSessionModelAndRevertsOnResume,
// serve_models_test.go, uses), and the turn is POSTed through a completely
// FRESH SessionManager/mux that never held the id live.
func TestTurnEndpointResumesSessionNotLiveOnThisManager(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}

	firstBackend := newTurnTestBackend(t)
	firstMgr := NewSessionManager(reg, turnTestSessionFactory(firstBackend))
	created, err := firstMgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.ID()
	created.cs.transcript.Close() // simulate the creating process (and its fslock) going away

	secondBackend := newTurnTestBackend(t)
	secondMgr := NewSessionManager(reg, turnTestSessionFactory(secondBackend))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, secondMgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	if _, ok := secondMgr.Get(id); ok {
		t.Fatalf("secondMgr already held %q live before the request — test setup is wrong", id)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+id+"/turn", strings.NewReader(`{"input":"hello"}`))
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
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	var got turnResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reply != "ok" {
		t.Errorf("reply = %q, want %q", got.Reply, "ok")
	}
	if secondBackend.requests() != 1 {
		t.Errorf("secondBackend saw %d requests, want 1", secondBackend.requests())
	}
	if _, ok := secondMgr.Get(id); !ok {
		t.Error("session was not tracked live on secondMgr after the transparent resume")
	}
}

// TestTurnEndpointResumeBusyReturns409 covers the other branch
// GetOrResume's error mapping introduces: a transcript another process
// currently holds the fslock on (rather than one that's simply missing)
// must not collapse into the same 404 as "no such session" — it's a
// distinct, retryable 409.
func TestTurnEndpointResumeBusyReturns409(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}

	holderMgr := NewSessionManager(reg, hermeticSessionFactory())
	created, err := holderMgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.ID()
	// Deliberately do NOT close created.cs.transcript — holderMgr keeps the
	// fslock held, standing in for a second live `cortex serve`/REPL process
	// still holding this session open.

	freshMgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, freshMgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+id+"/turn", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 409, body=%s", resp.StatusCode, body)
	}
}

func TestTurnEndpointUnknownSessionReturns404(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: t.TempDir()}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/does-not-exist/turn", strings.NewReader(`{"input":"hi"}`))
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

func TestTurnEndpointRequiresAuth(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newTurnTestBackend(t)
	mgr := NewSessionManager(reg, turnTestSessionFactory(backend))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn", "application/json", strings.NewReader(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if backend.requests() != 0 {
		t.Errorf("backend saw %d requests for an unauthenticated turn, want 0", backend.requests())
	}
}

// TestTurnEndpointSameSessionSerializes is the load-bearing test for
// GOAL.md §3 P4's "one turn at a time per session" guarantee: two turn
// requests fired concurrently against the SAME session id must never have
// both in flight against the backend at once.
func TestTurnEndpointSameSessionSerializes(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newTurnTestBackend(t)
	mgr := NewSessionManager(reg, turnTestSessionFactory(backend))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	turnURL := ts.URL + "/api/projects/blog/sessions/" + created.ID() + "/turn"

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, turnURL, strings.NewReader(`{"input":"hi"}`))
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			req.Header.Set("Authorization", "Bearer tok")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		}()
	}
	wg.Wait()

	if got := backend.maxConcurrent(); got != 1 {
		t.Errorf("backend saw %d concurrent requests on ONE session, want 1 (turns must serialize)", got)
	}
	if got := backend.requests(); got != 2 {
		t.Errorf("backend saw %d requests, want 2", got)
	}
}

// TestTurnEndpointDifferentSessionsRunConcurrently is the load-bearing test
// for the other half of GOAL.md §3 P4's "one turn at a time per session,
// different sessions concurrent" guarantee (M4.3): two turn requests fired
// concurrently against TWO DIFFERENT session ids must be allowed to overlap
// in flight, not serialize against each other. managedSession.mu is
// per-*managedSession (serve_session.go), so two distinct managedSession
// values never share a lock — this positively asserts that absence of
// shared locking actually yields observed concurrency, rather than just
// inferring it from the M4.2b2 same-session test's absence of a shared-lock
// assertion.
func TestTurnEndpointDifferentSessionsRunConcurrently(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newTurnTestBackend(t)
	mgr := NewSessionManager(reg, turnTestSessionFactory(backend))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", testLoopsStore(t))))
	defer ts.Close()

	first, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if first.ID() == second.ID() {
		t.Fatalf("expected two distinct session ids, got the same one twice: %q", first.ID())
	}

	var wg sync.WaitGroup
	for _, id := range []string{first.ID(), second.ID()} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			turnURL := ts.URL + "/api/projects/blog/sessions/" + id + "/turn"
			req, err := http.NewRequest(http.MethodPost, turnURL, strings.NewReader(`{"input":"hi"}`))
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			req.Header.Set("Authorization", "Bearer tok")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
		}(id)
	}
	wg.Wait()

	if got := backend.maxConcurrent(); got < 2 {
		t.Errorf("backend saw max %d concurrent requests across TWO sessions, want >= 2 (turns on different sessions must not serialize)", got)
	}
	if got := backend.requests(); got != 2 {
		t.Errorf("backend saw %d requests, want 2", got)
	}
}
