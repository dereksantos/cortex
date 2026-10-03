package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// Step 4 (issue #129): the composition root resolves a per-project Commands
// set and exposes it through CortexSession.ProjectCommands (the dynamic
// tools.ProjectCommands capability the write_file/edit_file post-edit hook
// consumes). A session that never resolved a project exposes no commands —
// the hook is a no-op and REPL write_file/edit_file stay byte-identical.
func TestProjectCommandsWiring(t *testing.T) {
	t.Run("explicit Go project resolves discovered commands", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
		ws, err := NewWorkspace(root)
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		cs := &CortexSession{workspace: ws}
		cs.projectCommands = resolveProjectCommands(ws.Root, cs.Config)
		cmds := cs.ProjectCommands()
		cmd, ok := cmds.Get(projectcmd.RoleFormat)
		if !ok || cmd.Cmd != "gofmt -w {file}" {
			t.Errorf("format = %q (ok=%v), want the discovered gofmt per-file command", cmd.Cmd, ok)
		}
		if _, ok := cmds.Get(projectcmd.RoleLint); !ok {
			t.Errorf("a Go project should discover a lint command (go vet)")
		}
	})

	t.Run("config declaration overrides discovery", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
		// AGENTS.md declares a format command; the config's project.commands
		// declares a different one — config must win (step 2 precedence:
		// config > AGENTS.md > discovery).
		writeProjectFile(t, filepath.Join(root, "AGENTS.md"), "## Commands\n\n- format: gofmt -s {file}\n")
		cfg := &Config{}
		cfg.Project.Commands = map[string]string{"format": "customfmt {file}"}
		cmds := resolveProjectCommands(root, cfg)
		got, _ := cmds.Get(projectcmd.RoleFormat)
		if got.Cmd != "customfmt {file}" {
			t.Errorf("config should beat AGENTS.md, got %q", got.Cmd)
		}
	})

	t.Run("AGENTS.md beats discovery when config is absent", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFile(t, filepath.Join(root, "go.mod"), "module t\n\ngo 1.26\n")
		writeProjectFile(t, filepath.Join(root, "AGENTS.md"), "## Commands\n\n- format: gofmt -s {file}\n")
		cmds := resolveProjectCommands(root, nil)
		got, _ := cmds.Get(projectcmd.RoleFormat)
		if got.Cmd != "gofmt -s {file}" {
			t.Errorf("AGENTS.md should beat discovery, got %q", got.Cmd)
		}
	})

	t.Run("session without a resolved project exposes no commands", func(t *testing.T) {
		cs := &CortexSession{}
		if got := cs.ProjectCommands(); got.Format.Cmd != "" || got.Lint.Cmd != "" {
			t.Errorf("a bare session must expose no project commands, got %+v", got)
		}
	})
}

func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
