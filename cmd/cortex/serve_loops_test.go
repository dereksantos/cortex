// serve_loops_test.go — M6.7b: GET /api/loops, wiring M6.7a's
// buildLoopsViewModel into the HTTP surface. See GOAL.md §6 M6.7 (split
// into M6.7a-e, STATE.md's 2026-07-13 split) and serve_loops.go.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
