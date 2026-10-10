package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// Render goldens for the REPL's own lines (docs/tui-polish.md, track 1): the
// prompt bar in each phase and the gutter-prefixed message line, with and
// without color. internal/tools/render_golden_test.go pins the tool surface;
// together they cover what a turn prints outside the markdown renderer.

// goldenSession is a session at a fixed, mid-pressure point: ~10k of zone A,
// ~60k of tail in a 131k window, a few tenths of a cent spent.
func goldenSession() *CortexSession {
	cs := &CortexSession{
		Window: 131072,
		Request: &AgentRequest{
			Model:    "qwen3-coder-q3",
			Messages: []Message{{Role: RoleSystem, Content: strings.Repeat("x", 40000)}},
		},
		LastPromptTokens: 70000,
		costUSD:          0.004,
	}
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 60000})
	return cs
}

// tagSGR makes SGR sequences readable in a golden: "\033[90m" → "<90>",
// reset → "</>".
func tagSGR(s string) string {
	s = strings.ReplaceAll(s, "\033[0m", "</>")
	for _, code := range []string{"1", "90", "36", "32", "33", "31", "34", "35", "96", "92"} {
		s = strings.ReplaceAll(s, "\033["+code+"m", "<"+code+">")
	}
	return s
}

func TestRenderGoldenPromptBar(t *testing.T) {
	tests := []struct {
		name       string
		phase      turnPhase
		color      bool
		wantLeft   string
		wantStatus string
	}{
		{"idle", phaseIdle, false, ". > ", "qwen3-coder-q3  10k|60k"},
		{"thinking", phaseThinking, false, "* > ", "qwen3-coder-q3  10k|60k"},
		{"streaming", phaseStreaming, false, "~ > ", "qwen3-coder-q3  10k|60k"},
		{"idle colored", phaseIdle, true, "<90>.</> <36>></> ", "<90>qwen3-coder-q3</>  <90>10k</><90>|</><33>60k</>"},
		{"thinking colored", phaseThinking, true, "<96>*</> <36>></> ", "<90>qwen3-coder-q3</>  <90>10k</><90>|</><33>60k</>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(tt.color)()
			cs := goldenSession()
			cs.phase = tt.phase
			if got := tagSGR(cs.Prompt()); got != tt.wantLeft {
				t.Errorf("prompt changed.\n got: %q\nwant: %q", got, tt.wantLeft)
			}
			if got := tagSGR(cs.PromptStatus()); got != tt.wantStatus {
				t.Errorf("prompt status changed.\n got: %q\nwant: %q", got, tt.wantStatus)
			}
		})
	}
}

func TestRenderGoldenAcceptedLine(t *testing.T) {
	prevNow, prevWidth := tools.Now, style.TermWidth
	tools.Now = func() time.Time { return time.Date(2026, 10, 9, 14, 2, 11, 0, time.UTC) }
	defer func() { tools.Now, style.TermWidth = prevNow, prevWidth }()
	tests := []struct {
		name, input string
		width       int
		color       bool
		want        string
	}{
		{"typed", "add a --json flag to cortex learn", 80, false, "14:02:11  add a --json flag to cortex learn"},
		{"typed colored", "add a --json flag", 80, true, "<90>14:02:11</>  <1>add a --json flag</>"},
		{"paste", "why does this panic\ngoroutine 1 [running]:\nmain.main()", 80, false, "14:02:11  why does this panic  [+2 lines]"},
		{"clipped to one row", strings.Repeat("word ", 20), 40, false, "14:02:11  word word word word word wor…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(tt.color)()
			style.TermWidth = func() int { return tt.width }
			got := tagSGR(goldenSession().acceptedLine(tt.input))
			if got != tt.want {
				t.Errorf("accepted line changed.\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRenderGoldenMessageLine(t *testing.T) {
	ts := time.Date(2026, 10, 9, 14, 2, 31, 0, time.UTC)
	tests := []struct {
		name  string
		color bool
		want  string
	}{
		{"plain", false, "14:02:31  Added --json to cortex learn. Tests pass."},
		{"colored", true, "<90>14:02:31</>  Added --json to cortex learn. Tests pass."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(tt.color)()
			got := tagSGR(Message{Role: "assistant", Content: "Added --json to cortex learn. Tests pass."}.render(ts))
			if got != tt.want {
				t.Errorf("message line changed.\n got: %q\nwant: %q", got, tt.want)
			}
			if gutter := tagSGR(tools.Gutter(ts)); !strings.HasPrefix(got, gutter) {
				t.Errorf("message line %q does not start with the shared gutter %q", got, gutter)
			}
		})
	}
}

// TestRenderGoldenAnswer pins how a streamed markdown answer lays out: the
// timestamp on its first line, every later line indented under that gutter,
// prose wrapped to the content width less the gutter (answerWrapWidth).
// Compared with color stripped — glamour's styling is its own concern; the
// layout is ours.
func TestRenderGoldenAnswer(t *testing.T) {
	prevNow := tools.Now
	tools.Now = func() time.Time { return time.Date(2026, 10, 9, 14, 2, 31, 0, time.UTC) }
	defer func() { tools.Now = prevNow }()

	answer := "Added `--json` to `cortex learn`. The report struct already carried json tags, " +
		"so the flag only switches the printer between the text and JSON encoders.\n\n" +
		"- `learn.go`: new `--json` flag\n- `learn_test.go`: covers both printers\n\n" +
		"Tests pass.\n"
	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"80 cols", 80, "\n14:02:31  Added  --json  to  cortex learn . The report struct already carried\n          json tags, so the flag only switches the printer between the text and\n          JSON encoders.\n\n          •  learn.go : new  --json  flag\n          •  learn_test.go : covers both printers\n\n          Tests pass.\n\n"},
		{"140 cols caps", 140, "\n14:02:31  Added  --json  to  cortex learn . The report struct already carried json tags, so the flag\n          only switches the printer between the text and JSON encoders.\n\n          •  learn.go : new  --json  flag\n          •  learn_test.go : covers both printers\n\n          Tests pass.\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			p := &streamPrinter{out: &buf, md: newMarkdownRenderer(answerWrapWidth(tt.width))}
			p.onContent(answer)
			p.finish()
			got := style.Strip(buf.String())
			if got != tt.want {
				t.Errorf("answer layout changed (width %d). got:\n%s\nwant:\n%s\n(quoted: %q)", tt.width, got, tt.want, got)
			}
		})
	}
}
