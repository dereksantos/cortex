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
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// The NO_COLOR subtest does NOT flip tools.colorDisabled from here (it lives
// in internal/tools and package main can't pin it): it instead checks that
// coloredContextGridLines, with the ANSI wrap stripped, equals
// renderContextGrid's pure uncolored form — the structural identity the
// two renderers promise (coloring wraps the same glyphs), and the frame the
// NO_COLOR terminal actually shows.
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
		gridGolden(t, "context_grid_colored", strings.Join(coloredContextGridLines(placement, window, hiWatermark), "\n"))
	})

	t.Run("no_color", func(t *testing.T) {
		colored := coloredContextGridLines(placement, window, hiWatermark)
		var sb strings.Builder
		for i, line := range colored {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(stripANSI(line))
		}
		want := strings.Join(renderContextGrid(placement, window, hiWatermark), "\n")
		if sb.String() != want {
			t.Fatalf("colored grid stripped of ANSI differs from the pure uncolored form:\n--- stripped colored ---\n%s\n--- renderContextGrid ---\n%s", sb.String(), want)
		}
		gridGolden(t, "context_grid_no_color", sb.String())
	})
}

// TestContextGridWindowGolden pins the /context grid frame across a sweep of
// representative model-window sizes (issue #112): 8k (a small local model),
// 128k (the default coding window), and 256k (a large window, whose gutter
// ladder is 0k/32k/…/224k and whose cell size is 2000 tokens). The turn-14
// mock (128k) is already pinned above for its own numbers; this sweep pins
// the *scaling* — how cell size, the gutter ladder, the demote-tick row, and
// the tail's red-vs-green split move as the window does — so a regression in
// any of those at a window other than 128k can't hide behind the single
// mock's snapshot.
//
// Each window keeps the same turn-14 component ratios (system 2.1k, outline
// 6.4k, memory 1.2k, skills 0.5k, tail 22.4k, demote watermark at 64k) so the
// only thing that moves between goldens is the window: the cell size changes,
// and so do the gutter labels and the row the demote tick lands on. The frame
// is a pure function of (window, components, tail, hiWatermark) — no wall
// clock — so the goldens are stable by construction.
//
// The NO_COLOR subtest is structural (colored stripped of ANSI equals
// renderContextGrid), not a byte-golden: it pins the same invariant the
// turn-14 test does, for each window, so the coloring pass and the pure
// layout can't drift apart at any size.
func TestContextGridWindowGolden(t *testing.T) {
	components := []gridComponent{
		{glyphSystem, 2100},
		{glyphOutline, 6400},
		{glyphMemory, 1200},
		{glyphSkills, 500},
	}
	const (
		tailTokens  = 22400
		hiWatermark = 64000
	)

	tests := []struct {
		name   string
		window int
	}{
		{"w8k", 8000},
		{"w128k", 128000},
		{"w256k", 256000},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			placement := computeContextGrid(components, tailTokens, tt.window)

			t.Run("colored", func(t *testing.T) {
				gridGolden(t, "context_grid_"+tt.name+"_colored",
					strings.Join(coloredContextGridLines(placement, tt.window, hiWatermark), "\n"))
			})

			t.Run("no_color", func(t *testing.T) {
				colored := coloredContextGridLines(placement, tt.window, hiWatermark)
				var sb strings.Builder
				for i, line := range colored {
					if i > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(stripANSI(line))
				}
				want := strings.Join(renderContextGrid(placement, tt.window, hiWatermark), "\n")
				if sb.String() != want {
					t.Fatalf("colored grid stripped of ANSI differs from the pure uncolored form:\n--- stripped colored ---\n%s\n--- renderContextGrid ---\n%s", sb.String(), want)
				}
			})
		})
	}
}
