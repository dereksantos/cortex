package main

import (
	"os"
	"path/filepath"
	"testing"
)

// project_dir_test.go guards the upward .cortex walk against selecting the
// USER-level directory (~/.cortex, or $CORTEX_HOME) as a project's context
// dir or as the project config layer. Observed 2026-10-11: `cortex` run in
// ~/demo/slug (no .cortex of its own) wrote its session to
// ~/.cortex/sessions and loaded the user's global memory notes.

// mkdirAll creates every dir under base and fails the test on error.
func mkdirAll(t *testing.T, base string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}

// writeConfigFixture writes a minimal .cortex/config.json under dir.
func writeConfigFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".cortex"), 0o755); err != nil {
		t.Fatalf("mkdir %s/.cortex: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cortex", "config.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
}

func TestProjectDirResolutionSkipsUserDir(t *testing.T) {
	tests := []struct {
		name string
		// setup builds the tree under base and returns (home, cortexHome, cwd),
		// all relative to base; cortexHome "" leaves $CORTEX_HOME unset.
		setup func(t *testing.T, base string) (home, cortexHome, cwd string)
		// wantRoot is the expected workspace root, relative to base.
		wantRoot string
		// wantConfig is the expected project config path relative to base,
		// or "" for no project config layer.
		wantConfig string
	}{
		{
			name: "workspace under home without its own .cortex",
			setup: func(t *testing.T, base string) (string, string, string) {
				writeConfigFixture(t, filepath.Join(base, "home"))
				mkdirAll(t, base, "home/demo/slug")
				return "home", "", "home/demo/slug"
			},
			wantRoot:   "home/demo/slug",
			wantConfig: "",
		},
		{
			name: "workspace with its own .cortex",
			setup: func(t *testing.T, base string) (string, string, string) {
				writeConfigFixture(t, filepath.Join(base, "home"))
				writeConfigFixture(t, filepath.Join(base, "home/proj"))
				return "home", "", "home/proj"
			},
			wantRoot:   "home/proj",
			wantConfig: "home/proj/.cortex/config.json",
		},
		{
			name: "nested dir whose non-home ancestor has .cortex",
			setup: func(t *testing.T, base string) (string, string, string) {
				writeConfigFixture(t, filepath.Join(base, "home"))
				writeConfigFixture(t, filepath.Join(base, "home/proj"))
				mkdirAll(t, base, "home/proj/a/b")
				return "home", "", "home/proj/a/b"
			},
			wantRoot:   "home/proj",
			wantConfig: "home/proj/.cortex/config.json",
		},
		{
			name: "CORTEX_HOME outside home is skipped as an ancestor",
			setup: func(t *testing.T, base string) (string, string, string) {
				mkdirAll(t, base, "home")
				writeConfigFixture(t, filepath.Join(base, "alt"))
				mkdirAll(t, base, "alt/ws")
				return "home", "alt/.cortex", "alt/ws"
			},
			wantRoot:   "alt/ws",
			wantConfig: "",
		},
		{
			name: "~/.cortex still skipped when CORTEX_HOME points elsewhere",
			setup: func(t *testing.T, base string) (string, string, string) {
				writeConfigFixture(t, filepath.Join(base, "home"))
				mkdirAll(t, base, "ch", "home/demo")
				return "home", "ch", "home/demo"
			},
			wantRoot:   "home/demo",
			wantConfig: "",
		},
		{
			name: "CWD is home itself",
			setup: func(t *testing.T, base string) (string, string, string) {
				writeConfigFixture(t, filepath.Join(base, "home"))
				return "home", "", "home"
			},
			// Home's own .cortex is the CWD's own dir, so it is the workspace
			// dir; but it is the user config, so it is not loaded a second
			// time as the project layer.
			wantRoot:   "home",
			wantConfig: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := resolvedPath(t, t.TempDir())
			home, cortexHome, cwd := tc.setup(t, base)
			t.Setenv("HOME", filepath.Join(base, home))
			if cortexHome != "" {
				t.Setenv("CORTEX_HOME", filepath.Join(base, cortexHome))
			} else {
				t.Setenv("CORTEX_HOME", "")
			}
			t.Chdir(filepath.Join(base, cwd))

			wantRoot := filepath.Join(base, tc.wantRoot)
			wantCtx := filepath.Join(wantRoot, ".cortex")

			ws := WorkspaceFromCWD()
			if got := resolvedPath(t, ws.Root); got != wantRoot {
				t.Errorf("WorkspaceFromCWD().Root = %q, want %q", got, wantRoot)
			}
			if got := ws.ContextDir(); filepath.Clean(got) != wantCtx {
				t.Errorf("WorkspaceFromCWD().ContextDir() = %q, want %q", got, wantCtx)
			}

			gotCtx, err := filepath.Abs(contextDir())
			if err != nil {
				t.Fatalf("Abs(contextDir()): %v", err)
			}
			if gotCtx != wantCtx {
				t.Errorf("contextDir() = %q, want %q", gotCtx, wantCtx)
			}

			wantConfig := ""
			if tc.wantConfig != "" {
				wantConfig = filepath.Join(base, tc.wantConfig)
			}
			if got := findConfigPath(); got != wantConfig {
				t.Errorf("findConfigPath() = %q, want %q", got, wantConfig)
			}
		})
	}
}
