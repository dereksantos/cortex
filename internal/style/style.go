// Package style is the REPL's single home for terminal styling: the semantic
// color roles, the NO_COLOR switch, and the width-aware layout helpers every
// printed surface shares (docs/tui-polish.md, track 1).
//
// Call sites pick a Role by what the text IS (secondary metadata, an action,
// a warning), never by hue, so the palette can be retuned in one place. The
// REPL is plain-text by decision (2026-07-19): roles carry meaning through
// color only, and every surface must still read with color stripped.
//
// This package is a leaf: it imports only the standard library, x/term and
// go-runewidth, so lineedit, loopui, tools and cmd/cortex can all depend on it.
package style

import (
	"os"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// Role is an ANSI SGR sequence named by its meaning.
type Role string

// Semantic roles. Two roles may share a hue (Action and OK are both green);
// they stay separate so either can move without touching the other's sites.
const (
	// Dim is secondary text: the timestamp gutter, tool arguments, status
	// metadata, diff context.
	Dim Role = "\033[90m"
	// Accent marks things to find again: headings, ids, citations, the
	// prompt marker.
	Accent Role = "\033[36m"
	// Action is something the agent did: tool verbs, subagent run lines.
	Action Role = "\033[32m"
	// OK, Warn and Err are state: healthy/added, pressure/interrupted,
	// failing/removed.
	OK   Role = "\033[32m"
	Warn Role = "\033[33m"
	Err  Role = "\033[31m"
	// Strong is the user's own words echoed into scrollback — the line a
	// turn opens on.
	Strong Role = "\033[1m"
	// Live and Streaming are the high-contrast variants the state light uses
	// while a turn is running, so they pop against idle Dim.
	Live      Role = "\033[96m"
	Streaming Role = "\033[92m"
)

// Hues for categorical data only — a chart's series, like the /context grid's
// components — where the color distinguishes kinds rather than signalling
// meaning. Everything else uses a Role.
const (
	HueBlue    Role = "\033[34m"
	HueMagenta Role = "\033[35m"
	HueYellow  Role = "\033[33m"
	HueGreen   Role = "\033[32m"
	HueRed     Role = "\033[31m"
)

const reset = "\033[0m"

// colorOff honors the NO_COLOR convention (https://no-color.org): any
// non-empty NO_COLOR strips every Paint. Read once at startup — the env
// doesn't change mid-session.
var colorOff = os.Getenv("NO_COLOR") != ""

// ColorEnabled reports whether Paint emits ANSI.
func ColorEnabled() bool { return !colorOff }

// ForceColor pins color on or off and returns a func that restores the prior
// setting — for tests that assert colored or NO_COLOR output.
func ForceColor(on bool) (restore func()) {
	prev := colorOff
	colorOff = !on
	return func() { colorOff = prev }
}

// Paint wraps s in r unless color is disabled. An empty s stays empty so
// callers can paint optional fragments without guarding.
func Paint(s string, r Role) string {
	if colorOff || s == "" {
		return s
	}
	return string(r) + s + reset
}

// TermWidth is stdout's column count, or 0 when stdout is not a terminal
// (piped, CI, tests). 0 means "no width": callers leave lines whole rather
// than clipping to a guess. A var so tests can pin a width without a pty.
var TermWidth = func() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}

// MaxContentWidth caps how wide the REPL lays out its own lines — prose wraps
// and tool results right-align at this column even on a wider terminal, so
// the eye never travels a 200-column line to find a result.
const MaxContentWidth = 100

// ContentWidth is the column the REPL lays its lines out to: the terminal
// width capped at MaxContentWidth, or 0 when there is no terminal.
func ContentWidth() int {
	w := TermWidth()
	if w > MaxContentWidth {
		return MaxContentWidth
	}
	return w
}

// Strip removes CSI escape sequences (ESC [ … final byte) so width math
// counts only visible cells.
func Strip(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
			}
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Width is the number of terminal cells s occupies, ignoring ANSI and
// counting wide runes as two.
func Width(s string) int { return runewidth.StringWidth(Strip(s)) }

// Clip shortens plain (uncolored) s to at most max cells, marking a cut with
// "…". max <= 0 yields "…": there is no room, and the mark says so.
func Clip(s string, max int) string {
	if max <= 0 {
		return "…"
	}
	if runewidth.StringWidth(s) <= max {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > max-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// Justify lays left and right on one line of width cells, right flush to the
// edge. When both don't fit with at least gap cells between them, right is
// dropped — the left side is the content, the right side is status. width
// <= 0 (no terminal) joins them with gap spaces.
func Justify(left, right string, width, gap int) string {
	lw, rw := Width(left), Width(right)
	if right == "" {
		return left
	}
	if width <= 0 {
		return left + strings.Repeat(" ", gap) + right
	}
	pad := width - lw - rw
	if pad < gap {
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// Wrap breaks plain s into lines of at most width cells at spaces, keeping
// existing newlines. A word longer than width gets a line of its own rather
// than being split. width <= 0 returns s's lines unwrapped.
func Wrap(s string, width int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if width <= 0 || runewidth.StringWidth(para) <= width {
			out = append(out, para)
			continue
		}
		indent := para[:len(para)-len(strings.TrimLeftFunc(para, unicode.IsSpace))]
		line, lw := indent, runewidth.StringWidth(indent)
		empty := true
		for _, word := range strings.Fields(para) {
			ww := runewidth.StringWidth(word)
			if !empty && lw+1+ww > width {
				out = append(out, line)
				line, lw, empty = indent, runewidth.StringWidth(indent), true
			}
			if !empty {
				line += " "
				lw++
			}
			line += word
			lw += ww
			empty = false
		}
		out = append(out, line)
	}
	return out
}
