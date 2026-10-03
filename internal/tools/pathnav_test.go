package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPathNotFoundError is the table-driven acceptance test for issue #142
// step 3: read_file, grep, and outline must not dead-end on a missing path
// with a bare "no such file or directory". Each case exercises a distinct
// orientation affordance of the shared pathNotFoundError:
//
//   - it points at outline/grep for orientation (every case),
//   - it states the actual workspace root when the given path is ABSOLUTE or
//     OUT-OF-WORKSPACE (a relative path inside the workdir does NOT repeat the
//     root — that would be noise),
//   - it offers nearby existing candidates (same-dir same-extension, or the
//     walk-up same-basename form when the parent doesn't exist),
//
// across all three tools so the shared helper is pinned at every seam that
// consumes it.
func TestPathNotFoundError(t *testing.T) {
	wd := t.TempDir() // the workspace root
	t.Chdir(wd)

	// Fixtures in the workdir:
	//   real.go           — a real file (nearby candidate for "missing.go")
	//   real.txt          — a real file (nearby candidate for "missing.txt")
	//   dir/              — a real subdirectory (so "dir/missing.go"'s parent exists)
	//   dir/sibling.go    — a real file in dir (nearby candidate for "dir/missing.go")
	//   sub/              — a real subdirectory (for the walk-up basename form)
	//   sub/other.go      — a real file in sub (nearby candidate for "sub/missing.go")
	if err := os.MkdirAll(filepath.Join(wd, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"real.go", "real.txt", "dir/sibling.go", "sub/other.go"} {
		if err := os.WriteFile(filepath.Join(wd, f), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	deps := wdDeps{wd: wd}
	readFileCall := func(p string) ToolCall {
		return callArgs(t, FunctionReadFile, map[string]any{"path": p})
	}
	grepCallT := func(p string) ToolCall {
		return callArgs(t, FunctionGrep, map[string]any{"pattern": "x", "path": p})
	}
	outlineCall := func(p string) ToolCall {
		return callArgs(t, FunctionOutline, map[string]any{"path": p})
	}

	tests := []struct {
		name    string
		tool    string // read | grep | outline
		args    ToolCall
		wantErr bool
		wantIn  []string // substrings that must appear in the error
		notIn   []string // substrings that must NOT appear in the error
	}{
		{
			name:    "read_file: relative missing file offers same-dir candidates, no root repeat",
			tool:    "read",
			args:    readFileCall("missing.go"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"outline",
				"grep",
				"real.go", // nearby candidate
			},
			// A relative path inside the workdir must NOT repeat the root —
			// the model already knows it's relative; the root is noise.
			notIn: []string{"workspace root is"},
		},
		{
			name:    "read_file: absolute missing file states the workspace root",
			tool:    "read",
			args:    readFileCall(filepath.Join(wd, "missing_abs.go")),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"workspace root is",
				wd,
				"real.go", // candidate (parent = wd exists)
			},
		},
		{
			name:    "read_file: out-of-workspace absolute path states the root",
			tool:    "read",
			args:    readFileCall("/no/such/root/missing.go"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"workspace root is",
				wd,
			},
		},
		{
			name:    "grep: missing path offers candidates and points at outline",
			tool:    "grep",
			args:    grepCallT("missing_dir"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"outline",
				"real.go", // nearby candidate in wd
			},
		},
		{
			name:    "grep: absolute missing path states the root",
			tool:    "grep",
			args:    grepCallT(filepath.Join(wd, "missing_abs_dir")),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"workspace root is",
				wd,
			},
		},
		{
			name:    "outline: missing path points at outline/grep and offers candidates",
			tool:    "outline",
			args:    outlineCall("missing.go"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"outline",
				"grep",
				"real.go",
			},
			notIn: []string{"workspace root is"},
		},
		{
			name:    "read_file: missing file in a real subdir offers that subdir's siblings",
			tool:    "read",
			args:    readFileCall("dir/missing.go"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"sibling.go", // candidate in dir/
			},
			notIn: []string{"real.go"}, // wd's file is not "nearby" dir/missing.go
		},
		{
			name:    "read_file: missing file under a missing parent offers walk-up basename match",
			tool:    "read",
			args:    readFileCall("ghost/other.go"),
			wantErr: true,
			wantIn: []string{
				"does not exist",
				"other.go", // same basename found in the nearest existing dir (wd)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			switch tt.tool {
			case "read":
				_, err = readFile(tt.args, deps)
			case "grep":
				_, err = grep(context.Background(), tt.args, deps)
			case "outline":
				_, err = outlineTool(tt.args, deps)
			default:
				t.Fatalf("unknown tool %q", tt.tool)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			msg := err.Error()
			for _, sub := range tt.wantIn {
				if !strings.Contains(msg, sub) {
					t.Errorf("error missing %q; got:\n%q", sub, msg)
				}
			}
			for _, sub := range tt.notIn {
				if strings.Contains(msg, sub) {
					t.Errorf("error should not contain %q; got:\n%q", sub, msg)
				}
			}
		})
	}
}

// TestPathNotFoundErrorRangedRead covers the ranged-read seam: read_file with a
// start/end on a missing file must return the same oriented error (not the old
// "read ...: no such file" shape), so the display path and workdir are carried
// through readRange too.
func TestPathNotFoundErrorRangedRead(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	if err := os.WriteFile(filepath.Join(wd, "real.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := callArgs(t, FunctionReadFile, map[string]any{"path": "missing.go", "start": 1, "end": 5})
	if _, err := readFile(tc, wdDeps{wd: wd}); err == nil {
		t.Fatal("expected an error for a missing ranged-read file")
	} else if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "outline") {
		t.Errorf("ranged-read error should be the oriented path error, got:\n%q", err)
	}
}

// TestNearCandidatesPinsTheHelpers verifies the two candidate-finder helpers
// directly (the table test above covers them through the tools; this pins the
// exact selection rule so a future refactor can't silently change what counts
// as "nearby").
func TestNearCandidatesPinsTheHelpers(t *testing.T) {
	wd := t.TempDir()
	// Same-extension match: missing.txt → real.txt (not real.go).
	os.WriteFile(filepath.Join(wd, "real.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(wd, "real.go"), []byte("x"), 0o644)
	got := nearCandidates(filepath.Join(wd, "missing.txt"))
	if !strings.Contains(got, "real.txt") {
		t.Errorf("nearCandidates should offer the same-extension sibling real.txt, got:\n%s", got)
	}
	if strings.Contains(got, "real.go") {
		t.Errorf("nearCandidates must not offer a different extension (real.go), got:\n%s", got)
	}
	// Walk-up basename: parent "ghost" missing → same basename in wd.
	got = parentCandidates(filepath.Join(wd, "ghost/real.txt"))
	if !strings.Contains(got, "real.txt") {
		t.Errorf("parentCandidates should offer the same-basename file in the nearest existing dir, got:\n%s", got)
	}
	// No candidates when nothing is nearby.
	if got := nearCandidates(filepath.Join(wd, "missing.zzz")); got != "" {
		t.Errorf("nearCandidates with no match should be empty, got:\n%s", got)
	}
}

// TestOutlineMissingPathIsOriented pins that outline.Render's raw missing-path
// error is intercepted by outlineTool and replaced with the oriented message
// (regression: without the intercept, the model would see the bare os error).
func TestOutlineMissingPathIsOriented(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	os.WriteFile(filepath.Join(wd, "real.go"), []byte("x"), 0o644)
	tc := callArgs(t, FunctionOutline, map[string]any{"path": "missing.go"})
	if _, err := outlineTool(tc, wdDeps{wd: wd}); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "grep") {
		t.Errorf("outline not-found should be the oriented message, got:\n%q", err)
	}
}
