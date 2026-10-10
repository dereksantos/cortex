package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// gutterPrefix renders the "HH:MM:SS  " shown before every printed line. The
// REPL is glyph-free by decision (2026-07-19): there is no per-role icon
// anymore, and the timestamp no longer carries a per-role color either
// (2026-07-19) — every timestamp, on every line (user/assistant/tool alike),
// is plain gray so the gutter reads as one consistent margin rather than a
// row of role-coded tags.
func gutterPrefix(ts time.Time) string { return tools.Gutter(ts) }

// gutterIndent is a blank gutter: continuation lines (an answer's wrapped
// prose, its later blocks) start here so the timestamps read as one column.
var gutterIndent = strings.Repeat(" ", len("15:04:05  "))

// answerWrapWidth is the width an answer's prose wraps to on a terminal of
// width w: the content width (capped at style.MaxContentWidth) less the
// gutter it sits behind.
func answerWrapWidth(w int) int {
	if w <= 0 || w > style.MaxContentWidth {
		w = min(max(w, 80), style.MaxContentWidth)
	}
	return max(w-len(gutterIndent), 20)
}

// indentUnderGutter prefixes each line of s with the blank gutter, skipping
// the first when it joins a gutter already printed. Blank lines stay empty.
func indentUnderGutter(s string, skipFirst bool) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if (i == 0 && skipFirst) || strings.TrimSpace(style.Strip(l)) == "" {
			continue
		}
		lines[i] = gutterIndent + l
	}
	return strings.Join(lines, "\n")
}

func (m Message) render(ts time.Time) string {
	return gutterPrefix(ts) + m.Content
}

func (m Message) Print() {
	fmt.Println(m.render(tools.Now()))
}

// turnPhase is the coder's current state relative to the model: idle
// (resting on the prompt, waiting for input), thinking (reasoning or running
// a tool — nothing user-facing yet), or streaming (answer content is
// printing). It drives the one-character state light at the far left of
// Prompt() — see phaseGlyph.
type turnPhase int

const (
	phaseIdle turnPhase = iota
	phaseThinking
	phaseStreaming
)

// phaseGlyph renders the state light: one static ASCII character whose shape
// (not just its color) carries the state, so it still reads under NO_COLOR —
// "." idle, "*" thinking (reasoning or a running tool), "~" streaming. Plain
// 7-bit ASCII with no blink or animation frames, per the REPL's plain-text
// typography (the 2026-07-19 sweep); only the caller-driven phase changes it.
// "~" rather than ">" for streaming so it can't be mistaken for promptGlyph.
func phaseGlyph(p turnPhase) string {
	switch p {
	case phaseThinking:
		return style.Paint("*", style.Live)
	case phaseStreaming:
		return style.Paint("~", style.Streaming)
	default:
		return style.Paint(".", style.Dim)
	}
}

// setPhase updates the state light and, while a turn is anchored, redraws the
// prompt so the change shows immediately. It is a no-op when the phase is
// unchanged: the streaming status callback fires on every one-second tick and
// every reasoning chunk, and redrawing the whole prompt for each of those
// would repaint the bar continuously for no visible change. Reports whether
// the phase changed.
func (cs *CortexSession) setPhase(p turnPhase) bool {
	if cs.phase == p {
		return false
	}
	cs.phase = p
	cs.refreshAnchor()
	return true
}

// refreshAnchor redraws a live turn's pinned prompt with the current state
// light and status. No-op outside an anchored turn.
func (cs *CortexSession) refreshAnchor() {
	if cs.live != nil {
		cs.live.SetPrompt(cs.Prompt())
	}
}

// Prompt is the input row: the dim status (PromptStatus), then the state
// light and the marker, with the cursor last — "qwen3.8-27b  1k|22k  . ❯ ".
// Version moved to the startup header and cost to each turn's footer
// (docs/tui-polish.md, track 2).
func (cs *CortexSession) Prompt() string {
	return fmt.Sprintf("%s  %s %s ", cs.PromptStatus(), phaseGlyph(cs.phase), style.Paint(promptGlyph, style.Accent))
}

// PromptStatus is the model, then the context gauge. The gauge is the
// two-zone numeric form (contextbar.go's gaugeZones) by default; coloredGauge
// composes its per-zone coloring (gray head/gray divider/pressure-colored
// tail) or, for the selectable bar styles, the single ctxColor wrap that
// predates gaugeZones. ctxColor keys off LastPromptTokens (the last request's
// actual billed size, not the gauge's own head+tail estimate).
func (cs *CortexSession) PromptStatus() string {
	return style.Paint(cs.Request.Model, style.Dim) + "  " + cs.coloredGauge(promptGaugeCells, cs.windowSize())
}

// acceptedLine is what a submitted input leaves in scrollback: the timestamp
// gutter and the input in the Strong role, so a turn opens on a line shaped
// like every other line it prints. Long input wraps under the gutter — these
// are the user's own words, so nothing is clipped — at the content width of a
// terminal width columns wide. A multi-line paste shows its first line and a
// count of the rest. Rows are joined with "\r\n": the editor is in raw mode.
func (cs *CortexSession) acceptedLine(input string, width int) string {
	if strings.TrimSpace(input) == "" {
		return cs.Prompt() // nothing submitted: leave the prompt row as it was, no bare timestamp
	}
	first, rest, multi := strings.Cut(input, "\n")
	tag := ""
	if multi {
		tag = fmt.Sprintf("  [+%d lines]", strings.Count(rest, "\n")+1)
	}
	rows := style.Wrap(first, answerWrapWidth(width))
	for i, r := range rows {
		rows[i] = style.Paint(r, style.Strong)
	}
	rows[len(rows)-1] += style.Paint(tag, style.Dim)
	return gutterPrefix(tools.Now()) + strings.Join(rows, "\r\n"+gutterIndent)
}

func streamingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_STREAM"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
