// serve_loops_test.go — M6.7b: GET /api/loops, wiring M6.7a's
// buildLoopsViewModel into the HTTP surface. See GOAL.md §6 M6.7 (split
// into M6.7a-e, STATE.md's 2026-07-13 split) and serve_loops.go.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

func TestListLoopsEndpointReturnsSpecsAndRunHistory(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	if err := store.Save(loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, MaxTurns: 25, Enabled: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/loops", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got loopsViewModel
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want, err := buildLoopsViewModel(store)
	if err != nil {
		t.Fatalf("buildLoopsViewModel: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal got: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("GET /api/loops body =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

// TestListLoopsEndpointRowsCarryD11SelfPacingFields is D11's self-pacing/
// terminal-state tuning's read side: GET /api/loops rows must carry
// next_run, next_reason, and disabled_reason — the webui/loops.js columns
// that read them.
func TestListLoopsEndpointRowsCarryD11SelfPacingFields(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	if err := store.Save(loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, Enabled: true}); err != nil {
		t.Fatalf("Save(nightly): %v", err)
	}
	if err := store.Save(loops.Spec{Name: "flaky", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, Enabled: false, DisabledReason: "3 consecutive failures"}); err != nil {
		t.Fatalf("Save(flaky): %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/loops", "tok")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// next_run is always present (empty string when there's no future
	// instant to name); disabled_reason and next_reason are omitempty like
	// every other optional field on this shape, so pin their presence via
	// a loop that actually has one set, not an unconditional substring.
	if !strings.Contains(string(body), `"next_run"`) {
		t.Errorf("GET /api/loops body missing field \"next_run\": %s", body)
	}
	if !strings.Contains(string(body), `"disabled_reason":"3 consecutive failures"`) {
		t.Errorf("GET /api/loops body missing disabled_reason for the auto-disabled loop: %s", body)
	}
}

func TestListLoopsEndpointEmptyStoreReturnsEmptyArray(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/loops", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got loopsViewModel
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Loops == nil || len(got.Loops) != 0 {
		t.Errorf("Loops = %#v, want empty non-nil slice", got.Loops)
	}
}

func TestListLoopsEndpointRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/loops")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// doAuthedPost issues an authenticated POST with a JSON body against url —
// mirrors serve_models_test.go's doAuthedPut for the sibling verb.
func doAuthedPost(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// M6.7c — POST /api/loops: creates a loop spec via internal/loops.
// FileStore.Save (httptest: happy path, cadence-floor rejection surfaced as
// a typed 400, auth) — mirrors M4.2c2b1's PUT /api/models/{role} pattern.

func TestCreateLoopEndpointCreatesLoop(t *testing.T) {
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/loops", "tok", `{"name":"nightly","project":"blog","prompt":"sweep TODOs","interval_minutes":60,"max_turns":25,"enabled":true}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", resp.StatusCode, body)
	}

	var got loops.Spec
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	want := loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, MaxTurns: 25, Enabled: true}
	if got != want {
		t.Errorf("response spec = %+v, want %+v", got, want)
	}

	saved, err := store.Lookup("nightly")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if saved != want {
		t.Errorf("persisted spec = %+v, want %+v", saved, want)
	}
}

func TestCreateLoopEndpointCadenceBelowFloorReturns400(t *testing.T) {
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/loops", "tok", `{"name":"toohot","project":"blog","prompt":"go","interval_minutes":3}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "cadence") && !strings.Contains(string(body), "floor") {
		t.Errorf("body = %q, want it to mention the cadence floor", body)
	}

	if _, err := store.Lookup("toohot"); err == nil {
		t.Errorf("Lookup(\"toohot\") succeeded, want the rejected spec to never persist")
	}
}

func TestCreateLoopEndpointRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/loops", "application/json", strings.NewReader(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// M6.7d — POST /api/loops/{name}/enable and .../disable: flip Spec.Enabled
// via Store.Lookup + Store.Save (an upsert-by-name full rewrite, mirroring
// M6.7c's Lookup-after-Save readback). Combined into one item since both
// routes are the same single-field toggle, mirroring M4.2c2b1's
// combined-writes precedent.

func TestSetLoopEnabledEndpointTogglesFlag(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "enable", path: "/enable", want: true},
		{name: "disable", path: "/disable", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
			seed := loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, MaxTurns: 25, Enabled: !tt.want}
			if err := store.Save(seed); err != nil {
				t.Fatalf("Save: %v", err)
			}

			reg := &fakeRegistry{}
			ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", store)))
			defer ts.Close()

			resp := doAuthedPost(t, ts.URL+"/api/loops/nightly"+tt.path, "tok", "")
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, body)
			}

			var got loops.Spec
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("Unmarshal response: %v", err)
			}
			if got.Enabled != tt.want {
				t.Errorf("response Enabled = %v, want %v", got.Enabled, tt.want)
			}
			if got.Name != "nightly" || got.Project != "blog" {
				t.Errorf("response spec dropped fields: %+v", got)
			}

			persisted, err := store.Lookup("nightly")
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if persisted.Enabled != tt.want {
				t.Errorf("persisted Enabled = %v, want %v", persisted.Enabled, tt.want)
			}
		})
	}
}

func TestSetLoopEnabledEndpointUnknownNameReturns404(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "enable", path: "/enable"},
		{name: "disable", path: "/disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeRegistry{}
			ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
			defer ts.Close()

			resp := doAuthedPost(t, ts.URL+"/api/loops/ghost"+tt.path, "tok", "")
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404, body = %s", resp.StatusCode, body)
			}
		})
	}
}

// M6.7e — POST /api/loops/{name}/run-now: fires the named loop synchronously
// via RunLoopFiring (loop_run.go, M6.3), reusing the SessionManager's own
// sessionFactory (mgr.newSession) rather than threading a new newServeMux
// parameter — the seam RunLoopFiring already accepts, and mgr is already
// passed into newServeMux for the turn/SSE routes. Mirrors M6.7c/d's
// Lookup-then-act shape (404 on an unknown name, 401 unauthenticated) plus
// loop_run_test.go's scripted-Sender-via-httptest pattern for the firing
// itself.

func TestRunLoopEndpointFiresLoopAndReturnsRunHistory(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	seed := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 60, MaxTurns: 25, Enabled: true}
	if err := store.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, noopTurnTestSessionFactory(t))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", store)))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/loops/nightly/run-now", "tok", "")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, body)
	}

	var got loopView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Name != "nightly" || got.Project != "blog" {
		t.Errorf("response spec = %+v, want name=nightly project=blog", got.Spec)
	}
	if len(got.Runs) != 1 {
		t.Fatalf("Runs = %d, want 1: %+v", len(got.Runs), got.Runs)
	}
	if got.Runs[0].Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Runs[0].Outcome = %q, want %q", got.Runs[0].Outcome, journal.LoopOutcomeSuccess)
	}

	// The journal is the source of truth for run history — prove the
	// endpoint's response matches it exactly, not just its own opinion.
	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("journal loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Name != "nightly" {
		t.Errorf("journal entry Name = %q, want %q", entries[0].Name, "nightly")
	}
}

func TestRunLoopEndpointUnknownNameReturns404(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/loops/ghost/run-now", "tok", "")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404, body = %s", resp.StatusCode, body)
	}
}

func TestRunLoopEndpointRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/loops/nightly/run-now", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSetLoopEnabledEndpointRequiresAuth(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "enable", path: "/enable"},
		{name: "disable", path: "/disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeRegistry{}
			ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "", testLoopsStore(t))))
			defer ts.Close()

			resp, err := http.Post(ts.URL+"/api/loops/nightly"+tt.path, "application/json", strings.NewReader(""))
			if err != nil {
				t.Fatalf("Post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}
