package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// gutterPrefix renders the "HH:MM:SS  " shown before every printed line. The
// REPL is glyph-free by decision (2026-07-19): there is no per-role icon
// anymore, and the timestamp no longer carries a per-role color either
// (2026-07-19) — every timestamp, on every line (user/assistant/tool alike),
// is plain gray so the gutter reads as one consistent margin rather than a
// row of role-coded tags.
func gutterPrefix(ts time.Time) string {
	return fmt.Sprintf("%s  ", withColor(ts.Format("15:04:05"), gray))
}

func (m Message) render(ts time.Time) string {
	return gutterPrefix(ts) + m.Content
}

func (m Message) Print() {
	fmt.Println(m.render(time.Now()))
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

// brightCyan and brightGreen are the aixterm "bright" SGR variants of the
// palette's existing cyan and green — the same hues Prompt() already uses
// elsewhere (cyan for the trailing promptGlyph, green for the gauge's
// healthy state), just at higher contrast. Used only for the state light's
// active phases, so it visibly pops against idle's already-dim gray rather
// than introducing a color the rest of the bar doesn't.
const (
	brightCyan  = "\033[96m"
	brightGreen = "\033[92m"
	// blinkBrightCyan pairs the thinking color with SGR 5 (blink). Unlike the
	// rejected spinner-frame approach, this asks the terminal emulator itself
	// to animate — it's still one static escape sequence baked into the same
	// glyph string phaseGlyph always returned, applied once per redraw exactly
	// like color already is. There is no ticker advancing frames and nothing
	// here calls SetPrompt/SetActivity more often than the phase actually
	// changes, so it can't reintroduce the missing-erase redraw bug that
	// SetPrompt's per-tick pulse hit earlier. Terminal support varies (most
	// GUI emulators honor it; a handful ignore it and just render static
	// bright cyan — a harmless fallback either way); NO_COLOR strips it along
	// with everything else via withColor.
	blinkBrightCyan = "\033[5;96m"
)

// phaseGlyph renders the state light: a single character whose shape (not
// just its color) carries the state, so it still reads under NO_COLOR. A
// dot filling in with activity — hollow idle, half thinking (blinking —
// terminal-native SGR 5, see blinkBrightCyan), solid streaming (○◐●,
// U+25CB/25D0/25CF) — the classic status-light idiom. A deliberate departure
// from the REPL's otherwise-ASCII typography (the 2026-07-19 sweep:
// middot→pipe, ellipsis→..., arrows→ASCII): still one fixed character, still
// no application-driven animation frames — the shape never cycles, only the
// caller-driven state changes it — but not 7-bit ASCII. Worth a callout in
// the upstream PR for exactly that reason.
func phaseGlyph(p turnPhase) string {
	switch p {
	case phaseThinking:
		return withColor("◐", blinkBrightCyan)
	case phaseStreaming:
		return withColor("●", brightGreen)
	default:
		return withColor("○", gray)
	}
}

// setPhase updates the state light and, while a turn is anchored, forces an
// immediate redraw so the change is visible the instant it happens rather
// than waiting for the next unrelated SetPrompt call.
func (cs *CortexSession) setPhase(p turnPhase) {
	cs.phase = p
	if cs.live != nil {
		cs.live.SetPrompt(cs.Prompt())
	}
}

func (cs *CortexSession) Prompt() string {
	win := cs.windowSize()
	status := withColor(fmt.Sprintf("cortex %s | %s | ", version(), cs.Request.Model), gray)
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
		cost = withColor(" | "+humanCost(cs.costUSD), gray)
	}
	return fmt.Sprintf("%s %s%s%s  %s ", phaseGlyph(cs.phase), status, gauge, cost, withColor(promptGlyph, cyan))
}

func streamingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_STREAM"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
