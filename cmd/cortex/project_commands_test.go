// project_commands_test.go covers the declaration half of issue #129:
// a `project.commands` section in .cortex/config.json and a `## Commands`
// section in AGENTS.md both declare a project's format/lint/test/build
// commands. The subtests below exercise the single real resolution path —
// resolveProjectCommands (discovery + the config declaration + the root's
// AGENTS.md, in that precedence order) — against a fixture tree, so the
// precedence the hook and `cortex project commands` rely on is what the
// production path computes.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// writeProjectFixture lays out a project tree: .cortex/config.json and
// AGENTS.md at the given paths ("" skips a file), plus any extra
// manifest files (e.g. a go.mod for discovery).
func writeProjectFixture(t *testing.T, configJSON, agentsMD string, extra map[string]string) (projectDir, projectConfigPath string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := ""
	if configJSON != "" {
		cfgPath = filepath.Join(dir, ".cortex", "config.json")
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(configJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if agentsMD != "" {
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agentsMD), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, cfgPath
}

const goMod = "module example.com/proj\n\ngo 1.26\n"

const commandsAgentsMD = `# Project instructions

Always run the checks before review.

## Commands

- format: custom-fmt -w {file}
- lint: golangci-lint run

## Other notes

- test: must-not-leak
`

func TestLoadMergedConfigProjectCommands(t *testing.T) {
	t.Run("project-config-declares-commands", func(t *testing.T) {
		dir, projPath := writeProjectFixture(t, `{
			"project": {
				"commands": {
					"format": "custom-fmt -w {file}",
					"test": "go test -race ./..."
				}
			}
		}`, "", nil)
		cfg := loadMergedConfig("", projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		declared := cfg.DeclaredProjectCommands()
		if cmd, ok := declared.Get(projectcmd.RoleFormat); !ok || cmd != "custom-fmt -w {file}" {
			t.Errorf("format = %q (ok=%v), want the config declaration", cmd, ok)
		}
		if cmd, ok := declared.Get(projectcmd.RoleTest); !ok || cmd != "go test -race ./..." {
			t.Errorf("test = %q (ok=%v)", cmd, ok)
		}
		if _, ok := declared.Get(projectcmd.RoleLint); ok {
			t.Error("lint was not declared")
		}
		// The resolved set (the single real path) must show the declaration
		// under the config provenance label.
		resolved := resolveProjectCommands(dir, cfg)
		if cmd, ok := resolved.Get(projectcmd.RoleFormat); !ok || cmd.Source != projectcmd.SourceConfig || cmd.Cmd != "custom-fmt -w {file}" {
			t.Errorf("resolved format = %+v (ok=%v), want the config declaration", cmd, ok)
		}
	})

	t.Run("config-beats-agents-md-beats-discovery", func(t *testing.T) {
		dir, projPath := writeProjectFixture(t, `{"project": {"commands": {"format": "cfg-fmt {file}"}}}`, commandsAgentsMD,
			map[string]string{"go.mod": goMod})
		cfg := loadMergedConfig("", projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		// The single real path: discovery + config declaration + the root's
		// AGENTS.md, in that precedence order.
		resolved := resolveProjectCommands(dir, cfg)
		if cmd, ok := resolved.Get(projectcmd.RoleFormat); !ok || cmd.Cmd != "cfg-fmt {file}" || cmd.Source != projectcmd.SourceConfig {
			t.Errorf("format = %+v (ok=%v), want config.json to win over AGENTS.md", cmd, ok)
		}
		if cmd, ok := resolved.Get(projectcmd.RoleLint); !ok || cmd.Cmd != "golangci-lint run" || cmd.Source != projectcmd.SourceAgents {
			t.Errorf("lint = %+v (ok=%v), want AGENTS.md to win over discovery", cmd, ok)
		}
		if cmd, ok := resolved.Get(projectcmd.RoleTest); !ok || cmd.Cmd != "go test ./..." || cmd.Source != "go.mod" {
			t.Errorf("test = %+v (ok=%v), want discovery to stand when neither source declares", cmd, ok)
		}
		if cmd, ok := resolved.Get(projectcmd.RoleBuild); !ok || cmd.Cmd != "go build ./..." || cmd.Source != "go.mod" {
			t.Errorf("build = %+v (ok=%v), want discovery to stand", cmd, ok)
		}
	})

	t.Run("agents-md-alone-beats-discovery", func(t *testing.T) {
		dir, projPath := writeProjectFixture(t, `{}`, commandsAgentsMD, map[string]string{"go.mod": goMod})
		cfg := loadMergedConfig("", projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		resolved := resolveProjectCommands(dir, cfg)
		if cmd, ok := resolved.Get(projectcmd.RoleFormat); !ok || cmd.Cmd != "custom-fmt -w {file}" || cmd.Source != projectcmd.SourceAgents {
			t.Errorf("format = %+v (ok=%v), want the AGENTS.md declaration over the go.mod discovery", cmd, ok)
		}
	})

	t.Run("user-config-declaration-survives-merge-and-beats-agents-md", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user.json")
		if err := os.WriteFile(userPath, []byte(`{"project": {"commands": {"lint": "user-lint"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		projDir, projPath := writeProjectFixture(t, `{}`, commandsAgentsMD, nil)
		cfg := loadMergedConfig(userPath, projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		if cmd, ok := cfg.Project.Commands["lint"]; !ok || cmd != "user-lint" {
			t.Errorf("user lint declaration lost in merge: %q", cmd)
		}
		// The user-level lint declaration must beat the project's
		// AGENTS.md lint declaration (config > AGENTS.md) in the resolved
		// set the hook uses.
		resolved := resolveProjectCommands(projDir, cfg)
		if cmd, ok := resolved.Get(projectcmd.RoleLint); !ok || cmd.Cmd != "user-lint" || cmd.Source != projectcmd.SourceConfig {
			t.Errorf("lint = %+v (ok=%v), want the user-level config declaration to beat AGENTS.md", cmd, ok)
		}
	})

	t.Run("no-config-no-agents-leaves-empty-declarations", func(t *testing.T) {
		if cfg := loadMergedConfig("", ""); cfg != nil {
			t.Fatalf("want nil config, got %+v", cfg)
		}
	})
}

func TestMergeProjectCommands(t *testing.T) {
	t.Run("field-by-field-override", func(t *testing.T) {
		base := ProjectConfig{Commands: map[string]string{"format": "base-fmt", "test": "base-test"}}
		over := ProjectConfig{Commands: map[string]string{"format": "over-fmt", "lint": "over-lint"}}
		got := mergeProject(base, over)
		if got.Commands["format"] != "over-fmt" {
			t.Errorf("format = %q, want the override", got.Commands["format"])
		}
		if got.Commands["lint"] != "over-lint" {
			t.Errorf("lint = %q, want the added key", got.Commands["lint"])
		}
		if got.Commands["test"] != "base-test" {
			t.Errorf("test = %q, want the inherited key", got.Commands["test"])
		}
	})

	t.Run("empty-override-inherits-whole", func(t *testing.T) {
		base := ProjectConfig{Commands: map[string]string{"test": "base-test"}}
		got := mergeProject(base, ProjectConfig{})
		if len(got.Commands) != 1 || got.Commands["test"] != "base-test" {
			t.Errorf("merge with empty override = %v, want base preserved", got.Commands)
		}
	})

	t.Run("no-override-inherits-nil", func(t *testing.T) {
		got := mergeProject(ProjectConfig{}, ProjectConfig{})
		if got.Commands != nil {
			t.Errorf("merge of two empty sections = %v, want nil", got.Commands)
		}
	})
}

func TestDeclaredProjectCommandsFiltersUnknownRoles(t *testing.T) {
	t.Run("unknown-roles-dropped-blank-trimmed", func(t *testing.T) {
		cfg := &Config{Project: ProjectConfig{Commands: map[string]string{
			"format": "ok {file}",
			"deploy": "nope",      // not a command role
			"lint":   "  spaced ", // whitespace-trimmed
			"test":   "   ",       // blank → ignored
		}}}
		declared := cfg.DeclaredProjectCommands()
		if cmd, ok := declared.Get(projectcmd.RoleFormat); !ok || cmd != "ok {file}" {
			t.Errorf("format = %q (ok=%v)", cmd, ok)
		}
		if cmd, ok := declared.Get(projectcmd.RoleLint); !ok || cmd != "spaced" {
			t.Errorf("lint = %q (ok=%v), want whitespace-trimmed", cmd, ok)
		}
		if _, ok := declared.Get(projectcmd.RoleTest); ok {
			t.Error("a blank test declaration must be ignored")
		}
		if len(declared) != 2 {
			t.Errorf("declared = %v, want exactly format and lint", declared)
		}
	})

	t.Run("role-keys-are-case-insensitive", func(t *testing.T) {
		cfg := &Config{Project: ProjectConfig{Commands: map[string]string{
			"TEST": "go test ./...",
		}}}
		declared := cfg.DeclaredProjectCommands()
		if cmd, ok := declared.Get(projectcmd.RoleTest); !ok || cmd != "go test ./..." {
			t.Errorf("upper-case role key must normalize: %q (ok=%v)", cmd, ok)
		}
	})

	t.Run("nil-config-is-safe", func(t *testing.T) {
		var cfg *Config
		if got := cfg.DeclaredProjectCommands(); len(got) != 0 {
			t.Errorf("nil config should declare nothing, got %v", got)
		}
	})
}
