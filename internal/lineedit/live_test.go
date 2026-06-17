package lineedit

import (
	"strings"
	"testing"
	"time"
)

// newTestAnchor builds an Anchor wired to an in-memory sink at a fixed width,
// without starting the terminal goroutines — enough to exercise the draw/erase
// and event logic deterministically.
func newTestAnchor(prompt, seed string, width int) (*Anchor, *strings.Builder) {
	out := &strings.Builder{}
	a := &Anchor{
		out:     out,
		widthFn: func() int { return width },
		prompt:  prompt,
		buf:     &buffer{},
	}
	if seed != "" {
		setBuffer(a.buf, seed)
	}
	return a, out
}

func TestAnchorDrawShowsPromptAndBuffer(t *testing.T) {
	a, out := newTestAnchor("> ", "hi", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	got := stripANSI(out.String())
	if !strings.Contains(got, "> hi") {
		t.Errorf("draw = %q, want it to contain %q", got, "> hi")
	}
	if a.rows != 1 {
		t.Errorf("rows = %d, want 1 (no status)", a.rows)
	}
}

func TestAnchorEmitLinePrintsAboveAndRedraws(t *testing.T) {
	a, out := newTestAnchor("> ", "draft", 80)
	a.mu.Lock()
	a.drawLocked() // initial pinned line
	a.mu.Unlock()
	out.Reset()

	a.EmitLine("\x1b[34mhello\x1b[0m world") // an output line with ANSI

	raw := out.String()
	vis := stripANSI(raw)
	if !strings.Contains(vis, "hello world") {
		t.Errorf("emit missing output line; visible = %q", vis)
	}
	// The pinned draft must be redrawn after the output line.
	if !strings.Contains(vis, "> draft") {
		t.Errorf("emit did not redraw prompt; visible = %q", vis)
	}
	if i, j := strings.Index(vis, "hello world"), strings.Index(vis, "> draft"); i < 0 || j < 0 || i > j {
		t.Errorf("output line should precede the redrawn prompt; visible = %q", vis)
	}
}

func TestAnchorThinkingStatusRow(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()

	a.SetThinking(true, "verifying the token")
	if got := stripANSI(out.String()); !strings.Contains(got, "thinking…") || !strings.Contains(got, "verifying the token") {
		t.Errorf("status row missing thinking tail; visible = %q", got)
	}
	if a.rows != 2 {
		t.Errorf("rows = %d, want 2 (status + input)", a.rows)
	}

	out.Reset()
	a.SetThinking(false, "")
	if a.rows != 1 {
		t.Errorf("rows after clear = %d, want 1", a.rows)
	}
	if a.status != "" {
		t.Errorf("status not cleared: %q", a.status)
	}
}

func TestAnchorActivityStatusRow(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()

	a.SetActivity("study(main.go)")
	if got := stripANSI(out.String()); !strings.Contains(got, "study(main.go)") {
		t.Errorf("activity row missing tool label; visible = %q", got)
	}
	if a.rows != 2 {
		t.Errorf("rows = %d, want 2 (status + input)", a.rows)
	}

	// A tick advances the glyph while activity is set.
	a.mu.Lock()
	a.spinIdx++
	a.refreshStatusLocked()
	a.mu.Unlock()

	a.SetActivity("")
	if a.rows != 1 || a.status != "" {
		t.Errorf("activity not cleared: rows=%d status=%q", a.rows, a.status)
	}
}

func TestAnchorTickAnimates(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.stop = make(chan struct{})
	a.mu.Lock()
	a.activity = "study(main.go)"
	a.drawLocked()
	a.mu.Unlock()

	done := make(chan struct{})
	go func() { defer close(done); a.tickLoop() }()
	time.Sleep(350 * time.Millisecond) // ~3-4 ticks at 90ms
	close(a.stop)
	<-done

	glyphs := map[rune]bool{}
	for _, r := range stripANSI(out.String()) {
		for _, g := range anchorSpinner {
			if r == g {
				glyphs[r] = true
			}
		}
	}
	if len(glyphs) < 2 {
		t.Errorf("spinner did not animate: saw %d distinct glyphs, want >=2", len(glyphs))
	}
}

func TestAnchorApplyEventEditsBuffer(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 80)
	for _, r := range "abc" {
		a.applyEvent(keyEvent{kind: keyRune, r: r})
	}
	a.applyEvent(keyEvent{kind: keyBackspace})
	if got := a.buf.string(); got != "ab" {
		t.Errorf("buffer = %q, want %q", got, "ab")
	}
	// Enter and history keys are inert in the anchored editor.
	a.applyEvent(keyEvent{kind: keyEnter})
	a.applyEvent(keyEvent{kind: keyUp})
	if got := a.buf.string(); got != "ab" {
		t.Errorf("inert key changed buffer to %q", got)
	}
}
