package tools

// golden_test.go — the shared golden-file mechanism for issue #112's
// snapshot tests, plus the glyph-contract test that enforces the 2026-07-19
// plain-text decision on the render paths this package owns.
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
// The contract test covers the three render paths this package owns: the
// tool-action line, the file diff, and the nested subagent lines. Each must
// stay free of box-drawing (U+2500–U+257F) and of the retired icon set
// (❯◆▸✻⤷⚠✦). The other three paths audit their own PRODUCTION code in the
// packages that own it — the approval prompt and the anchored status row in
// internal/lineedit (TestAnchoredRowGlyphContract), and the /context grid in
// cmd/cortex (TestContextGridGlyphContract, the deliberate block-element
// exception: █▓▒░■· are its design, the demote tick ◂ structural).
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

	"github.com/dereksantos/cortex/internal/style"
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
// a subagent done line carry ("12ms", "1.4s", "2m03s"). The leading group
// keeps it out of SGR codes: "\x1b[32mstudy" must not read as "32ms" +
// "tudy" — the tool lines put a colored verb straight after its SGR.
var goldenElapsedRe = regexp.MustCompile(`(^|[^\d\[;])(\d+m\d{2}s|\d+\.\d+s|\d+ms)`)

// normalizeFrame makes a rendered frame deterministic before it is compared
// to or stored as a golden file: the wall clock is pinned to a fixed instant
// and every elapsed time to a fixed "1.0s", so the golden records the shape
// of the frame — colors, clipping, indentation, markers — and nothing that
// depends on when the test ran.
func normalizeFrame(s string) string {
	s = wallClockRe.ReplaceAllString(s, "12:34:56")
	s = goldenElapsedRe.ReplaceAllString(s, "${1}1.0s")
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
		{
			name: "an SGR code before a verb is not an elapsed",
			in:   "\x1b[32mstudy\x1b[0m  12ms",
			want: "\x1b[32mstudy\x1b[0m  1.0s",
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
	prev := style.TermWidth
	style.TermWidth = func() int { return w }
	return func() { style.TermWidth = prev }
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

// plainRuneAllowed reports whether r may appear in a non-grid terminal
// frame: printable ASCII plus the sanctioned ellipsis — no box-drawing
// (U+2500–U+257F), no retired icons (bannedIconSet), no grid cell glyphs
// (those are cmd/cortex's /context grid's design, and its audit lives in
// cmd/cortex's TestContextGridGlyphContract).
func plainRuneAllowed(r rune) bool {
	if r >= 0x2500 && r <= 0x257F {
		return false
	}
	if strings.ContainsRune(bannedIconSet, r) {
		return false
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
// decision on the three render paths this package owns, independent of any
// golden file: a frame that starts emitting box-drawing (U+2500–U+257F) or a
// retired icon (❯◆▸✻⤷⚠✦) fails here. The other three paths audit their own
// production code where it lives — the approval prompt and the anchored
// status row in internal/lineedit (TestAnchoredRowGlyphContract, over the
// real Anchor drawLocked/eraseLocked frames), and the /context grid in
// cmd/cortex (TestContextGridGlyphContract, over renderContextGrid and
// coloredContextGridLines, with the positive check that the grid's cell
// glyphs █▓▒░■· and the demote tick ◂ are still present).
//
// Every frame is produced at a representative fixed width (80) and checked
// in BOTH color and NO_COLOR form. No TTY is needed: termWidth is pinned
// (restoreWidth).
func TestNoBannedGlyphsInRenderPaths(t *testing.T) {
	before, after := "a\nb\nc\nd\ne\n", "a\nX\nb\nc\nd\n"
	read := seedFile(t, 3)

	t.Run("tool line", func(t *testing.T) {
		defer restoreWidth(t, 80)()
		colored, nocolor := framePair(t, func() string {
			return formatToolLine("", "read_file("+read+")", "", false)
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
			if _, _, err := Execute(context.Background(), editCall(t, FunctionReadFile,
				map[string]any{"path": read}), d); err != nil {
				t.Errorf("nested read: %v", err)
			}
		}
		colored, nocolor := framePair(t, func() string {
			out := captureStdout(t, func() {
				if _, _, err := Execute(context.Background(), studyCall(t, "internal/tools", "where is the gutter"), deps); err != nil {
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
}
