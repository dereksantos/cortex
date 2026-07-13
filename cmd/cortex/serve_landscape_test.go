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
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t))))
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
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json") // never written

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t))))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/landscape", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", resp.StatusCode)
	}
}

func TestLandscapeEndpointRequiresAuth(t *testing.T) {
	homeDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, homeDir, testLoopsStore(t))))
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
