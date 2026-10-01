package projectcmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFixture creates a temp dir with the given name→content files and
// returns its path. Cleanup is deferred to the test.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir for fixture %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return dir
}

func mustCommands(t *testing.T, root string) Commands {
	t.Helper()
	cmds, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover(%s): %v", root, err)
	}
	return cmds
}

// getOrSkip returns the command for role, failing the test if absent.
func getOrSkip(t *testing.T, cmds Commands, role Role) Command {
	t.Helper()
	cmd, ok := cmds.Get(role)
	if !ok {
		t.Fatalf("role %s: no command discovered; got %q", role, cmds.Render())
	}
	return cmd
}

func TestDiscover(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  map[Role]string
		// wantSource pins the manifest a role's command came from.
		wantSource map[Role]string
		// wantPerFile pins which roles must be per-file ({file} placeholder).
		wantPerFile map[Role]bool
		// wantExtends pins which roles carry a source-extension set (a nil
		// value present in the map pins "no extends" — applies to all files).
		wantExtends map[Role][]string
		// wantRoots pins the manifests that contributed.
		wantRoots []string
	}{
		{
			name: "go",
			files: map[string]string{
				"go.mod": "module example.com/proj\n\ngo 1.26\n",
			},
			want: map[Role]string{
				RoleFormat: "gofmt -w {file}",
				RoleLint:   "go vet {dir}",
				RoleTest:   "go test ./...",
				RoleBuild:  "go build ./...",
			},
			wantSource: map[Role]string{
				RoleFormat: "go.mod", RoleLint: "go.mod", RoleTest: "go.mod", RoleBuild: "go.mod",
			},
			wantPerFile: map[Role]bool{RoleFormat: true, RoleLint: false, RoleTest: false, RoleBuild: false},
			// Go is the one toolchain with a recognized source set: format
			// and lint (package-scoped now — the per-file vet's false
			// undefined: errors) apply to .go only.
			wantExtends: map[Role][]string{RoleFormat: {".go"}, RoleLint: {".go"}, RoleTest: nil, RoleBuild: nil},
			wantRoots:   []string{"go.mod"},
		},
		{
			name: "node-scripts",
			files: map[string]string{
				"package.json": `{
  "name": "proj",
  "scripts": {
    "format": "prettier --write .",
    "lint": "eslint src",
    "test": "node --test",
    "build": "tsc -p tsconfig.json"
  }
}`,
			},
			want: map[Role]string{
				// Scripts are reported as their runnable npm form (the body
				// only runs inside npm, which puts node_modules/.bin on
				// PATH); the test script uses npm's "npm test" alias.
				RoleFormat: "npm run format",
				RoleLint:   "npm run lint",
				RoleTest:   "npm test",
				RoleBuild:  "npm run build",
			},
			wantSource: map[Role]string{
				RoleFormat: "package.json", RoleLint: "package.json", RoleTest: "package.json", RoleBuild: "package.json",
			},
			wantPerFile: map[Role]bool{RoleFormat: false, RoleLint: false, RoleTest: false, RoleBuild: false},
			wantExtends: map[Role][]string{RoleFormat: nil, RoleLint: nil},
			wantRoots:   []string{"package.json"},
		},
		{
			name: "node-script-aliases-and-dependency-fallback",
			files: map[string]string{
				"package.json": `{
  "name": "proj",
  "scripts": { "lint": "biome check .", "check": "should-not-win", "test:all": "vitest run" },
  "devDependencies": { "prettier": "3.0.0" }
}`,
			},
			// "check" must NOT supply lint (lint key wins); prettier in
			// devDependencies supplies format (npx fallback); test:all
			// supplies test.
			want: map[Role]string{
				RoleFormat: "npx prettier --write {file}",
				RoleLint:   "npm run lint",
				RoleTest:   "npm run test:all",
			},
			wantSource: map[Role]string{
				RoleFormat: "package.json", RoleLint: "package.json", RoleTest: "package.json",
			},
			wantPerFile: map[Role]bool{RoleFormat: true, RoleLint: false, RoleTest: false},
			// Only the npx prettier FALLBACK carries extends (the dep
			// implies the tool); the declared "biome check ." script does
			// not — discovery can't infer a source set from a script line.
			wantExtends: map[Role][]string{RoleFormat: jsTSExtends, RoleLint: nil, RoleTest: nil},
			wantRoots:   []string{"package.json"},
		},
		{
			name: "python-ruff",
			files: map[string]string{
				"pyproject.toml": `[project]
name = "proj"
version = "0.1.0"

[tool.ruff]
line-length = 100
`,
			},
			want: map[Role]string{
				RoleFormat: "ruff format {file}",
				RoleLint:   "ruff check {file}",
				RoleTest:   "pytest",
				RoleBuild:  "python -m build",
			},
			wantSource: map[Role]string{
				RoleFormat: "pyproject.toml", RoleLint: "pyproject.toml", RoleTest: "pyproject.toml", RoleBuild: "pyproject.toml",
			},
			wantPerFile: map[Role]bool{RoleFormat: true, RoleLint: true, RoleTest: false, RoleBuild: false},
			wantExtends: map[Role][]string{RoleFormat: {".py", ".pyi"}, RoleLint: {".py", ".pyi"}, RoleTest: nil, RoleBuild: nil},
			wantRoots:   []string{"pyproject.toml"},
		},
		{
			name: "python-black-only",
			files: map[string]string{
				"pyproject.toml": `[tool.black]
line-length = 88

[tool.pytest.ini_options]
addopts = "-q"
`,
			},
			want: map[Role]string{
				RoleFormat: "black {file}",
				RoleTest:   "pytest",
			},
			wantSource:  map[Role]string{RoleFormat: "pyproject.toml", RoleTest: "pyproject.toml"},
			wantPerFile: map[Role]bool{RoleFormat: true, RoleTest: false},
			wantExtends: map[Role][]string{RoleFormat: {".py", ".pyi"}},
			wantRoots:   []string{"pyproject.toml"},
		},
		{
			name: "rust",
			files: map[string]string{
				"Cargo.toml": `[package]
name = "proj"
version = "0.1.0"
edition = "2021"
`,
			},
			want: map[Role]string{
				RoleFormat: "cargo fmt",
				RoleLint:   "cargo clippy --all-targets",
				RoleTest:   "cargo test",
				RoleBuild:  "cargo build",
			},
			wantSource: map[Role]string{
				RoleFormat: "Cargo.toml", RoleLint: "Cargo.toml", RoleTest: "Cargo.toml", RoleBuild: "Cargo.toml",
			},
			wantPerFile: map[Role]bool{RoleFormat: false, RoleLint: false, RoleTest: false, RoleBuild: false},
			wantExtends: map[Role][]string{RoleFormat: nil, RoleLint: nil},
			wantRoots:   []string{"Cargo.toml"},
		},
		{
			name: "makefile",
			files: map[string]string{
				// Tab-indented recipe lines and comments must be
				// ignored; "CC :=" is an assignment, not a target;
				// "test: lint" lists lint as a prerequisite, not a name.
				"Makefile": "CC := cc\n" +
					"\n" +
					"# a comment that mentions format:\n" +
					"format:\n" +
					"\tgofmt -w .\n" +
					"\n" +
					"lint:\n" +
					"\tgolangci-lint run\n" +
					"\n" +
					"test: lint\n" +
					"\tgo test ./...\n" +
					"\n" +
					"build:\n" +
					"\tgo build -o bin/proj ./...\n" +
					"\n" +
					"unrelated:\n" +
					"\techo hi\n",
			},
			want: map[Role]string{
				RoleFormat: "make format",
				RoleLint:   "make lint",
				RoleTest:   "make test",
				RoleBuild:  "make build",
			},
			wantSource: map[Role]string{
				RoleFormat: "Makefile", RoleLint: "Makefile", RoleTest: "Makefile", RoleBuild: "Makefile",
			},
			wantPerFile: map[Role]bool{RoleFormat: false, RoleLint: false, RoleTest: false, RoleBuild: false},
			wantExtends: map[Role][]string{RoleFormat: nil, RoleLint: nil},
			wantRoots:   []string{"Makefile"},
		},
		{
			name: "makefile-fills-gaps-only",
			files: map[string]string{
				"pyproject.toml": "[tool.black]\n",
				"Makefile":       "test:\n\tpytest -q\nbuild:\n\tpython -m build\n",
			},
			// pyproject supplies format+test; Makefile may fill the
			// missing build but must NOT override test.
			want: map[Role]string{
				RoleFormat: "black {file}",
				RoleTest:   "pytest",
				RoleBuild:  "make build",
			},
			wantSource:  map[Role]string{RoleFormat: "pyproject.toml", RoleTest: "pyproject.toml", RoleBuild: "Makefile"},
			wantPerFile: map[Role]bool{RoleFormat: true, RoleTest: false, RoleBuild: false},
			wantExtends: map[Role][]string{RoleFormat: {".py", ".pyi"}, RoleTest: nil, RoleBuild: nil},
			wantRoots:   []string{"pyproject.toml", "Makefile"},
		},
		{
			name: "no-recognized-manifest",
			files: map[string]string{
				"README.md":        "# not a project manifest",
				"notes.txt":        "hi",
				"package.json.bak": "{}",
			},
			want: map[Role]string{},
		},
		{
			name:  "empty-dir",
			files: map[string]string{},
			want:  map[Role]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeFixture(t, tt.files)
			cmds := mustCommands(t, root)

			for role, wantCmd := range tt.want {
				cmd := getOrSkip(t, cmds, role)
				if cmd.Cmd != wantCmd {
					t.Errorf("%s: command = %q, want %q", role, cmd.Cmd, wantCmd)
				}
				if wantSrc := tt.wantSource[role]; wantSrc != "" && cmd.Source != wantSrc {
					t.Errorf("%s: source = %q, want %q", role, cmd.Source, wantSrc)
				}
				if wantPF, ok := tt.wantPerFile[role]; ok && cmd.PerFile != wantPF {
					t.Errorf("%s: per-file = %v, want %v", role, cmd.PerFile, wantPF)
				}
				if wantExt, ok := tt.wantExtends[role]; ok && !reflect.DeepEqual(cmd.Extends, wantExt) {
					t.Errorf("%s: extends = %v, want %v", role, cmd.Extends, wantExt)
				}
			}
			for _, role := range []Role{RoleFormat, RoleLint, RoleTest, RoleBuild} {
				if _, want := tt.want[role]; want {
					continue
				}
				if _, ok := cmds.Get(role); ok {
					t.Errorf("%s: command discovered, want absent (render: %s)", role, cmds.Render())
				}
			}
			if len(tt.wantRoots) > 0 {
				if len(cmds.Roots) != len(tt.wantRoots) {
					t.Fatalf("roots = %v, want %v", cmds.Roots, tt.wantRoots)
				}
				for i := range tt.wantRoots {
					if cmds.Roots[i] != tt.wantRoots[i] {
						t.Errorf("roots = %v, want %v", cmds.Roots, tt.wantRoots)
					}
				}
			}
		})
	}
}

func TestDiscoverErrors(t *testing.T) {
	t.Run("missing-root", func(t *testing.T) {
		if _, err := Discover(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
			t.Error("expected an error for a missing root, got nil")
		}
	})
	t.Run("root-is-a-file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(f, []byte("module x\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Discover(f); err == nil {
			t.Error("expected an error for a file root, got nil")
		}
	})
}

func TestDiscoverMalformedManifestsAreSilentlySkipped(t *testing.T) {
	t.Run("broken-package-json", func(t *testing.T) {
		root := writeFixture(t, map[string]string{
			"package.json": `{ "scripts": { "test": "node --test", oops }`,
		})
		cmds := mustCommands(t, root)
		if _, ok := cmds.Get(RoleTest); ok {
			t.Error("a malformed package.json must not contribute commands")
		}
		if cmds.Render() == "" {
			t.Error("render of an empty discovery must still be non-empty")
		}
	})
	t.Run("broken-mixed-with-good-makes", func(t *testing.T) {
		// A broken package.json must not shadow a later source for a
		// role it failed to supply.
		root := writeFixture(t, map[string]string{
			"package.json": `{ not json`,
			"Makefile":     "test:\n\tpytest\n",
		})
		cmds := mustCommands(t, root)
		cmd := getOrSkip(t, cmds, RoleTest)
		if cmd.Cmd != "make test" || cmd.Source != "Makefile" {
			t.Errorf("test command = %q (source %q), want make test from Makefile", cmd.Cmd, cmd.Source)
		}
	})
}

func TestRenderGolden(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"go.mod": "module x\n"})
		got := mustCommands(t, root).Render()
		want := "format gofmt -w {file} (go.mod)\n" +
			"lint   go vet {dir} (go.mod)\n" +
			"test   go test ./... (go.mod)\n" +
			"build  go build ./... (go.mod)"
		if got != want {
			t.Errorf("render = %q\nwant %q", got, want)
		}
	})
	t.Run("partial-shows-none-discovered", func(t *testing.T) {
		root := writeFixture(t, map[string]string{"pyproject.toml": "[tool.black]\n"})
		got := mustCommands(t, root).Render()
		for _, want := range []string{
			"format black {file} (pyproject.toml)",
			"lint   (none discovered)",
			"test   pytest (pyproject.toml)",
			"build  (none discovered)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("render missing %q:\n%s", want, got)
			}
		}
	})
}
