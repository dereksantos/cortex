// Package ui holds the terminal color helpers and display constants shared
// across the loop packages (tools, render, session, main). Centralizing them
// keeps the color palette and the NO_COLOR convention in one place so the
// extracted packages don't each re-declare the palette.
package ui

import (
	"fmt"
	"os"
)

// Display glyphs.
const (
	PromptGlyph = "❯"
	IconCortex  = "◆" // assistant / cortex
	IconTool    = "▸" // tool action
	IconUser    = "❯" // user
)

// ANSI color codes.
const (
	Red    = "\033[31m"
	Cyan   = "\033[36m"
	Green  = "\033[32m"
	Blue   = "\033[34m"
	Yellow = "\033[33m"
	Gray   = "\033[90m" // bright black, for dim status text
	Reset  = "\033[0m"
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
