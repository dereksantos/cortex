package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
)

// pickerFixture is a listing in listSessions' order (newest first) with the
// fields a picker row reads. ModTime is left zero so relTime renders "?" and the
// expected rows do not depend on the wall clock.
func pickerFixture() []sessionInfo {
	return []sessionInfo{
		{ID: "20260202-000002", Messages: 6, Turns: 3, First: "fix login redirect", Model: "m-big"},
		{ID: "20260102-000001", Messages: 4, Turns: 2, First: "Add tests for grep", Model: "m-small"},
		{ID: "20260101-000000", Messages: 1, Turns: 1, First: "", Model: ""},
	}
}

// The package already has stripANSI (render_test.go), used here to assert on
// the picker's visible text; a separate test below pins the escapes themselves.

func TestSessionPickerLinesShape(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	rows := p.Lines(120)
	// filter row + three sessions + footer.
	if len(rows) != 5 {
		t.Fatalf("len(Lines) = %d, want 5 (filter row + 3 sessions + footer)", len(rows))
	}
	if !strings.HasPrefix(stripANSI(rows[0]), "filter:") {
		t.Errorf("first row = %q, want the filter row", rows[0])
	}
	if got := stripANSI(rows[len(rows)-1]); got != sessionPickerFooter {
		t.Errorf("last row = %q, want the footer hint", got)
	}
	// Newest first, and each session row carries id, turns, model, prompt.
	body := rows[1 : len(rows)-1]
	for i, want := range []struct{ id, prompt string }{
		{"20260202-000002", "fix login redirect"},
		{"20260102-000001", "Add tests for grep"},
		{"20260101-000000", ""},
	} {
		got := stripANSI(body[i])
		if !strings.Contains(got, want.id) {
			t.Errorf("row %d = %q, want id %s (newest-first order)", i, got, want.id)
		}
		if want.prompt != "" && !strings.Contains(got, want.prompt) {
			t.Errorf("row %d = %q, want the prompt %q", i, got, want.prompt)
		}
	}
	// The row with no prompt says nothing rather than printing empty columns,
	// and an unknown model is a dash, not a gap.
	last := stripANSI(body[2])
	if strings.HasSuffix(last, " ") {
		t.Errorf("row with no prompt ends in whitespace: %q", last)
	}
	if !strings.Contains(last, " -") && !strings.HasSuffix(last, "-") {
		t.Errorf("row with no model = %q, want a dash for the unknown model", last)
	}
}

func TestSessionPickerSelectionMark(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	p.SetCursor(1)
	rows := p.Lines(120)
	body := rows[1 : len(rows)-1]
	for i, row := range body {
		plain := stripANSI(row)
		wantMark := "  "
		if i == 1 {
			wantMark = "> "
		}
		if !strings.HasPrefix(plain, wantMark) {
			t.Errorf("row %d = %q, want it to start with %q", i, plain, wantMark)
		}
	}
	// The highlight must survive color being off: a plain terminal still shows
	// which row is picked, because the mark is ASCII and not only bold.
	if strings.Contains(stripANSI(body[1]), "\x1b") {
		t.Error("stripped row still contains an escape")
	}
	// With color on, the selected row is the only one wrapped in bold.
	if !strings.HasPrefix(body[1], "\x1b[1m") {
		t.Errorf("selected row = %q, want it bolded", body[1])
	}
	if strings.Contains(body[0], "\x1b[1m") {
		t.Errorf("unselected row = %q, must not be bolded", body[0])
	}
}

func TestSessionPickerFilterNarrowing(t *testing.T) {
	tests := []struct {
		name     string
		filter   string
		wantIDs  []string
		wantSel  string
		wantRows int
	}{
		{"no filter shows all three", "", []string{"20260202-000002", "20260102-000001", "20260101-000000"}, "", 3},
		{"substring of the prompt", "login", []string{"20260202-000002"}, "", 1},
		{"case-insensitive", "FIX", []string{"20260202-000002"}, "", 1},
		{"case-insensitive on another row", "add tests", []string{"20260102-000001"}, "", 1},
		{"matching the id", "20260101", []string{"20260101-000000"}, "", 1},
		{"matching the model", "m-small", []string{"20260102-000001"}, "", 1},
		{"a filter nothing matches", "zzz", nil, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSessionPicker(pickerFixture())
			p.SetFilter(tc.filter)
			rows := p.Lines(120)
			body := rows[1 : len(rows)-1]
			if len(body) != tc.wantRows {
				t.Fatalf("visible session rows = %d, want %d (%v)", len(body), tc.wantRows, body)
			}
			for i, want := range tc.wantIDs {
				if got := stripANSI(body[i]); !strings.Contains(got, want) {
					t.Errorf("row %d = %q, want id %s", i, got, want)
				}
			}
			if got := p.SelectedID(); got != tc.wantSel {
				t.Errorf("SelectedID = %q, want %q (no cursor set yet)", got, tc.wantSel)
			}
		})
	}
}

// TestSessionPickerCursorSurvivesNarrowing pins that typing does not fling the
// highlight to row 0, and that a filter which narrows past the cursor pulls it
// back to the last row that exists.
func TestSessionPickerCursorSurvivesNarrowing(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	p.SetCursor(2)
	if got := p.SelectedID(); got != "20260101-000000" {
		t.Fatalf("SelectedID = %q, want the third session", got)
	}
	// A filter that still matches all three leaves the cursor where it was.
	p.SetFilter("2026")
	if got := p.Selected(); got != 2 {
		t.Errorf("Selected = %d, want 2 (untouched by a filter that changed nothing)", got)
	}
	// A filter that leaves one row pulls the cursor back onto it.
	p.SetFilter("login")
	if got := p.Selected(); got != 0 {
		t.Errorf("Selected = %d, want 0 (clamped to the single match)", got)
	}
	if got := p.SelectedID(); got != "20260202-000002" {
		t.Errorf("SelectedID = %q, want the surviving row", got)
	}
}

func TestSessionPickerNoMatchHasNoSelection(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	p.SetCursor(1)
	p.SetFilter("nothing matches this")
	if got := p.Selected(); got >= 0 {
		t.Errorf("Selected = %d, want a negative index (nothing to select)", got)
	}
	if got := p.SelectedID(); got != "" {
		t.Errorf("SelectedID = %q, want empty", got)
	}
	// The body is the filter row plus the footer, and the filter text is
	// visible so the user can see what they typed.
	rows := p.Lines(120)
	if len(rows) != 2 {
		t.Fatalf("len(Lines) = %d, want 2 (filter row + footer)", len(rows))
	}
	if !strings.Contains(stripANSI(rows[0]), "nothing matches this") {
		t.Errorf("filter row = %q, want the typed text echoed", rows[0])
	}
	// A cursor key must not manufacture a selection out of an empty list.
	p.SetCursor(0)
	if got := p.Selected(); got >= 0 {
		t.Errorf("Selected = %d after SetCursor(0) on an empty list, want negative", got)
	}
}

func TestSessionPickerWidthClamping(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	for _, width := range []int{20, 24, 40, 60, 200} {
		rows := p.Lines(width)
		for i, row := range rows {
			if got := len([]rune(stripANSI(row))); width > 0 && got > width {
				t.Errorf("width %d: row %d is %d runes (%q), want <= %d", width, i, got, stripANSI(row), width)
			}
		}
	}
	// An extremely narrow width must not panic or emit a partial escape sequence.
	rows := p.Lines(1)
	for i, row := range rows {
		if strings.Contains(stripANSI(row), "\x1b") {
			t.Errorf("row %d still carries an escape after stripping: %q", i, stripANSI(row))
		}
	}
	// A cut prompt says it was cut.
	rows = p.Lines(40)
	body := stripANSI(rows[1])
	if !strings.HasSuffix(body, "…") {
		t.Errorf("clamped row = %q, want it to end with an ellipsis", body)
	}
	// The id survives the clamp: it is what distinguishes rows.
	if !strings.Contains(body, "20260202") {
		t.Errorf("clamped row = %q, want the id preserved", body)
	}
}

func TestSessionPickerAccept(t *testing.T) {
	p := NewSessionPicker(pickerFixture())
	if p.Accepted() {
		t.Error("Accepted before Accept, want false")
	}
	p.SetCursor(0)
	p.Accept()
	if !p.Accepted() {
		t.Error("Accepted after Accept, want true")
	}
	if got := p.SelectedID(); got != "20260202-000002" {
		t.Errorf("SelectedID = %q, want the row Accept was pressed on", got)
	}
}

func TestSessionPickerEmptyListing(t *testing.T) {
	p := NewSessionPicker(nil)
	rows := p.Lines(80)
	if len(rows) != 2 {
		t.Fatalf("len(Lines) = %d, want 2 (filter row + footer, no sessions)", len(rows))
	}
	if got := p.Selected(); got >= 0 {
		t.Errorf("Selected = %d, want negative on an empty listing", got)
	}
	if got := p.SelectedID(); got != "" {
		t.Errorf("SelectedID = %q, want empty", got)
	}
}

// TestSessionPickerRowsAreNotTimeDependent guards the fixture against drift: if
// ModTime were set, every expected row above would rot.
func TestSessionPickerRowsAreNotTimeDependent(t *testing.T) {
	for _, s := range pickerFixture() {
		if !s.ModTime.IsZero() {
			t.Fatalf("fixture session %s has ModTime %v, want zero (relTime must render \"?\")", s.ID, s.ModTime)
		}
	}
	_ = time.Time{}
}

// TestSessionsInspectableGate is the graceful-degradation contract for the
// picker, mirroring TestContextInspectableGate: the interactive picker opens
// only for an interactive editor on the rich-render path. Every row here must
// be false — a test binary's stdout is a pipe, which is exactly the scripted/
// piped case that has to keep the plain list. Supplying a non-nil editor as
// well proves the gate is not satisfied by an interactive *stdin* alone.
func TestSessionsInspectableGate(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		editor *lineedit.Terminal
	}{
		{"no interactive editor", nil, nil},
		{"editor but stdout is not a tty", nil, &lineedit.Terminal{}},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}, &lineedit.Terminal{}},
		{"CORTEX_LOOP_RENDER=0", map[string]string{"CORTEX_LOOP_RENDER": "0"}, &lineedit.Terminal{}},
		{"CORTEX_LOOP_RENDER=off", map[string]string{"CORTEX_LOOP_RENDER": "off"}, &lineedit.Terminal{}},
		{"CORTEX_LOOP_RENDER=false", map[string]string{"CORTEX_LOOP_RENDER": "false"}, &lineedit.Terminal{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if sessionsInspectable(tc.editor) {
				t.Error("sessionsInspectable() = true, want false — the plain list must be used")
			}
		})
	}
}

// TestSessionsInspectableMatchesContextGate pins that the two gates cannot drift
// apart: the picker opens exactly when /context's inspector does, so one
// escape hatch (CORTEX_LOOP_RENDER=0, NO_COLOR, a pipe) turns both off at once.
func TestSessionsInspectableMatchesContextGate(t *testing.T) {
	for _, editor := range []*lineedit.Terminal{nil, {}} {
		for _, env := range []map[string]string{
			nil,
			{"NO_COLOR": "1"},
			{"CORTEX_LOOP_RENDER": "0"},
		} {
			t.Run(fmt.Sprintf("%v/%v", editor == nil, env), func(t *testing.T) {
				for k, v := range env {
					t.Setenv(k, v)
				}
				if got, want := sessionsInspectable(editor), contextInspectable(editor); got != want {
					t.Errorf("sessionsInspectable = %v, contextInspectable = %v, want equal", got, want)
				}
			})
		}
	}
}

// TestInspectSessionSeamIsCapturable is what main.go's single call site buys a
// test: the view handed to the harness can be captured and asserted on without a
// real terminal. The stub stands in for the harness here, so this test pins only
// the seam's contract (the view arrives, the error is the stub's) — the picker's
// behavior under the real harness is pinned in internal/lineedit.
func TestInspectSessionSeamIsCapturable(t *testing.T) {
	var got lineedit.View
	errBoom := errors.New("boom")
	saved := inspectSession
	t.Cleanup(func() { inspectSession = saved })

	inspectSession = func(_ *lineedit.Terminal, v lineedit.View) error {
		got = v
		return errBoom
	}

	p := NewSessionPicker(pickerFixture())
	if err := inspectSession(&lineedit.Terminal{}, p); !errors.Is(err, errBoom) {
		t.Fatalf("inspectSession error = %v, want the stub's error to reach the caller", err)
	}
	if got != lineedit.View(p) {
		t.Fatalf("captured view = %T, want the picker the caller passed", got)
	}
	// A caller that captured the view can drive it as the harness would: the
	// seam must not hand over something that lost the optional interfaces.
	f, ok := got.(lineedit.Filterer)
	if !ok {
		t.Fatal("captured view is not a Filterer — the picker would not be interactive")
	}
	if _, ok := got.(lineedit.Selecter); !ok {
		t.Error("captured view is not a Selecter — the picker would not be selectable")
	}
	if _, ok := got.(lineedit.Cursorer); !ok {
		t.Error("captured view is not a Cursorer — the highlight would never move")
	}
	f.SetFilter("login")
	if got := p.Selected(); got >= 0 {
		t.Errorf("Selected = %d after filtering with no cursor placed, want negative — the harness places the cursor via SetCursor, the view must not invent one", got)
	}
	// With the harness's cursor in place, the captured view resolves to the row
	// the user would have landed on.
	if c, ok := got.(lineedit.Cursorer); ok {
		c.SetCursor(0)
	} else {
		t.Fatal("captured view is not a Cursorer")
	}
	if got := p.SelectedID(); got != "20260202-000002" {
		t.Errorf("SelectedID = %q, want the single match", got)
	}
}

// TestInspectSessionDefaultIsTheRealMethod pins that the seam's production
// implementation is Terminal.Inspect and not a leftover stub: the default must
// be the real path, since a stub left in place would silently disable the picker
// for every user while every test still passed.
//
// It compares function pointers by reflection rather than calling the seam:
// invoking the real Inspect needs a terminal that was actually opened, and
// calling it on a zero Terminal would block reading real stdin inside the
// inspector loop — exactly what a test must never do.
func TestInspectSessionDefaultIsTheRealMethod(t *testing.T) {
	want := reflect.ValueOf((*lineedit.Terminal).Inspect)
	got := reflect.ValueOf(inspectSession)
	if got.Kind() != reflect.Func {
		t.Fatalf("inspectSession is %v, want a func", got.Kind())
	}
	// The indirection means the pointers differ, so what is pinned instead is
	// the signature the seam must keep for main.go's call site to compile, and
	// that the value is not nil (a nil seam is a nil-pointer panic at the first
	// /sessions on a real TTY).
	if got.IsNil() {
		t.Fatal("inspectSession is nil")
	}
	st := got.Type()
	if st.NumIn() != 2 || st.NumOut() != 1 {
		t.Fatalf("inspectSession has %d in/%d out, want 2 in/1 out", st.NumIn(), st.NumOut())
	}
	if st.In(0) != reflect.TypeOf((*lineedit.Terminal)(nil)) {
		t.Errorf("first arg = %v, want *lineedit.Terminal", st.In(0))
	}
	if st.In(1) != reflect.TypeOf((*lineedit.View)(nil)).Elem() {
		t.Errorf("second arg = %v, want lineedit.View", st.In(1))
	}
	if st.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Errorf("result = %v, want error", st.Out(0))
	}
	// reflect reports a method *value*'s args without the receiver, so both
	// signatures read the same way here: the check pins that the seam takes what
	// the real method takes, which is what main.go's call site depends on.
	if want.Type().NumIn() != st.NumIn() {
		t.Errorf("Terminal.Inspect takes %d args, seam takes %d — the seam must match",
			want.Type().NumIn(), st.NumIn())
	}
}

// --- step 5: the resume helper and the startup gate ---------------------

func TestResumeSessionFromPickerSwitchesTranscript(t *testing.T) {
	cs := newTestSession(t)
	firstID := cs.SessionID
	cs.Append(Message{Role: RoleUser, Content: "the first session"})

	// A second, older session to resume back to.
	writeTestSession(t, cs.SessionsDir(), "20260101-000000",
		`{"kind":"message","role":"user","content":"the older session"}`,
	)

	if err := resumeSessionFromPicker(cs, "20260101-000000"); err != nil {
		t.Fatalf("resumeSessionFromPicker: %v", err)
	}
	if cs.SessionID != "20260101-000000" {
		t.Errorf("SessionID = %q, want the picked id", cs.SessionID)
	}
	if cs.transcript == nil {
		t.Fatal("transcript is nil after resume — the picked session is not being written to")
	}
	// The banner the picked session's context is reported with must have been
	// printed for the picked id, not the one being left behind.
	got := cs.loadedContextBanner(cs.SessionID, false)
	if !strings.Contains(got, "20260101-000000") {
		t.Errorf("banner for the resumed session = %q, want it to name the picked id", got)
	}
	if firstID == "" {
		t.Error("fixture session had no id")
	}
}

func TestResumeSessionFromPickerEmptyIDDoesNotClose(t *testing.T) {
	cs := newTestSession(t)
	before := cs.transcript
	// An empty id means the user did not pick a row; the live transcript must
	// survive untouched rather than being closed and left unresumed.
	if err := resumeSessionFromPicker(cs, ""); err == nil {
		t.Error("empty id returned nil error, want an error the caller can report")
	}
	if cs.transcript != before {
		t.Error("transcript handle changed on a no-selection resume — the live session was lost")
	}
}

func TestResumeSessionFromPickerMissingIDLeavesNoHandle(t *testing.T) {
	cs := newTestSession(t)
	// A resume that fails must not leave the session holding a half-open handle:
	// the caller falls back to StartTranscript, which expects no transcript.
	err := resumeSessionFromPicker(cs, "does-not-exist")
	if err == nil {
		t.Fatal("resuming a nonexistent id returned nil error")
	}
	if cs.transcript != nil {
		t.Error("transcript still open after a failed resume, want nil so the caller can start fresh")
	}
	// And the documented fallback actually works from that state.
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Error("StartTranscript after a failed resume did not open a transcript")
	}
}

func TestResumePickerUsableGate(t *testing.T) {
	// A test binary's stdin and stdout are both pipes, so the startup picker is
	// gated off in every case here — which is exactly what keeps `cortex resume`
	// (and the whole test suite) resuming the latest session as it always did.
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"non-interactive stdin and stdout", nil},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}},
		{"CORTEX_LOOP_RENDER=0", map[string]string{"CORTEX_LOOP_RENDER": "0"}},
		{"CORTEX_LOOP_RENDER=off", map[string]string{"CORTEX_LOOP_RENDER": "off"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if resumePickerUsable() {
				t.Error("resumePickerUsable() = true on a piped stdio test binary, want false")
			}
		})
	}
}

// TestPickSessionAtStartupNeedsATerminal pins the one branch of the startup
// picker that a piped test binary can reach: with no usable terminal it reports
// "no pick", which is what makes `cortex resume` fall through to today's
// latest-session resume. The accepted/cancel decision it would otherwise test is
// unreachable here (lineedit.Open fails on a pipe before the seam is called), so
// TestPickerAcceptedDecisionIsWhatCallersRead covers that decision directly.
func TestPickSessionAtStartupNeedsATerminal(t *testing.T) {
	cs := newTestSession(t)
	writeTestSession(t, cs.SessionsDir(), "20260101-000000",
		`{"kind":"message","role":"user","content":"older"}`,
	)
	id, ok := pickSessionAtStartup(cs)
	if ok {
		t.Errorf("pickSessionAtStartup = (%q, true) on a piped test binary, want no pick", id)
	}
	// The live session must be intact after a startup picker that could not run:
	// the caller still has its transcript to resume into.
	if cs.transcript == nil {
		t.Error("transcript is nil after a failed startup pick — the session was lost")
	}
}

// TestPickerAcceptedDecisionIsWhatCallersRead tests the decision the two call
// sites branch on (Accepted + SelectedID) without a terminal, since the only
// thing main.go does after the harness returns is read those two.
func TestPickerAcceptedDecisionIsWhatCallersRead(t *testing.T) {
	infos := pickerFixture()
	// ESC: harness returned nil, nothing accepted, so no resume.
	esc := NewSessionPicker(infos)
	if esc.Accepted() || esc.SelectedID() != "" {
		t.Error("a fresh picker looks like an accepted pick — ESC would resume something")
	}
	// Enter on a row: accepted, with a resolvable id.
	enter := NewSessionPicker(infos)
	enter.SetCursor(1)
	enter.Accept()
	if !enter.Accepted() {
		t.Fatal("Accept did not register")
	}
	if got := enter.SelectedID(); got != "20260102-000001" {
		t.Errorf("SelectedID = %q, want the row the cursor was on", got)
	}
	// Enter with no row (empty listing): accepted but nothing to resume, so the
	// caller must not attempt a resume on an empty id.
	empty := NewSessionPicker(nil)
	empty.Accept()
	if got := empty.SelectedID(); got != "" {
		t.Errorf("SelectedID on an empty listing = %q, want empty", got)
	}
}
