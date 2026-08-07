// context_view.go — /context as the first consumer of the alternate-screen
// inspector (internal/lineedit/inspect.go).
//
// This adds no rendering. The grid, the legend, and the prefix-cache headline
// are exactly context_cmd.go's and context_grid.go's; the view only decides
// which of those lines is the title bar and which are the scrollable body.
// The two forms of /context therefore cannot drift: both are
// contextReportLines.
//
// Which form the user gets is a strict enhancement gate. The inspector needs
// an interactive line editor (stdin is a TTY) *and* the rich-render path
// (renderEnabled: stdout is a TTY, NO_COLOR unset, CORTEX_LOOP_RENDER not
// disabled). Anything scripted or piped — a driver, CI, a test, `cortex |
// less` — misses the gate and prints the same plain scrolling report it always
// did.
package main

import (
	"github.com/dereksantos/cortex/internal/lineedit"
)

// contextView adapts the /context report to the inspector's View contract.
// Both methods re-pull from the live session, so the map stays current while
// the user reads it — a turn finishing in the background (or a resize)
// repaints it within one poll tick.
type contextView struct{ cs *CortexSession }

// Title is the report's own header line ("context — <model> · <window>
// window · turn N"), promoted to the inspector's title row so it stays
// pinned while the body scrolls under it.
func (v contextView) Title() string { return v.cs.contextHeaderLine() }

// Lines is the report minus that header (the title row already carries it)
// and minus the blank that followed it, since the inspector's title row
// supplies its own separation.
//
// width is ignored: the grid is a fixed 8x16 frame by design — it reads as
// "the whole window, to scale" precisely because it never reflows
// (context_grid.go) — and the legend rows are short. The harness clamps
// anything over-wide on a narrow terminal rather than wrapping it.
func (v contextView) Lines(width int) []string {
	lines := v.cs.contextReportLines()
	if len(lines) > 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return lines
}

// contextInspectable reports whether /context should open the full-screen
// inspector rather than print the plain scrolling report. See the file comment
// for why both conditions are required.
func contextInspectable(editor *lineedit.Terminal) bool {
	return editor != nil && renderEnabled()
}
