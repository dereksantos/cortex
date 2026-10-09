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
	for _, code := range []string{"90", "36", "32", "33", "31", "34", "35", "96", "92"} {
		s = strings.ReplaceAll(s, "\033["+code+"m", "<"+code+">")
	}
	return s
}

func TestRenderGoldenPromptBar(t *testing.T) {
	tests := []struct {
		name  string
		phase turnPhase
		color bool
		want  string
	}{
		{"idle", phaseIdle, false, ". cortex <version> | qwen3-coder-q3 | 10k|60k | $0.0040  > "},
		{"thinking", phaseThinking, false, "* cortex <version> | qwen3-coder-q3 | 10k|60k | $0.0040  > "},
		{"streaming", phaseStreaming, false, "~ cortex <version> | qwen3-coder-q3 | 10k|60k | $0.0040  > "},
		{"idle colored", phaseIdle, true, "<90>.</> <90>cortex <version> | qwen3-coder-q3 | </><90>10k</><90>|</><33>60k</><90> | $0.0040</>  <36>></> "},
		{"thinking colored", phaseThinking, true, "<96>*</> <90>cortex <version> | qwen3-coder-q3 | </><90>10k</><90>|</><33>60k</><90> | $0.0040</>  <36>></> "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer style.ForceColor(tt.color)()
			cs := goldenSession()
			cs.phase = tt.phase
			got := tagSGR(strings.Replace(cs.Prompt(), version(), "<version>", 1))
			if got != tt.want {
				t.Errorf("prompt bar changed.\n got: %q\nwant: %q", got, tt.want)
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
