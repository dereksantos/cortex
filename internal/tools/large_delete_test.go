package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// large_delete_test.go — issue #141's tool-level complement to the turn-level
// testguard receipt (cmd/cortex testwatch): when a write_file or edit_file
// call removes most of an existing file's content, the tool's own returned
// string carries a named warning so a headless run — one where the terminal
// diff never appears — still shows the model what the call did.

// bodyLines renders n lines of file content (line i+1).
func bodyLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line " + largeDelItoa(i) + "\n")
	}
	return b.String()
}

func largeDelItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// largeDelSeedFile writes a fixture file.
func largeDelSeedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLargeDeletionWarningWrite is the write_file arm, end to end: the
// warning rides the tool's returned string only when a substantial shrinkage
// of an EXISTING file happens — the #127 incident shape is a 23-line test
// file overwritten with nothing.
func TestLargeDeletionWarningWrite(t *testing.T) {
	tests := []struct {
		name     string
		before   string
		after    string
		wantWarn bool
	}{
		{name: "new file", before: "", after: bodyLines(50), wantWarn: false},
		{name: "emptying a large file", before: bodyLines(23), after: "", wantWarn: true},
		{name: "shrink to a stub", before: bodyLines(30), after: "left\n", wantWarn: true},
		{name: "no change", before: bodyLines(23), after: bodyLines(23), wantWarn: false},
		{name: "grows", before: bodyLines(10), after: bodyLines(40), wantWarn: false},
		{name: "routine refactor (60% kept) stays under the threshold", before: bodyLines(30), after: bodyLines(18), wantWarn: false},
		{name: "small file emptying is below the floor", before: bodyLines(5), after: "", wantWarn: false},
		{name: "empty file rewrite is a no-op", before: "", after: "", wantWarn: false},
		{name: "just past the floor and threshold", before: bodyLines(20), after: "x\n", wantWarn: true},
		{name: "exactly 50% kept is not a warning", before: bodyLines(30), after: bodyLines(15), wantWarn: false},
		{name: "just under 50% kept warns", before: bodyLines(30), after: bodyLines(14), wantWarn: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/f.txt"
			if tc.before != "" {
				largeDelSeedFile(t, path, tc.before)
			}
			out, _, err := Execute(context.Background(), callArgs(t, FunctionWriteFile,
				map[string]any{"path": path, "content": tc.after}), headlessDeps{})
			if err != nil {
				t.Fatalf("write_file: %v", err)
			}
			if got := strings.Contains(out, "NOTE:"); got != tc.wantWarn {
				t.Errorf("write_file result = %q; note present = %v, want %v", out, got, tc.wantWarn)
			}
		})
	}
}

// TestLargeDeletionWarningWriteQuiet pins issue #141's core requirement: the
// large-deletion note must still fire in a QUIET/headless session (cortex
// turn, serve, CORTEX_LOOP_RENDER=0), where the terminal diff is never
// printed. quietDeps.Quiet() is true, so priorContent reports the before-side
// as not diffable — the warning must reach the model through the tool result
// regardless.
func TestLargeDeletionWarningWriteQuiet(t *testing.T) {
	tests := []struct {
		name     string
		before   string
		after    string
		wantWarn bool
	}{
		{name: "emptying a large file in a quiet session", before: bodyLines(23), after: "", wantWarn: true},
		{name: "shrink to a stub in a quiet session", before: bodyLines(30), after: "left\n", wantWarn: true},
		{name: "routine refactor stays under the threshold in a quiet session", before: bodyLines(30), after: bodyLines(18), wantWarn: false},
		{name: "new file in a quiet session", before: "", after: bodyLines(50), wantWarn: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/f.txt"
			if tc.before != "" {
				largeDelSeedFile(t, path, tc.before)
			}
			// Capture stdout to prove the quiet session stays silent on the
			// diff side while the warning still rides the tool result.
			out := captureStdout(t, func() {
				var werr error
				var res string
				res, _, werr = Execute(context.Background(), callArgs(t, FunctionWriteFile,
					map[string]any{"path": path, "content": tc.after}), quietDeps{})
				if werr != nil {
					t.Fatalf("write_file: %v", werr)
				}
				if got := strings.Contains(res, "NOTE:"); got != tc.wantWarn {
					t.Errorf("write_file result = %q; note present = %v, want %v", res, got, tc.wantWarn)
				}
			})
			// A quiet session prints nothing to the terminal — the diff and
			// action line are suppressed. The note is NOT a terminal
			// print; it lives in the returned string the model reads.
			if out != "" {
				t.Errorf("quiet session must print nothing to stdout, got %q", out)
			}
		})
	}
}

// TestLargeDeletionWarningEdit is the edit_file arm: a find/replace whose
// new_string deletes most of the file's lines (the "sed out a test" shape).
func TestLargeDeletionWarningEdit(t *testing.T) {
	tests := []struct {
		name       string
		before     string
		oldStr     string
		newStr     string
		replaceAll bool
		wantWarn   bool
	}{
		{
			name:       "deleting most lines via replace_all",
			before:     strings.Repeat("line x\n", 25),
			oldStr:     "line x\n",
			newStr:     "",
			replaceAll: true,
			wantWarn:   true,
		},
		{
			name:     "small targeted edit",
			before:   bodyLines(30),
			oldStr:   "line 2\n",
			newStr:   "line two\n",
			wantWarn: false,
		},
		{
			name:     "growing edit",
			before:   bodyLines(10),
			oldStr:   "line 10\n",
			newStr:   "line 10\nline 11\nline 12\nline 13\nline 14\n",
			wantWarn: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/f.txt"
			largeDelSeedFile(t, path, tc.before)
			args := map[string]any{"path": path, "old_string": tc.oldStr, "new_string": tc.newStr}
			if tc.replaceAll {
				args["replace_all"] = true
			}
			out, _, err := Execute(context.Background(), callArgs(t, FunctionEditFile, args), headlessDeps{})
			if err != nil {
				t.Fatalf("edit_file: %v", err)
			}
			if got := strings.Contains(out, "NOTE:"); got != tc.wantWarn {
				t.Errorf("edit_file result = %q; note present = %v, want %v", out, got, tc.wantWarn)
			}
		})
	}
}

// TestLargeDeletionWarningPure pins the helper's thresholds directly: the
// 20-line floor (below it no proportion is meaningful) and the 50% kept
// threshold ("most of a file" is the loss, not a routine refactor).
func TestLargeDeletionWarningPure(t *testing.T) {
	tests := []struct {
		beforeLines int
		afterLines  int
		want        bool
	}{
		{0, 0, false},
		{5, 0, false},
		{19, 0, false},
		{20, 0, true},
		{20, 4, true},
		{20, 10, false},
		{20, 9, true},
		{30, 15, false},
		{30, 14, true},
		{100, 90, false},
		{100, 50, false},
		{100, 49, true},
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			got := largeDeletionWarning(bodyLines(tc.beforeLines), bodyLines(tc.afterLines))
			if (got != "") != tc.want {
				t.Errorf("largeDeletionWarning(%d lines → %d lines) = %q; warn = %v, want %v",
					tc.beforeLines, tc.afterLines, got, got != "", tc.want)
			}
		})
	}
}
