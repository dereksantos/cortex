// serve_status_test.go — GET /api/status (serve_status.go, design item 4):
// JSON shape and allowlist gating.
package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/loops"
)

func TestStatusEndpointReturnsUptimeSessionsAndLoops(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := testSessionManager(reg)
	store := testLoopsStore(t)
	if err := store.Save(loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep", IntervalMinutes: 60, Enabled: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	startedAt := time.Now().Add(-5 * time.Second)
	mux := newServeMux(reg, mgr, "", "", store, newRunningSet())
	mux.HandleFunc("GET /api/status", handleStatus(startedAt, mgr, store))
	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doGet(t, ts.URL+"/api/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UptimeSeconds < 5 {
		t.Errorf("UptimeSeconds = %d, want >= 5", got.UptimeSeconds)
	}
	if got.LiveSessions != 0 {
		t.Errorf("LiveSessions = %d, want 0 (no sessions created)", got.LiveSessions)
	}
	if len(got.Loops) != 1 {
		t.Fatalf("got %d loops, want 1", len(got.Loops))
	}
	if got.Loops[0].Name != "nightly" || !got.Loops[0].Enabled {
		t.Errorf("Loops[0] = %+v, want name=nightly enabled=true", got.Loops[0])
	}
}

func TestStatusEndpointRejectsForeignHost(t *testing.T) {
	reg := &fakeRegistry{}
	mgr := testSessionManager(reg)
	store := testLoopsStore(t)
	mux := newServeMux(reg, mgr, "", "", store, newRunningSet())
	mux.HandleFunc("GET /api/status", handleStatus(time.Now(), mgr, store))
	ts := newTestServeServer(t, mux)
	defer ts.Close()

	resp := doForeignHost(t, http.MethodGet, ts.URL+"/api/status", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
