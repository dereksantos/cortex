package lineedit

import (
	"strings"
	"testing"
	"time"
)

// testNow pins the test anchors' clock: every test that renders elapsed
// seconds pins TurnStart as an offset from this instant, so the rendered
// "34s" is deterministic (issue #109 review: the wall clock measured from
// package init could flake to "35s" on a loaded CI).
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// newTestAnchor builds an Anchor wired to an in-memory sink at a fixed width,
// without starting the terminal goroutines — enough to exercise the draw/erase
// and event logic deterministically. Its clock (nowFn) is pinned at testNow
// so the status row's elapsed seconds are deterministic.
func newTestAnchor(prompt, seed string, width int) (*Anchor, *strings.Builder) {
	out := &strings.Builder{}
	a := &Anchor{
		out:     out,
		widthFn: func() int { return width },
		prompt:  prompt,
		buf:     &buffer{},
		nowFn:   func() time.Time { return testNow },
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
	if got := stripANSI(out.String()); !strings.Contains(got, "thinking...") || !strings.Contains(got, "verifying the token") {
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

	// A tick repaints the status row while activity is set.
	a.mu.Lock()
	a.refreshStatusLocked()
	a.mu.Unlock()

	a.SetActivity("")
	if a.rows != 1 || a.status != "" {
		t.Errorf("activity not cleared: rows=%d status=%q", a.rows, a.status)
	}
}

// TestAnchorTickRepaints covers the de-glyphed status row (2026-07-19): there
// is no animated frame to cycle anymore, so the tick loop's job is just to
// keep repainting the current activity label on cadence without dropping it
// or crashing across several ticks.
func TestAnchorTickRepaints(t *testing.T) {
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

	if got := stripANSI(out.String()); !strings.Contains(got, "study(main.go)") {
		t.Errorf("status row missing activity label after ticking; visible = %q", got)
	}
}

func TestSplitConfirm(t *testing.T) {
	q := "\nrisky: installs software globally\n    npm install -g wrangler\n  run it? [y/N] "
	body, ask := splitConfirm(q)
	if ask != "run it? [y/N]" {
		t.Errorf("ask = %q, want %q", ask, "run it? [y/N]")
	}
	if len(body) != 2 || !strings.Contains(body[0], "risky:") || !strings.Contains(body[1], "npm install") {
		t.Errorf("body = %q, want the warning + command lines", body)
	}
}

func TestAnchorConfirmShowsAskOnStatusRow(t *testing.T) {
	a, out := newTestAnchor("> ", "draft", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	out.Reset()

	a.mu.Lock()
	a.confirm = &confirmState{ask: "run it? [y/N]"}
	a.eraseLocked()
	a.drawLocked()
	a.mu.Unlock()

	got := stripANSI(out.String())
	if !strings.Contains(got, "run it? [y/N]") {
		t.Errorf("confirm ask missing from status row; visible = %q", got)
	}
	if !strings.Contains(got, "> draft") {
		t.Errorf("prompt row not redrawn under the ask; visible = %q", got)
	}
	if a.rows != 2 {
		t.Errorf("rows = %d, want 2 (ask + input)", a.rows)
	}
}

func TestAnchorConfirmAnswers(t *testing.T) {
	cases := []struct {
		name string
		key  byte
		want ConfirmChoice
	}{
		{"y accepts", 'y', ConfirmYes},
		{"Y accepts", 'Y', ConfirmYes},
		{"n declines", 'n', ConfirmNo},
		{"enter declines", '\r', ConfirmNo},
		{"ctrl-c declines", 0x03, ConfirmNo},
		{"a approves the exact command for the session", 'a', ConfirmAlwaysExact},
		{"p approves the command prefix for the session", 'p', ConfirmAlwaysPrefix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestAnchor("> ", "", 80)
			a.stop = make(chan struct{})
			var canceled bool
			a.cancel = func() { canceled = true }

			result := make(chan ConfirmChoice, 1)
			go func() {
				result <- a.Confirm("\nrisky: reason\n    cmd\n  run it? [y once | a this command | p always \"cmd*\" | n] ")
			}()

			// Spin until Confirm has registered its pending state, then feed the key.
			deadline := time.Now().Add(time.Second)
			for {
				a.mu.Lock()
				ready := a.confirm != nil
				a.mu.Unlock()
				if ready || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			a.handleByte(tc.key)

			select {
			case got := <-result:
				if got != tc.want {
					t.Errorf("Confirm() = %v, want %v", got, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("Confirm did not return after the answer key")
			}
			a.mu.Lock()
			stillPending := a.confirm != nil
			a.mu.Unlock()
			if stillPending {
				t.Error("confirm state not cleared after answer")
			}
			if tc.key == 0x03 && !canceled {
				t.Error("Ctrl-C should cancel the turn")
			}
		})
	}
}

// TestHandleConfirmByte is the acceptance item for issue #107's table-driven
// key handling: every byte's confirm behavior is asserted — the answer keys
// (y/Y once, n/N/Enter decline, a/A always-exact, p/P always-prefix, Ctrl-C
// decline-and-cancel) deliver their ConfirmChoice, clear the pending confirm,
// and cancel the turn only for Ctrl-C; every other byte is ignored (no
// answer, confirm still pending, turn not canceled); and a byte with no
// pending confirm is inert.
func TestHandleConfirmByte(t *testing.T) {
	answerKeys := []byte{'y', 'Y', 'n', 'N', '\r', '\n', 'a', 'A', 'p', 'P', 0x03}
	strayKeys := []byte{'x', ' ', 'q', 'e', '1', ';', 0x7f, 0x1b, 0x04}
	keyName := func(b byte) string {
		switch b {
		case '\r':
			return "cr"
		case '\n':
			return "lf"
		case 0x03:
			return "ctrl-c"
		case 0x7f:
			return "del"
		case 0x1b:
			return "esc"
		case 0x04:
			return "ctrl-d"
		case ' ':
			return "space"
		default:
			return string(rune(b))
		}
	}
	for _, key := range append(append([]byte{}, answerKeys...), strayKeys...) {
		t.Run(keyName(key), func(t *testing.T) {
			a, _ := newTestAnchor("> ", "", 80)
			a.stop = make(chan struct{})
			var canceled bool
			a.cancel = func() { canceled = true }

			v, isAnswer := confirmKeyAction[key]
			wantChoice, wantCancel := v.choice, v.cancel
			result := make(chan ConfirmChoice, 1)
			a.mu.Lock()
			a.confirm = &confirmState{ask: "run it? [y/N]", res: result}
			a.mu.Unlock()
			a.handleConfirmByte(key)
			a.mu.Lock()
			pending := a.confirm != nil
			a.mu.Unlock()

			if isAnswer {
				select {
				case got := <-result:
					if got != wantChoice {
						t.Errorf("choice = %v, want %v", got, wantChoice)
					}
				case <-time.After(100 * time.Millisecond):
					t.Fatal("answer key delivered no choice")
				}
				if pending {
					t.Error("answer key must clear the pending confirm")
				}
				if canceled != wantCancel {
					t.Errorf("canceled = %v, want %v (only Ctrl-C cancels the turn)", canceled, wantCancel)
				}
			} else {
				if !pending {
					t.Error("stray key must leave the confirm pending")
				}
				select {
				case <-result:
					t.Fatal("stray key must not deliver an answer")
				case <-time.After(50 * time.Millisecond):
				}
				if canceled {
					t.Error("stray key must not cancel the turn")
				}
			}
		})
	}

	t.Run("no pending confirm is inert", func(t *testing.T) {
		a, _ := newTestAnchor("> ", "", 80)
		var canceled bool
		a.cancel = func() { canceled = true }
		a.handleConfirmByte('y') // no confirm in flight — must be a no-op
		if canceled {
			t.Error("byte without a pending confirm must not cancel")
		}
	})
}

func TestAnchorConfirmIgnoresUnrelatedKeys(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 80)
	a.stop = make(chan struct{})

	result := make(chan ConfirmChoice, 1)
	go func() {
		result <- a.Confirm("\nrisky: reason\n    cmd\n  run it? [y once | a this command | p always \"cmd*\" | n] ")
	}()
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		ready := a.confirm != nil
		a.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	a.handleByte('x') // not an answer — must be ignored
	select {
	case <-result:
		t.Fatal("Confirm returned on an unrelated key")
	case <-time.After(50 * time.Millisecond):
	}
	a.handleByte('y') // now answer
	select {
	case got := <-result:
		if got != ConfirmYes {
			t.Errorf("Confirm() = %v, want ConfirmYes after y", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Confirm did not return after y")
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

func TestAnchorSetPromptRedraws(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	out.Reset()

	// Simulate an updated prompt with a different context gauge
	newPrompt := "> [500/128k] "
	a.SetPrompt(newPrompt)

	got := stripANSI(out.String())
	if !strings.Contains(got, "> [500/128k] ") {
		t.Errorf("SetPrompt did not update prompt text; visible = %q", got)
	}
	if a.prompt != newPrompt {
		t.Errorf("prompt field = %q, want %q", a.prompt, newPrompt)
	}
}

// TestAnchorSetPromptErasesLiveStatusRow guards the bug a caller-driven state
// light in the prompt (e.g. a "thinking"/"streaming" indicator refreshed on
// every reasoning delta) exposed: SetPrompt's only two original callers
// (turn.go's onStatusUpdate/onAfterToolResult) always fired with the status
// row already hidden, so a redraw without a preceding erase looked correct —
// it just overwrote a 1-row block. The moment something calls SetPrompt WHILE
// the status row is live (a 2-row block), drawing from the cursor's resting
// position — the input row, the bottom of the block — instead of the block's
// top pushes a fresh line onto the terminal instead of overwriting it, which
// is exactly the runaway-scrolling symptom this test would have caught.
func TestAnchorSetPromptErasesLiveStatusRow(t *testing.T) {
	a, out := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.activity = "thinking... 1s"
	a.refreshStatusLocked() // status row now live: a 2-row block
	a.mu.Unlock()
	if a.rows != 2 {
		t.Fatalf("setup: rows = %d, want 2 (status row live)", a.rows)
	}
	out.Reset()

	a.SetPrompt("> [700/128k] ")

	raw := out.String()
	// A correct redraw over a live 2-row block must step up over the status
	// row (eraseLocked's "\033[1A") before clearing and repainting — the same
	// pattern every other mutator in this file (refreshStatusLocked,
	// refreshInputLocked, handleConfirmByte) already follows. Its absence is
	// exactly the missing-erase bug: the new content lands below the old
	// block instead of on top of it.
	if !strings.Contains(raw, "\033[1A") {
		t.Errorf("SetPrompt over a live status row did not step up before redrawing; raw = %q", raw)
	}
	if a.rows != 2 {
		t.Errorf("rows after SetPrompt = %d, want 2 (status row still live)", a.rows)
	}
	// A second call in a row (the realistic case — a phase light refreshed on
	// every reasoning delta) must erase every time, not just the first.
	out.Reset()
	a.SetPrompt("> [900/128k] ")
	if !strings.Contains(out.String(), "\033[1A") {
		t.Errorf("second SetPrompt over a live status row did not step up before redrawing")
	}
}

// statusStatsSample is the full StatusStats the StatusLine tests pin: every
// segment present, in the issue's own example figures (model, 42% ctx, 18.2k
// in / 1.1k out, a reported $0.013 cost, 34s elapsed — the latter derived
// from TurnStart against the pinned clock testNow, not the wall clock;
// issue #109 review).
var statusStatsSample = StatusStats{
	Model:      "anthropic/claude-sonnet-4.5",
	Ctx:        0.42,
	InTokens:   18200,
	OutTokens:  1100,
	CostUSD:    0.013,
	SessionIn:  42000,
	SessionOut: 9000,
	TurnStart:  testNow.Add(-34 * time.Second),
	Elapsed:    34 * time.Second,
}

// TestStatusLineRenders pins the full stats line (issue #109's row format):
// every segment present in "<model> · ctx 42% · 18.2k in / 1.1k out ·
// $0.013 · 34s" order, " · "-joined, cost shown only because the backend
// reported it. The 34s figure is derived from TurnStart against the pinned
// clock testNow (not the wall clock — issue #109 review).
func TestStatusLineRenders(t *testing.T) {
	got := statusStatsSample.StatusLine(0)
	want := "anthropic/claude-sonnet-4.5 · ctx 42% · 18.2k in / 1.1k out · $0.013 · 34s"
	if got != want {
		t.Errorf("StatusLine = %q, want %q", got, want)
	}
}

// TestStatusLineOmitsAbsentData is the issue's "cost appears only when the
// backend reports it (never estimated)" rule plus the other absent-data
// omissions, as a table: each case flips one field to its absent value and
// the corresponding segment must disappear (with its joiner) rather than
// render an empty slot.
func TestStatusLineOmitsAbsentData(t *testing.T) {
	cases := []struct {
		name string
		mut  func(s *StatusStats)
		want string
	}{
		{
			name: "no cost when the backend never reported one",
			mut:  func(s *StatusStats) { s.CostUSD = 0 },
			want: "anthropic/claude-sonnet-4.5 · ctx 42% · 18.2k in / 1.1k out · 34s",
		},
		{
			name: "no ctx before any request",
			mut:  func(s *StatusStats) { s.Ctx = 0 },
			want: "anthropic/claude-sonnet-4.5 · 18.2k in / 1.1k out · $0.013 · 34s",
		},
		{
			name: "no tokens before any request",
			mut:  func(s *StatusStats) { s.InTokens, s.OutTokens = 0, 0 },
			want: "anthropic/claude-sonnet-4.5 · ctx 42% · $0.013 · 34s",
		},
		{
			name: "no elapsed before the turn starts",
			mut:  func(s *StatusStats) { s.TurnStart = time.Time{} },
			want: "anthropic/claude-sonnet-4.5 · ctx 42% · 18.2k in / 1.1k out · $0.013",
		},
		{
			name: "nothing set means no line",
			mut:  func(s *StatusStats) { *s = StatusStats{} },
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := statusStatsSample
			tc.mut(&s)
			if got := s.StatusLine(0); got != tc.want {
				t.Errorf("StatusLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStatusLineTruncatesByPriority is the issue's narrow-width rule: when
// the full line does not fit, segments drop right-to-left — the cost first,
// then the tokens, then the ctx — with the model kept last. Width 0 ("no
// width known") never truncates.
func TestStatusLineTruncatesByPriority(t *testing.T) {
	s := statusStatsSample
	full := s.StatusLine(0)
	if full == "" || len(full) < 40 {
		t.Fatalf("sample line too short to exercise the drops: %q", full)
	}
	for _, w := range []int{40, 35, 30, 25, 20, 15, 10, 5} {
		w := w
		t.Run("w"+itoa(w), func(t *testing.T) {
			got := s.StatusLine(w)
			if got == full {
				t.Errorf("w%d: no truncation at all: %q", w, got)
			}
			if dw := displayWidth(got); dw > w {
				t.Errorf("w%d: visible width %d exceeds budget %d: %q", w, dw, w, got)
			}
			// The model is kept until it alone cannot fit; everything it
			// keeps must be a prefix of the full line's segments, in order —
			// drops are right-to-left, never reordering.
			if w >= displayWidth("anthropic/claude-sonnet-4.5") {
				if !strings.HasPrefix(got, "anthropic/claude-sonnet-4.5") {
					t.Errorf("w%d: model dropped before the budget ran out: %q", w, got)
				}
			}
			// Once the budget cannot hold model + " · " + ctx, the ctx segment
			// is gone; and it can never come back after the tokens drop.
			if !strings.Contains(got, "ctx ") {
				if strings.Contains(got, " in / ") || strings.Contains(got, "$") || strings.HasSuffix(got, "s") {
					t.Errorf("w%d: a lower-priority segment survived while ctx was dropped: %q", w, got)
				}
			}
			// No dangling joiner: a truncated line never ends with or carries
			// an orphan " · " (a drop must take its joiner with it).
			if strings.HasSuffix(got, " ·") || strings.Contains(got, "· ·") {
				t.Errorf("w%d: dangling joiner in %q", w, got)
			}
			if strings.Contains(got, "·") && !strings.Contains(got, " · ") {
				t.Errorf("w%d: malformed joiner in %q", w, got)
			}
		})
	}
}

// TestStatusLineElapses exercises the REAL clock (time.Now), the one test
// that must: the seconds segment counts whole seconds since TurnStart —
// tolerant assertions (a range, not an exact figure) because the turn runs
// on the wall clock between the two reads.
func TestStatusLineElapses(t *testing.T) {
	t.Run("zero before the first second", func(t *testing.T) {
		s := statusStatsSample
		s.TurnStart = time.Now()
		s.Elapsed = 0 // caller hasn't computed it yet — StatusLine falls back to time.Since
		got := s.StatusLine(0)
		if got == "" || (!strings.HasSuffix(got, " · 0s") && !strings.HasSuffix(got, " · 1s")) {
			t.Errorf("fresh turn: %q, want it to end \"· 0s\" (or \"· 1s\" if the second ticked between the two reads)", got)
		}
	})
	t.Run("advances with the turn", func(t *testing.T) {
		s := statusStatsSample
		s.TurnStart = time.Now().Add(-2500 * time.Millisecond)
		s.Elapsed = 0
		got := s.StatusLine(0)
		if got == "" || (!strings.HasSuffix(got, " · 2s") && !strings.HasSuffix(got, " · 3s")) {
			t.Errorf("2.5s in: %q, want it to end \"· 2s\" (or \"· 3s\" if the second ticked between the two reads)", got)
		}
	})
}

// TestStatusLineFormats pins the number formatting the row relies on: the
// k-notation (18200 -> "18.2k"), the percent (0.42 -> "42%"), and the cost
// magnitudes ($0.013 / $1.25). These ride the goldens too; asserting them
// here keeps a formatting change from failing only at the snapshot boundary.
func TestStatusLineFormats(t *testing.T) {
	if got := humanK(18200); got != "18.2k" {
		t.Errorf("humanK(18200) = %q, want 18.2k", got)
	}
	if got := humanK(999); got != "999" {
		t.Errorf("humanK(999) = %q, want 999", got)
	}
	if got := humanK(1000); got != "1k" {
		t.Errorf("humanK(1000) = %q, want 1k", got)
	}
	if got := humanK(2_300_000); got != "2.3M" {
		t.Errorf("humanK(2300000) = %q, want 2.3M", got)
	}
	if got := pct(0.42); got != "42%" {
		t.Errorf("pct(0.42) = %q, want %q", got, "42%")
	}
	if got := pct(0.426); got != "43%" {
		t.Errorf("pct(0.426) = %q, want %q (round-half-up)", got, "43%")
	}
	if got := "$" + humanCost(0.013); got != "$0.013" {
		t.Errorf("cost 0.013 = %q, want $0.013", got)
	}
	if got := "$" + humanCost(1.25); got != "$1.25" {
		t.Errorf("cost 1.25 = %q, want $1.25", got)
	}
}

// TestStatusLineOneCounterPerRow is the issue #109 review fix: the row shows
// ONE elapsed counter. When the caller's activity label already carries its
// own seconds tick (SetThinking's "thinking... 12s"), the stats line's
// trailing turn-elapsed segment is dropped — two counters that disagree (the
// label resets per thinking phase) would be noise. A label without a tick
// keeps the stats line's counter.
func TestStatusLineOneCounterPerRow(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 200)

	// A ticking label: the stats line's turn-elapsed segment is dropped.
	a.activity = "thinking... 12s"
	a.stats = statusStatsSample
	a.mu.Lock()
	row := a.statusLine()
	a.mu.Unlock()
	// statusLine drops the turn-elapsed segment for a ticking label; the
	// sample's other segments stay.
	s := statusStatsSample
	s.TurnStart = time.Time{}
	want := "thinking... 12s · " + s.StatusLine(0)
	if row != want {
		t.Errorf("ticking label: row = %q, want %q", row, want)
	}
	if strings.Count(row, "s ·") > 1 || strings.HasSuffix(row, " · 34s") {
		t.Errorf("ticking label: row shows two counters: %q", row)
	}

	// A non-ticking label (a tool, say): the stats line's counter stays.
	a.activity = "study(main.go)"
	a.stats = statusStatsSample
	a.mu.Lock()
	row = a.statusLine()
	a.mu.Unlock()
	if !strings.Contains(row, "· 34s") {
		t.Errorf("non-ticking label: row lost the stats line's turn-elapsed segment: %q", row)
	}
}

// TestAnchorSetStatusAppendsStatsToTheRow pins the anchor-side half: with an
// activity label live, the rendered status row is the dim label plus the
// stats line, " · "-joined (issue #109's one plain-text status row); with
// stats cleared (zero value) the row is exactly what it was before this
// change — the dim label alone — so an existing caller that never wires
// SetStatus sees a byte-identical row. The assertions read a.status (the
// stored row text) rather than parsing redraw escape sequences off the
// sink, which the golden frames below already cover byte for byte.
func TestAnchorSetStatusAppendsStatsToTheRow(t *testing.T) {
	s := statusStatsSample
	// The anchor's clock (nowFn) is pinned at testNow; the sample's TurnStart
	// is 34s before it, so the turn-elapsed segment renders exactly "34s" —
	// deterministic (issue #109 review).
	wantRow := dim("thinking... · " + s.StatusLine(0))

	a, _ := newTestAnchor("> ", "", 200)
	// The label is live (SetThinking's "thinking...") before the stats
	// arrive — the production order.
	a.SetActivity("thinking...")
	a.SetStatus(s)
	if a.rows != 2 {
		t.Fatalf("rows = %d, want 2 (status + input)", a.rows)
	}
	if a.status != wantRow {
		t.Errorf("status row = %q, want %q", a.status, wantRow)
	}

	// A repaint (the tick loop's cadence) re-derives the same row.
	a.mu.Lock()
	a.refreshStatusLocked()
	a.mu.Unlock()
	if a.status != wantRow {
		t.Errorf("repainted row = %q, want %q", a.status, wantRow)
	}

	// Clearing the stats returns the row to label-only.
	a.SetStatus(StatusStats{})
	if a.status != dim("thinking...") {
		t.Errorf("cleared row = %q, want the bare dim label %q", a.status, dim("thinking..."))
	}
}

// TestAnchorSetStatusTrimsToTheRowWidth is the narrow-width half at the row
// level (the StatusLine-level priority order is covered by
// TestStatusLineTruncatesByPriority): at 40 columns the 14-column label plus
// the full stats line cannot fit, so the stats side trims to the width left
// after the label — and the visible row stays within the width.
func TestAnchorSetStatusTrimsToTheRowWidth(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 40)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()

	a.mu.Lock()
	a.activity = "thinking... 3s"
	a.refreshStatusLocked()
	a.stats = statusStatsSample
	a.refreshStatusLocked()
	a.mu.Unlock()

	if a.rows != 2 {
		t.Fatalf("rows = %d, want 2 (status + input)", a.rows)
	}
	visible := stripANSI(a.status)
	if dw := displayWidth(visible); dw > 40 {
		t.Errorf("row visible width %d exceeds 40: %q", dw, visible)
	}
	if !strings.HasPrefix(visible, "thinking... 3s") {
		t.Errorf("label must keep its full width (the caller's text, never trimmed by the stats): %q", visible)
	}
	// At 40 columns the 14-column label leaves 23 for the stats; the 31-
	// column model is itself clipped to those 23 ("…"-suffixed, via
	// StatusLine's final truncate), so NOTHING beyond the model can
	// survive — and the cost can never appear here.
	if strings.Contains(visible, "$") || strings.Contains(visible, "ctx ") || strings.Contains(visible, " in / ") {
		t.Errorf("row at 40 columns must keep the label + (clipped) model only: %q", visible)
	}
	// The row is the label, the joiner, and the model clipped to exactly the
	// width left after the label.
	want := "thinking... 3s · " + truncate(statusStatsSample.Model, 40-14-3)
	if visible != want {
		t.Errorf("row = %q, want %q", visible, want)
	}
}

// TestPlainReportPathsUnchanged is the step's acceptance item at the
// lineedit level: the row's degradation gates (NO_COLOR, CORTEX_LOOP_RENDER=0,
// non-TTY) live in cmd/cortex's renderEnabled() — anchoredInput() is false
// under all three, so the Anchor (and this status row) is never created and
// the plain scrolling report is the pre-change output, byte for byte. The
// row itself is plain text by design: its only ANSI is the hardcoded dim SGR
// lineedit wraps the status row in (no NO_COLOR toggle in this package — see
// the golden_test.go header), so nothing changes here for those envs. What IS
// pinned here: with no stats and no activity the block is one row and the row
// renders exactly the prompt — the shape the plain path must keep — and a
// stats value with no activity never opens a row on its own.
func TestPlainReportPathsUnchanged(t *testing.T) {
	a, out := newTestAnchor("> ", "draft", 80)
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	out.Reset()

	// A fresh redraw of the bare block is exactly the prompt line.
	a.mu.Lock()
	a.drawLocked()
	a.mu.Unlock()
	if a.rows != 1 {
		t.Fatalf("rows = %d, want 1 (no activity, no stats)", a.rows)
	}
	if got := strings.TrimSpace(stripANSI(out.String())); got != "> draft" {
		t.Errorf("bare row = %q, want the prompt and buffer only", got)
	}
	// A stats value with no activity must not open a status row on its own —
	// the row rides the activity label (thinking/tool), never the reverse.
	a.mu.Lock()
	a.stats = statusStatsSample
	a.refreshStatusLocked()
	a.mu.Unlock()
	if a.rows != 1 || a.status != "" {
		t.Errorf("stats without activity opened a status row: rows=%d status=%q", a.rows, a.status)
	}
}

// TestAnchorSetStatusClearsOnStop pins the Stop() field-clearing half of the
// lifecycle directly (the goroutine half — close(stop), <-done — runs against
// pre-closed channels here; the field drops are what the next turn's fresh
// anchor relies on not to inherit). Issue #109 review: an earlier version
// copied Stop()'s clearing statement inline and asserted its own copy, so it
// passed even if Stop stopped resetting — this drives the real Stop().
func TestAnchorSetStatusClearsOnStop(t *testing.T) {
	a, _ := newTestAnchor("> ", "", 80)
	a.mu.Lock()
	a.activity = "thinking..."
	a.stats = statusStatsSample
	a.refreshStatusLocked() // status row live with stats
	a.mu.Unlock()

	// Exercise the REAL Stop(): no real key/tick loops were started, so the
	// anchor's own channels stand in for the goroutines — done is pre-closed
	// (the goroutines have "exited"), and Stop() performs its own
	// close(stop) / <-done / eraseLocked / resetLocked for real.
	a.mu.Lock()
	a.stop = make(chan struct{})
	a.done = make(chan struct{})
	a.cancel = func() {}
	close(a.done) // the goroutines have "exited"
	a.mu.Unlock()
	got := a.Stop()
	if got != "" {
		t.Errorf("Stop() = %q, want the empty line", got)
	}
	if a.stats != (StatusStats{}) {
		t.Errorf("stats not cleared: %+v", a.stats)
	}
	if a.activity != "" || a.status != "" {
		t.Errorf("activity/status not cleared: %q %q", a.activity, a.status)
	}
}
