package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspace_test.go proves the M3.1 DoD: the same fixture repo resolved via
// the CWD-implicit path (WorkspaceFromCWD, which reuses the existing
// findUp(".cortex") upward search) and via an explicit root
// (NewWorkspace(root), no search) yield IDENTICAL contextDir, instructions,
// and confinement verdicts.

// newFixtureRepo builds a temp directory with a .cortex dir (so findUp finds
// it), an AGENTS.md with known content, a nested subdirectory (to exercise
// the upward search), and a plain file inside the root for confinement
// checks.
func newFixtureRepo(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
		t.Fatalf("mkdir .cortex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Keep it small.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir sub/pkg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "pkg", "file.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("write sub/pkg/file.go: %v", err)
	}
	return root
}

// resolvedPath EvalSymlinks a path so comparisons are immune to macOS's
// /tmp → /private/tmp-style aliasing (t.TempDir() and a post-chdir
// os.Getwd() are not guaranteed to agree on which spelling they hand back,
// even though both name the same directory).
func resolvedPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

func TestWorkspaceFromCWDMatchesExplicitRootContextDir(t *testing.T) {
	root := newFixtureRepo(t)
	// Chdir into a nested subdirectory so WorkspaceFromCWD must actually walk
	// upward to find .cortex — the case the free contextDir()/findUp() were
	// built for.
	t.Chdir(filepath.Join(root, "sub", "pkg"))

	implicit := WorkspaceFromCWD()
	explicit, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	if got, want := resolvedPath(t, implicit.Root), resolvedPath(t, explicit.Root); got != want {
		t.Errorf("Root mismatch: implicit=%q explicit=%q", got, want)
	}
	implicitCtx, explicitCtx := resolvedPath(t, implicit.ContextDir()), resolvedPath(t, explicit.ContextDir())
	if implicitCtx != explicitCtx {
		t.Errorf("ContextDir mismatch: implicit=%q explicit=%q", implicitCtx, explicitCtx)
	}
	// SessionsDir() (.cortex/sessions) isn't created by the fixture — compare
	// the "sessions" join against the already-resolved ContextDir rather than
	// EvalSymlinks-ing a path that doesn't exist on disk yet.
	if got, want := filepath.Join(implicitCtx, "sessions"), filepath.Join(explicitCtx, "sessions"); got != want {
		t.Errorf("SessionsDir mismatch: implicit=%q explicit=%q", got, want)
	}
	if got, want := implicit.SessionsDir(), filepath.Join(implicit.ContextDir(), "sessions"); got != want {
		t.Errorf("implicit.SessionsDir() = %q, want %q", got, want)
	}
}

func TestWorkspaceFromCWDMatchesExplicitRootInstructions(t *testing.T) {
	root := newFixtureRepo(t)
	t.Chdir(filepath.Join(root, "sub", "pkg"))

	implicit := WorkspaceFromCWD()
	explicit, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	implicitPath, implicitInst := implicit.Instructions()
	explicitPath, explicitInst := explicit.Instructions()
	if implicitInst == "" {
		t.Fatal("implicit Instructions() is empty, want the fixture AGENTS.md content")
	}
	if implicitInst != explicitInst {
		t.Errorf("Instructions mismatch:\nimplicit=%q\nexplicit=%q", implicitInst, explicitInst)
	}
	if resolvedPath(t, implicitPath) != resolvedPath(t, explicitPath) {
		t.Errorf("Instructions path mismatch:\nimplicit=%q\nexplicit=%q", implicitPath, explicitPath)
	}
}

// TestResolveInstructionFileMatchesWorkspaceRoot pins the per-root contract
// both instruction legs share: an explicit NewWorkspace(root) and the CWD
// chain (the working directory itself, as a root) resolve the SAME file —
// so Workspace.Instructions() (--project, serve) and projectInstructions()
// (CWD-implicit) stay in agreement for a repo where the file sits in the
// working directory (the fresh-workspace case) or the explicit root.
func TestResolveInstructionFileMatchesWorkspaceRoot(t *testing.T) {
	root := newFixtureRepo(t) // AGENTS.md at the root, .cortex, nested sub/pkg
	explicit, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// From the root directory itself (as a CWD-derived root would be):
	t.Chdir(root)
	if cwdRoot, err := os.Getwd(); err == nil {
		if got, want := resolveInstructionFile(cwdRoot), resolveInstructionFile(explicit.Root); resolvedPath(t, got) != resolvedPath(t, want) {
			t.Errorf("instruction file resolved differently by CWD root vs explicit root: %q vs %q", got, want)
		}
	}

	// From a nested subdirectory: WorkspaceFromCWD anchors at the fixture
	// root (findUp(".cortex")), so the per-root resolver resolves the
	// fixture's file from the nested CWD too — the same file the explicit
	// root resolves.
	t.Chdir(filepath.Join(root, "sub", "pkg"))
	if got, want := resolveInstructionFile(WorkspaceFromCWD().Root), resolveInstructionFile(explicit.Root); resolvedPath(t, got) != resolvedPath(t, want) {
		t.Errorf("nested CWD: WorkspaceFromCWD root vs explicit root resolve differently: %q vs %q", got, want)
	}
	if filepath.Base(resolveInstructionFile(explicit.Root)) != "AGENTS.md" {
		t.Errorf("explicit root should resolve the fixture AGENTS.md")
	}
}

// TestWorkspaceInstructionsFixtureDirs is the table-driven core of #147's
// acceptance over the WORKSPACE leg: for a fixture root carrying a given mix
// of candidate files, Workspace.Instructions() returns the FIRST
// agentInstructionFiles entry present (AGENTS.md, then CLAUDE.md, then
// .github/copilot-instructions.md) — trimmed, capped, never concatenated —
// and names the file it resolved. The oversized case also pins the dynamic
// truncation marker (it names the file it came from).
func TestWorkspaceInstructionsFixtureDirs(t *testing.T) {
	oversized := strings.Repeat("x", maxInstructionBytes+100)
	tests := []struct {
		name     string
		files    map[string]string // rel path under root -> content
		wantPath string            // "" = no instruction file at all
		wantBody string            // "" = no instructions section at all
	}{
		{
			name:     "AGENTS.md only",
			files:    map[string]string{"AGENTS.md": "agents body\n"},
			wantPath: "AGENTS.md",
			wantBody: "agents body",
		},
		{
			name:     "CLAUDE.md only",
			files:    map[string]string{"CLAUDE.md": "claude body\n"},
			wantPath: "CLAUDE.md",
			wantBody: "claude body",
		},
		{
			name: "both — AGENTS.md wins, CLAUDE.md is not concatenated",
			files: map[string]string{
				"AGENTS.md": "agents body\n",
				"CLAUDE.md": "claude body\n",
			},
			wantPath: "AGENTS.md",
			wantBody: "agents body",
		},
		{
			name:     "copilot-instructions.md only",
			files:    map[string]string{filepath.Join(".github", "copilot-instructions.md"): "copilot body\n"},
			wantPath: filepath.Join(".github", "copilot-instructions.md"),
			wantBody: "copilot body",
		},
		{
			name:  "neither",
			files: map[string]string{"README.md": "readme\n"},
		},
		{
			name:     "oversized file is capped",
			files:    map[string]string{"CLAUDE.md": oversized},
			wantPath: "CLAUDE.md",
			wantBody: strings.Repeat("x", maxInstructionBytes) + "\n...[CLAUDE.md truncated]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.files {
				p := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", rel, err)
				}
			}
			ws, err := NewWorkspace(root)
			if err != nil {
				t.Fatalf("NewWorkspace: %v", err)
			}
			path, got := ws.Instructions()
			if want := tc.wantBody; got != want {
				if len(got) > 120 {
					got = got[:120] + "…"
				}
				if len(want) > 120 {
					want = want[:120] + "…"
				}
				t.Fatalf("Instructions() = %q, want %q", got, want)
			}
			// The resolved path is the expected candidate file (or "" when none
			// exists) — the per-root half of the resolution the body above
			// pins through the readInstructions trim+cap.
			wantPath := ""
			if tc.wantPath != "" {
				wantPath = filepath.Join(root, tc.wantPath)
			}
			if path != wantPath {
				t.Errorf("Instructions() path = %q, want %q", path, wantPath)
			}
		})
	}
}

// TestProjectInstructionsEquivalence pins the CWD-implicit leg
// (projectInstructions, session_core.go's CortexArgs.Request) against the
// explicit Workspace.Instructions() leg for the same fixture: a fresh
// workspace (file in the CWD itself) and a .cortex-anchored repo (file at
// the root, CWD nested) must both load the identical body — the contract
// M3.1 proved for AGENTS.md, now holding across the whole
// agentInstructionFiles list.
func TestProjectInstructionsEquivalence(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string // under the root
		wantBody string
	}{
		{"fresh workspace, AGENTS.md in CWD", map[string]string{"AGENTS.md": "agents body\n"}, "agents body"},
		{"fresh workspace, CLAUDE.md in CWD", map[string]string{"CLAUDE.md": "claude body\n"}, "claude body"},
		{"fresh workspace, both — AGENTS.md wins", map[string]string{"AGENTS.md": "agents body\n", "CLAUDE.md": "claude body\n"}, "agents body"},
		{"fresh workspace, neither", map[string]string{"README.md": "readme\n"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.files {
				if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", rel, err)
				}
			}
			t.Chdir(root)
			ws, err := NewWorkspace(root)
			if err != nil {
				t.Fatalf("NewWorkspace: %v", err)
			}
			wsPath, got := ws.Instructions()
			if want := tc.wantBody; got != want {
				t.Errorf("Workspace.Instructions() = %q, want %q", got, want)
			}
			freePath, free := projectInstructions()
			if free != tc.wantBody {
				t.Errorf("projectInstructions() = %q, want %q", free, tc.wantBody)
			}
			// #147: both legs resolve the SAME file (path, not just body).
			if wsPath != "" && resolvedPath(t, wsPath) != resolvedPath(t, freePath) {
				t.Errorf("leg paths diverge: workspace=%q free=%q", wsPath, freePath)
			}
		})
	}

	t.Run(".cortex-anchored repo, file at the root, CWD nested", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".cortex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("root claude\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(nested)

		ws := WorkspaceFromCWD()
		if ws.Root == "" || resolvedPath(t, ws.Root) != resolvedPath(t, root) {
			t.Fatalf("WorkspaceFromCWD root = %q, want the fixture root (anchored by .cortex)", ws.Root)
		}
		wsPath, got := ws.Instructions()
		if want := "root claude"; got != want {
			t.Errorf("Workspace.Instructions() = %q, want %q", got, want)
		}
		freePath, free := projectInstructions()
		if free != "root claude" {
			t.Errorf("projectInstructions() = %q, want %q (must resolve the ancestor root's file from the nested CWD)", free, "root claude")
		}
		// #147: both legs resolve the SAME file.
		if wsPath != "" && resolvedPath(t, wsPath) != resolvedPath(t, freePath) {
			t.Errorf("leg paths diverge: workspace=%q free=%q", wsPath, freePath)
		}
	})
}

func TestWorkspaceFromCWDMatchesExplicitRootConfinement(t *testing.T) {
	root := newFixtureRepo(t)
	t.Chdir(filepath.Join(root, "sub", "pkg"))

	implicit := WorkspaceFromCWD()
	explicit, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	allowed := ToolCall{Function: FunctionCall{Name: "read_file", Arguments: `{"path":"sub/pkg/file.go"}`}}
	if _, err := implicit.ConfinePath(allowed); err != nil {
		t.Errorf("implicit ConfinePath rejected an in-root path: %v", err)
	}
	if _, err := explicit.ConfinePath(allowed); err != nil {
		t.Errorf("explicit ConfinePath rejected an in-root path: %v", err)
	}

	escape := ToolCall{Function: FunctionCall{Name: "read_file", Arguments: `{"path":"../../../etc/passwd"}`}}
	if _, err := implicit.ConfinePath(escape); err == nil {
		t.Error("implicit ConfinePath allowed an escape attempt")
	}
	if _, err := explicit.ConfinePath(escape); err == nil {
		t.Error("explicit ConfinePath allowed an escape attempt")
	}
}

// TestNewWorkspaceConfinePathRejectsEscapes is M3.2's table: escape attempts
// against a NON-CWD root (NewWorkspace(root), the leg M3.5's --project will
// use) covering absolute paths, ..-prefixed relative paths, deeper
// traversal, and — the real gap the prior iteration's Next Up flagged —
// symlinks planted inside the root that resolve to a location outside it.
// ConfinePath's Abs+Clean+Rel guard is purely lexical and does not follow
// symlinks, so a symlink escape must be caught by resolving the real
// filesystem path before the containment check.
func TestNewWorkspaceConfinePathRejectsEscapes(t *testing.T) {
	root := newFixtureRepo(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"absolute path", "/etc/passwd"},
		{"parent-relative", "../secret"},
		{"double parent-relative", "../../secret"},
		{"deep traversal below root", "sub/pkg/../../../etc/passwd"},
		{"deep traversal collapses to root parent", "sub/../../.."},
		{"symlink escape to existing file", "escape/secret.txt"},
		{"symlink escape to nonexistent file", "escape/does-not-exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := ToolCall{Function: FunctionCall{Name: "read_file", Arguments: `{"path":` + jsonQuote(tc.path) + `}`}}
			if _, err := ws.ConfinePath(call); err == nil {
				t.Errorf("ConfinePath(%q) against non-CWD root should be rejected as an escape", tc.path)
			}
		})
	}
}

// jsonQuote is a tiny helper so the table above can embed arbitrary path
// strings (some containing backslash-free but quote-sensitive characters on
// Windows-style separators is not a concern here) into a literal JSON args
// string without importing encoding/json just for this.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestNewWorkspaceResolvesRelativeRootToAbsolute(t *testing.T) {
	root := newFixtureRepo(t)
	t.Chdir(root)

	ws, err := NewWorkspace(".")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if !filepath.IsAbs(ws.Root) {
		t.Errorf("NewWorkspace(\".\").Root = %q, want an absolute path", ws.Root)
	}
	if got, want := resolvedPath(t, ws.Root), resolvedPath(t, root); got != want {
		t.Errorf("Root = %q, want %q", got, want)
	}
}

func TestWorkspaceFromCWDFallsBackToWorkingDirWhenNoCortexDirFound(t *testing.T) {
	// A fresh directory tree with no .cortex anywhere up the chain: falls back
	// to the working directory itself, matching contextDir()'s existing
	// ".cortex" (relative-to-CWD) fallback semantics.
	fresh := t.TempDir()
	nested := filepath.Join(fresh, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(nested)

	ws := WorkspaceFromCWD()
	if got, want := resolvedPath(t, ws.Root), resolvedPath(t, nested); got != want {
		t.Errorf("Root = %q, want the working directory %q", got, want)
	}
}
