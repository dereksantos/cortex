// serve_restart_test.go — M4.6 (GOAL.md §6 M4.6): "serve owns no state: kill
// + restart re-derives every list from disk (restart the manager, listings
// identical)." A second, independent registry.FileRegistry + SessionManager
// pair — standing in for a process restart, sharing no in-memory state with
// the first — must return byte-identical GET /api/projects and GET
// /api/projects/{name}/sessions listings, because both handlers (serve_routes.go)
// read straight off disk (reg.List(), listSessions()) rather than consulting
// the manager's live session map. Session-level rehydration of an individual
// live session is already proven at the SessionManager layer by
// TestSessionManagerResumeRehydratesFromTranscriptAfterRestart
// (serve_session_test.go, M4.2b1); this test proves the two LISTING
// endpoints specifically survive a restart, which that test does not cover.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestServeListingsIdenticalAcrossManagerRestart(t *testing.T) {
	home := t.TempDir()
	regPath := filepath.Join(home, "projects.json")
	projectRoot := t.TempDir()

	reg1 := registry.NewAt(regPath)
	if err := reg1.Save(registry.Project{Name: "blog", Root: projectRoot}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	mgr1 := NewSessionManager(reg1, hermeticSessionFactory())
	ts1 := httptest.NewServer(authMiddleware("tok", newServeMux(reg1, mgr1, "", "")))

	// Create a session while the "first process" is live, so there is
	// something for the second process to re-derive from disk.
	req, err := http.NewRequest(http.MethodPost, ts1.URL+"/api/projects/blog/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	createResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	var created createSessionResponse
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	createResp.Body.Close()
	if created.ID == "" {
		t.Fatal("created session id is empty")
	}

	projectsBefore := bodyString(t, doAuthedGet(t, ts1.URL+"/api/projects", "tok"))
	sessionsBefore := bodyString(t, doAuthedGet(t, ts1.URL+"/api/projects/blog/sessions", "tok"))
	ts1.Close()

	// Simulate a restart: a fresh FileRegistry instance and a fresh
	// SessionManager (empty in-memory map, no reference to mgr1's session)
	// pointed at the very same on-disk projects.json and project root.
	reg2 := registry.NewAt(regPath)
	mgr2 := NewSessionManager(reg2, hermeticSessionFactory())
	ts2 := httptest.NewServer(authMiddleware("tok", newServeMux(reg2, mgr2, "", "")))
	defer ts2.Close()

	projectsAfter := bodyString(t, doAuthedGet(t, ts2.URL+"/api/projects", "tok"))
	sessionsAfter := bodyString(t, doAuthedGet(t, ts2.URL+"/api/projects/blog/sessions", "tok"))

	if projectsBefore != projectsAfter {
		t.Errorf("projects listing changed across restart:\nbefore: %s\nafter:  %s", projectsBefore, projectsAfter)
	}
	if sessionsBefore != sessionsAfter {
		t.Errorf("sessions listing changed across restart:\nbefore: %s\nafter:  %s", sessionsBefore, sessionsAfter)
	}
	if !strings.Contains(sessionsAfter, created.ID) {
		t.Errorf("post-restart sessions listing = %s, want it to contain created session id %s", sessionsAfter, created.ID)
	}
	if _, live := mgr2.Get(created.ID); live {
		t.Error("mgr2 (the fresh, restarted manager) should not already track the session live — the listing must come from disk, not a carried-over map")
	}
}

// bodyString reads and closes resp.Body, failing the test on a read error.
func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
