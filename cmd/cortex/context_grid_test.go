package main

import (
	"strings"
	"testing"
)

// TestComputeContextGridEmpty covers the "nothing assembled yet" case: every
// component and the tail are zero tokens, so every cell in the fixed
// 128-cell frame is free — no component gets even its floor-1 cell, since
// the floor only applies to a genuinely non-zero component.
func TestComputeContextGridEmpty(t *testing.T) {
	placement := computeContextGrid(nil, 0, 128000)
	if len(placement.glyphs) != gridCells {
		t.Fatalf("len(glyphs) = %d, want %d", len(placement.glyphs), gridCells)
	}
	for i, g := range placement.glyphs {
		if g != glyphFree {
			t.Errorf("glyphs[%d] = %q, want free glyph %q", i, g, glyphFree)
		}
	}
	if placement.tailStart != 0 || placement.tailCells != 0 {
		t.Errorf("tailStart/tailCells = %d/%d, want 0/0", placement.tailStart, placement.tailCells)
	}
}

// TestComputeContextGridMockTurn14 pins the exact 128-cell layout for the
// approved mock's turn-14 figures: a 128,000-token window (cellSize=1000)
// with system=2.1k, outline=6.4k (41 entries), memory=1.2k (9 notes),
// skills=0.5k (3 skills), tail=22.4k (7 turns verbatim, demote >64k, drain
// to 42.7k), leaving free=95.4k — every one of those legend figures sums
// exactly to the 128k window, so this is the mock's own numbers, not an
// invented fixture.
func TestComputeContextGridMockTurn14(t *testing.T) {
	const window = 128000
	components := []gridComponent{
		{glyphSystem, 2100},
		{glyphOutline, 6400},
		{glyphMemory, 1200},
		{glyphSkills, 500},
	}
	const tailTokens = 22400
	const hiWatermark = 64000

	placement := computeContextGrid(components, tailTokens, window)

	// Cell counts: round(tokens/1000) per component (skills' 0.5 rounds up
	// to 1, Go's math.Round rounding half away from zero), tail
	// round(22.4)=22, free absorbs whatever's left (96 cells — not
	// round(95.4)=95 — since free is never itself rounded from a token
	// count, it just fills the remaining frame).
	wantCounts := map[rune]int{
		glyphSystem:  2,
		glyphOutline: 6,
		glyphMemory:  1,
		glyphSkills:  1,
		glyphTail:    22,
		glyphFree:    96,
	}
	gotCounts := map[rune]int{}
	for _, g := range placement.glyphs {
		gotCounts[g]++
	}
	for glyph, want := range wantCounts {
		if gotCounts[glyph] != want {
			t.Errorf("cell count for %q = %d, want %d", glyph, gotCounts[glyph], want)
		}
	}
	if placement.tailStart != 10 || placement.tailCells != 22 {
		t.Errorf("tailStart/tailCells = %d/%d, want 10/22", placement.tailStart, placement.tailCells)
	}

	gotLines := renderContextGrid(placement, window, hiWatermark)
	wantLines := []string{
		"  0k  ██▓▓▓▓▓▓▒░■■■■■■",
		" 16k  ■■■■■■■■■■■■■■■■",
		" 32k  ················",
		" 48k  ················",
		" 64k  ················  ◂ demote",
		" 80k  ················",
		" 96k  ················",
		"112k  ················",
	}
	if len(gotLines) != len(wantLines) {
		t.Fatalf("renderContextGrid returned %d lines, want %d:\n%s", len(gotLines), len(wantLines), strings.Join(gotLines, "\n"))
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Errorf("row %d = %q, want %q", i, gotLines[i], wantLines[i])
		}
	}
}

// TestGridCellsForFloor covers the floor-1-cell rule: a genuinely non-zero
// component always shows at least one cell, even when it rounds to zero at
// the window's cell size (a 1-token component in a window with an
// enormous per-cell span).
func TestGridCellsForFloor(t *testing.T) {
	const window = 1_000_000 // cellSize = 7812.5
	cellSize := gridCellSize(window)
	if got := gridCellsFor(1, cellSize); got != 1 {
		t.Errorf("gridCellsFor(1, %v) = %d, want 1 (floor)", cellSize, got)
	}
	if got := gridCellsFor(0, cellSize); got != 0 {
		t.Errorf("gridCellsFor(0, %v) = %d, want 0 (zero stays zero, no floor)", cellSize, got)
	}
}

// TestComputeContextGridOverfullClamp covers the "later components
// truncate — tail last" rule: when the head component alone already fills
// the frame, tail is truncated to zero and free never gets a chance to
// absorb anything either.
func TestComputeContextGridOverfullClamp(t *testing.T) {
	const window = 1000 // cellSize = 1000/128 = 7.8125
	components := []gridComponent{{glyphSystem, 200_000}}
	placement := computeContextGrid(components, 50_000, window)

	if len(placement.glyphs) != gridCells {
		t.Fatalf("len(glyphs) = %d, want %d", len(placement.glyphs), gridCells)
	}
	for i, g := range placement.glyphs {
		if g != glyphSystem {
			t.Errorf("glyphs[%d] = %q, want system glyph %q (clamped, no room for anything else)", i, g, glyphSystem)
		}
	}
	if placement.tailStart != gridCells {
		t.Errorf("tailStart = %d, want %d (system consumed every cell)", placement.tailStart, gridCells)
	}
	if placement.tailCells != 0 {
		t.Errorf("tailCells = %d, want 0 (no room left for tail)", placement.tailCells)
	}
}

// TestTailCellPastWatermark covers the red-vs-green split: a tail cell's
// classification flips exactly at the cell whose starting offset first
// reaches the high watermark, matching the demote tick's own row placement
// (TestComputeContextGridMockTurn14 ticks row 4, offset 64000, for the same
// window/watermark pair).
func TestTailCellPastWatermark(t *testing.T) {
	const window = 128000     // cellSize = 1000
	const hiWatermark = 64000 // exactly cell index 64's starting offset

	tests := []struct {
		idx  int
		want bool
	}{
		{0, false},
		{63, false}, // offset 63000 < 64000
		{64, true},  // offset 64000 >= 64000: the boundary cell itself
		{65, true},  // offset 65000 > 64000
		{127, true},
	}
	for _, tt := range tests {
		if got := tailCellPastWatermark(tt.idx, window, hiWatermark); got != tt.want {
			t.Errorf("tailCellPastWatermark(%d, %d, %d) = %v, want %v", tt.idx, window, hiWatermark, got, tt.want)
		}
	}

	// No watermark at all (e.g. no working set yet, hiWatermark<=0): never
	// past it, no matter the index.
	if got := tailCellPastWatermark(127, window, 0); got {
		t.Errorf("tailCellPastWatermark with hiWatermark=0 = %v, want false", got)
	}
}

// TestDemoteRowIndex covers the demote tick's row selection: the first row
// whose starting offset equals or exceeds the high watermark, or -1 when
// there's no watermark to show or it falls beyond the last row's start.
func TestDemoteRowIndex(t *testing.T) {
	tests := []struct {
		name         string
		window, hiWM int
		want         int
	}{
		{"no watermark yet", 128000, 0, -1},
		{"exact row boundary", 128000, 64000, 4},
		{"between rows rounds up to the next row", 128000, 50000, 4}, // row3=48000<50000, row4=64000>=50000
		{"watermark beyond the grid", 128000, 999_000_000, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := demoteRowIndex(tt.window, tt.hiWM); got != tt.want {
				t.Errorf("demoteRowIndex(%d, %d) = %d, want %d", tt.window, tt.hiWM, got, tt.want)
			}
		})
	}
}

// TestRenderContextGrid256kWindow covers cell scaling and gutter labels at
// a larger window: 256,000 tokens gives cellSize=2000, so each row spans
// 32,000 tokens and the gutters read 0k/32k/64k/.../224k — not the 128k
// window's 0k/16k/32k/... ladder.
func TestRenderContextGrid256kWindow(t *testing.T) {
	const window = 256000
	placement := computeContextGrid(nil, 0, window)
	lines := renderContextGrid(placement, window, 0)

	wantGutters := []string{"0k", "32k", "64k", "96k", "128k", "160k", "192k", "224k"}
	if len(lines) != len(wantGutters) {
		t.Fatalf("renderContextGrid returned %d lines, want %d", len(lines), len(wantGutters))
	}
	for i, want := range wantGutters {
		if !strings.Contains(lines[i], want) {
			t.Errorf("row %d = %q, want gutter %q", i, lines[i], want)
		}
		// Every cell is free (no components, no tail) at this window.
		if !strings.Contains(lines[i], strings.Repeat(string(glyphFree), gridCols)) {
			t.Errorf("row %d = %q, want %d free cells", i, lines[i], gridCols)
		}
	}
	// No demote tick anywhere: hiWatermark was 0.
	for i, line := range lines {
		if strings.Contains(line, "demote") {
			t.Errorf("row %d = %q, should not carry a demote tick (no watermark)", i, line)
		}
	}
}

// TestGridGutterWidth covers the right-aligned gutter column width tracking
// the largest row offset's digit count as the window grows.
func TestGridGutterWidth(t *testing.T) {
	tests := []struct {
		window int
		want   int
	}{
		{128000, 4}, // last row offset 112000 tokens -> "112k"
		{256000, 4}, // last row offset 224000 tokens -> "224k"
		{8000, 2},   // cellSize=62.5, last row offset 7000 tokens -> "7k"
	}
	for _, tt := range tests {
		if got := gridGutterWidth(tt.window); got != tt.want {
			t.Errorf("gridGutterWidth(%d) = %d, want %d", tt.window, got, tt.want)
		}
	}
}
