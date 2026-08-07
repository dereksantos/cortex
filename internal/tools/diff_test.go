package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ansiRe matches the SGR codes Color emits. Every assertion here runs on
// ANSI-stripped output so the expected shapes stay readable — the color path
// itself is covered by TestRenderDiffColorPaths.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansiRe.ReplaceAllString(s, "") }

func stripAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strip(l)
	}
	return out
}

// lines joins numbered content into file bodies for the table below.
func lines(n int, f func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s\n", f(i))
	}
	return b.String()
}

func TestRenderDiff(t *testing.T) {
	tests := []struct {
		name        string
		before      string
		after       string
		opt         diffOptions
		wantLines   []string // exact, ANSI stripped, when non-nil
		wantContain []string // substring assertions otherwise
		wantAbsent  []string
	}{
		{
			name:   "one-line change shows markers, numbers and collapsed context",
			before: "a\nb\nc\nd\ne\nf\ng\nh\ni\n",
			after:  "a\nb\nc\nd\nE\nf\ng\nh\ni\n",
			opt:    diffOptions{Context: 1},
			wantLines: []string{
				"            @@ -4,3 +4,3 @@",
				"            4   d",
				"            5 - e",
				"            5 + E",
				"            6   f",
			},
			// Lines outside the context window are collapsed away.
			wantAbsent: []string{" a", " i"},
		},
		{
			name:   "new file renders as all additions",
			before: "",
			after:  "package main\n\nfunc main() {}\n",
			opt:    diffOptions{Context: 1},
			wantLines: []string{
				"            new file, 3 lines",
				"            @@ -0,0 +1,3 @@",
				"            1 + package main",
				"            2 + ",
				"            3 + func main() {}",
			},
		},
		{
			name:   "emptied file renders as all removals",
			before: "one\ntwo\n",
			after:  "",
			opt:    diffOptions{Context: 1},
			wantLines: []string{
				"            emptied, 2 lines removed",
				"            @@ -1,2 +0,0 @@",
				"            1 - one",
				"            2 - two",
			},
		},
		{
			name:      "identical content says so instead of drawing nothing",
			before:    "same\n",
			after:     "same\n",
			wantLines: []string{"            no change"},
		},
		{
			name:      "empty write is named rather than reported as no change",
			before:    "",
			after:     "",
			wantLines: []string{"            wrote an empty file"},
		},
		{
			name:        "pure insertion in the middle numbers the new lines",
			before:      "a\nb\n",
			after:       "a\nx\ny\nb\n",
			opt:         diffOptions{Context: 1},
			wantContain: []string{"2 + x", "3 + y", "@@ -1,2 +1,4 @@"},
		},
		{
			name:        "binary content is summarized, never dumped",
			before:      "text\n",
			after:       "bin\x00ary\n",
			wantContain: []string{"binary content"},
			wantAbsent:  []string{"+ bin"},
		},
		{
			name:        "tabs are expanded so the number column holds",
			before:      "func f() {\n\treturn 1\n}\n",
			after:       "func f() {\n\treturn 2\n}\n",
			opt:         diffOptions{Context: 0},
			wantContain: []string{"2 -     return 1", "2 +     return 2"},
		},
		{
			name:        "escape sequences in file content are neutered",
			before:      "plain\n",
			after:       "\x1b[31mred\x1b[0m\n",
			wantContain: []string{"+ ?[31mred?[0m"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripAll(renderDiff(tt.before, tt.after, tt.opt))
			if tt.wantLines != nil {
				if len(got) != len(tt.wantLines) {
					t.Fatalf("got %d lines, want %d:\ngot:\n%s\nwant:\n%s",
						len(got), len(tt.wantLines), strings.Join(got, "\n"), strings.Join(tt.wantLines, "\n"))
				}
				for i := range got {
					if got[i] != tt.wantLines[i] {
						t.Errorf("line %d:\n got %q\nwant %q", i, got[i], tt.wantLines[i])
					}
				}
			}
			joined := strings.Join(got, "\n")
			for _, want := range tt.wantContain {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q in:\n%s", want, joined)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Errorf("unexpected %q in:\n%s", absent, joined)
				}
			}
		})
	}
}

func TestRenderDiffElisionCap(t *testing.T) {
	t.Run("a large rewrite is capped and the remainder counted", func(t *testing.T) {
		before := lines(200, func(i int) string { return fmt.Sprintf("old line %d", i) })
		after := lines(200, func(i int) string { return fmt.Sprintf("new line %d", i) })

		got := stripAll(renderDiff(before, after, diffOptions{MaxLines: 10}))
		if len(got) != 11 { // 10 body rows + the elision note
			t.Fatalf("got %d lines, want 11:\n%s", len(got), strings.Join(got, "\n"))
		}
		last := got[len(got)-1]
		// 400 rows total (200 removed + 200 added), one hunk header, 10 kept.
		if want := "… 391 more lines (+200 -200 total)"; !strings.Contains(last, want) {
			t.Errorf("elision line = %q, want it to contain %q", last, want)
		}
	})

	t.Run("the cap never leaves a dangling hunk header", func(t *testing.T) {
		before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\n"
		after := "A\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nL\n"
		// Two hunks; a cap of 4 lands exactly on the second hunk's header.
		got := stripAll(renderDiff(before, after, diffOptions{Context: 1, MaxLines: 4}))
		for i, l := range got[:len(got)-1] {
			if strings.Contains(l, "@@") && i == len(got)-2 {
				t.Errorf("hunk header left dangling at the cap:\n%s", strings.Join(got, "\n"))
			}
		}
		if !strings.Contains(got[len(got)-1], "more lines") {
			t.Errorf("want an elision note, got:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("a too-large pair degrades to a line-count summary", func(t *testing.T) {
		big := strings.Repeat("x\n", diffMaxInputBytes)
		got := stripAll(renderDiff("small\n", big, diffOptions{}))
		if len(got) != 1 || !strings.Contains(got[0], "too large to diff") {
			t.Fatalf("want a one-line degradation, got:\n%s", strings.Join(got, "\n"))
		}
	})
}

func TestRenderDiffWidth(t *testing.T) {
	long := strings.Repeat("y", 200)
	t.Run("rows are clipped to the terminal width", func(t *testing.T) {
		got := stripAll(renderDiff("a\n", long+"\n", diffOptions{Width: 40}))
		for _, l := range got {
			if len([]rune(l)) > 40 {
				t.Errorf("line exceeds width 40 (%d): %q", len([]rune(l)), l)
			}
		}
	})
	t.Run("width 0 (no terminal) leaves lines whole", func(t *testing.T) {
		got := stripAll(renderDiff("a\n", long+"\n", diffOptions{Width: 0}))
		joined := strings.Join(got, "\n")
		if !strings.Contains(joined, long) {
			t.Errorf("piped output should not be clipped:\n%s", joined)
		}
	})
}

func TestRenderDiffColorPaths(t *testing.T) {
	t.Run("colored output wraps added and removed rows", func(t *testing.T) {
		defer restoreColor(t, false)()
		got := renderDiff("a\n", "b\n", diffOptions{})
		joined := strings.Join(got, "\n")
		if !strings.Contains(joined, Green) || !strings.Contains(joined, Red) {
			t.Errorf("want green + red in colored output, got %q", joined)
		}
	})

	t.Run("NO_COLOR output carries no escape codes", func(t *testing.T) {
		defer restoreColor(t, true)()
		got := renderDiff("a\n", "b\n", diffOptions{})
		joined := strings.Join(got, "\n")
		if strings.Contains(joined, "\x1b[") {
			t.Errorf("NO_COLOR output must be plain, got %q", joined)
		}
		if !strings.Contains(joined, "- a") || !strings.Contains(joined, "+ b") {
			t.Errorf("markers still carry the meaning without color, got %q", joined)
		}
	})
}

// restoreColor pins colorDisabled for one test and restores it after.
func restoreColor(t *testing.T, disabled bool) func() {
	t.Helper()
	prev := colorDisabled
	colorDisabled = disabled
	return func() { colorDisabled = prev }
}

// --- the tools that print diffs -----------------------------------------

// quietDeps is headlessDeps with emission suppressed — the headless `cortex
// turn` / serve / discord path.
type quietDeps struct{ headlessDeps }

func (quietDeps) Quiet() bool { return true }

// captureStdout runs f with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f()
	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

func editCall(t *testing.T, fn string, args map[string]any) ToolCall {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return ToolCall{Function: FunctionCall{Name: fn, Arguments: string(b)}}
}

func TestEditFilePrintsDiff(t *testing.T) {
	t.Run("edit_file shows the changed lines under its action line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.go")
		if err := os.WriteFile(path, []byte("package main\n\nfunc f() int {\n\treturn 1\n}\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), editCall(t, FunctionEditFile, map[string]any{
				"path": path, "old_string": "return 1", "new_string": "return 2",
			}), nil); err != nil {
				t.Fatalf("edit_file: %v", err)
			}
		})
		got := strip(out)
		for _, want := range []string{"tool: edit_file(", "4 -     return 1", "4 +     return 2"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("write_file to a new path renders as a create", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.txt")
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), editCall(t, FunctionWriteFile, map[string]any{
				"path": path, "content": "hello\nworld\n",
			}), nil); err != nil {
				t.Fatalf("write_file: %v", err)
			}
		})
		got := strip(out)
		for _, want := range []string{"new file, 2 lines", "1 + hello", "2 + world"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("write_file over an existing file diffs against it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		if err := os.WriteFile(path, []byte("keep\ndrop\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), editCall(t, FunctionWriteFile, map[string]any{
				"path": path, "content": "keep\nadd\n",
			}), nil); err != nil {
				t.Fatalf("write_file: %v", err)
			}
		})
		got := strip(out)
		if strings.Contains(got, "new file") {
			t.Errorf("an overwrite must not read as a create:\n%s", got)
		}
		for _, want := range []string{"2 - drop", "2 + add"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("quiet deps print nothing at all", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), editCall(t, FunctionWriteFile, map[string]any{
				"path": path, "content": "hello\n",
			}), quietDeps{}); err != nil {
				t.Fatalf("write_file: %v", err)
			}
		})
		if out != "" {
			t.Errorf("quiet session must stay silent, got %q", out)
		}
	})

	t.Run("CORTEX_LOOP_RENDER=0 falls back to the flat action line", func(t *testing.T) {
		prev := richRenderDisabled
		richRenderDisabled = true
		defer func() { richRenderDisabled = prev }()

		path := filepath.Join(t.TempDir(), "f.txt")
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), editCall(t, FunctionWriteFile, map[string]any{
				"path": path, "content": "hello\n",
			}), nil); err != nil {
				t.Fatalf("write_file: %v", err)
			}
		})
		got := strip(out)
		if !strings.Contains(got, "tool: write_file(") {
			t.Errorf("the action line must survive the escape hatch:\n%s", got)
		}
		if strings.Contains(got, "+ hello") {
			t.Errorf("no diff body expected with rendering off:\n%s", got)
		}
	})
}
