// M4.2a — read-only project + session listing over `cortex serve`'s HTTP
// surface. See GOAL.md §6 M4.2 (split into M4.2a/b/c, STATE.md).
package main

import (
	"encoding/json"
	"io"
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

func doAuthedGet(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects", "tok")
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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects", "tok")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "[]" {
		t.Errorf("body = %q, want %q (empty JSON array, not null)", body, "[]")
	}
}

func TestListProjectsEndpointRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: "/tmp/blog"}}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/projects")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/blog/sessions", "tok")
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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/fresh/sessions", "tok")
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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/doesnotexist/sessions", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
