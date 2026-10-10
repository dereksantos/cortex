// Display constants shared across the loop packages (tools, render, session,
// main). Color roles, NO_COLOR, and width helpers live in internal/style.
package tools

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// PromptGlyph is the input affordance at the end of the prompt row. The REPL
// dropped its icon set on 2026-07-19 (ANSI color carries role distinctions),
// but Derek brought this one mark back on 2026-10-09 — the ASCII ">" read as
// unfinished — so the prompt is the one glyph the REPL keeps.
const PromptGlyph = "❯"

// SetColorDisabledForTest pins NO_COLOR behavior for the duration of one
// test and returns a func that restores the previous value. Exported ONLY for
// that purpose; it delegates to style.ForceColor, the one color switch.
func SetColorDisabledForTest(disabled bool) (restore func()) {
	return style.ForceColor(!disabled)
}

// richRenderDisabled honors the same CORTEX_LOOP_RENDER=0 escape hatch
// cmd/cortex's renderEnabled reads: with it set, the terminal falls back to
// the plainest output the REPL has — which now also means the flat one-line
// tool actions, with no diff body under a file edit. Read once at startup;
// tests set it directly.
var richRenderDisabled = isOff(os.Getenv("CORTEX_LOOP_RENDER"))

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// TimestampPrefix renders the "HH:MM:SS  " gutter shown before every printed
// line — user/assistant turns (cmd/cortex's gutterPrefix) and tool-action
// lines (printToolAction) alike — so a scrolled session stays vertically
// aligned regardless of which side printed the line. Always dim (2026-07-19):
// the timestamp carries no per-role/per-tool color, so it reads as one
// consistent margin rather than a row of color-coded tags.
func TimestampPrefix() string { return Gutter(Now()) }

// Gutter renders the timestamp gutter for ts.
func Gutter(ts time.Time) string {
	return fmt.Sprintf("%s  ", style.Paint(ts.Format("15:04:05"), style.Dim))
}

// Now is the REPL's display clock: every gutter timestamp reads it. A var so
// render goldens can pin the time.
var Now = time.Now
