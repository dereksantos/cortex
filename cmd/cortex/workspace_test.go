package main

import (
	"os"
	"path/filepath"
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

	implicitInst := implicit.Instructions()
	explicitInst := explicit.Instructions()
	if implicitInst == "" {
		t.Fatal("implicit Instructions() is empty, want the fixture AGENTS.md content")
	}
	if implicitInst != explicitInst {
		t.Errorf("Instructions mismatch:\nimplicit=%q\nexplicit=%q", implicitInst, explicitInst)
	}
	// Also proves Workspace.Instructions() agrees with the pre-existing
	// CWD-implicit projectInstructions() free function (still used by
	// CortexArgs.Request() until --project wiring lands in M3.5).
	if free := projectInstructions(); free != implicitInst {
		t.Errorf("Workspace.Instructions() diverges from projectInstructions(): workspace=%q free=%q", implicitInst, free)
	}
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
