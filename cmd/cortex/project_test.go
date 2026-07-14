package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/landscape"
	"github.com/dereksantos/cortex/internal/registry"
)

// TestAddProjectSavesToRegistry pins that addProject resolves the given
// root to an absolute path and upserts it into the registry under name.
func TestAddProjectSavesToRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	root := t.TempDir()
	if err := addProject(reg, "blog", root); err != nil {
		t.Fatalf("addProject: %v", err)
	}

	got, err := reg.Lookup("blog")
	if err != nil {
		t.Fatalf("Lookup(blog): %v", err)
	}
	if got.Root != root {
		t.Errorf("Lookup(blog).Root = %q, want %q", got.Root, root)
	}
}

// TestAddProjectResolvesRelativeRootToAbsolute pins that a relative root
// argument (as a user would type from a shell) is resolved against the
// current working directory before being persisted.
func TestAddProjectResolvesRelativeRootToAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	parent := t.TempDir()
	sub := filepath.Join(parent, "blog")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Chdir(parent)

	if err := addProject(reg, "blog", "blog"); err != nil {
		t.Fatalf("addProject: %v", err)
	}

	got, err := reg.Lookup("blog")
	if err != nil {
		t.Fatalf("Lookup(blog): %v", err)
	}
	if !filepath.IsAbs(got.Root) {
		t.Errorf("Lookup(blog).Root = %q, want an absolute path", got.Root)
	}
	if got.Root != sub {
		t.Errorf("Lookup(blog).Root = %q, want %q", got.Root, sub)
	}
}

// TestAddProjectRejectsEmptyArgs pins the usage error for missing
// name/root arguments — the CLI layer's own validation, distinct from
// anything internal/registry itself enforces.
func TestAddProjectRejectsEmptyArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	if err := addProject(reg, "", "/some/root"); !errors.Is(err, ErrProjectUsage) {
		t.Errorf("addProject(empty name) error = %v, want ErrProjectUsage", err)
	}
	if err := addProject(reg, "blog", ""); !errors.Is(err, ErrProjectUsage) {
		t.Errorf("addProject(empty root) error = %v, want ErrProjectUsage", err)
	}
}

// TestRemoveProjectRemovesFromRegistry pins the round-trip: add, then
// remove, then a lookup is gone.
func TestRemoveProjectRemovesFromRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	root := t.TempDir()
	if err := addProject(reg, "blog", root); err != nil {
		t.Fatalf("addProject: %v", err)
	}
	if err := removeProject(reg, "blog"); err != nil {
		t.Fatalf("removeProject: %v", err)
	}

	if _, err := reg.Lookup("blog"); !errors.Is(err, registry.ErrProjectNotFound) {
		t.Errorf("Lookup(blog) after removeProject error = %v, want ErrProjectNotFound", err)
	}
}

// TestRemoveProjectUnknownNamePropagatesTypedError pins that removeProject
// is a thin pass-through to Registry.Remove — the typed error surfaces
// unwrapped, not swallowed or replaced.
func TestRemoveProjectUnknownNamePropagatesTypedError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	if err := removeProject(reg, "does-not-exist"); !errors.Is(err, registry.ErrProjectNotFound) {
		t.Errorf("removeProject(does-not-exist) error = %v, want ErrProjectNotFound", err)
	}
}

// TestRenderProjectListGolden pins the exact text layout `cortex project
// list` prints — same golden-pinned-literal convention as
// renderScanReport/greetingPrompt.
func TestRenderProjectListGolden(t *testing.T) {
	got := renderProjectList([]registry.Project{
		{Name: "api", Root: "/home/derek/api"},
		{Name: "blog", Root: "/home/derek/blog"},
	})
	want := "Registered projects (2):\n" +
		"  - api -> /home/derek/api\n" +
		"  - blog -> /home/derek/blog\n"
	if got != want {
		t.Errorf("renderProjectList = %q, want %q", got, want)
	}
}

// TestRenderProjectListEmptyGolden pins the empty-registry message.
func TestRenderProjectListEmptyGolden(t *testing.T) {
	got := renderProjectList(nil)
	want := "No projects registered. Add one: cortex project add <name> <root>\n"
	if got != want {
		t.Errorf("renderProjectList(nil) = %q, want %q", got, want)
	}
}

// TestRegisterDiscoveredProjectsFeedsRegistry plants a fixture project
// tree, scans it via landscape.ScanProjects (the real M2.1 discovery
// path), and asserts registerDiscoveredProjects upserts one registry
// entry per discovered project, keyed by directory basename.
func TestRegisterDiscoveredProjectsFeedsRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	root := t.TempDir()
	projA := filepath.Join(root, "proj-a")
	mustMkdirAllScan(t, filepath.Join(projA, ".git"))
	mustWriteFileScan(t, filepath.Join(projA, "AGENTS.md"), "sentinel-a")

	found, _, err := landscape.ScanProjects(root, landscape.Caps{})
	if err != nil {
		t.Fatalf("ScanProjects: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("ScanProjects found %d projects, want 1", len(found))
	}

	if err := registerDiscoveredProjects(reg, found); err != nil {
		t.Fatalf("registerDiscoveredProjects: %v", err)
	}

	got, err := reg.Lookup("proj-a")
	if err != nil {
		t.Fatalf("Lookup(proj-a): %v", err)
	}
	if got.Root != projA {
		t.Errorf("Lookup(proj-a).Root = %q, want %q", got.Root, projA)
	}
}

// TestRegisterDiscoveredProjectsUpsertsOnRerun pins that running
// registration twice over the same discovered set does not duplicate
// entries (Registry.Save's documented update-in-place semantics).
func TestRegisterDiscoveredProjectsUpsertsOnRerun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	reg, err := registry.New()
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	projects := []landscape.Project{{Path: "/fixture/proj-a", Markers: []string{"AGENTS.md"}}}

	if err := registerDiscoveredProjects(reg, projects); err != nil {
		t.Fatalf("registerDiscoveredProjects (1st): %v", err)
	}
	if err := registerDiscoveredProjects(reg, projects); err != nil {
		t.Fatalf("registerDiscoveredProjects (2nd): %v", err)
	}

	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() after re-registering = %v, want 1 entry (upsert, not append)", list)
	}
}
