// M4.2a — read-only project + session listing over `cortex serve`'s HTTP
// surface. See GOAL.md §6 M4.2 (split into M4.2a/b/c, STATE.md).
package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// fakeRegistry is a small in-memory registry.Registry double, keeping
// these tests model-free and httptest-only per GOAL.md §3 P4's "handlers
// accept small interfaces... httptest-testable without a model".
type fakeRegistry struct {
	projects map[string]registry.Project
}

func (f *fakeRegistry) List() ([]registry.Project, error) {
	out := make([]registry.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeRegistry) Lookup(name string) (registry.Project, error) {
	p, ok := f.projects[name]
	if !ok {
		return registry.Project{}, registry.ErrProjectNotFound
	}
	return p, nil
}

func (f *fakeRegistry) Save(p registry.Project) error {
	if f.projects == nil {
		f.projects = map[string]registry.Project{}
	}
	f.projects[p.Name] = p
	return nil
}

func (f *fakeRegistry) Remove(name string) error {
	delete(f.projects, name)
	return nil
}

// testSessionManager builds a SessionManager whose sessionFactory never
// touches config/network (hermeticSessionFactory, serve_session_test.go) —
// reused here by the M4.2a listing tests too since newServeMux now always
// takes a *SessionManager, even for endpoints that don't exercise it.
func testSessionManager(reg registry.Registry) *SessionManager {
	return NewSessionManager(reg, hermeticSessionFactory())
}

// testLoopsStore returns a loops.Store backed by a fresh temp-dir loops.json
// (loops.NewAt, bypassing internal/userhome) — mirrors testSessionManager's
// role of giving every newServeMux call site a hermetic, isolated seam for a
// dependency most tests don't otherwise care about. M6.7b: GET /api/loops.
func testLoopsStore(t *testing.T) loops.Store {
	t.Helper()
	return loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
}

// newTestServeServer starts an httptest server wrapping mux in
// hostOriginMiddleware (serve.go), using the server's OWN bound port for
// the allowlist — this mirrors runServeCLI's real bind-then-wrap sequence,
// which needs the actual bound port (not a guess) so `--port` keeps
// working. httptest.NewServer binds before its handler can learn its own
// port, so the middleware is installed via a late-bound indirection: the
// server starts with a thin forwarding handler, then — once the real port
// is known — `handler` is assigned the real hostOriginMiddleware-wrapped
// mux before the caller ever issues a request.
func newTestServeServer(t *testing.T, mux http.Handler) *httptest.Server {
	t.Helper()
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", ts.Listener.Addr(), err)
	}
	handler = hostOriginMiddleware(port, mux)
	return ts
}

// doGet issues a plain GET — under the 2026-07-19 Host/Origin allowlist
// (serve.go's hostOriginMiddleware) a request from Go's own http.Client
// against an httptest server already carries a correct Host header and no
// Origin header, so nothing needs to be attached here. Named doGet (not
// doAuthedGet) to mark that this is no longer standing in for a bearer
// token — see SECURITY.md's posture note.
func doGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return resp
}

// foreignHost is a Host header value that will never appear in a server's
// Host/Origin allowlist (allowedHostSet, serve.go) regardless of which
// port it's bound to — used below to prove an "/api/..." endpoint is
// actually gated, now that a bearer token no longer exists to withhold.
const foreignHost = "attacker.example"

// doForeignHost issues method against url with the Host header forged to
// foreignHost, so hostOriginMiddleware rejects it with 403 — the
// replacement for the old "no bearer token" 401 checks (RequiresAuth
// tests, one per endpoint) now that the gate is Host/Origin-based rather
// than a shared secret.
func doForeignHost(t *testing.T, method, url string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = foreignHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestListProjectsEndpointReturnsRegisteredProjects(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog":   {Name: "blog", Root: "/tmp/blog"},
		"cortex": {Name: "cortex", Root: "/tmp/cortex", Notes: "main repo"},
	}}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/projects")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []registry.Project
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d projects, want 2", len(got))
	}
	byName := map[string]registry.Project{}
	for _, p := range got {
		byName[p.Name] = p
	}
	if byName["blog"].Root != "/tmp/blog" {
		t.Errorf("blog root = %q, want /tmp/blog", byName["blog"].Root)
	}
	if byName["cortex"].Notes != "main repo" {
		t.Errorf("cortex notes = %q, want %q", byName["cortex"].Notes, "main repo")
	}
}

func TestListProjectsEndpointEmptyRegistryReturnsEmptyArrayNotNull(t *testing.T) {
	reg := &fakeRegistry{}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/projects")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "[]" {
		t.Errorf("body = %q, want %q (empty JSON array, not null)", body, "[]")
	}
}

func TestListProjectsEndpointRejectsForeignHost(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: "/tmp/blog"}}}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doForeignHost(t, http.MethodGet, ts.URL+"/api/projects", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestListProjectSessionsEndpointListsNewestFirst(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, ".cortex", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeTestSession(t, sessDir, "20260101-000000",
		`{"kind":"message","role":"user","content":"older prompt"}`,
		`{"kind":"message","role":"assistant","content":"reply"}`,
	)
	writeTestSession(t, sessDir, "20260202-000000",
		`{"kind":"message","role":"user","content":"newer prompt"}`,
	)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/projects/blog/sessions")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []sessionSummary
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].ID != "20260202-000000" || got[1].ID != "20260101-000000" {
		t.Errorf("order = [%s, %s], want newest first", got[0].ID, got[1].ID)
	}
	if got[1].Messages != 2 {
		t.Errorf("older session Messages = %d, want 2", got[1].Messages)
	}
	if got[1].First != "older prompt" {
		t.Errorf("older session First = %q, want %q", got[1].First, "older prompt")
	}
}

func TestListProjectSessionsEndpointNoSessionsYetReturnsEmptyArray(t *testing.T) {
	root := t.TempDir() // no .cortex/sessions dir created at all
	reg := &fakeRegistry{projects: map[string]registry.Project{"fresh": {Name: "fresh", Root: root}}}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/projects/fresh/sessions")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "[]" {
		t.Errorf("body = %q, want %q", body, "[]")
	}
}

func TestListProjectSessionsEndpointUnknownProjectReturns404(t *testing.T) {
	reg := &fakeRegistry{}
	ts := newTestServeServer(t, newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/projects/doesnotexist/sessions")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
