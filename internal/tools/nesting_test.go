package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// subDeps is headlessDeps with a working subagent seam: RunSubagent runs the
// supplied closure as the subagent's tool loop, so a nested run can be driven
// with no model and no network.
type subDeps struct {
	headlessDeps
	loop   func(deps ToolDeps)
	digest string
	err    error
	silent bool
}

func (d *subDeps) Outline(string, int) (string, error) { return "outline", nil }
func (d *subDeps) Quiet() bool                         { return d.silent }
func (d *subDeps) RunSubagent(_ context.Context, _ Subagent, _ string) (string, error) {
	if d.loop != nil {
		d.loop(d)
	}
	return d.digest, d.err
}

// resetNesting clears the nesting state so one test's frames can't leak into
// the next if an assertion fails mid-run.
func resetNesting(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		nest.mu.Lock()
		nest.frames, nest.pending = nil, nil
		nest.mu.Unlock()
	})
}

// elapsedRe matches the per-call timing ("120ms", "1.4s", "2m03s").
var elapsedRe = regexp.MustCompile(`\d+ms|\d+\.\d+s|\d+m\d\ds`)

// seedFile writes n numbered lines and returns the path.
func seedFile(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seed.txt")
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return path
}

func studyCall(t *testing.T, path, goal string) ToolCall {
	t.Helper()
	return editCall(t, FunctionStudy, map[string]any{"path": path, "goal": goal})
}

func TestNestedSubagentActivity(t *testing.T) {
	resetNesting(t)
	read := seedFile(t, 3)

	t.Run("child calls indent under the parent and report time and result", func(t *testing.T) {
		deps := &subDeps{digest: strings.Repeat("x", 812)}
		deps.loop = func(d ToolDeps) {
			for i := 0; i < 2; i++ {
				if _, err := Execute(context.Background(), editCall(t, FunctionReadFile,
					map[string]any{"path": read}), d); err != nil {
					t.Errorf("nested read: %v", err)
				}
			}
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), studyCall(t, "internal/tools", "where is the gutter"), deps); err != nil {
				t.Fatalf("study: %v", err)
			}
		})
		got := strings.Split(strings.TrimRight(strip(out), "\n"), "\n")
		if len(got) != 4 {
			t.Fatalf("want 4 lines (parent, 2 children, done), got %d:\n%s", len(got), strings.Join(got, "\n"))
		}

		// The parent announces at the margin, goal included.
		if !strings.HasPrefix(got[0][10:], "tool: study(internal/tools, where is the gutter)") {
			t.Errorf("parent line = %q", got[0])
		}
		// Children sit one level in, each with elapsed + a result summary.
		for _, child := range got[1:3] {
			if !strings.HasPrefix(child[10:], "  tool: read_file(") {
				t.Errorf("child should be indented two spaces: %q", child)
			}
			if !elapsedRe.MatchString(child) {
				t.Errorf("child should carry its elapsed time: %q", child)
			}
			if !strings.Contains(child, "3 lines") {
				t.Errorf("child should summarize its result: %q", child)
			}
		}
		// The done line closes the block at the children's margin.
		if !strings.HasPrefix(got[3][10:], "  study done: 2 calls, ") {
			t.Errorf("done line = %q", got[3])
		}
		if !strings.Contains(got[3], "digest 812 B") {
			t.Errorf("done line should size the digest: %q", got[3])
		}
	})

	t.Run("a failing subagent reports the error on its done line", func(t *testing.T) {
		deps := &subDeps{err: fmt.Errorf("backend refused the request")}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), studyCall(t, "x", ""), deps); err == nil {
				t.Fatal("want the error to propagate")
			}
		})
		got := strip(out)
		if !strings.Contains(got, "study done: 0 calls") || !strings.Contains(got, "error: backend refused the request") {
			t.Errorf("want an error done line, got:\n%s", got)
		}
	})

	t.Run("a quiet session prints nothing, nested or not", func(t *testing.T) {
		deps := &subDeps{silent: true, digest: "d"}
		deps.loop = func(d ToolDeps) {
			if _, err := Execute(context.Background(), editCall(t, FunctionReadFile,
				map[string]any{"path": read}), d); err != nil {
				t.Errorf("nested read: %v", err)
			}
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), studyCall(t, "x", ""), deps); err != nil {
				t.Fatalf("study: %v", err)
			}
		})
		if out != "" {
			t.Errorf("quiet session must stay silent, got %q", out)
		}
	})
}

func TestNestedDisplayCap(t *testing.T) {
	resetNesting(t)
	read := seedFile(t, 1)

	t.Run("a chatty subagent costs a bounded number of rows", func(t *testing.T) {
		calls := nestedCallDisplayCap + 5
		deps := &subDeps{digest: "d"}
		deps.loop = func(d ToolDeps) {
			for i := 0; i < calls; i++ {
				if _, err := Execute(context.Background(), editCall(t, FunctionReadFile,
					map[string]any{"path": read}), d); err != nil {
					t.Errorf("nested read: %v", err)
				}
			}
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), studyCall(t, "x", ""), deps); err != nil {
				t.Fatalf("study: %v", err)
			}
		})
		got := strings.Split(strings.TrimRight(strip(out), "\n"), "\n")
		// parent + capped children + done
		if want := nestedCallDisplayCap + 2; len(got) != want {
			t.Fatalf("got %d lines, want %d", len(got), want)
		}
		done := got[len(got)-1]
		if !strings.Contains(done, fmt.Sprintf("%d calls", calls)) {
			t.Errorf("done line should count every call: %q", done)
		}
		if !strings.Contains(done, "5 not shown") {
			t.Errorf("done line should report the suppressed calls: %q", done)
		}
	})
}

func TestNestedDiffFollowsItsCall(t *testing.T) {
	resetNesting(t)

	t.Run("a subagent's file diff lands under the call that made it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f.txt")
		deps := &subDeps{digest: "d"}
		deps.loop = func(d ToolDeps) {
			if _, err := Execute(context.Background(), editCall(t, FunctionWriteFile,
				map[string]any{"path": path, "content": "hello\n"}), d); err != nil {
				t.Errorf("nested write: %v", err)
			}
		}
		out := captureStdout(t, func() {
			if _, err := Execute(context.Background(), studyCall(t, "x", ""), deps); err != nil {
				t.Fatalf("study: %v", err)
			}
		})
		got := strings.Split(strings.TrimRight(strip(out), "\n"), "\n")
		action, diff := -1, -1
		for i, l := range got {
			if strings.Contains(l, "tool: write_file(") {
				action = i
			}
			if strings.Contains(l, "1 + hello") {
				diff = i
			}
		}
		if action < 0 || diff < 0 {
			t.Fatalf("want both the action and its diff:\n%s", strings.Join(got, "\n"))
		}
		if diff < action {
			t.Errorf("the diff must follow its announcement, not precede it:\n%s", strings.Join(got, "\n"))
		}
		if !strings.HasPrefix(got[diff], "              ") {
			t.Errorf("a nested diff row should carry the nesting margin: %q", got[diff])
		}
		// The diff is the result, so the line above it carries time only — no
		// "wrote N bytes to …" echo duplicating what's rendered underneath.
		if !elapsedRe.MatchString(got[action]) {
			t.Errorf("the call should still report its elapsed time: %q", got[action])
		}
		if strings.Contains(got[action], "wrote ") {
			t.Errorf("a diff-bearing call should not also summarize itself: %q", got[action])
		}
	})
}

func TestSummarizeResult(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{name: "short single line is echoed", out: "edited f.go (1 replacement)", want: "edited f.go (1 replacement)"},
		{name: "long single line is clipped", out: strings.Repeat("z", 100), want: strings.Repeat("z", 59) + "…"},
		{name: "multi-line reports shape", out: "a\nb\nc", want: "3 lines, 5 B"},
		{name: "kilobytes read as kilobytes", out: strings.Repeat("a\n", 1024), want: "1024 lines, 2.0 KB"},
		{name: "empty output is named", out: "   ", want: "no output"},
		{name: "an error wins over the output", out: "partial", err: fmt.Errorf("read x: no such file"), want: "error: read x: no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeResult(tt.out, tt.err); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFmtElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{d: 12 * time.Millisecond, want: "12ms"},
		{d: 999 * time.Millisecond, want: "999ms"},
		{d: 1500 * time.Millisecond, want: "1.5s"},
		{d: 90 * time.Second, want: "1m30s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := fmtElapsed(tt.d); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
