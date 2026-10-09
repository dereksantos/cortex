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
	if cs.live != nil {
		cs.live.SetPrompt(cs.Prompt())
	}
	return true
}

func (cs *CortexSession) Prompt() string {
	win := cs.windowSize()
	status := style.Paint(fmt.Sprintf("cortex %s | %s | ", version(), cs.Request.Model), style.Dim)
	// The gauge is the two-zone numeric form (contextbar.go's gaugeZones) by
	// default; coloredGauge composes its per-zone coloring (gray head/gray
	// divider/pressure-colored tail) or, for the selectable bar styles, the
	// single ctxColor wrap that predates gaugeZones. ctxColor keys off
	// LastPromptTokens (the last request's actual billed size, not the
	// gauge's own head+tail estimate) — same green/yellow/red threshold
	// semantics as before this style existed.
	gauge := cs.coloredGauge(promptGaugeCells, win)
	cost := ""
	if cs.costUSD > 0 {
		cost = style.Paint(" | "+humanCost(cs.costUSD), style.Dim)
	}
	return fmt.Sprintf("%s %s%s%s  %s ", phaseGlyph(cs.phase), status, gauge, cost, style.Paint(promptGlyph, style.Accent))
}

func streamingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_STREAM"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
