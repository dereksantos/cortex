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
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// lineWidths is the width axis: 40 and 200 are the clip boundary on either
// side of 80, and 0 is the non-TTY/piped form (renderLine falls back to 80).
var lineWidths = []int{40, 80, 200, 0}

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
			lineGolden(t, "status_"+caseName(w), statusFrame(w, "thinking... 3s"))
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
			lineGolden(t, "confirm_"+caseName(w), confirmFrame(w, "draft", "run it? [y/N]"))
		})
	}
}
