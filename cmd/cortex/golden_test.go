package main

// golden_test.go — the /context grid's golden snapshot (issue #112).
//
// The grid is the one terminal render path that lives in package main (its
// pure arithmetic in context_grid.go, its coloring in context_cmd.go), so
// its snapshot lives here; the other five render paths' snapshots live in
// internal/tools (golden_test.go + snapshot_test.go), sharing that package's
// goldenFrame helper. Same mechanism: testdata/<name>.golden next to the
// test, byte-for-byte comparison, `-update` regenerates.
//
// The grid frame has no wall clock or elapsed time — it is a pure function
// of (window, components, tail, high watermark) — so no normalization is
// needed and the golden is stable by construction.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// updateGridGoldens is the -update flag (issue #112), the cmd/cortex-side
// copy of internal/tools' updateGoldens (the two test binaries can't share
// a flag var): registered on the global flag set in init so `go test
// ./cmd/cortex -update` forwards it to the test binary and a bare
// `go test ./...` is unaffected.
var updateGridGoldens = flag.Bool("update", false, "write testdata/*.golden files instead of comparing against them")

// gridGolden compares got against testdata/name+".golden" and fails with the
// full diff on mismatch; with -update it (re)writes the file.
func gridGolden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name + ".golden"
	if *updateGridGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden %s: mkdir: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("golden %s: write: %v", path, err)
		}
		t.Logf("golden %s: updated (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: read: %v — run with -update to create it", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch:\n--- want ---\n%s\n--- got ---\n%s\n(re-run with -update to accept the new frame)",
			path, want, got)
	}
}

// TestContextGridGolden pins the /context grid frame as it actually renders
// in the terminal — colored (coloredContextGridLines) and NO_COLOR — for
// the approved mock's turn-14 figures: a 128,000-token window (cellSize
// 1000) with system=2.1k, outline=6.4k, memory=1.2k, skills=0.5k, tail=22.4k,
// demote watermark at 64k. Those are the same numbers
// TestComputeContextGridMockTurn14 pins for the pure layout, so the colored
// frame and the uncolored layout can't drift apart silently: the coloring
// pass wraps the same glyphs, and the grid's own vocabulary (█▓▒░■·◂ — the
// deliberate block-element design of 2026-07-19, NOT box-drawing) is what
// the snapshot records.
//
// Color is PINNED via tools.SetColorDisabledForTest so the goldens render
// identically regardless of the developer's NO_COLOR environment: the
// colored subtest forces color on, the NO_COLOR one forces it off and
// snapshots the frame that a NO_COLOR terminal actually shows.
func TestContextGridGolden(t *testing.T) {
	const (
		window      = 128000
		hiWatermark = 64000
	)
	components := []gridComponent{
		{glyphSystem, 2100},
		{glyphOutline, 6400},
		{glyphMemory, 1200},
		{glyphSkills, 500},
	}
	const tailTokens = 22400

	placement := computeContextGrid(components, tailTokens, window)

	t.Run("colored", func(t *testing.T) {
		defer tools.SetColorDisabledForTest(false)()
		gridGolden(t, "context_grid_colored", strings.Join(coloredContextGridLines(placement, window, hiWatermark), "\n"))
	})

	t.Run("no_color", func(t *testing.T) {
		defer tools.SetColorDisabledForTest(true)()
		got := strings.Join(coloredContextGridLines(placement, window, hiWatermark), "\n")
		want := strings.Join(renderContextGrid(placement, window, hiWatermark), "\n")
		if got != want {
			t.Fatalf("NO_COLOR coloredContextGridLines differs from the pure uncolored form:\n--- NO_COLOR colored ---\n%s\n--- renderContextGrid ---\n%s", got, want)
		}
		gridGolden(t, "context_grid_no_color", got)
	})
}

// TestContextGridWindowGolden pins the /context grid frame across a sweep of
// representative model-window sizes (issue #112): 8k (a small local model)
// and 256k (a large window, whose gutter ladder is 0k/32k/…/224k and whose
// cell size is 2000 tokens). The 128k window is already pinned by
// TestContextGridGolden for its turn-14 figures, so it stays out of this
// sweep — its golden would be a duplicate.
//
// Components, tail, and the demote watermark scale proportionally with the
// window (as fractions of it: system 1/128, outline 1/20, memory 1/107,
// skills 1/256, tail 1/6, watermark 1/2), so every size shows all segments —
// system, outline, memory, skills, tail, free — and a demote tick, and the
// frame's scaling is what the sweep pins: how cell size, the gutter ladder,
// the demote-tick row, and the tail's red-vs-green split move as the window
// does. A regression in any of those at a window other than 128k can't hide
// behind the single mock's snapshot.
//
// Color is pinned like TestContextGridGolden: the colored subtest renders
// with color forced on, the no_color subtest with it forced off.
func TestContextGridWindowGolden(t *testing.T) {
	tests := []struct {
		name   string
		window int
	}{
		{"w8k", 8000},
		{"w256k", 256000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			components := []gridComponent{
				{glyphSystem, tt.window / 128},
				{glyphOutline, tt.window / 20},
				{glyphMemory, tt.window / 107},
				{glyphSkills, tt.window / 256},
			}
			tailTokens := tt.window / 6
			hiWatermark := tt.window / 2

			placement := computeContextGrid(components, tailTokens, tt.window)

			t.Run("colored", func(t *testing.T) {
				defer tools.SetColorDisabledForTest(false)()
				gridGolden(t, "context_grid_"+tt.name+"_colored",
					strings.Join(coloredContextGridLines(placement, tt.window, hiWatermark), "\n"))
			})

			t.Run("no_color", func(t *testing.T) {
				defer tools.SetColorDisabledForTest(true)()
				got := strings.Join(coloredContextGridLines(placement, tt.window, hiWatermark), "\n")
				want := strings.Join(renderContextGrid(placement, tt.window, hiWatermark), "\n")
				if got != want {
					t.Fatalf("NO_COLOR coloredContextGridLines differs from the pure uncolored form:\n--- NO_COLOR colored ---\n%s\n--- renderContextGrid ---\n%s", got, want)
				}
			})
		})
	}
}

// --- glyph contract (the grid's half) ---------------------------------------

// The 2026-07-19 plain-text decision bans box-drawing (U+2500–U+257F) and the
// retired icon set (❯◆▸✻⤷⚠✦) on every terminal render path. The grid is the
// deliberate exception: its cell glyphs █▓▒░■· (plus the demote tick ◂) are
// its design. The audit below checks the grid's REAL render functions
// (renderContextGrid, coloredContextGridLines) — not a re-implementation —
// so a glyph regression in either one fails here. The other render paths
// run the same audit in the packages that own them (internal/tools,
// internal/lineedit); each package owns its own helpers.

const (
	// bannedGridIconSet is the retired icon glyphs (see the package doc).
	bannedGridIconSet = "❯◆▸✻⤷⚠✦"
	// gridGlyphs is the grid's intentional block-element vocabulary.
	gridGlyphs = "█▓▒░■·◂"
)

// gridRuneAllowed reports whether r may appear in the /context grid's frame:
// box-drawing and retired icons are banned (as everywhere), grid glyphs are
// allowed, and anything else must be printable ASCII (… is not expected in
// the grid but is allowed, matching the plain-path audit's punctuation
// tolerance).
func gridRuneAllowed(r rune) bool {
	if r >= 0x2500 && r <= 0x257F {
		return false // box-drawing: banned on the grid too
	}
	if strings.ContainsRune(bannedGridIconSet, r) {
		return false
	}
	if strings.ContainsRune(gridGlyphs, r) {
		return true
	}
	return r >= 0x20 && r < 0x7f || r == '…'
}

// glyphAudit scans a frame against an allowed-rune predicate and returns one
// line per offender ("" when clean). Control characters the terminal
// protocol itself needs — CR/LF and ESC (ANSI color/cursor codes, present in
// colored frames by design) — are never flagged: the decision under test is
// about the GLYPHS a frame prints, not the escape plumbing that carries its
// colors.
func glyphAudit(frame string, allowed func(rune) bool) string {
	var bad []string
	for i, line := range strings.Split(frame, "\n") {
		for _, r := range line {
			if r == '\r' || r == '\x1b' {
				continue
			}
			if !allowed(r) {
				bad = append(bad, fmt.Sprintf("line %d: U+%04X %q", i+1, r, r))
			}
		}
	}
	return strings.Join(bad, "\n")
}

// TestContextGridGlyphContract enforces the grid's glyph vocabulary on the
// grid's real render functions, in both color modes: no box-drawing, no
// retired icons, nothing outside the sanctioned set — and the positive
// check that the grid's cell glyphs (█▓▒░■·) and the demote tick (◂) are
// still present, since a "simplification" that strips them changes the
// report's design.
func TestContextGridGlyphContract(t *testing.T) {
	components := []gridComponent{
		{glyphSystem, 2100},
		{glyphOutline, 6400},
		{glyphMemory, 1200},
		{glyphSkills, 500},
	}
	const (
		window      = 128000
		tailTokens  = 22400
		hiWatermark = 64000
	)
	placement := computeContextGrid(components, tailTokens, window)

	{
		defer tools.SetColorDisabledForTest(false)()
		colored := strings.Join(coloredContextGridLines(placement, window, hiWatermark), "\n")
		if bad := glyphAudit(colored, gridRuneAllowed); bad != "" {
			t.Errorf("colored /context grid leaves its sanctioned vocabulary:\n%s\nframe:\n%s", bad, colored)
		}
	}
	{
		defer tools.SetColorDisabledForTest(true)()
		plain := strings.Join(renderContextGrid(placement, window, hiWatermark), "\n")
		if bad := glyphAudit(plain, gridRuneAllowed); bad != "" {
			t.Errorf("uncolored /context grid leaves its sanctioned vocabulary:\n%s\nframe:\n%s", bad, plain)
		}
		for _, g := range []string{"█", "▓", "▒", "░", "■", "·", "◂ demote"} {
			if !strings.Contains(plain, g) {
				t.Errorf("/context grid missing glyph %q:\n%s", g, plain)
			}
		}
	}
}
