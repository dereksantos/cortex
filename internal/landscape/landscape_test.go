package landscape

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanHarnesses(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
			t.Fatalf("failed to plant fixture: %v", err)
		}
		found, err := ScanHarnesses(root, Caps{})
		if err != nil {
			t.Fatalf("ScanHarnesses returned error: %v", err)
		}
		if !containsTool(found, "claude", filepath.Join(root, ".claude")) {
			t.Errorf("ScanHarnesses(%q) = %+v, want a claude entry", root, found)
		}
	})

	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		found, err := ScanHarnesses(root, Caps{})
		if err != nil {
			t.Fatalf("ScanHarnesses returned error: %v", err)
		}
		if len(found) != 0 {
			t.Errorf("ScanHarnesses(%q) = %+v, want none", root, found)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		root := t.TempDir()
		// A broken symlink where a harness dir would live must be
		// treated as absent, never as a crash.
		if err := os.Symlink(filepath.Join(root, "nonexistent-target"), filepath.Join(root, ".claude")); err != nil {
			t.Fatalf("failed to plant broken-symlink fixture: %v", err)
		}
		found, err := ScanHarnesses(root, Caps{})
		if err != nil {
			t.Fatalf("ScanHarnesses returned error on broken symlink: %v", err)
		}
		if len(found) != 0 {
			t.Errorf("ScanHarnesses(%q) = %+v, want none (broken symlink treated as absent)", root, found)
		}
	})
}

func TestScanRuntimes(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".ollama"), 0o755); err != nil {
			t.Fatalf("failed to plant fixture: %v", err)
		}
		found, err := ScanRuntimes(root, Caps{})
		if err != nil {
			t.Fatalf("ScanRuntimes returned error: %v", err)
		}
		if !containsRuntime(found, "ollama", filepath.Join(root, ".ollama")) {
			t.Errorf("ScanRuntimes(%q) = %+v, want an ollama entry", root, found)
		}
	})

	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		found, err := ScanRuntimes(root, Caps{})
		if err != nil {
			t.Fatalf("ScanRuntimes returned error: %v", err)
		}
		if len(found) != 0 {
			t.Errorf("ScanRuntimes(%q) = %+v, want none", root, found)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "nonexistent-target"), filepath.Join(root, ".ollama")); err != nil {
			t.Fatalf("failed to plant broken-symlink fixture: %v", err)
		}
		found, err := ScanRuntimes(root, Caps{})
		if err != nil {
			t.Fatalf("ScanRuntimes returned error on broken symlink: %v", err)
		}
		if len(found) != 0 {
			t.Errorf("ScanRuntimes(%q) = %+v, want none (broken symlink treated as absent)", root, found)
		}
	})
}

func TestScanProjects(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		root := t.TempDir()
		proj := filepath.Join(root, "proj1")
		mustMkdirAll(t, filepath.Join(proj, ".git"))
		mustWriteFile(t, filepath.Join(proj, "AGENTS.md"), "sentinel")

		found, err := ScanProjects(root, Caps{})
		if err != nil {
			t.Fatalf("ScanProjects returned error: %v", err)
		}
		p := findProject(found, proj)
		if p == nil {
			t.Fatalf("ScanProjects(%q) = %+v, want an entry for %s", root, found, proj)
		}
		if !containsString(p.Markers, "AGENTS.md") {
			t.Errorf("project %s markers = %v, want AGENTS.md", proj, p.Markers)
		}
	})

	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		proj := filepath.Join(root, "proj2")
		mustMkdirAll(t, proj) // no .git, no markers
		found, err := ScanProjects(root, Caps{})
		if err != nil {
			t.Fatalf("ScanProjects returned error: %v", err)
		}
		if findProject(found, proj) != nil {
			t.Errorf("ScanProjects(%q) = %+v, want no entry for %s (no .git)", root, found, proj)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		root := t.TempDir()
		proj := filepath.Join(root, "proj3")
		mustMkdirAll(t, proj)
		mustWriteFile(t, filepath.Join(proj, "CLAUDE.md"), "sentinel")
		// .git is a broken symlink (simulating a corrupt submodule
		// pointer) — must not crash the walk and must not count as a
		// detected repo.
		if err := os.Symlink(filepath.Join(proj, "nonexistent-target"), filepath.Join(proj, ".git")); err != nil {
			t.Fatalf("failed to plant broken-symlink fixture: %v", err)
		}
		found, err := ScanProjects(root, Caps{})
		if err != nil {
			t.Fatalf("ScanProjects returned error on broken .git symlink: %v", err)
		}
		if findProject(found, proj) != nil {
			t.Errorf("ScanProjects(%q) = %+v, want no entry for %s (broken .git symlink)", root, found, proj)
		}
	})
}

func TestScanComposesAllFamilies(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatalf("failed to plant harness fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".ollama"), 0o755); err != nil {
		t.Fatalf("failed to plant runtime fixture: %v", err)
	}
	proj := filepath.Join(root, "proj1")
	mustMkdirAll(t, filepath.Join(proj, ".git"))
	mustWriteFile(t, filepath.Join(proj, "CLAUDE.md"), "sentinel")

	result, err := Scan(root, Caps{})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if !containsTool(result.Tools, "claude", filepath.Join(root, ".claude")) {
		t.Errorf("Scan(%q).Tools = %+v, want a claude entry", root, result.Tools)
	}
	if !containsRuntime(result.Runtimes, "ollama", filepath.Join(root, ".ollama")) {
		t.Errorf("Scan(%q).Runtimes = %+v, want an ollama entry", root, result.Runtimes)
	}
	if findProject(result.Projects, proj) == nil {
		t.Errorf("Scan(%q).Projects = %+v, want an entry for %s", root, result.Projects, proj)
	}
}

// --- fixture + assertion helpers ---

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("failed to mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func containsTool(tools []Tool, name, path string) bool {
	for _, tool := range tools {
		if tool.Name == name && tool.Path == path {
			return true
		}
	}
	return false
}

func containsRuntime(runtimes []Runtime, name, path string) bool {
	for _, r := range runtimes {
		if r.Name == name && r.Path == path {
			return true
		}
	}
	return false
}

func containsString(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

func findProject(projects []Project, path string) *Project {
	for i := range projects {
		if projects[i].Path == path {
			return &projects[i]
		}
	}
	return nil
}
