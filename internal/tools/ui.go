// Package ui holds the terminal color helpers and display constants shared
// across the loop packages (tools, render, session, main). Centralizing them
// keeps the color palette and the NO_COLOR convention in one place so the
// extracted packages don't each re-declare the palette.
package tools

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// PromptGlyph is the input affordance at the end of the status line and the
// REPL's plain marker for a user-originated line. The REPL is glyph-free by
// decision (2026-07-19): ANSI color carries the role distinction that the
// old icon set (❯◆▸✻) used to carry, so only this single ASCII marker
// remains.
const PromptGlyph = ">"

// ANSI color codes.
const (
	Red     = "\033[31m"
	Cyan    = "\033[36m"
	Green   = "\033[32m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Yellow  = "\033[33m"
	Gray    = "\033[90m" // bright black, for dim status text
	Reset   = "\033[0m"
)

// colorDisabled honors the NO_COLOR convention (https://no-color.org): any
// non-empty NO_COLOR strips ANSI from every Color call. Read once at
// startup — the env doesn't change mid-session.
var colorDisabled = os.Getenv("NO_COLOR") != ""

// Color wraps v in c unless NO_COLOR is set. The single source of truth for
// ANSI coloring across the loop packages.
func Color(v, c string) string {
	if colorDisabled {
		return v
	}
	return fmt.Sprintf("%s%s%s", c, v, Reset)
}

// richRenderDisabled honors the same CORTEX_LOOP_RENDER=0 escape hatch
// cmd/cortex's renderEnabled reads: with it set, the terminal falls back to
// the plainest output the REPL has — which now also means the flat one-line
// tool actions, with no diff body under a file edit. Read once at startup,
// like colorDisabled; tests set it directly.
var richRenderDisabled = isOff(os.Getenv("CORTEX_LOOP_RENDER"))

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// termWidth is the column count of stdout, or 0 when stdout is not a terminal
// (piped, CI, the test harness). 0 means "don't truncate": a non-TTY consumer
// gets the raw, unclipped line, matching how every other render path here
// degrades. A package var so tests can pin a width without a pty.
var termWidth = func() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}

// TimestampPrefix renders the "HH:MM:SS  " gutter shown before every printed
// line — user/assistant turns (cmd/cortex's gutterPrefix) and tool-action
// lines (printToolAction) alike — so a scrolled session stays vertically
// aligned regardless of which side printed the line. Always gray (2026-07-19):
// the timestamp no longer carries a per-role/per-tool color, so it reads as
// one consistent margin rather than a row of color-coded tags.
func TimestampPrefix() string {
	return fmt.Sprintf("%s  ", Color(time.Now().Format("15:04:05"), Gray))
}
