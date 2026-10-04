package tools

// golden_test.go — the shared golden-file mechanism for issue #112's
// snapshot tests, plus the glyph-contract test that enforces the 2026-07-19
// plain-text decision across the terminal render paths.
//
// The helper is stdlib-only (testing + os + path/filepath + regexp +
// strings + flag) and lives in internal/tools so the snapshot tests in this
// package and the /context grid snapshot in cmd/cortex share one mechanism:
// a golden frame is a testdata/<name>.golden file next to the test, compared
// byte-for-byte, and `go test ./internal/tools -update` (or
// `go test ./cmd/cortex -update` for the grid) regenerates it. Frames must be
// DETERMINISTIC: the helpers below normalize the two quantities that vary run
// to run — the wall-clock gutter (time.Now()) and the per-call elapsed times
// — so a render change shows up as a golden diff in review and nothing else
// does.
//
// The contract test covers the six terminal render paths: the tool-action
// line, the file diff, nested subagent lines, the approval prompt, the
// anchored status row, and the /context grid. The first five must stay free
// of box-drawing (U+2500–U+257F) and of the retired icon set (❯◆▸✻⤷⚠✦);
// the grid is the deliberate exception — its cell glyphs █▓▒░■· are its design
// (cmd/cortex/context_grid.go), and the demote tick ◂ and middle dot · are
// structural. The grid's real colored frame is snapshotted in cmd/cortex
// (the only package that can import its render functions); here the grid
// path is checked against a re-implementation of the same pure arithmetic,
// so a glyph regression on either side fails in the package that owns it.
//
// The -update flag is registered on the test binary's global flag set — the
// stdlib-only way to add a test flag: `go test ./internal/tools -update`
// forwards -update to the test binary, the generated main parses it, and a
// bare `go test ./...` (no extra args, no TTY) is unaffected.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// goldenFrame compares got against dir/name+".golden" and fails with the full
// diff on mismatch. With the -update flag it (re)writes the file instead. The
// frame is normalized first; the golden files on disk already carry the
// normalized form. dir is the package's testdata in the render-path snapshot
// tests and a scratch dir in the self-test below.
func goldenFrame(t *testing.T, dir, name, got string) {
	t.Helper()
	got = normalizeFrame(got)
	path := filepath.Join(dir, name+".golden")
	if *updateGoldens {
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

// TestGoldenFrameCompareAndUpdate pins the helper's contract: -update
// rewrites the golden file, and a compare of the same frame then passes
// without recording a failure. It runs against a scratch dir, so the
// package's testdata is never touched. (The "changed frame fails" direction
// is verified by the actual golden tests: a stale .golden file makes them
// fail with the recorded diff, which is the whole point of the mechanism.)
func TestGoldenFrameCompareAndUpdate(t *testing.T) {
	dir := t.TempDir()
	prev := *updateGoldens
	defer func() { *updateGoldens = prev }()

	// -update mode: the golden is written.
	*updateGoldens = true
	goldenFrame(t, dir, "u", "frame one")
	got, err := os.ReadFile(filepath.Join(dir, "u.golden"))
	if err != nil || string(got) != "frame one" {
		t.Fatalf("after -update, golden = %q (err %v), want \"frame one\"", got, err)
	}

	// Compare mode: the written frame compares clean (no failure recorded).
	*updateGoldens = false
	before := t.Failed()
	goldenFrame(t, dir, "u", "frame one")
	if t.Failed() != before {
		t.Error("goldenFrame recorded a failure for an unchanged frame")
	}
}

// updateGoldens is the -update flag (issue #112): when set, goldenFrame
// (re)writes the .golden files instead of comparing. Registered on the test
// binary's global flag set so `go test ./internal/tools -update` works (see
// the package doc).
var updateGoldens = flag.Bool("update", false, "write testdata/*.golden files instead of comparing against them")

// --- deterministic normalization -------------------------------------------

// wallClockRe matches the "HH:MM:SS" gutter TimestampPrefix renders from
// time.Now() at the start of every printed line.
var wallClockRe = regexp.MustCompile(`\d{2}:\d{2}:\d{2}`)

// goldenElapsedRe matches the per-call elapsed times a nested call line and
// a subagent done line carry ("12ms", "1.4s", "2m03s").
var goldenElapsedRe = regexp.MustCompile(`\d+m\d{2}s|\d+\.\d+s|\d+ms`)

// normalizeFrame makes a rendered frame deterministic before it is compared
// to or stored as a golden file: the wall clock is pinned to a fixed instant
// and every elapsed time to a fixed "1.0s", so the golden records the shape
// of the frame — colors, clipping, indentation, markers — and nothing that
// depends on when the test ran.
func normalizeFrame(s string) string {
	s = wallClockRe.ReplaceAllString(s, "12:34:56")
	s = goldenElapsedRe.ReplaceAllString(s, "1.0s")
	return s
}

// TestNormalizeFrame pins the normalization: the gutter (time.Now()) and the
// per-call elapsed times are the only run-to-run quantities in these frames,
// and both are pinned — so a golden diff records a render change, never the
// wall clock.
func TestNormalizeFrame(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "wall clock is pinned",
			in:   "22:46:32  tool: read_file(f.go)",
			want: "12:34:56  tool: read_file(f.go)",
		},
		{
			name: "elapsed forms are pinned",
			in:   "  12ms  3 lines, 21 B\n  1.4s  2 lines\n  2m03s  1 line",
			want: "  1.0s  3 lines, 21 B\n  1.0s  2 lines\n  1.0s  1 line",
		},
		{
			name: "already-normalized input is a fixed point",
			in:   "12:34:56  1.0s",
			want: "12:34:56  1.0s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeFrame(tt.in); got != tt.want {
				t.Errorf("normalizeFrame(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// restoreWidth pins tools' termWidth (the non-TTY width source) for the
// duration of a test and restores it on cleanup. Width 0 is the non-TTY
// form: no clipping anywhere.
func restoreWidth(t *testing.T, w int) func() {
	t.Helper()
	prev := termWidth
	termWidth = func() int { return w }
	return func() { termWidth = prev }
}

// --- glyph contract ----------------------------------------------------------

// bannedIconSet is the retired icon glyphs the REPL dropped on 2026-07-19.
// Box-drawing (U+2500–U+257F) is checked separately by rune range. The
// ellipsis … (U+2026) is NOT banned: it is the REPL's ASCII-incompatible
// but deliberate truncation marker (clipRunes, truncatePlain, diff's elision
// count) — the same class of punctuation as the grid's middle dot — and the
// 2026-07-19 decision bans the icon set and box-drawing, not the ellipsis.
const bannedIconSet = "❯◆▸✻⤷⚠✦"

// ellipsis is the sanctioned truncation marker (see bannedIconSet).
const ellipsis = '…'

// gridGlyphs are the glyphs the /context grid INTENTIONALLY keeps (and the
// demote tick / middle-dot punctuation it uses): the grid is a spatial map,
// so block elements are its design, not a regression of the plain-text
// decision. Everything else the grid emits is ASCII (the ellipsis is
// allowed too, per plainRuneAllowed).
const gridGlyphs = "█▓▒░■·◂"

// plainRuneAllowed reports whether r may appear in a NON-grid terminal
// frame: printable ASCII plus the sanctioned ellipsis — no box-drawing, no
// retired icons, no grid cells.
func plainRuneAllowed(r rune) bool {
	return r >= 0x20 && r < 0x7f || r == ellipsis
}

// gridRuneAllowed reports whether r is part of the grid's sanctioned
// vocabulary: box-drawing and retired icons are banned (as everywhere), grid
// glyphs are allowed, and anything else must be printable ASCII.
func gridRuneAllowed(r rune) bool {
	if r >= 0x2500 && r <= 0x257F {
		return false // box-drawing: banned on the grid too
	}
	if strings.ContainsRune(bannedIconSet, r) {
		return false
	}
	if strings.ContainsRune(gridGlyphs, r) {
		return true
	}
	return r >= 0x20 && r < 0x7f || r == ellipsis
}

// glyphAudit scans a frame against one vocabulary and returns every
// offending rune as a single error string ("", or a line per offender).
// Control characters the terminal protocol itself needs — CR/LF (line
// structure) and ESC (ANSI color/cursor codes, present in colored frames by
// design) — are never flagged: the decision under test is about the GLYPHS
// a frame prints, not the escape plumbing that carries its colors.
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

// framePair runs f once with color on and once under NO_COLOR, returning both
// frames. The glyph set must hold under both: a regression could in
// principle add a glyph only to the uncolored text, or only to the colored
// wrap.
func framePair(t *testing.T, f func() string) (colored, nocolor string) {
	t.Helper()
	{
		defer restoreColor(t, false)()
		colored = f()
	}
	{
		defer restoreColor(t, true)()
		nocolor = f()
	}
	return colored, nocolor
}

// TestNoBannedGlyphsInRenderPaths enforces the 2026-07-19 plain-text
// decision across all six terminal render paths, independent of any golden
// file: a frame that starts emitting box-drawing (U+2500–U+257F) or a
// retired icon (❯◆▸✻⤷⚠✦) fails here, and the /context grid additionally
// stays within its sanctioned glyph vocabulary.
//
// Every frame is produced at a representative fixed width (80) and checked
// in BOTH color and NO_COLOR form. No TTY is needed: termWidth is pinned
// (restoreWidth) and the anchored rows are drawn against a strings.Builder
// via this package's anchorFrame (below) — the same draw sequence
// lineedit.Anchor.drawLocked runs, rebuilt here so the test needs no
// terminal fd.
func TestNoBannedGlyphsInRenderPaths(t *testing.T) {
	before, after := "a\nb\nc\nd\ne\n", "a\nX\nb\nc\nd\n"
	read := seedFile(t, 3)
	const prompt = "> "
	const status = "thinking... 3s"
	question := "\nrisky: deletes build artifacts\n    rm -rf build\n  run it? [y/N] "

	t.Run("tool line", func(t *testing.T) {
		defer restoreWidth(t, 80)()
		colored, nocolor := framePair(t, func() string {
			return formatToolAction("", "read_file("+read+")", "")
		})
		for _, fr := range []string{colored, nocolor} {
			if bad := glyphAudit(fr, plainRuneAllowed); bad != "" {
				t.Errorf("tool line carries banned glyphs:\n%s\nframe:\n%s", bad, fr)
			}
		}
	})

	t.Run("diff under an edit", func(t *testing.T) {
		defer restoreWidth(t, 80)()
		colored, nocolor := framePair(t, func() string {
			return strings.Join(renderDiff(before, after, diffOptions{Width: 80}), "\n")
		})
		for _, fr := range []string{colored, nocolor} {
			if bad := glyphAudit(fr, plainRuneAllowed); bad != "" {
				t.Errorf("diff carries banned glyphs:\n%s\nframe:\n%s", bad, fr)
			}
		}
	})

	t.Run("nested subagent lines", func(t *testing.T) {
		resetNesting(t)
		defer restoreWidth(t, 80)()
		deps := &subDeps{digest: "a digest of the study"}
		deps.loop = func(d ToolDeps) {
			if _, err := Execute(context.Background(), editCall(t, FunctionReadFile,
				map[string]any{"path": read}), d); err != nil {
				t.Errorf("nested read: %v", err)
			}
		}
		colored, nocolor := framePair(t, func() string {
			out := captureStdout(t, func() {
				if _, err := Execute(context.Background(), studyCall(t, "internal/tools", "where is the gutter"), deps); err != nil {
					t.Fatalf("study: %v", err)
				}
			})
			return strings.TrimRight(out, "\n")
		})
		for _, fr := range []string{colored, nocolor} {
			if bad := glyphAudit(fr, plainRuneAllowed); bad != "" {
				t.Errorf("nested subagent lines carry banned glyphs:\n%s\nframe:\n%s", bad, fr)
			}
		}
	})

	t.Run("approval prompt", func(t *testing.T) {
		defer restoreWidth(t, 80)()
		_, ask := splitConfirm(question)
		colored, nocolor := framePair(t, func() string {
			fr := newAnchorFrame(prompt, 80)
			fr.a.drawLocked()
			fr.a.confirm = &confirmState{ask: ask}
			fr.a.eraseLocked()
			fr.a.drawLocked()
			return fr.out.String()
		})
		for _, fr := range []string{colored, nocolor} {
			if bad := glyphAudit(fr, plainRuneAllowed); bad != "" {
				t.Errorf("approval prompt carries banned glyphs:\n%s\nframe:\n%s", bad, fr)
			}
		}
	})

	t.Run("status row", func(t *testing.T) {
		defer restoreWidth(t, 80)()
		colored, nocolor := framePair(t, func() string {
			fr := newAnchorFrame(prompt, 80)
			fr.a.drawLocked()
			fr.a.status = dim(status)
			fr.a.eraseLocked()
			fr.a.drawLocked()
			return fr.out.String()
		})
		for _, fr := range []string{colored, nocolor} {
			if bad := glyphAudit(fr, plainRuneAllowed); bad != "" {
				t.Errorf("status row carries banned glyphs:\n%s\nframe:\n%s", bad, fr)
			}
		}
	})

	t.Run("/context grid", func(t *testing.T) {
		// The grid is pure text (its coloring happens in cmd/cortex's
		// coloredContextGridLines), so one frame covers both modes. Its
		// real frame is snapshotted in cmd/cortex; this path checks the
		// same pure arithmetic re-declared below, so a glyph regression
		// on the grid's own shape fails here too.
		comps := []gridComponent{
			{glyph: glyphSystem, tokens: 2100},
			{glyph: glyphOutline, tokens: 6400},
			{glyph: glyphMemory, tokens: 1200},
			{glyph: glyphSkills, tokens: 500},
		}
		fr := strings.Join(gridFrame(comps, 22400, 128000, 64000), "\n")
		if bad := glyphAudit(fr, gridRuneAllowed); bad != "" {
			t.Errorf("/context grid leaves its sanctioned vocabulary:\n%s\nframe:\n%s", bad, fr)
		}
		// Positive: the grid's cell glyphs must still be there — a
		// "simplification" that strips them changes the report's design.
		for _, g := range []string{"█", "▓", "▒", "░", "■", "·", "◂ demote"} {
			if !strings.Contains(fr, g) {
				t.Errorf("/context grid missing glyph %q:\n%s", g, fr)
			}
		}
	})
}

// --- anchored-row frame (approval prompt + status row) ---------------------

// anchorFrame draws the REPL's pinned two-row block (an optional status row
// above the input row) against an in-memory sink at a fixed width — no TTY,
// no terminal fd, no goroutines. It re-declares the draw/erase sequence
// lineedit.Anchor.drawLocked/eraseLocked run (see live.go), so the frames
// this test and the snapshot tests produce are byte-identical to what the
// real terminal would show minus the input-row cursor movement (the
// snapshot frames fix an empty buffer, where the cursor is at column 0 and
// emits no movement sequence anyway).
type anchorFrame struct {
	out *strings.Builder
	a   *anchor
}

type anchor struct {
	out     *strings.Builder
	widthFn func() int
	prompt  string
	buf     string
	status  string        // dimmed activity label; "" hides the row
	confirm *confirmState // in-flight ask; takes the status row, bright
	rows    int
}

type confirmState struct{ ask string }

func newAnchorFrame(prompt string, width int) *anchorFrame {
	out := &strings.Builder{}
	return &anchorFrame{out: out, a: &anchor{
		out:     out,
		widthFn: func() int { return width },
		prompt:  prompt,
	}}
}

// drawLocked renders the status row (if any) and the input row, mirroring
// lineedit.Anchor.drawLocked: a pending confirmation takes the status row
// bright (not dimmed), and the input row is prompt + buffer. (The real
// anchor additionally truncates the status to width and renders the input
// row with cursor movement; both are inert for these frames — every status
// label is well under 80 columns, and the buffer is empty, so the cursor
// sits at column 0 and emits no movement sequence.)
func (a *anchor) drawLocked() {
	var b strings.Builder
	rows := 1
	status := a.status
	if a.confirm != nil {
		status = a.confirm.ask
	}
	if status != "" {
		b.WriteString("\r\033[K")
		b.WriteString(status)
		b.WriteString("\r\n")
		rows = 2
	}
	b.WriteString(a.prompt)
	b.WriteString(a.buf)
	a.out.WriteString(b.String())
	a.rows = rows
}

// eraseLocked clears the pinned block, mirroring lineedit.Anchor.eraseLocked.
func (a *anchor) eraseLocked() {
	if a.rows == 0 {
		return
	}
	if a.rows == 2 {
		a.out.WriteString("\033[1A")
	}
	a.out.WriteString("\r\033[J")
	a.rows = 0
}

// dim mirrors lineedit's dim (bright-black SGR) so the status row's frame
// carries the same color codes the real terminal emits.
func dim(s string) string { return "\033[90m" + s + "\033[0m" }

// splitConfirm mirrors lineedit.splitConfirm: the last line is the ask
// (shown on the status row), the earlier non-blank lines are the scrollback
// body.
func splitConfirm(q string) (body []string, ask string) {
	lines := strings.Split(strings.Trim(q, "\n"), "\n")
	ask = strings.TrimSpace(lines[len(lines)-1])
	for _, l := range lines[:len(lines)-1] {
		if strings.TrimSpace(l) != "" {
			body = append(body, strings.TrimRight(l, " "))
		}
	}
	return body, ask
}

// --- /context grid (pure re-implementation for the glyph contract) --------

// The grid's real render functions live in cmd/cortex (package main), which
// this package cannot import. The grid is the one render path whose shape is
// fixed (8×16 = 128 cells, wire-order components, tail, free — see
// cmd/cortex/context_grid.go), so the contract test exercises a local
// re-implementation of that arithmetic against the same glyph vocabulary.
// The grid's real colored frame is snapshotted in cmd/cortex.

const (
	gridRows  = 8
	gridCols  = 16
	gridCells = gridRows * gridCols
)

const (
	glyphSystem  = '█'
	glyphOutline = '▓'
	glyphMemory  = '▒'
	glyphSkills  = '░'
	glyphTail    = '■'
	glyphFree    = '·'
)

type gridComponent struct {
	glyph  rune
	tokens int
}

// gridCellSize: tokens one grid cell spans (window/128, min 1).
func gridCellSize(window int) float64 {
	if window <= 0 {
		return 1
	}
	return float64(window) / float64(gridCells)
}

// gridCellsFor: proportional cell count, floor 1 for any non-zero tokens.
func gridCellsFor(tokens int, cellSize float64) int {
	if tokens <= 0 || cellSize <= 0 {
		return 0
	}
	n := int(roundFloat(float64(tokens) / cellSize))
	if n < 1 {
		n = 1
	}
	return n
}

// gridFrame renders the uncolored 8×16 grid exactly as
// cmd/cortex/renderContextGrid does: components in wire order, then tail,
// then free; a "<offset>k" gutter per row; a trailing "  ◂ demote" on the
// row whose start offset first reaches the high watermark.
func gridFrame(components []gridComponent, tailTokens, window, hiWatermark int) []string {
	cellSize := gridCellSize(window)
	var glyphs []rune
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
	place(glyphTail, tailTokens)
	for len(glyphs) < gridCells {
		glyphs = append(glyphs, glyphFree)
	}

	// Demote row: first row whose start offset >= hiWatermark, or -1.
	demoteRow := -1
	if hiWatermark > 0 {
		for r := 0; r < gridRows; r++ {
			if int(float64(r*gridCols)*cellSize) >= hiWatermark {
				demoteRow = r
				break
			}
		}
	}

	last := int(float64((gridRows-1)*gridCols) * cellSize)
	width := len(fmt.Sprintf("%dk", last/1000))
	lines := make([]string, 0, gridRows)
	for r := 0; r < gridRows; r++ {
		offset := int(float64(r*gridCols) * cellSize)
		cells := string(glyphs[r*gridCols : (r+1)*gridCols])
		line := fmt.Sprintf("%*s", width, fmt.Sprintf("%dk", offset/1000)) + "  " + cells
		if r == demoteRow {
			line += "  ◂ demote"
		}
		lines = append(lines, line)
	}
	return lines
}

// roundFloat rounds half away from zero, matching math.Round (the grid's
// placement arithmetic uses it; re-declared here so this test stays
// stdlib-only and imports nothing from cmd/cortex).
func roundFloat(f float64) int {
	if f >= 0 {
		return int(f + 0.5)
	}
	return int(f - 0.5)
}
