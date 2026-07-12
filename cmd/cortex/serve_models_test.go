// M4.2c2a — GET /api/models over `cortex serve`'s HTTP surface: the
// read-only half of GOAL.md §6 M4.2's models slice (STATE.md splits M4.2c2
// into M4.2c2a read / M4.2c2b scoped writes). Reuses discoverFleet
// (config.go, already tested against a fake /model/info server in
// main_test.go's fleetServer helper) and Config.resolveBinding verbatim —
// no parallel fleet/binding logic.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelsEndpointReturnsRolesAndFleet(t *testing.T) {
	srv := fleetServer(t, 200, fleetInfoJSON)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfgBody := `{"backend":{"type":"openrouter","endpoint":"` + srv.URL + `","key_env":"CORTEX_TEST_MODELS_KEY"},"models":{"code":{"model":"coder"}}}`
	if err := os.WriteFile(configPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("CORTEX_TEST_MODELS_KEY", "super-secret-value")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/models", "tok")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "super-secret-value") {
		t.Fatalf("response leaked resolved key material: %s", body)
	}

	var got modelsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	code, ok := got.Roles[roleCode]
	if !ok || code.Model != "coder" {
		t.Errorf("Roles[code] = %+v, ok=%v, want Model=coder", code, ok)
	}
	if code.KeyEnv != "CORTEX_TEST_MODELS_KEY" {
		t.Errorf("Roles[code].KeyEnv = %q, want the source env var name (not its value)", code.KeyEnv)
	}
	if _, ok := got.Fleet["coder"]; !ok {
		t.Errorf("Fleet missing discovered model %q: %+v", "coder", got.Fleet)
	}
	if _, ok := got.Roles[roleStudy]; !ok {
		t.Errorf("Roles missing role %q — expected every known role bound, even unconfigured ones", roleStudy)
	}
}

func TestModelsEndpointFleetUnreachableStillReturnsRoles(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfgBody := `{"backend":{"type":"openrouter","endpoint":"http://127.0.0.1:1"}}`
	if err := os.WriteFile(configPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp := doAuthedGet(t, ts.URL+"/api/models", "tok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Fleet) != 0 {
		t.Errorf("Fleet = %+v, want empty when the backend is unreachable", got.Fleet)
	}
	if _, ok := got.Roles[roleCode]; !ok {
		t.Errorf("Roles missing %q even with an unreachable fleet", roleCode)
	}
}

func TestModelsEndpointRequiresAuth(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
