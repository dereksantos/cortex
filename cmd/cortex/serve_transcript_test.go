// serve_transcript_test.go — M5.3c1: GET /api/projects/{name}/sessions/{id},
// wiring M5.2b's buildTranscriptViewModel into the HTTP surface so the
// session screen's JS has a single endpoint to fetch a transcript from. See
// GOAL.md §6 M5.3 / STATE.md's M5.3c Next Up note.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestGetSessionEndpointReturnsTranscriptViewModel(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, ".cortex", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeTestSession(t, sessionsDir, "20260101-000000",
		`{"kind":"message","turn":0,"role":"system","content":"you are cortex"}`,
		`{"kind":"message","turn":1,"role":"user","content":"hello"}`,
		`{"kind":"message","turn":1,"role":"assistant","content":"hi there"}`,
	)

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/blog/sessions/20260101-000000", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got transcriptViewModel
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want, err := buildTranscriptViewModel(filepath.Join(sessionsDir, "20260101-000000.jsonl"))
	if err != nil {
		t.Fatalf("buildTranscriptViewModel: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("GET .../sessions/{id} body =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

func TestGetSessionEndpointUnknownProjectReturns404(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/doesnotexist/sessions/whatever", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetSessionEndpointUnknownSessionIDReturns404(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/projects/blog/sessions/does-not-exist", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetSessionEndpointRequiresAuth(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/projects/blog/sessions/whatever")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
