// Package ui holds the terminal color helpers and display constants shared
// across the loop packages (tools, render, session, main). Centralizing them
// keeps the color palette and the NO_COLOR convention in one place so the
// extracted packages don't each re-declare the palette.
package tools

import (
	"fmt"
	"os"
	"time"
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

// TimestampPrefix renders the "HH:MM:SS  " gutter shown before every printed
// line — user/assistant turns (cmd/cortex's gutterPrefix) and tool-action
// lines (printToolAction) alike — so a scrolled session stays vertically
// aligned regardless of which side printed the line. Always gray (2026-07-19):
// the timestamp no longer carries a per-role/per-tool color, so it reads as
// one consistent margin rather than a row of color-coded tags.
func TimestampPrefix() string {
	return fmt.Sprintf("%s  ", Color(time.Now().Format("15:04:05"), Gray))
}
