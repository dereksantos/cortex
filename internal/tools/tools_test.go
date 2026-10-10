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

// TestReadFileDirectoryReturnsListing covers issue #142 step 2: read_file on a
// DIRECTORY must not dead-end with a bare "is a directory" os.ReadFile error.
// Instead it returns a bounded, ls-like listing (directories marked with a
// trailing "/") plus a note that read_file targets a FILE and that
// outline(path) goes deeper. The cases pin each behavior the step calls for.
func TestReadFileDirectoryReturnsListing(t *testing.T) {
	dir := t.TempDir()
	// Fixture: two subdirectories and two files.
	if err := os.Mkdir(filepath.Join(dir, "subdir_a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir_b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		target    string // path read_file is asked for
		start     int    // ranged read: 0 = no start arg
		wantErr   bool
		wantIn    []string // substrings that must appear in the output
		notIn     []string // substrings that must NOT appear
		wantNoErr bool
	}{
		{
			name:      "directory yields a listing, not the is-a-directory error",
			target:    dir,
			wantNoErr: true,
			wantIn: []string{
				"alpha.go",
				"beta.txt",
				"subdir_a/",
				"subdir_b/",
				"is a directory, not a file",
				"read_file reads a FILE",
				"outline(",
			},
			// The old dead-end error must be gone: a bare "is a directory"
			// os.ReadFile error is the thing this step removes.
			notIn: []string{"read " + dir + ": " + "readdir"},
		},
		{
			name:      "ranged read on a directory yields the listing, not the is-a-directory error",
			target:    dir,
			start:     1,
			wantNoErr: true,
			wantIn: []string{
				"alpha.go",
				"is a directory, not a file",
				"outline(",
			},
			// The pre-fix path: readRange's os.ReadFile on a directory
			// returned the bare "read <dir>: is a directory" error.
			notIn: []string{"read " + dir + ":"},
		},
		{
			name:      "subdirectory also yields a listing",
			target:    filepath.Join(dir, "subdir_a"),
			wantNoErr: true,
			wantIn: []string{
				"is a directory, not a file",
				"outline(",
			},
		},
		{
			name:    "a regular file is still read, not listed",
			target:  filepath.Join(dir, "alpha.go"),
			wantErr: false,
			wantIn:  []string{"package main"},
			notIn:   []string{"is a directory, not a file"},
		},
		{
			name:    "a missing path still errors",
			target:  filepath.Join(dir, "does_not_exist.go"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := fmt.Sprintf(`{"path":%q`, tt.target)
			if tt.start > 0 {
				args += fmt.Sprintf(`,"start":%d`, tt.start)
			}
			args += "}"
			tc := ToolCall{Function: FunctionCall{Name: FunctionReadFile, Arguments: args}}
			out, err := readFile(tc, headlessDeps{})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, sub := range tt.wantIn {
				if !strings.Contains(out, sub) {
					t.Errorf("output missing %q; got:\n%s", sub, out)
				}
			}
			for _, sub := range tt.notIn {
				if strings.Contains(out, sub) {
					t.Errorf("output should not contain %q; got:\n%s", sub, out)
				}
			}
		})
	}
}

// TestReadFileDirectoryListingIsBounded covers the cap: a directory with more
// children than the listing cap returns only the first cap entries plus a
// "… +N more" elision, so a huge directory can't blow up the context.
func TestReadFileDirectoryListingIsBounded(t *testing.T) {
	dir := t.TempDir()
	const n = 250 // > cap (200)
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tc := ToolCall{Function: FunctionCall{Name: FunctionReadFile, Arguments: fmt.Sprintf(`{"path":%q}`, dir)}}
	out, err := readFile(tc, headlessDeps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The first child is listed…
	if !strings.Contains(out, "f000.txt") {
		t.Errorf("expected the first child in the listing; got:\n%s", out)
	}
	// …and the elision note reports how many more were cut.
	if !strings.Contains(out, "+50 more") {
		t.Errorf("expected a \"+50 more\" elision for %d children past the cap; got:\n%s", n-200, out)
	}
}

// TestReadFileTooLargeNonGoGetsSkeleton proves the too-large redirect is
// language-agnostic: a big Python file gets outline.Render's regex-tier
// skeleton (real "def "-headed sections), not the old Go-only gate's flat
// "use study(...) instead" error.
func TestReadFileTooLargeNonGoGetsSkeleton(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "big.py")
	var sb strings.Builder
	for i := 0; i < 1200; i++ {
		fmt.Fprintf(&sb, "def handler_%d(request):\n    return process(%d, request)\n\n", i, i)
	}
	if sb.Len() <= active.CurationBudgetTokens*4 {
		t.Fatalf("fixture too small to trip the curation gate: %d bytes", sb.Len())
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tc := ToolCall{Function: FunctionCall{Name: FunctionReadFile, Arguments: fmt.Sprintf(`{"path":%q}`, file)}}
	out, err := readFile(tc, headlessDeps{})
	if err != nil {
		t.Fatalf("expected a skeleton, not an error: %v", err)
	}
	if !strings.Contains(out, "too large to read whole") {
		t.Errorf("missing the too-large notice; got:\n%s", out)
	}
	if !strings.Contains(out, "def handler_0") {
		t.Errorf("expected the Python declaration regex tier to fire (a \"def handler_0\" entry); got:\n%s", out)
	}
}

// TestReadFileTooLargeNoStructureStillGetsSkeleton proves the positional-floor
// tier still hands back something for a too-large file with no recognizable
// declarations/headings/paragraphs at all — never a dead-end error.
func TestReadFileTooLargeNoStructureStillGetsSkeleton(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "big.log")
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&sb, "line of unstructured log content %d filler filler filler\n", i)
	}
	if sb.Len() <= active.CurationBudgetTokens*4 {
		t.Fatalf("fixture too small to trip the curation gate: %d bytes", sb.Len())
	}
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	tc := ToolCall{Function: FunctionCall{Name: FunctionReadFile, Arguments: fmt.Sprintf(`{"path":%q}`, file)}}
	out, err := readFile(tc, headlessDeps{})
	if err != nil {
		t.Fatalf("expected the positional-floor skeleton, not an error: %v", err)
	}
	if !strings.Contains(out, "lines 1-") {
		t.Errorf("expected a positional-floor entry (\"lines 1-N\"); got:\n%s", out)
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
