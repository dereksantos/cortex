// webui_models_test.go — M5.2d: the models view-model (GOAL.md §6 M5.2,
// split into M5.2a/b/c/d per STATE.md). Golden-pins the exact JSON shape
// buildModelsViewModel produces over a fixed config+fleet fixture, following
// the "golden-pinned = literal string in a test" convention established at
// M1.5/M2.5/M5.2a/M5.2b/M5.2c.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixedRoleFleet is deliberately small: one fleet entry tagged "reasoner"
// (MaxInput 0, so applyFleet's Window-fill never fires — keeps the golden
// simple) matches BOTH the "reason" and "study" roles (same tag), giving
// coverage of "configured" (role "code", set explicitly below),
// "fleet-auto" (roles "reason"/"study", picked from this fleet), and
// "unbound" (every other role: no config entry, no matching fleet tag).
var fixedRoleFleet = Fleet{"study-model": {Role: "reasoner"}}

func TestBuildModelsViewModelGolden(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfgBody := `{"backend":{"endpoint":"http://fixture:9999","key_env":"CORTEX_TEST_MODELS_VM_KEY"},"models":{"code":{"model":"coder-explicit"}}}`
	if err := os.WriteFile(configPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("CORTEX_TEST_MODELS_VM_KEY", "super-secret-vm-value")

	cfg := loadMergedConfig(configPath, "")
	vm := buildModelsViewModel(cfg, fixedRoleFleet)

	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	const keyEnv = `"key_env":"CORTEX_TEST_MODELS_VM_KEY"`
	row := func(role, model, thinking, source string) string {
		return `{"role":"` + role + `","binding":{"endpoint":"http://fixture:9999","model":"` + model +
			`","window":0,"max_tokens":0,"temperature":null,` + keyEnv + `,"key_service":"","thinking":` + thinking +
			`},"source":"` + source + `"}`
	}

	want := `{"bindings":[` +
		row("code", "coder-explicit", "false", "configured") + "," +
		row("embed", "", "null", "unbound") + "," +
		row("fast", "", "false", "unbound") + "," +
		row("hard-code", "", "null", "unbound") + "," +
		row("reason", "study-model", "null", "fleet-auto") + "," +
		row("rerank", "", "null", "unbound") + "," +
		row("study", "study-model", "null", "fleet-auto") + "," +
		row("tools", "", "null", "unbound") +
		`],"fleet":{"study-model":{"MaxInput":0,"Role":"reasoner","Silicon":"",` +
		`"Thinking":false,"SwapGroup":"","AlwaysWarm":false,"Experimental":false}}}`

	if string(got) != want {
		t.Errorf("models view-model JSON =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(string(got), "super-secret-vm-value") {
		t.Fatalf("view-model leaked resolved key material: %s", got)
	}
}

func TestBuildModelsViewModelNilConfigEveryKnownRoleStillPresent(t *testing.T) {
	vm := buildModelsViewModel(nil, fixedRoleFleet)

	if len(vm.Bindings) != len(rolePolicies) {
		t.Fatalf("len(Bindings) = %d, want %d (one per known role)", len(vm.Bindings), len(rolePolicies))
	}
	byRole := make(map[string]modelBindingView, len(vm.Bindings))
	for _, b := range vm.Bindings {
		byRole[b.Role] = b
	}
	reason, ok := byRole["reason"]
	if !ok || reason.Source != bindingSourceFleetAuto || reason.Binding.Model != "study-model" {
		t.Errorf("Bindings[reason] = %+v, ok=%v, want Source=fleet-auto Model=study-model even with a nil config", reason, ok)
	}
	tools, ok := byRole["tools"]
	if !ok || tools.Source != bindingSourceUnbound || tools.Binding.Model != "" {
		t.Errorf("Bindings[tools] = %+v, ok=%v, want Source=unbound Model=\"\" with no config and no matching fleet tag", tools, ok)
	}
}

func TestBuildModelsViewModelRolesSortedAlphabetically(t *testing.T) {
	vm := buildModelsViewModel(nil, Fleet{})
	var roles []string
	for _, b := range vm.Bindings {
		roles = append(roles, b.Role)
	}
	want := []string{"code", "embed", "fast", "hard-code", "reason", "rerank", "study", "tools"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("roles[%d] = %q, want %q (want alphabetical order): got %v", i, roles[i], want[i], roles)
		}
	}
}
