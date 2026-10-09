package tools

import (
	"errors"
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

// cannedTurn renders the fixture turn's tool lines in print order.
func cannedTurn(width int) []string {
	var out []string
	out = append(out,
		formatToolAction("", "outline(cmd/cortex/learn.go)", ""),
		formatToolAction("", `grep("NewFlagSet", cmd/cortex)`, ""),
		formatToolAction("", "read_file(cmd/cortex/learn.go:40-96)", ""),
		formatToolAction("", "edit_file(cmd/cortex/learn.go)", ""),
	)
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
	out = append(out, renderDiff(before, after, diffOptions{Width: width})...)
	out = append(out,
		formatToolAction("", "bash(go test ./cmd/cortex -run Learn)", ""),
		formatToolAction("", "study(internal/loops, how does the scheduler pick the next due loop)", ""),
		formatToolAction("  ", "outline(internal/loops)", "0.4s  31 lines, 1.2 KB"),
		formatToolAction("  ", "read_file(internal/loops/scheduler.go:12-58)", "0.1s  47 lines, 1.9 KB"),
		formatSubagentDone(&nestFrame{name: "study", calls: 2}, 6200*time.Millisecond, strings.Repeat("x", 2400), nil),
		formatSubagentDone(&nestFrame{name: "agent", calls: 1, suppressed: 3}, 900*time.Millisecond, "", errors.New("model returned no content")),
	)
	return out
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
		{"no tty", 0, false, `14:02:11  tool: outline(cmd/cortex/learn.go)
14:02:11  tool: grep("NewFlagSet", cmd/cortex)
14:02:11  tool: read_file(cmd/cortex/learn.go:40-96)
14:02:11  tool: edit_file(cmd/cortex/learn.go)
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []string) error {
            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)
            3       project := fs.String("project", "", "project name")
            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")
            5       fs.Parse(args)
            5 -     return runLearn(*project)
            6 +     return runLearn(*project, *jsonOut)
            7   }
14:02:11  tool: bash(go test ./cmd/cortex -run Learn)
14:02:11  tool: study(internal/loops, how does the scheduler pick the next due loop)
14:02:11    tool: outline(internal/loops)  0.4s  31 lines, 1.2 KB
14:02:11    tool: read_file(internal/loops/scheduler.go:12-58)  0.1s  47 lines, 1.9 KB
14:02:11  study done: 2 calls, 6.2s, digest 2.3 KB
14:02:11  agent done: 1 call, 900ms, 3 not shown, error: model returned no content`},
		{"80 cols", 80, false, `14:02:11  tool: outline(cmd/cortex/learn.go)
14:02:11  tool: grep("NewFlagSet", cmd/cortex)
14:02:11  tool: read_file(cmd/cortex/learn.go:40-96)
14:02:11  tool: edit_file(cmd/cortex/learn.go)
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []string) error {
            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)
            3       project := fs.String("project", "", "project name")
            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")
            5       fs.Parse(args)
            5 -     return runLearn(*project)
            6 +     return runLearn(*project, *jsonOut)
            7   }
14:02:11  tool: bash(go test ./cmd/cortex -run Learn)
14:02:11  tool: study(internal/loops, how does the scheduler pick the next due …
14:02:11    tool: outline(internal/loops)  0.4s  31 lines, 1.2 KB
14:02:11    tool: read_file(internal/loops/scheduler.go…  0.1s  47 lines, 1.9 KB
14:02:11  study done: 2 calls, 6.2s, digest 2.3 KB
14:02:11  agent done: 1 call, 900ms, 3 not shown, error: model returned no cont…`},
		// Known gap, pinned as-is: a nested line's elapsed/summary suffix is
		// never clipped, so at narrow widths it overruns the terminal
		// (track 2's aligned tool column fixes this).
		{"40 cols clips", 40, false, `14:02:11  tool: outline(cmd/cortex/lear…
14:02:11  tool: grep("NewFlagSet", cmd/…
14:02:11  tool: read_file(cmd/cortex/le…
14:02:11  tool: edit_file(cmd/cortex/le…
            @@ -1,6 +1,7 @@
            1   func learnCmd(args []st…
            2       fs := flag.NewFlagS…
            3       project := fs.Strin…
            4 +     jsonOut := fs.Bool(…
            5       fs.Parse(args)
            5 -     return runLearn(*pr…
            6 +     return runLearn(*pr…
            7   }
14:02:11  tool: bash(go test ./cmd/cort…
14:02:11  tool: study(internal/loops, h…
14:02:11    tool: outline…  0.4s  31 lines, 1.2 KB
14:02:11    tool: read_file…  0.1s  47 lines, 1.9 KB
14:02:11  study done: 2 calls, 6.2s, di…
14:02:11  agent done: 1 call, 900ms, 3 …`},
		{"80 cols colored", 80, true, `<90>14:02:11</>  <32>tool: outline</><90>(cmd/cortex/learn.go)</>
<90>14:02:11</>  <32>tool: grep</><90>("NewFlagSet", cmd/cortex)</>
<90>14:02:11</>  <32>tool: read_file</><90>(cmd/cortex/learn.go:40-96)</>
<90>14:02:11</>  <32>tool: edit_file</><90>(cmd/cortex/learn.go)</>
<90>            @@ -1,6 +1,7 @@</>
<90>            1   func learnCmd(args []string) error {</>
<90>            2       fs := flag.NewFlagSet("learn", flag.ExitOnError)</>
<90>            3       project := fs.String("project", "", "project name")</>
<32>            4 +     jsonOut := fs.Bool("json", false, "emit the report as JSON")</>
<90>            5       fs.Parse(args)</>
<31>            5 -     return runLearn(*project)</>
<32>            6 +     return runLearn(*project, *jsonOut)</>
<90>            7   }</>
<90>14:02:11</>  <32>tool: bash</><90>(go test ./cmd/cortex -run Learn)</>
<90>14:02:11</>  <32>tool: study</><90>(internal/loops, how does the scheduler pick the next due …</>
<90>14:02:11</>    <32>tool: outline</><90>(internal/loops)</><90>  0.4s  31 lines, 1.2 KB</>
<90>14:02:11</>    <32>tool: read_file</><90>(internal/loops/scheduler.go…</><90>  0.1s  47 lines, 1.9 KB</>
<90>14:02:11</>  <90>study done: 2 calls, 6.2s, digest 2.3 KB</>
<90>14:02:11</>  <90>agent done: 1 call, 900ms, 3 not shown, error: model returned no cont…</>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinDisplay(t, tt.width, tt.color)
			got := tagANSI(strings.Join(cannedTurn(tt.width), "\n"))
			if got != tt.want {
				t.Errorf("render changed (width %d, color %v). got:\n%s\n\nwant:\n%s", tt.width, tt.color, got, tt.want)
			}
		})
	}
}
