// serve_memory_test.go — httptest coverage for the dashboard MEMORY
// screen's HTTP surface (docs/cross-source-learning.md piece 4):
// GET /api/memory (tier tagging + shadowed-name two-rows), GET/DELETE
// /api/memory/note (tier routing, never cross-tier, 404 on miss), and the
// Host/Origin allowlist gating all three the same way every other
// "/api/..." route is gated (serve_routes_test.go's doForeignHost).
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/registry"
)

// seedProjectNote writes a note directly into the named project's
// project-tier store, resolved the exact same way projectMemoryStore does
// (NewWorkspace(root) -> memory.New(ws.ContextDir())) — so a seeded note is
// indistinguishable from one memory_write would have produced.
func seedProjectNote(t *testing.T, root, name, body string) {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	store, err := memory.New(ws.ContextDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := store.Write(name, body, time.Now()); err != nil {
		t.Fatalf("Write(%q): %v", name, err)
	}
}

// seedUserNote writes a note directly into the user-tier store — CORTEX_HOME
// must already be redirected to a temp dir (t.Setenv) by the caller so this
// never touches a real machine's ~/.cortex.
func seedUserNote(t *testing.T, name, body string) {
	t.Helper()
	store, err := userMemoryStore()
	if err != nil {
		t.Fatalf("userMemoryStore: %v", err)
	}
	if _, err := store.Write(name, body, time.Now()); err != nil {
		t.Fatalf("Write(%q): %v", name, err)
	}
}

// doDelete issues a DELETE against url — the missing sibling to
// serve_loops_test.go's doPost / serve_routes_test.go's doGet for the third
// verb this screen needs.
func doDelete(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// memoryTestMux builds a newServeMux wired to a registered "blog" project
// (root: a fresh temp dir) and a CORTEX_HOME-redirected user tier — the
// fixture every test in this file shares.
func memoryTestMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{
		"blog": {Name: "blog", Root: root},
	}}
	mux := newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t), newRunningSet())
	return mux, root
}

func TestListMemoryEndpointTierTagsAndShadowedNamesAppearTwice(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")
	seedProjectNote(t, root, "proj-only", "only in project tier")
	seedUserNote(t, "shared", "USER version of shared")
	seedUserNote(t, "user-only", "only in user tier")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/memory?project=blog")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var vm memoryViewModel
	if err := json.NewDecoder(resp.Body).Decode(&vm); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if vm.Project != "blog" {
		t.Errorf("Project = %q, want blog", vm.Project)
	}

	tagged := map[string]int{} // "tier/name" -> count
	for _, n := range vm.Notes {
		tagged[n.Tier+"/"+n.Name]++
	}
	want := []string{"project/shared", "project/proj-only", "user/shared", "user/user-only"}
	for _, k := range want {
		if tagged[k] != 1 {
			t.Errorf("note %q appeared %d times, want exactly 1", k, tagged[k])
		}
	}
	if len(vm.Notes) != 4 {
		t.Errorf("len(Notes) = %d, want 4 (shadowed \"shared\" must appear as TWO tagged rows, not merged)", len(vm.Notes))
	}
}

func TestListMemoryEndpointRequiresProjectParam(t *testing.T) {
	mux, _ := memoryTestMux(t)
	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/memory")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing ?project=)", resp.StatusCode)
	}
}

func TestListMemoryEndpointUnregisteredProjectIs404(t *testing.T) {
	mux, _ := memoryTestMux(t)
	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/memory?project=nope")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetMemoryNoteRoutesToTheExactTierRequested(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")
	seedUserNote(t, "shared", "USER version of shared")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	tests := []struct {
		name     string
		qs       string
		wantBody string
	}{
		{"project tier", "?tier=project&name=shared&project=blog", "PROJECT version of shared"},
		{"user tier", "?tier=user&name=shared", "USER version of shared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := doGet(t, ts.URL+"/api/memory/note"+tt.qs)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var got memoryNoteDetail
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q — tier routing must never blend the two stores", got.Body, tt.wantBody)
			}
		})
	}
}

func TestGetMemoryNoteErrorCases(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	tests := []struct {
		name string
		qs   string
		want int
	}{
		{"missing name", "?tier=project&project=blog", http.StatusBadRequest},
		{"unsupported tier", "?tier=bogus&name=shared", http.StatusBadRequest},
		{"project tier without project param", "?tier=project&name=shared", http.StatusBadRequest},
		{"unregistered project", "?tier=project&name=shared&project=nope", http.StatusNotFound},
		{"note missing in tier", "?tier=user&name=does-not-exist", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := doGet(t, ts.URL+"/api/memory/note"+tt.qs)
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

// TestDeleteMemoryNoteNeverCrossesTiers is the piece 4 contract's core
// safety property: deleting a name in one tier must never touch the
// same-named note in the other tier.
func TestDeleteMemoryNoteNeverCrossesTiers(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")
	seedUserNote(t, "shared", "USER version of shared")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doDelete(t, ts.URL+"/api/memory/note?tier=user&name=shared")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got deleteMemoryNoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Removed || got.Tier != "user" || got.Name != "shared" {
		t.Errorf("response = %+v, want removed=true tier=user name=shared", got)
	}

	// The project-tier "shared" note must have survived untouched.
	projResp := doGet(t, ts.URL+"/api/memory/note?tier=project&name=shared&project=blog")
	defer projResp.Body.Close()
	if projResp.StatusCode != http.StatusOK {
		t.Fatalf("project-tier \"shared\" status = %d, want 200 — delete must never cross tiers", projResp.StatusCode)
	}
	var projNote memoryNoteDetail
	if err := json.NewDecoder(projResp.Body).Decode(&projNote); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if projNote.Body != "PROJECT version of shared" {
		t.Errorf("project-tier \"shared\" body = %q, want unchanged", projNote.Body)
	}

	// The user-tier note is genuinely gone now.
	userResp := doGet(t, ts.URL+"/api/memory/note?tier=user&name=shared")
	defer userResp.Body.Close()
	if userResp.StatusCode != http.StatusNotFound {
		t.Errorf("user-tier \"shared\" status = %d, want 404 (it was just deleted)", userResp.StatusCode)
	}
}

func TestDeleteMemoryNoteMissingIs404(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doDelete(t, ts.URL+"/api/memory/note?tier=project&name=does-not-exist&project=blog")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	// Deleting the same real note twice: gone the second time.
	first := doDelete(t, ts.URL+"/api/memory/note?tier=project&name=shared&project=blog")
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first delete status = %d, want 200", first.StatusCode)
	}
	second := doDelete(t, ts.URL+"/api/memory/note?tier=project&name=shared&project=blog")
	defer second.Body.Close()
	if second.StatusCode != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404 (already gone)", second.StatusCode)
	}
}

// TestMemoryEndpointsForeignHostRejected proves the three memory routes
// sit under the same Host/Origin allowlist (hostOriginMiddleware) every
// other "/api/..." route is gated by — mirrors serve_loops_test.go's
// TestListLoopsEndpointRequiresAuth (renamed for the 2026-07-19 auth model,
// see doForeignHost's own doc comment).
func TestMemoryEndpointsForeignHostRejected(t *testing.T) {
	mux, root := memoryTestMux(t)
	seedProjectNote(t, root, "shared", "PROJECT version of shared")

	ts := newTestServeServer(t, mux)
	defer ts.Close()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/api/memory?project=blog"},
		{"note", http.MethodGet, "/api/memory/note?tier=project&name=shared&project=blog"},
		{"delete", http.MethodDelete, "/api/memory/note?tier=project&name=shared&project=blog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			resp := doForeignHost(t, tt.method, ts.URL+tt.path, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}
