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

	"github.com/dereksantos/cortex/internal/loops"
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

	resp := doAuthedPost(t, ts.URL+"/api/loops", "tok", `{"name":"toohot","project":"blog","prompt":"go","interval_minutes":5}`)
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
