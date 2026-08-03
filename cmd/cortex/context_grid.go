// context_grid.go — the /context report's square glyph grid: a fixed
// 128-cell (8 rows × 16 cols) spatial map of the whole model window, one
// glyph per component (system prompt, outline, memory index, skills index,
// hydrated tail, free space). Unlike the prompt row's renderContextBar
// (contextbar.go), which rescales its fill to whatever width the caller
// asks for, this frame never changes shape: only cell size (window/128)
// moves as the window does, so the grid always reads as "the whole window,
// to scale" rather than "however much is currently used."
//
// Split the same way contextbar.go splits pure computation from color: the
// placement/row arithmetic below is pure and unit-testable without ANSI;
// contextGridLines in context_cmd.go composes the coloring on top.
package main

import (
	"fmt"
	"math"
)

// gridRows/gridCols/gridCells: the frame. Always 8×16 = 128 cells,
// regardless of window size — see the package doc comment above.
const (
	gridRows  = 8
	gridCols  = 16
	gridCells = gridRows * gridCols
)

// The grid's glyph vocabulary, one per wire-order component plus tail and
// free space. Colors are assigned alongside these in context_cmd.go's
// contextGridCellColor, not here — this file stays plain-glyph pure.
const (
	glyphSystem  = '█'
	glyphOutline = '▓'
	glyphMemory  = '▒'
	glyphSkills  = '░'
	glyphTail    = '■'
	glyphFree    = '·'
)

// gridComponent is one wire-order piece of zone A (the stable prefix) sized
// in tokens: system prompt, session outline, memory index, skills index, in
// that order. Tail and free are handled separately by computeContextGrid
// since they need watermark-aware treatment the head components don't.
type gridComponent struct {
	glyph  rune
	tokens int
}

// gridCellSize returns how many tokens one grid cell spans: window/128. A
// non-positive window has no meaningful span; callers treat that as "can't
// place anything" (gridCellsFor already floors to 0 for tokens<=0, and a
// zero/negative cellSize would divide-by-zero or invert the scale, so this
// pins 1 as a safe non-zero fallback that never over-counts cells for a
// degenerate window).
func gridCellSize(window int) float64 {
	if window <= 0 {
		return 1
	}
	return float64(window) / float64(gridCells)
}

// gridCellsFor converts a token count to a cell count at the given cell
// span: proportional (rounded), with a floor of 1 cell for any non-zero
// token count — a component that has *something* always shows *something*,
// even a 40-token skills index in a 128k window that would otherwise round
// to 0.
func gridCellsFor(tokens int, cellSize float64) int {
	if tokens <= 0 || cellSize <= 0 {
		return 0
	}
	n := int(math.Round(float64(tokens) / cellSize))
	if n < 1 {
		n = 1
	}
	return n
}

// contextGridPlacement is the fully assigned 128-cell frame: one glyph per
// cell in row-major order (index 0 = row 0 col 0, index 127 = row 7 col 15),
// plus where the tail segment landed (grid-cell indices, not tokens) so
// context_cmd.go's coloring pass can classify each tail cell against the
// demote watermark without re-deriving the fill arithmetic.
type contextGridPlacement struct {
	glyphs    []rune
	tailStart int
	tailCells int
}

// computeContextGrid assigns glyphs to the fixed 128-cell frame in wire
// order: components (system, outline, memory, skills — whatever order the
// caller passes, expected to be that one), then tail, then free.
//
// Each component gets gridCellsFor(tokens, cellSize) cells, clamped to
// whatever room remains in the 128-cell frame. Free is never itself
// rounded from a token count — it simply absorbs however many cells are
// left after everything else is placed, so a component's rounding never
// leaves an off-by-one gap unaccounted for. If the head + tail components
// together already fill (or overflow) the frame, later components in the
// sequence get truncated — tail, being placed last (right before free),
// is the one most exposed to truncation, and free legitimately drops to
// zero cells in that case.
func computeContextGrid(components []gridComponent, tailTokens, window int) contextGridPlacement {
	cellSize := gridCellSize(window)
	glyphs := make([]rune, 0, gridCells)

	place := func(glyph rune, tokens int) int {
		n := gridCellsFor(tokens, cellSize)
		remaining := gridCells - len(glyphs)
		if n > remaining {
			n = remaining
		}
		if n < 0 {
			n = 0
		}
		for i := 0; i < n; i++ {
			glyphs = append(glyphs, glyph)
		}
		return n
	}

	for _, c := range components {
		place(c.glyph, c.tokens)
	}

	tailStart := len(glyphs)
	tailCells := place(glyphTail, tailTokens)

	for len(glyphs) < gridCells {
		glyphs = append(glyphs, glyphFree)
	}

	return contextGridPlacement{glyphs: glyphs, tailStart: tailStart, tailCells: tailCells}
}

// gridRowOffset returns the starting token offset of grid row r (0-based):
// the same quantity the row's left-gutter label renders (via humanK) and
// the value the demote watermark tick is compared against.
func gridRowOffset(r int, cellSize float64) int {
	return int(float64(r*gridCols) * cellSize)
}

// demoteRowIndex returns the row whose starting token offset equals or
// first exceeds the high watermark — the row that gets the trailing
// "◂ demote" tick — or -1 when there's no watermark to show (hiWatermark
// <= 0, e.g. no working set yet) or the watermark falls beyond the last
// row's start (an unusually large watermark relative to the window).
func demoteRowIndex(window, hiWatermark int) int {
	if hiWatermark <= 0 {
		return -1
	}
	cellSize := gridCellSize(window)
	for r := 0; r < gridRows; r++ {
		if gridRowOffset(r, cellSize) >= hiWatermark {
			return r
		}
	}
	return -1
}

// tailCellPastWatermark reports whether the tail cell at grid index idx
// (row-major, 0-based over the full 128-cell frame) sits at or past the
// high watermark — the red-vs-green split the grid mechanics call for.
// idx's "absolute token offset" is idx*cellSize, the same offset the row
// gutters and demoteRowIndex use, deliberately not adjusted for how far
// into the frame the tail segment actually starts (see context_grid.go's
// package doc and the task's grid-mechanics note: the tick and the cell
// split share one coordinate system, the frame itself).
func tailCellPastWatermark(idx, window, hiWatermark int) bool {
	if hiWatermark <= 0 {
		return false
	}
	cellSize := gridCellSize(window)
	return float64(idx)*cellSize >= float64(hiWatermark)
}

// gridGutterLabel renders a row's starting token offset the way the grid's
// left gutter always does: an integer count of thousands with an explicit
// "k" suffix, including "0k" — plain humanK (loopui.HumanK) only appends
// "k" above 1000 and would misrender the very first row's "0" as bare "0".
// Row offsets are always exact multiples of gridCols*cellSize by
// construction (gridRowOffset), so integer-dividing by 1000 loses no
// row-boundary precision in practice. width right-pads the "Nk" text so
// all 8 rows' gutters line up regardless of how many digits the largest
// row offset needs (a 256k window's "112k" vs a small window's "0k").
func gridGutterLabel(offsetTokens, width int) string {
	return fmt.Sprintf("%*s", width, fmt.Sprintf("%dk", offsetTokens/1000))
}

// gridGutterWidth returns the character width the widest row-offset label
// needs — the last row's offset, since offsets only grow — so every
// gutter in the grid right-aligns to one common column.
func gridGutterWidth(window int) int {
	cellSize := gridCellSize(window)
	last := gridRowOffset(gridRows-1, cellSize)
	return len(fmt.Sprintf("%dk", last/1000))
}

// renderContextGrid renders the pure, uncolored 8-row grid: one line per
// row, "<gutter>  <16 glyph cells>" plus a trailing "  ◂ demote" on the row
// demoteRowIndex identifies (omitted when there's no watermark to show).
// Color composition (context_cmd.go's contextGridLines) wraps this same
// placement with ANSI per-glyph, so the two stay pixel-identical in
// structure.
func renderContextGrid(placement contextGridPlacement, window, hiWatermark int) []string {
	demoteRow := demoteRowIndex(window, hiWatermark)
	cellSize := gridCellSize(window)
	width := gridGutterWidth(window)
	lines := make([]string, 0, gridRows)
	for r := 0; r < gridRows; r++ {
		offset := gridRowOffset(r, cellSize)
		cells := string(placement.glyphs[r*gridCols : (r+1)*gridCols])
		line := gridGutterLabel(offset, width) + "  " + cells
		if r == demoteRow {
			line += "  ◂ demote"
		}
		lines = append(lines, line)
	}
	return lines
}
