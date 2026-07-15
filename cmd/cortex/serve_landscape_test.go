// M4.2c1 — GET /api/landscape over `cortex serve`'s HTTP surface. See
// GOAL.md §6 M4.2 (split into M4.2a/b/c; M4.2c split further into
// M4.2c1/c2, STATE.md). Reuses scan.go's resolveScanRoots/buildScanReport
// verbatim (the same functions `cortex scan` itself calls) so the endpoint
// and the CLI can never drift on what "the landscape" means; this handler
// adds no scanning logic of its own.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

func TestLandscapeEndpointReturnsScanReport(t *testing.T) {
	homeDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")

	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("# fixture"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := PersistScanRoots(configPath, []string{projectRoot}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/landscape", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got ScanReport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Path != projectRoot {
		t.Fatalf("Projects = %+v, want one entry for %s", got.Projects, projectRoot)
	}
	if len(got.Roots) != 1 || got.Roots[0] != projectRoot {
		t.Errorf("Roots = %v, want [%s]", got.Roots, projectRoot)
	}
}

func TestLandscapeEndpointNoRootsConfiguredIsTypedRefusal(t *testing.T) {
	// Isolated CORTEX_HOME: with no roots persisted AND no journaled scan
	// (this dev's real ~/.cortex/journal must never leak into a "no roots
	// configured" test), the endpoint has nothing to fall back to either.
	t.Setenv("CORTEX_HOME", t.TempDir())
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json") // never written

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/landscape", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
}

// TestLandscapeEndpointFallsBackToJournalWhenNoRootsPersisted pins the fix
// for the seam bug: `cortex scan --root <path>` journals a landscape.scan
// event; without any roots persisted (e.g. an older scan run before
// resolveAndPersistScanRoots existed), GET /api/landscape must surface that
// journaled event — scanned_at, roots, source:"journal" — rather than 412
// as if nothing had ever happened.
func TestLandscapeEndpointFallsBackToJournalWhenNoRootsPersisted(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json") // no roots persisted

	if err := journal.AppendLandscapeScan(journal.LandscapeScanPayload{
		Roots:        []string{"/home/derek/eng"},
		ToolCount:    2,
		RuntimeCount: 1,
		ProjectCount: 3,
	}); err != nil {
		t.Fatalf("AppendLandscapeScan: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/landscape", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got LandscapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Source != "journal" {
		t.Errorf("Source = %q, want journal", got.Source)
	}
	if len(got.Roots) != 1 || got.Roots[0] != "/home/derek/eng" {
		t.Errorf("Roots = %v, want [/home/derek/eng]", got.Roots)
	}
	if got.ScannedAt.IsZero() {
		t.Error("ScannedAt is zero, want the journaled event's timestamp")
	}
}

// TestLandscapeEndpointReturnsScanReportIsLiveSource pins that the
// roots-present path (unchanged behavior) reports source:"live" with a
// fresh ScannedAt, distinguishing it from the journal fallback above.
func TestLandscapeEndpointReturnsScanReportIsLiveSource(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := PersistScanRoots(configPath, []string{projectRoot}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	before := time.Now().Add(-time.Second)
	resp := doAuthedGet(t, ts.URL+"/api/landscape", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got LandscapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Source != "live" {
		t.Errorf("Source = %q, want live", got.Source)
	}
	if got.ScannedAt.Before(before) {
		t.Errorf("ScannedAt = %v, want a fresh timestamp after %v", got.ScannedAt, before)
	}
}

// TestLandscapeRescanEndpointRunsScanAndAppendsJournalEvent pins POST
// /api/landscape/rescan: it scans from persisted roots, journals the
// result exactly like `cortex scan` does, and returns the fresh report
// with source:"live".
func TestLandscapeRescanEndpointRunsScanAndAppendsJournalEvent(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("# fixture"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := PersistScanRoots(configPath, []string{projectRoot}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/landscape/rescan", "tok", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got LandscapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Source != "live" {
		t.Errorf("Source = %q, want live", got.Source)
	}
	if len(got.Projects) != 1 || got.Projects[0].Path != projectRoot {
		t.Errorf("Projects = %+v, want one entry for %s", got.Projects, projectRoot)
	}

	// The rescan must have journaled a landscape.scan event too.
	_, _, found, err := journal.LatestLandscapeScan()
	if err != nil {
		t.Fatalf("LatestLandscapeScan: %v", err)
	}
	if !found {
		t.Error("rescan did not append a landscape.scan journal event")
	}
}

// TestLandscapeRescanEndpointNoRootsIsTypedRefusal pins that rescan 412s
// under the same condition as GET /api/landscape's live branch — there is
// nothing to rescan without persisted roots, and this endpoint (unlike GET)
// has no journal fallback to offer instead.
func TestLandscapeRescanEndpointNoRootsIsTypedRefusal(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json") // never written

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp := doAuthedPost(t, ts.URL+"/api/landscape/rescan", "tok", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
}

// TestLandscapeRescanEndpointRequiresAuth mirrors
// TestLandscapeEndpointRequiresAuth for the new endpoint.
func TestLandscapeRescanEndpointRequiresAuth(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/landscape/rescan", "application/json", nil)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLandscapeEndpointRequiresAuth(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t), newRunningSet())))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/landscape")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
