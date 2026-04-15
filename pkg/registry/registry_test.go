package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")

	r, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}

	projects := r.List()
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}
}

func TestRegisterAndList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	projectDir := t.TempDir()

	r, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}

	entry, err := r.Register(projectDir)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if entry.Path != projectDir {
		t.Fatalf("expected path %q, got %q", projectDir, entry.Path)
	}
	if entry.Name == "" {
		t.Fatal("expected non-empty name")
	}

	projects := r.List()
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
}

func TestRegisterIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	projectDir := t.TempDir()

	r, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}

	e1, _ := r.Register(projectDir)
	e2, _ := r.Register(projectDir)

	if e1.ID != e2.ID {
		t.Fatalf("re-registration changed ID: %q → %q", e1.ID, e2.ID)
	}
	if len(r.List()) != 1 {
		t.Fatalf("re-registration created duplicate")
	}
}

func TestUnregister(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	projectDir := t.TempDir()

	r, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}

	entry, _ := r.Register(projectDir)
	if err := r.Unregister(entry.ID); err != nil {
		t.Fatalf("Unregister failed: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatal("expected 0 projects after unregister")
	}
}

func TestFindByPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	projectDir := t.TempDir()

	r, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt failed: %v", err)
	}

	entry, _ := r.Register(projectDir)

	// Exact match
	found := r.FindByPath(projectDir)
	if found == nil {
		t.Fatal("FindByPath returned nil for exact match")
	}
	if found.ID != entry.ID {
		t.Fatalf("expected ID %q, got %q", entry.ID, found.ID)
	}

	// Subdirectory match
	subDir := filepath.Join(projectDir, "src", "pkg")
	found = r.FindByPath(subDir)
	if found == nil {
		t.Fatal("FindByPath returned nil for subdirectory")
	}
	if found.ID != entry.ID {
		t.Fatalf("subdirectory match: expected ID %q, got %q", entry.ID, found.ID)
	}

	// No match
	found = r.FindByPath("/nonexistent/path")
	if found != nil {
		t.Fatal("FindByPath should return nil for unregistered path")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	projectDir := t.TempDir()

	// Register in one instance
	r1, _ := OpenAt(path)
	r1.Register(projectDir)

	// Load in a new instance
	r2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("second OpenAt failed: %v", err)
	}
	projects := r2.List()
	if len(projects) != 1 {
		t.Fatalf("expected 1 project after reload, got %d", len(projects))
	}
	if projects[0].Path != projectDir {
		t.Fatalf("expected path %q after reload, got %q", projectDir, projects[0].Path)
	}
}

func TestUniqueSlug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")

	r, _ := OpenAt(path)

	// Create two temp dirs with the same base name
	parent1 := t.TempDir()
	parent2 := t.TempDir()
	proj1 := filepath.Join(parent1, "myproject")
	proj2 := filepath.Join(parent2, "myproject")
	os.MkdirAll(proj1, 0755)
	os.MkdirAll(proj2, 0755)

	e1, _ := r.Register(proj1)
	e2, _ := r.Register(proj2)

	if e1.ID == e2.ID {
		t.Fatalf("two projects with same base name got same slug: %q", e1.ID)
	}
}

func TestSlugFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Users/derek/projects/cortex", "cortex"},
		{"/Users/derek/projects/My Project", "my-project"},
		{"/Users/derek/projects/foo_bar", "foo-bar"},
		{"/Users/derek/projects/123", "123"},
	}
	for _, tt := range tests {
		got := slugFromPath(tt.path)
		if got != tt.want {
			t.Errorf("slugFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
