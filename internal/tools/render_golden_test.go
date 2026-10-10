package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// Render goldens (docs/tui-polish.md, track 1): one canned turn's tool surface
// — action lines, an edit's diff, a nested subagent block — rendered at fixed
// widths with and without color. They pin today's output byte-for-byte so every
// visual change is a reviewable diff of these literals. Golden-pinned means a
// literal string in the test (the greetingPrompt convention), not files on disk.

// pinDisplay fixes the clock, the terminal width, and color for one render.
func pinDisplay(t *testing.T, width int, color bool) {
	t.Helper()
	prevNow, prevWidth := Now, style.TermWidth
	Now = func() time.Time { return time.Date(2026, 10, 9, 14, 2, 11, 0, time.UTC) }
	style.TermWidth = func() int { return width }
	restoreColor := style.ForceColor(color)
	t.Cleanup(func() {
		Now, style.TermWidth = prevNow, prevWidth
		restoreColor()
	})
}

// loud is a non-quiet Quieter: the interactive REPL's view of the tools.
type loud struct{}

func (loud) Quiet() bool { return false }

// cannedTurn drives the fixture turn's tool calls through the real print path
// — announce, diff, finish — and returns what reached stdout, line by line.
func cannedTurn(t *testing.T) []string {
	before := "func learnCmd(args []string) error {\n" +
		"\tfs := flag.NewFlagSet(\"learn\", flag.ExitOnError)\n" +
		"\tproject := fs.String(\"project\", \"\", \"project name\")\n" +
		"\tfs.Parse(args)\n" +
		"\treturn runLearn(*project)\n" +
		"}\n"
	after := "func learnCmd(args []string) error {\n" +
		"\tfs := flag.NewFlagSet(\"learn\", flag.ExitOnError)\n" +
		"\tproject := fs.String(\"project\", \"\", \"project name\")\n" +
		"\tjsonOut := fs.Bool(\"json\", false, \"emit the report as JSON\")\n" +
		"\tfs.Parse(args)\n" +
		"\treturn runLearn(*project, *jsonOut)\n" +
		"}\n"
	call := func(action string, d time.Duration, out string, err error) {
		printToolAction(loud{}, action)
		finishCall(d, out, err)
	}
	out := captureStdout(t, func() {
		call("outline(cmd/cortex/learn.go)", 40*time.Millisecond, "func learnCmd\nfunc runLearn\n", nil)
		call(`grep("NewFlagSet", cmd/cortex)`, 90*time.Millisecond, "cmd/cortex/learn.go:41: fs := flag.NewFlagSet", nil)
		call("read_file(cmd/cortex/learn.go:40-96)", 2*time.Millisecond, strings.Repeat("line\n", 57), nil)

		printToolAction(loud{}, "edit_file(cmd/cortex/learn.go)")
		printFileDiff(loud{}, before, after)
		finishCall(5*time.Millisecond, "edited cmd/cortex/learn.go", nil)

		call("bash(go test ./cmd/cortex -run Learn)", 6800*time.Millisecond, "ok  \tgithub.com/dereksantos/cortex/cmd/cortex\t6.512s\n", nil)
		call("read_file(cmd/cortex/missing.go)", time.Millisecond, "", errors.New("open cmd/cortex/missing.go: no such file or directory"))

		printToolAction(loud{}, "study(internal/loops, how does the scheduler pick the next due loop)")
		flushAction()
		f := pushNest("study")
		beginNestedCall()
		call("outline(internal/loops)", 400*time.Millisecond, strings.Repeat("x\n", 31), nil)
		beginNestedCall()
		call("read_file(internal/loops/scheduler.go:12-58)", 1200*time.Millisecond, strings.Repeat("y\n", 47), nil)
		popNest()
		fmt.Println(formatSubagentDone(f, 6200*time.Millisecond, strings.Repeat("x", 2400), nil))
	})
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// tagANSI makes SGR sequences readable in a golden: "\033[90m" → "<90>",
// reset → "</>".
func tagANSI(s string) string {
	s = strings.ReplaceAll(s, "\033[0m", "</>")
	for _, code := range []string{"90", "36", "32", "33", "31", "34", "35", "96", "92"} {
		s = strings.ReplaceAll(s, "\033["+code+"m", "<"+code+">")
	}
	return s
}

func TestRenderGoldenToolSurface(t *testing.T) {
	tests := []struct {
		name  string
		width int
		color bool
		want  string
	}{
		{"no tty", 0, false, `14:02:11  outline learn.go · grep "NewFlagSet" · read learn.go
14:02:11  edit     cmd/cortex/learn.go  +2 -1
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []string) error {
            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)
            3       project := fs.String("project", "", "project name")
            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")
            5       fs.Parse(args)
            5 -     return runLearn(*project)
            6 +     return runLearn(*project, *jsonOut)
            7   }
14:02:11  bash     go test ./cmd/cortex -run Learn  6.8s  ok github.com/dereksantos/cortex/cmd/cortex 6.512s
14:02:11  read     cmd/cortex/missing.go  error: open cmd/cortex/missing.go: no such file or directory
14:02:11  study    internal/loops, how does the scheduler pick the next due loop
14:02:11    outline  internal/loops  31 lines, 62 B
14:02:11    read     internal/loops/scheduler.go:12-58  1.2s  47 lines, 94 B
14:02:11    study done: 2 calls, 6.2s, digest 2.3 KB`},
		{"80 cols", 80, false, `14:02:11  outline learn.go · grep "NewFlagSet" · read learn.go
14:02:11  edit     cmd/cortex/learn.go                                     +2 -1
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []string) error {
            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)
            3       project := fs.String("project", "", "project name")
            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")
            5       fs.Parse(args)
            5 -     return runLearn(*project)
            6 +     return runLearn(*project, *jsonOut)
            7   }
14:02:11  bash     go test ./cmd/cortex -run Learn          6.8s  ok github.com…
14:02:11  read     cmd/cortex/missing.go                    error: open cmd/cor…
14:02:11  study    internal/loops, how does the scheduler pick the next due loop
14:02:11    outline  internal/loops                               31 lines, 62 B
14:02:11    read     internal/loops/scheduler.go:12-58       1.2s  47 lines, 94…
14:02:11    study done: 2 calls, 6.2s, digest 2.3 KB`},
		{"140 cols caps at 100", 140, false, `14:02:11  outline learn.go · grep "NewFlagSet" · read learn.go
14:02:11  edit     cmd/cortex/learn.go                                                         +2 -1
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []string) error {
            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)
            3       project := fs.String("project", "", "project name")
            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")
            5       fs.Parse(args)
            5 -     return runLearn(*project)
            6 +     return runLearn(*project, *jsonOut)
            7   }
14:02:11  bash     go test ./cmd/cortex -run Learn                       6.8s  ok github.com/dereks…
14:02:11  read     cmd/cortex/missing.go                                 error: open cmd/cortex/mis…
14:02:11  study    internal/loops, how does the scheduler pick the next due loop
14:02:11    outline  internal/loops                                                   31 lines, 62 B
14:02:11    read     internal/loops/scheduler.go:12-58                          1.2s  47 lines, 94 B
14:02:11    study done: 2 calls, 6.2s, digest 2.3 KB`},
		{"40 cols clips", 40, false, `14:02:11  outline learn.go · grep "NewF…
14:02:11  edit     cmd/cortex/le…  +2 -1
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []st…
            2       fs := flag.NewFlagS…
            3       project := fs.Strin…
            4 +     jsonOut := fs.Bool(…
            5       fs.Parse(args)
            5 -     return runLearn(*pr…
            6 +     return runLearn(*pr…
            7   }
14:02:11  bash     go tes…  6.8s  ok gi…
14:02:11  read     cmd/co…  error: open…
14:02:11  study    internal/loops, how …
14:02:11    outline  inte…  31 lines, 6…
14:02:11    read     inte…  1.2s  47 li…
14:02:11    study done: 2 calls, 6.2s, …`},
		{"80 cols colored", 80, true, `<90>14:02:11</>  <90>outline learn.go · grep "NewFlagSet" · read learn.go</>
<90>14:02:11</>  <32>edit</>     cmd/cortex/learn.go                                     <90>+2 -1</>
<90>            @@ -1,6 +1,7 @@</>
<90>            1   func learnCmd(args []string) error {</>
<90>            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)</>
<90>            3       project := fs.String("project", "", "project name")</>
<32>            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")</>
<90>            5       fs.Parse(args)</>
<31>            5 -     return runLearn(*project)</>
<32>            6 +     return runLearn(*project, *jsonOut)</>
<90>            7   }</>
<90>14:02:11</>  <32>bash</>     go test ./cmd/cortex -run Learn          <90>6.8s  ok github.com…</>
<90>14:02:11</>  <31>read</>     cmd/cortex/missing.go                    <31>error: open cmd/cor…</>
<90>14:02:11</>  <32>study</>    internal/loops, how does the scheduler pick the next due loop
<90>14:02:11</>    <32>outline</>  internal/loops                               <90>31 lines, 62 B</>
<90>14:02:11</>    <32>read</>     internal/loops/scheduler.go:12-58       <90>1.2s  47 lines, 94…</>
<90>14:02:11</>  <90>  study done: 2 calls, 6.2s, digest 2.3 KB</>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinDisplay(t, tt.width, tt.color)
			got := tagANSI(strings.Join(cannedTurn(t), "\n"))
			if got != tt.want {
				t.Errorf("render changed (width %d, color %v). got:\n%s\n\nwant:\n%s", tt.width, tt.color, got, tt.want)
			}
		})
	}
}
