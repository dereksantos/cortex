// project_trust_test.go covers issue #129's trust gate on the config side:
// the per-workspace trust list (project.trusted), its merge semantics (the
// USER-level list only — a repository cannot mark itself trusted), the
// path-matching accessor, and the `cortex project trust` store functions.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMergeProjectTrustIsUserLevelOnly pins the trust source (the blocker's
// "never settable from the repository itself"): the project-level (over)
// config's project.trusted is DROPPED by the merge — only the user-level
// (base) list survives. A repository's own .cortex/config.json claiming
// trust for itself is ignored; a user-level list survives intact.
func TestMergeProjectTrustIsUserLevelOnly(t *testing.T) {
	t.Run("repo-claim-dropped-user-list-kept", func(t *testing.T) {
		base := ProjectConfig{Trusted: []string{"/home/u/real-repo"}}
		over := ProjectConfig{Trusted: []string{"/home/u/real-repo", "/repo-claims-itself"}}
		got := mergeProject(base, over)
		if len(got.Trusted) != 1 || got.Trusted[0] != "/home/u/real-repo" {
			t.Errorf("merge = %v, want only the user-level list", got.Trusted)
		}
	})

	t.Run("no-user-list-means-untrusted-even-if-repo-claims", func(t *testing.T) {
		over := ProjectConfig{Trusted: []string{"/repo-claims-itself"}}
		got := mergeProject(ProjectConfig{}, over)
		if len(got.Trusted) != 0 {
			t.Errorf("a repo claim with no user list must be untrusted, got %v", got.Trusted)
		}
	})

	t.Run("project-commands-still-merge-over-trust", func(t *testing.T) {
		base := ProjectConfig{Commands: map[string]string{"format": "user-fmt"}, Trusted: []string{"/a"}}
		over := ProjectConfig{Commands: map[string]string{"format": "repo-fmt"}, Trusted: []string{"/b"}}
		got := mergeProject(base, over)
		if got.Commands["format"] != "repo-fmt" {
			t.Errorf("commands must still merge field-by-field (repo wins), got %q", got.Commands["format"])
		}
		if len(got.Trusted) != 1 || got.Trusted[0] != "/a" {
			t.Errorf("trust must stay user-level, got %v", got.Trusted)
		}
	})
}

// TestWorkspaceTrustedMatching pins Config.WorkspaceTrusted's path
// comparison: exact match, trailing-slash insensitivity, a missing entry
// (untrusted), and a nil config (untrusted — the safe default). The
// directory is created so the abs/symlink normalization is real, and the
// list is written to the redirected USER config — the authoritative source
// (trustFromUserConfig), so the assertion is the end-to-end one: the list
// the operator persists is the list that decides.
func TestWorkspaceTrustedMatching(t *testing.T) {
	// Redirect CORTEX_HOME FIRST: this test writes the user config, and
	// TestMain's redirect applies only when CORTEX_HOME is unset — a
	// developer or CI job that sets it would otherwise lose their real
	// user config (t.TempDir cleanup is enough for the temp dir).
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()
	cfg := &Config{Project: ProjectConfig{Trusted: []string{root}}}
	cfgPath := userConfigPath()
	if cfgPath == "" {
		t.Skip("no user config path")
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"project": {"trusted": ["`+root+`"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !cfg.WorkspaceTrusted(root) {
		t.Errorf("the listed root must be trusted")
	}
	if !cfg.WorkspaceTrusted(root + "/") {
		t.Errorf("a trailing-slash spelling of the listed root must match (it normalizes to the same path)")
	}
	if cfg.WorkspaceTrusted(filepath.Join(root, "sub")) {
		t.Errorf("a SUBDIRECTORY of a trusted root is not itself trusted (no prefix matching)")
	}
	if cfg.WorkspaceTrusted(t.TempDir()) {
		t.Errorf("an unlisted root must be untrusted")
	}
	var nilCfg *Config
	if nilCfg.WorkspaceTrusted(root) {
		t.Errorf("a nil config must be untrusted")
	}
}

// TestWorkspaceTrustedFromMergedConfigIsUserLevelOnly is the regression
// guard for the trust-source bypass: loadMergedConfig returns the project
// config VERBATIM when there is no user config (the zero-config default) or
// the user config is malformed (readConfigFile's fallback), so a project
// config that carries project.trusted (a repo claim) would sit in the merged
// config's Project.Trusted. WorkspaceTrusted must still say NO in both
// cases — it reads the user config directly (Config.TrustedList /
// trustFromUserConfig), never the merged field. The CORTEX_HOME redirect
// makes the "no user config" case real for the direct read too (an empty
// CORTEX_HOME dir, not just an absent merged-layer path).
func TestWorkspaceTrustedFromMergedConfigIsUserLevelOnly(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir()) // empty user home: the zero-config default

	projRoot := t.TempDir() // the repository, claiming trust for itself
	if err := os.MkdirAll(filepath.Join(projRoot, ".cortex"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"project": {"trusted": ["` + projRoot + `"]}}`
	projPath := filepath.Join(projRoot, ".cortex", "config.json")
	if err := os.WriteFile(projPath, []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		userPath string // user config path (or "" for none)
	}{
		{name: "no-user-config", userPath: ""},
		{name: "malformed-user-config", userPath: func() string {
			p := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(p, []byte(`{"project": {"trusted": [`), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged := loadMergedConfig(tc.userPath, projPath)
			if merged == nil {
				t.Fatal("merged config must not be nil")
			}
			// The repo's claim IS in the merged config (that is the
			// vulnerability the direct user read closes).
			if len(merged.Project.Trusted) == 0 {
				t.Fatalf("merged config must carry the repo's claim (the bypass precondition): %v", merged.Project.Trusted)
			}
			if merged.WorkspaceTrusted(projRoot) {
				t.Errorf("a repo-claimed root must NOT be trusted when the user config is %s", tc.name)
			}
			if merged.WorkspaceTrusted(t.TempDir()) {
				t.Errorf("an unrelated root must be untrusted")
			}
		})
	}

	// Positive control: the same repo claim, but the USER config trusts the
	// root — and only then does the claim become real.
	userHome := t.TempDir()
	t.Setenv("CORTEX_HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, "config.json"),
		[]byte(`{"project": {"trusted": ["`+projRoot+`"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	merged := loadMergedConfig(filepath.Join(userHome, "config.json"), projPath)
	if !merged.WorkspaceTrusted(projRoot) {
		t.Errorf("the user-listed root must be trusted (positive control)")
	}
}

// TestWorkspaceTrustedEndToEndFromConfigs is the full merge path against
// real config files: the project config CLAIMS trust for its own root (the
// repo-claim scenario), the user config trusts a DIFFERENT root. The
// merged config must trust the user-listed root and NOT the repo-claimed
// one — and the project config's claim must be dropped even though it
// names its own directory exactly.
func TestWorkspaceTrustedEndToEndFromConfigs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CORTEX_HOME", home)

	projRoot := t.TempDir() // the repository under test
	userRoot := t.TempDir() // a workspace the operator trusts

	// The repo's own config claims trust for itself (must be ignored).
	if err := os.MkdirAll(filepath.Join(projRoot, ".cortex"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"project": {"trusted": ["` + projRoot + `"]}}`
	if err := os.WriteFile(filepath.Join(projRoot, ".cortex", "config.json"), []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// The USER config trusts the OTHER root (must be honored).
	userCfg := `{"project": {"trusted": ["` + userRoot + `"]}}`
	userCfgPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(userCfgPath, []byte(userCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	merged := loadMergedConfig(userCfgPath, filepath.Join(projRoot, ".cortex", "config.json"))
	if merged == nil {
		t.Fatal("merged config must not be nil")
	}
	if !merged.WorkspaceTrusted(userRoot) {
		t.Errorf("the user-listed root must be trusted")
	}
	if merged.WorkspaceTrusted(projRoot) {
		t.Errorf("a repo-local trust claim must NOT make the repo trusted")
	}
}

// TestTrustStoreRoundTrip pins the `cortex project trust` store: add is
// idempotent, remove of a missing root errors, and a save/load round-trip
// PRESERVES the operator's other config fields (backend, tools) and unknown
// top-level keys — a trust edit must never clobber the user config.
func TestTrustStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CORTEX_HOME", home)

	cfgPath := filepath.Join(home, "config.json")
	seed := `{"backend": {"type": "ollama", "endpoint": "http://localhost:11434"}, "tools": {"enable_web": true}, "experimental_key": {"a": 1}}`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store, path, err := trustLoad()
	if err != nil {
		t.Fatalf("trustLoad: %v", err)
	}
	if path != cfgPath {
		t.Errorf("store path = %q, want %q", path, cfgPath)
	}
	abs, err := addTrust(store, root)
	if err != nil {
		t.Fatalf("addTrust: %v", err)
	}
	// Idempotent: adding the same root again does not duplicate.
	if _, err := addTrust(store, root); err != nil {
		t.Fatalf("addTrust again: %v", err)
	}
	if len(store.Project.Trusted) != 1 || store.Project.Trusted[0] != abs {
		t.Errorf("trust list = %v, want exactly [%s]", store.Project.Trusted, abs)
	}
	if err := trustSave(store, path); err != nil {
		t.Fatalf("trustSave: %v", err)
	}

	back, _, err := trustLoad()
	if err != nil {
		t.Fatalf("trustLoad after save: %v", err)
	}
	if len(back.Project.Trusted) != 1 || back.Project.Trusted[0] != abs {
		t.Errorf("round-tripped trust = %v, want [%s]", back.Project.Trusted, abs)
	}
	// The operator's other fields survived.
	if back.Backend.Type != "ollama" || back.Backend.Endpoint != "http://localhost:11434" {
		t.Errorf("backend clobbered by the trust edit: %+v", back.Backend)
	}
	if back.Tools.EnableWeb == nil || !*back.Tools.EnableWeb {
		t.Errorf("tools.enable_web clobbered by the trust edit: %+v", back.Tools)
	}
	// The unknown top-level key survived.
	var raw map[string]json.RawMessage
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["experimental_key"]; !ok {
		t.Errorf("unknown top-level key dropped by the trust edit: %s", data)
	}
	// remove works, and removing an absent root errors.
	if err := removeTrust(back, root); err != nil {
		t.Fatalf("removeTrust: %v", err)
	}
	if len(back.Project.Trusted) != 0 {
		t.Errorf("after remove, trust = %v, want empty", back.Project.Trusted)
	}
	if err := removeTrust(back, root); err == nil {
		t.Errorf("removing an absent root must error")
	}
}

// TestTrustListRender pins the `cortex project trust list` output (empty
// message, and one line per sorted root).
func TestTrustListRender(t *testing.T) {
	if got := trustList(nil); got != "No workspaces trusted. Trust one: cortex project trust add <root>\n" {
		t.Errorf("empty trust list = %q", got)
	}
	got := trustList([]string{"/b/repo", "/a/repo"})
	want := "Trusted workspaces (2):\n  - /a/repo\n  - /b/repo\n"
	if got != want {
		t.Errorf("trustList = %q, want %q", got, want)
	}
}
