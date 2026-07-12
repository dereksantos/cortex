// serve_dashboard_test.go — M5.3b: GET /api/dashboard, wiring M5.2a's
// buildDashboardViewModel into the HTTP surface so the dashboard screen's
// JS has a single endpoint to fetch. See GOAL.md §6 M5.3.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
)

func TestDashboardEndpointReturnsViewModel(t *testing.T) {
	root := t.TempDir() // never git-initialized — exercises the ChangeError leg too
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := &fakeRegistry{projects: map[string]registry.Project{
		"plain": {Name: "plain", Root: root},
	}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "")))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/dashboard", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got dashboardViewModel
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want, err := buildDashboardViewModel(reg)
	if err != nil {
		t.Fatalf("buildDashboardViewModel: %v", err)
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
		t.Errorf("GET /api/dashboard body =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

func TestDashboardEndpointEmptyRegistryReturnsEmptyProjectsNotNull(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "")))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/dashboard", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != `{"projects":[]}` {
		t.Errorf("body = %s, want empty projects array", body)
	}
}

func TestDashboardEndpointRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), "", "")))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/dashboard")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
