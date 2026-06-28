package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileRange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	// 10 lines: "L1".."L10".
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "L%d\n", i)
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(args string) (string, error) {
		tc := ToolCall{Function: FunctionCall{Name: FunctionReadFile, Arguments: args}}
		return readFile(tc, headlessDeps{})
	}

	// Explicit range returns exactly those lines with a header.
	out, err := read(fmt.Sprintf(`{"path":%q,"start":3,"end":5}`, file))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "@"+file+":3-5\n") {
		t.Errorf("missing range header; got:\n%s", out)
	}
	if !strings.Contains(out, "L3\nL4\nL5") || strings.Contains(out, "L2") || strings.Contains(out, "L6") {
		t.Errorf("range body wrong; got:\n%s", out)
	}

	// end past EOF clamps to the last line, not an error.
	out, err = read(fmt.Sprintf(`{"path":%q,"start":9,"end":100}`, file))
	if err != nil {
		t.Fatalf("clamp to EOF should succeed: %v", err)
	}
	if !strings.Contains(out, ":9-10\n") || !strings.Contains(out, "L10") {
		t.Errorf("expected lines 9-10; got:\n%s", out)
	}

	// start past EOF is an error.
	if _, err := read(fmt.Sprintf(`{"path":%q,"start":50}`, file)); err == nil {
		t.Error("start past EOF should error")
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
