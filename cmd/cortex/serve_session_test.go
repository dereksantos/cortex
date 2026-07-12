// M4.2b1 — SessionManager: live create/resume session lifecycle over
// `cortex serve` (GOAL.md §6 M4.2, split into M4.2b1/b2/b3 per STATE.md).
// Turn/SSE (M4.2b2) is not exercised here.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

// hermeticSessionFactory returns a sessionFactory that never touches real
// config or the network (CortexArgs{}.Request() ignores its receiver; no
// BaseURL is needed since M4.2b1 never runs a turn) — the same hand-built
// *CortexSession{} pattern greeting_test.go established for hermetic
// session tests.
func hermeticSessionFactory() sessionFactory {
	return func() *CortexSession {
		return &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	}
}

func TestSessionManagerCreateStartsAndTracksNewSession(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	ms, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ms.ID() == "" {
		t.Fatal("Create() left the session id empty")
	}

	if _, err := os.Stat(filepath.Join(root, ".cortex", "sessions", ms.ID()+".jsonl")); err != nil {
		t.Errorf("transcript file not written for %s: %v", ms.ID(), err)
	}

	got, ok := mgr.Get(ms.ID())
	if !ok {
		t.Fatal("Get() did not find the session Create() just tracked")
	}
	if got != ms {
		t.Error("Get() returned a different *managedSession than Create() produced")
	}
}

func TestSessionManagerCreateUnknownProjectReturnsTypedError(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	if _, err := mgr.Create("nope"); err == nil {
		t.Fatal("Create(unregistered project) = nil error, want registry.ErrProjectNotFound")
	} else if !strings.Contains(err.Error(), "nope") {
		t.Errorf("Create error %q does not mention the unknown project name", err)
	}
}

func TestSessionManagerResumeOfLiveSessionReturnsSamePointerWithoutReopening(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resumed, err := mgr.Resume("blog", created.ID())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed != created {
		t.Error("Resume() of an already-live session id returned a different *managedSession, want the same live one (no reopen)")
	}
}

// TestSessionManagerResumeRehydratesFromTranscriptAfterRestart proves the
// "serve owns no state" invariant (GOAL.md M4.6) at the SessionManager
// level: a second, independent manager instance — standing in for a serve
// restart — resumes the same session id straight from the transcript file
// the first manager's Create wrote, recovering the same messages.
func TestSessionManagerResumeRehydratesFromTranscriptAfterRestart(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}

	firstMgr := NewSessionManager(reg, hermeticSessionFactory())
	created, err := firstMgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantMessages := len(created.cs.Request.Messages)
	created.cs.transcript.Close() // simulate the process (and its transcript lock) going away

	secondMgr := NewSessionManager(reg, hermeticSessionFactory())
	resumed, err := secondMgr.Resume("blog", created.ID())
	if err != nil {
		t.Fatalf("Resume after restart: %v", err)
	}
	if resumed.ID() != created.ID() {
		t.Errorf("resumed id = %q, want %q", resumed.ID(), created.ID())
	}
	if got := len(resumed.cs.Request.Messages); got != wantMessages {
		t.Errorf("resumed session has %d messages, want %d (rehydrated from transcript)", got, wantMessages)
	}
}

func TestSessionManagerResumeUnknownIDReturnsError(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	if _, err := mgr.Resume("blog", "does-not-exist"); err == nil {
		t.Fatal("Resume(unknown id) = nil error, want an error")
	}
}

func TestCreateSessionEndpointCreatesNewSession(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", nil)
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
	var got createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == "" {
		t.Error("response id is empty")
	}
	if got.Resumed {
		t.Error("Resumed = true for a fresh create, want false")
	}
	if _, ok := mgr.Get(got.ID); !ok {
		t.Errorf("session %s not tracked live by the manager after create", got.ID)
	}
}

func TestCreateSessionEndpointResumesWhenBodyNamesID(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := strings.NewReader(`{"resume":"` + created.ID() + `"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions", body)
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
	var got createSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID() {
		t.Errorf("resumed id = %q, want %q", got.ID, created.ID())
	}
	if !got.Resumed {
		t.Error("Resumed = false for a resume request, want true")
	}
}

func TestCreateSessionEndpointUnknownProjectReturns404(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/doesnotexist/sessions", nil)
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

func TestCreateSessionEndpointRequiresAuth(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "")))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/projects/blog/sessions", "application/json", nil)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
