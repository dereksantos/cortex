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

	"github.com/dereksantos/cortex/internal/registry"
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

// M4.2c2b1 — PUT /api/models/{role}?scope=user|project: scoped role-binding
// writes, the file-backed half of GOAL.md §6 M4.2's models slice (STATE.md
// splits M4.2c2b into c2b1 user+project / c2b2 session — session is
// in-memory-only per docs/cortex-web.md Phase 4 and main.go's own /model
// handler, a different mechanism entirely, so it's not accepted here yet).

// doAuthedPut issues an authenticated PUT with a JSON body against url.
func doAuthedPut(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
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

func TestSetModelBindingUserScopeWritesAndUnknownFieldsSurvive(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	original := `{"backend":{"type":"ollama"},"models":{"study":{"model":"existing-study-model","window":9999}},"tools":{"allow_delete":false}}`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/code?scope=user", "tok", `{"model":"new-code-model","endpoint":"http://x.example"}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}

	var got setModelBindingResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Role != roleCode || got.Scope != "user" {
		t.Errorf("Role/Scope = %q/%q, want %q/%q", got.Role, got.Scope, roleCode, "user")
	}
	if got.Binding.Model != "new-code-model" || got.Binding.Endpoint != "http://x.example" {
		t.Errorf("Binding = %+v, want Model=new-code-model Endpoint=http://x.example", got.Binding)
	}

	doc, err := readJSONDoc(configPath)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if !jsonEqual(t, doc["tools"], []byte(`{"allow_delete":false}`)) {
		t.Errorf("tools field mutated: %s", doc["tools"])
	}
	if !jsonEqual(t, doc["backend"], []byte(`{"type":"ollama"}`)) {
		t.Errorf("backend field mutated: %s", doc["backend"])
	}
	var models map[string]ModelSpec
	if err := json.Unmarshal(doc["models"], &models); err != nil {
		t.Fatalf("Unmarshal models: %v", err)
	}
	if got := models[roleStudy]; got.Model != "existing-study-model" || got.Window != 9999 {
		t.Errorf("models[study] mutated: %+v, want untouched Model=existing-study-model Window=9999", got)
	}
	if got := models[roleCode]; got.Model != "new-code-model" || got.Endpoint != "http://x.example" {
		t.Errorf("models[code] on disk = %+v, want the just-written binding", got)
	}
}

func TestSetModelBindingResponseNeverIncludesSecretValue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CORTEX_TEST_SET_MODEL_KEY", "super-secret-write-value")

	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/code?scope=user", "tok", `{"key_env":"CORTEX_TEST_SET_MODEL_KEY"}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "super-secret-write-value") {
		t.Fatalf("response leaked resolved key material: %s", body)
	}
}

func TestSetModelBindingProjectScopeWritesUnderProjectConfigAndLeavesUserConfigUntouched(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"backend":{"type":"user-scope-marker"}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	projectRoot := t.TempDir()

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: projectRoot}}}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), configPath, t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/study?scope=project&project=blog", "tok", `{"model":"proj-study-model"}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	var got setModelBindingResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Scope != "project" || got.Binding.Model != "proj-study-model" {
		t.Errorf("Scope/Binding = %q/%+v, want project/Model=proj-study-model", got.Scope, got.Binding)
	}

	projDoc, err := readJSONDoc(filepath.Join(projectRoot, ".cortex", "config.json"))
	if err != nil {
		t.Fatalf("readJSONDoc(project): %v", err)
	}
	var models map[string]ModelSpec
	if err := json.Unmarshal(projDoc["models"], &models); err != nil {
		t.Fatalf("Unmarshal project models: %v", err)
	}
	if models[roleStudy].Model != "proj-study-model" {
		t.Errorf("project config models[study].Model = %q, want proj-study-model", models[roleStudy].Model)
	}

	userBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(user config): %v", err)
	}
	if !jsonEqual(t, userBytes, []byte(`{"backend":{"type":"user-scope-marker"}}`)) {
		t.Errorf("user config mutated by a project-scope write: %s", userBytes)
	}
}

func TestSetModelBindingProjectScopeMissingProjectQueryReturns400(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), filepath.Join(t.TempDir(), "config.json"), t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/code?scope=project", "tok", `{"model":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSetModelBindingProjectScopeUnknownProjectReturns404(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), filepath.Join(t.TempDir(), "config.json"), t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/code?scope=project&project=doesnotexist", "tok", `{"model":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSetModelBindingUnknownRoleReturns400(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), filepath.Join(t.TempDir(), "config.json"), t.TempDir())))
	defer ts.Close()

	resp := doAuthedPut(t, ts.URL+"/api/models/not-a-real-role?scope=user", "tok", `{"model":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSetModelBindingUnsupportedScopeReturns400(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), filepath.Join(t.TempDir(), "config.json"), t.TempDir())))
	defer ts.Close()

	for _, scope := range []string{"session", "", "bogus"} {
		resp := doAuthedPut(t, ts.URL+"/api/models/code?scope="+scope, "tok", `{"model":"x"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("scope=%q: status = %d, want 400", scope, resp.StatusCode)
		}
	}
}

func TestSetModelBindingRequiresAuth(t *testing.T) {
	reg := &fakeRegistry{}
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, testSessionManager(reg), filepath.Join(t.TempDir(), "config.json"), t.TempDir())))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/models/code?scope=user", strings.NewReader(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
