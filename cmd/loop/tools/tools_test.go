package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultStudyPasses(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want int
	}{
		{"file", file, 1},
		{"dir", dir, DirStudyPasses},
		{"missing", filepath.Join(dir, "nope"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defaultStudyPasses(c.path); got != c.want {
				t.Errorf("defaultStudyPasses(%s) = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

func TestSpillShellOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	out := []byte(strings.Repeat("log line\n", 100))
	p1, err := spillShellOutput("go test ./...", out)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	data, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != string(out) {
		t.Error("spill content differs from output")
	}
	if !strings.HasPrefix(filepath.ToSlash(p1), ".cortex/shell/go-") {
		t.Errorf("spill path %q, want .cortex/shell/go-<hash>.txt", p1)
	}
	// Content-addressed: same output → same path (no pile-up).
	p2, err := spillShellOutput("go test ./...", out)
	if err != nil {
		t.Fatalf("spill 2: %v", err)
	}
	if p1 != p2 {
		t.Errorf("same output spilled to different paths: %q vs %q", p1, p2)
	}
}

func TestConfinedPath(t *testing.T) {
	root := t.TempDir()
	ok := []struct{ in, wantRel string }{
		{"file.go", "file.go"},
		{"sub/dir", "sub/dir"},
		{"./a/b/../c", "a/c"},
	}
	for _, tt := range ok {
		got, err := confinedPath(root, tt.in)
		if err != nil {
			t.Errorf("confinedPath(%q) errored: %v", tt.in, err)
			continue
		}
		if want := filepath.Join(root, tt.wantRel); got != want {
			t.Errorf("confinedPath(%q) = %q, want %q", tt.in, got, want)
		}
	}

	bad := []string{
		"",                  // empty
		".",                 // the root itself
		"..",                // escape up
		"../sibling",        // escape up
		"sub/../../escape",  // traversal escape
		"/etc/passwd",       // absolute outside
		".git",              // protected
		".git/config",       // protected subtree
		".cortex",           // protected
		".cortex/journal/x", // protected subtree
	}
	for _, in := range bad {
		if _, err := confinedPath(root, in); err == nil {
			t.Errorf("confinedPath(%q) should have been refused", in)
		}
	}
}

func TestConfinedPathSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// "link/x" is lexically in-root but its real parent is outside.
	if _, err := confinedPath(root, "link/x"); err == nil {
		t.Error("expected symlink-escape refusal for link/x")
	}
}
