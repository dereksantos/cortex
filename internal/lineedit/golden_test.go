package lineedit

// golden_test.go — golden snapshots for the terminal-emitting anchored frame
// (issue #112): the status row (SetActivity/SetThinking) and the in-flight
// Confirm ask. Both are pure functions of (prompt, buffer, status, confirm,
// width) once the anchor's draw/erase sequence is run, so they are captured
// against an in-memory sink with a FAKE WIDTH — no terminal fd, no goroutines.
// The same drawLocked/eraseLocked sequence newTestAnchor drives is what the
// real terminal shows, so the frames are byte-identical to production output.
//
// The color axis is one here: lineedit's dim/bright constants are hardcoded
// (no NO_COLOR toggle in this package), so "colored" is the only form and the
// golden records the exact SGR codes the terminal receives.
//
// The goldens live in testdata/*.golden next to this file; regenerate with
// `go test ./internal/lineedit -update`.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// updateLineeditGoldens is the -update flag (issue #112): when set, lineGolden
// (re)writes the .golden files instead of comparing. Registered on the test
// binary's global flag set so `go test ./internal/lineedit -update` works; a
// bare `go test ./...` is unaffected.
var updateLineeditGoldens = flag.Bool("update", false, "write testdata/*.golden files instead of comparing against them")

// lineGolden compares got against testdata/name+".golden" and fails with the
// full diff on mismatch; with -update it (re)writes the file.
func lineGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateLineeditGoldens {
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

// lineWidths is the width axis: 40 is the tight clip boundary (both labels
// below exceed it, so the w40 goldens record the truncated "…" form), 80 is
// the conventional fallback width (Terminal.width() falls back to it on a
// non-TTY, lineedit.go), and 200 leaves both labels unclipped. Width 0 is NOT
// swept: in production Terminal.width() never returns 0 (it falls back to 80),
// so a 0-width frame would pin a state no user can ever see.
var lineWidths = []int{40, 80, 200}

// statusLabel is the activity label the status-row goldens pin. It is between
// 40 and 80 columns, so the w40 case records the truncated form (truncate
// appends "…") and the unclipped form only at w200.
const statusLabel = "thinking about the change, verifying each step against the spec… and back again 3s"

// confirmAsk is the Confirm ask the approval goldens pin. It is longer than
// 40 columns, so the w40 case records the truncated form, and fits the wider
// sweeps (w80 and w200) unclipped.
const confirmAsk = "run the full test suite against the working tree? [y/N]"

// statusFrame builds an anchor with the given width and activity label, runs
// the exact draw/erase/draw sequence the real tick loop performs for a status
// row (refreshStatusLocked), and returns the raw frame the terminal would
// receive. The frame is deterministic: no wall clock or elapsed time, only the
// dim label (the caller's own "N s" text) and the input row.
func statusFrame(width int, label string) string {
	out := &strings.Builder{}
	a := &Anchor{out: out, widthFn: func() int { return width }, prompt: prompt, buf: &buffer{}}
	a.mu.Lock()
	a.drawLocked() // initial 1-row input line
	a.eraseLocked()
	a.activity = label
	a.refreshStatusLocked() // erase + draw the 2-row block (status + input)
	a.mu.Unlock()
	return out.String()
}

// confirmFrame builds an anchor at the given width with seed as the in-flight
// buffer, runs the exact draw/erase/confirm sequence the real Confirm()
// performs for its ask (the body goes to scrollback, so only the ask and the
// redrawn input row are in the frame), and returns the raw frame.
func confirmFrame(width int, seed, ask string) string {
	out := &strings.Builder{}
	a := &Anchor{out: out, widthFn: func() int { return width }, prompt: prompt, buf: &buffer{}}
	setBuffer(a.buf, seed)
	a.mu.Lock()
	a.drawLocked() // initial 1-row input line
	a.confirm = &confirmState{ask: ask}
	a.eraseLocked()
	a.drawLocked() // the ask takes the status row (bright); the input redraws
	a.mu.Unlock()
	return out.String()
}

// TestStatusRowGolden pins the anchored status row (a dimmed activity label
// above the input row) across the width axis.
func TestStatusRowGolden(t *testing.T) {
	for _, w := range lineWidths {
		w := w
		t.Run(caseName(w), func(t *testing.T) {
			lineGolden(t, "status_"+caseName(w), statusFrame(w, statusLabel))
		})
	}
}

// TestConfirmAskGolden pins the anchored approval (Confirm) frame — the
// question's ask on the status row (bright, not dimmed) above the in-flight
// input row — across the width axis.
func TestConfirmAskGolden(t *testing.T) {
	for _, w := range lineWidths {
		w := w
		t.Run(caseName(w), func(t *testing.T) {
			lineGolden(t, "confirm_"+caseName(w), confirmFrame(w, "draft", confirmAsk))
		})
	}
}

// statusLabelStats is the label + stats the status-STATS goldens pin (issue
// #109): the same 14-column thinking label the other row goldens use, with a
// full StatusStats whose TurnStart is fixed (34s in the past), so the frame
// is byte-stable for the snapshot — the elapsed counter is the only thing
// that moves in production, and the goldens pin one instant of it.
const statusStatsLabel = "thinking... 34s"

var statusStatsGolden = StatusStats{
	Model:     "anthropic/claude-sonnet-4.5",
	Ctx:       0.42,
	InTokens:  18200,
	OutTokens: 1100,
	CostUSD:   0.013,
	TurnStart: time.Now().Add(-34 * time.Second),
}

// statusStatsFrame builds an anchor at the given width with the stats label
// and stats, runs the exact draw/erase/refresh sequence the real tick loop
// performs for a stats-carrying status row (SetStatus, then a tick's
// refreshStatusLocked), and returns the raw frame the terminal would receive.
// The frame is deterministic except for the 34s figure's own second — the
// label and the stats' elapsed figure both read from the same fixed
// TurnStart, so a snapshot taken within the same second is byte-identical;
// -update regenerates it.
func statusStatsFrame(width int) string {
	out := &strings.Builder{}
	a := &Anchor{out: out, widthFn: func() int { return width }, prompt: prompt, buf: &buffer{}}
	a.mu.Lock()
	a.drawLocked() // initial 1-row input line
	a.activity = statusStatsLabel
	a.refreshStatusLocked()
	a.stats = statusStatsGolden
	a.refreshStatusLocked() // the tick: re-derives label + stats at width
	a.mu.Unlock()
	return out.String()
}

// TestStatusStatsGolden pins the issue #109 status row — label + "<model> ·
// ctx 42% · 18.2k in / 1.1k out · $0.013 · 34s" — across the width axis. At
// 200 columns the full line shows; at 80 the right-to-left priority trim
// (elapsed → cost → tokens → ctx, model kept last) drops the tail; at 40 the
// 14-column label leaves only the model for the stats side. Cost appears in
// every frame because this stats sample carries a backend-reported figure —
// the no-cost case is covered by TestStatusLineOmitsAbsentData.
func TestStatusStatsGolden(t *testing.T) {
	for _, w := range lineWidths {
		w := w
		t.Run(caseName(w), func(t *testing.T) {
			lineGolden(t, "status_stats_"+caseName(w), statusStatsFrame(w))
		})
	}
}

// --- glyph contract (the anchored row's half) --------------------------------

// The 2026-07-19 plain-text decision bans box-drawing (U+2500–U+257F) and the
// retired icon set (❯◆▸✻⤷⚠✦) on every terminal render path; the anchored
// status row and Confirm ask are two of those paths. This audit checks their
// REAL frames (statusFrame/confirmFrame above — the same drawLocked/eraseLocked
// sequence the terminal shows), so a glyph regression in lineedit's row
// rendering fails here. (internal/tools and cmd/cortex run the same audit in
// the packages that own their paths.)

const (
	// bannedLineeditIconSet is the retired icon glyphs the REPL dropped
	// on 2026-07-19 (see the internal/tools counterpart).
	bannedLineeditIconSet = "❯◆▸✻⤷⚠✦"
	// ellipsis is the sanctioned truncation marker: clipRunes' "…" in a
	// long status label or buffer is the REPL's deliberate elision marker,
	// not part of the retired icon set.
	ellipsis = '…'
)

// lineeditRuneAllowed reports whether r may appear in an anchored row frame:
// printable ASCII plus the sanctioned ellipsis — no box-drawing
// (U+2500–U+257F), no retired icons (bannedLineeditIconSet).
func lineeditRuneAllowed(r rune) bool {
	if r >= 0x2500 && r <= 0x257F {
		return false
	}
	if strings.ContainsRune(bannedLineeditIconSet, r) {
		return false
	}
	return r >= 0x20 && r < 0x7f || r == ellipsis
}

// glyphAudit scans a frame against an allowed-rune predicate and returns one
// line per offender ("" when clean). Control characters the terminal
// protocol itself needs — CR/LF and ESC (ANSI color/cursor codes, present in
// the colored frames by design) — are never flagged: the decision under test
// is about the GLYPHS a frame prints, not the escape plumbing that carries
// its colors.
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

// sweepLabels are the longest label/ask the golden sweep drives the sweep
// width with, so a glyph that only surfaces at a clipped width still gets
// checked in its unclipped form.
var sweepLabels = []struct{ status, ask string }{
	{statusLabel, confirmAsk},
	{"thinking... 3s", "run it? [y/N]"},
}

// TestAnchoredRowGlyphContract enforces the plain-text decision on the
// anchored status row and Confirm ask frames, at every width the golden
// sweep uses (a glyph regression could in principle surface only at a
// clipped width): no box-drawing, no retired icons.
func TestAnchoredRowGlyphContract(t *testing.T) {
	for _, w := range lineWidths {
		w := w
		t.Run(caseName(w), func(t *testing.T) {
			for _, l := range sweepLabels {
				if bad := glyphAudit(statusFrame(w, l.status), lineeditRuneAllowed); bad != "" {
					t.Errorf("status row carries banned glyphs:\n%s\nframe:\n%s", bad, statusFrame(w, l.status))
				}
				if bad := glyphAudit(confirmFrame(w, "draft", l.ask), lineeditRuneAllowed); bad != "" {
					t.Errorf("confirm ask carries banned glyphs:\n%s\nframe:\n%s", bad, confirmFrame(w, "draft", l.ask))
				}
			}
		})
	}
}
