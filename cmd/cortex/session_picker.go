package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/lineedit"
)

// sessionPickerView is the interactive session picker of issue #110: a
// lineedit.View over the session listing that the inspector harness scrolls,
// filters, and highlights. It implements the harness's three optional
// interfaces — Filterer (typed text narrows the rows), Cursorer (the harness
// says which row is highlighted), and Selecter (the caller asks, after the user
// accepts, which session that was) — so the harness needs to know nothing about
// sessions: it moves a cursor over however many rows the view chooses to show.
//
// The view owns all session-shaped presentation and none of the terminal:
// filtering, row text, the selection mark, and what "selected" means (a session
// id, not a row index) all live here; scrolling, repaints, and keys stay in
// internal/lineedit.
type sessionPickerView struct {
	// all is the listing in the order listSessions produced it — newest first.
	// The view never re-sorts: a picker is a listing the user already knows the
	// shape of, and re-ordering it under a filter would move rows the user is
	// looking at for no reason a filter can justify.
	all []sessionInfo

	// cursor is -1 rather than 0 on purpose: the harness's Selecter contract is
	// "a negative index when there is nothing to select", and a picker that has
	// not been given a cursor yet should not claim row 0 is picked.
	filter   string
	cursor   int
	accepted bool
}

// sessionsInspectable reports whether the session listing should open as the
// full-screen interactive picker rather than print the plain list. The gate is
// the same strict enhancement gate as /context's (context_view.go): it needs an
// interactive line editor (stdin is a TTY) AND the rich-render path (renderEnabled
// — stdout is a TTY, NO_COLOR unset, CORTEX_LOOP_RENDER not disabled). Anything
// scripted or piped — a driver, CI, a test, `cortex | less` — misses the gate
// and gets the same plain scrolling list it always did.
//
// The asymmetry is deliberate and is the whole point of the gate: the plain list
// can be read by a pipe, so nothing is lost by not opening the picker there.
func sessionsInspectable(editor *lineedit.Terminal) bool {
	return editor != nil && renderEnabled()
}

// inspectSession is the one call site main.go uses to open the picker, as a var
// so a test can capture the view handed to the harness without a real terminal.
// The seam is deliberately thin — it adds nothing but the indirection: a test
// that exercised a stubbed Inspect would be testing the stub, while the picker's
// own behavior is tested against the real harness in internal/lineedit.
var inspectSession = func(t *lineedit.Terminal, v lineedit.View) error { return t.Inspect(v) }

// resumePickerUsable is the startup gate for bare `cortex resume` (issue #110).
//
// It cannot reuse sessionsInspectable: the REPL's *lineedit.Terminal is opened
// later in main() than the resume block runs, so there is no editor value to
// test at this point. The equivalent condition is interactive stdin, which is
// what lineedit.Open would have succeeded on — plus the same renderEnabled() the
// /context and /sessions gates use, so NO_COLOR and CORTEX_LOOP_RENDER=0 keep
// today's plain latest-session resume.
//
// First-run bootstrap is excluded by the caller, not here: decideBootstrap has
// already run by the time this is consulted, and a first-run user (no config at
// all) has no sessions to pick — the picker must not stand between them and the
// setup flow.
func resumePickerUsable() bool {
	return lineedit.IsInteractive(os.Stdin) && renderEnabled()
}

// pickSessionAtStartup opens the picker for `cortex resume` with no id and
// reports whether the user chose a session. It returns ok=false on ESC, on an
// empty listing, and on any harness error, so the caller resumes the latest
// session exactly as it did before the picker existed — cancelling must never
// cost the user the resume they asked for by typing `resume`.
//
// The editor is opened here and closed before returning: the caller's resume
// runs after this, and the REPL opens its own editor later, so the picker must
// not hold the terminal (or raw mode) past its own screen.
//
// The session is passed in rather than the directory being derived from the CWD:
// `cortex resume --project <name>` re-targets cs at another workspace *before*
// this runs, and the listing must be that project's sessions, not the CWD's.
var pickSessionAtStartup = func(cs *CortexSession) (string, bool) {
	t, err := lineedit.Open(os.Stdin, os.Stdout)
	if err != nil {
		return "", false
	}
	defer t.Close()
	infos := listSessionsOrEmpty(cs.SessionsDir())
	if len(infos) == 0 {
		return "", false
	}
	picker := NewSessionPicker(infos)
	if err := inspectSession(t, picker); err != nil {
		return "", false
	}
	if !picker.Accepted() {
		return "", false
	}
	id := picker.SelectedID()
	if id == "" {
		return "", false
	}
	return id, true
}

// resumeSessionFromPicker switches the live session to the one the user picked:
// close the transcript currently open, re-hydrate the chosen one, and print the
// same loaded-context banner an ordinary `cortex resume <id>` prints.
//
// Closing first is required, not tidiness: StartTranscript/ResumeTranscript
// both assign cs.transcript, so the handle to the session being left behind
// would leak (and its buffered writes could land after the switch). Close
// nils the field, which is what ResumeTranscript expects of a session that has
// no transcript open.
//
// A failed resume is returned to the caller rather than papered over: the two
// call sites need different fallbacks (the REPL keeps the session it was
// already on; bare `cortex resume` starts fresh, which is today's behavior).
func resumeSessionFromPicker(cs *CortexSession, id string) error {
	if id == "" {
		return fmt.Errorf("no session selected")
	}
	cs.Close()
	if err := cs.ResumeTranscript(id); err != nil {
		return err
	}
	cs.showLoadedContext(id)
	return nil
}

// resumeFromSessionPicker switches the live session to the one the user picked
// from inside the REPL, and is what the /sessions branch calls: a resume that
// fails reopens the session the user was already on, so a bad id cannot lose or
// duplicate the conversation (see resumeOnPickerFailure).
func resumeFromSessionPicker(cs *CortexSession, id string) error {
	return resumeOnPickerFailure(cs, resumeSessionFromPicker(cs, id))
}

// resumeOnPickerFailure keeps a failed resume from costing the user their
// conversation. resumeSessionFromPicker closes the transcript that was open
// before it tries, so when the resume fails the session is left with no handle
// and the previous conversation still in memory: reopening prev is the only way
// to stay on it, because StartTranscript would write those same messages into a
// brand-new session file (and lose the pointer to the old one).
//
// It returns the resume error so the caller can still report it; when reopening
// prev also fails, that error is the more alarming one and is reported instead.
func resumeOnPickerFailure(cs *CortexSession, resumeErr error) error {
	if resumeErr == nil {
		return nil
	}
	prev := cs.SessionID
	fmt.Printf("resume: %v - staying on %s\n", resumeErr, prev)
	if err := cs.ResumeTranscript(prev); err != nil {
		fmt.Printf("resume: %v\n", err)
		return err
	}
	return resumeErr
}

// NewSessionPicker builds the picker over a session listing. The caller has
// already ordered it newest-first (listSessions does); the view preserves that
// order through any filter.
func NewSessionPicker(infos []sessionInfo) *sessionPickerView {
	return &sessionPickerView{all: infos, cursor: -1}
}

// Title implements lineedit.View: the prompt plus the filter text the user has
// typed so far. The filter lives here rather than in the body because the body
// must contain only selectable rows: the harness clamps its cursor against
// len(Lines()) and reports "row N of len(Lines())", so a filter row or a hint
// row inside the body would count as a selectable row and put the highlight, the
// footer's row number, and SelectedID out of step with each other. The harness's
// own footer already names the keys (type to filter, enter picks, esc cancels).
func (p *sessionPickerView) Title() string {
	if p.filter == "" {
		return "resume session"
	}
	return "resume session — filter: " + p.filter
}

// Filter implements lineedit.Filterer.
func (p *sessionPickerView) Filter() string { return p.filter }

// SetFilter implements lineedit.Filterer. The harness owns the string (it
// appends the typed rune and deletes one on Backspace); the view only decides
// what it means. Re-applying a filter leaves the cursor wherever it was rather
// than snapping to the top row — unless it now points past the end of the
// narrowed list, in which case it falls back to the last row that exists, so
// there is always a row under the cursor when there is a row at all.
func (p *sessionPickerView) SetFilter(s string) {
	p.filter = s
	matches := p.match()
	switch {
	case len(matches) == 0:
		p.cursor = -1
	case p.cursor > len(matches)-1:
		p.cursor = len(matches) - 1
	}
}

// SetCursor implements lineedit.Cursorer. The harness clamps the index to the
// row count it just read — which for this view is exactly the number of matching
// sessions, since the body holds nothing but rows — so the only judgement left
// here is the empty list, where there is no row to highlight.
func (p *sessionPickerView) SetCursor(i int) {
	if len(p.match()) == 0 {
		p.cursor = -1
		return
	}
	if i > len(p.match())-1 {
		i = len(p.match()) - 1
	}
	p.cursor = i
}

// Selected implements lineedit.Selecter: the highlighted row within the rows
// currently shown (the filtered list, not the full one), negative when there is
// nothing to select. The harness reads this to decide a view is selectable at
// all (inspectRun.selectable is set by the type assertion), so the signature is
// the harness's and returns a row index.
//
// The id the row stands for is the picker-specific answer, and it has its own
// accessor: SelectedID below. The two cannot share a name — Go has no
// overloading, and renaming the harness's method would break every other view —
// so the row index keeps the interface name and the caller of Inspect reads the
// id after the harness returns.
func (p *sessionPickerView) Selected() int { return p.cursor }

// SelectedID reports the session id the cursor is on — "" when nothing matches,
// so a caller that resumes on Enter can tell "nothing picked" from a real id
// without knowing the row layout. It is deliberately an id rather than an index:
// the row index is meaningless once the picker closes and the filter is
// forgotten.
func (p *sessionPickerView) SelectedID() string {
	matches := p.match()
	if p.cursor < 0 || p.cursor > len(matches)-1 {
		return ""
	}
	return matches[p.cursor].ID
}

// Accept implements lineedit.Accepter: it records that the user pressed Enter
// on the current row so the caller can tell an accepted pick from a bare ESC
// exit (both leave the inspector; only one should resume a session).
func (p *sessionPickerView) Accept() { p.accepted = true }

// Accepted reports whether the user accepted a row (Enter) rather than leaving
// with ESC.
func (p *sessionPickerView) Accepted() bool { return p.accepted }

// Lines implements lineedit.View: one row per matching session, and nothing
// else. The contract that matters is the row count — every line the body returns
// is a row the harness can highlight and count, so the filter text and the key
// hints stay out of it (the title carries the filter, the harness's footer names
// the keys). Rows are clamped to width: a session id is 15 characters and the
// prompt preview takes what is left, never more, so a row cannot wrap and break
// the harness's line accounting (its frame test pins the same rule for
// /context's view).
func (p *sessionPickerView) Lines(width int) []string {
	matches := p.match()
	rows := make([]string, 0, len(matches))
	for i, s := range matches {
		rows = append(rows, p.row(s, i == p.cursor, width))
	}
	return rows
}

// row renders one session: the selection mark, the id, the age, the turn count,
// the model, and as much of the first prompt as the width allows. The mark is
// ASCII (">" against " ") per the REPL's plain-text rule, and the selected row
// is also bolded, so a terminal without color still shows which row is picked.
func (p *sessionPickerView) row(s sessionInfo, selected bool, width int) string {
	mark := "  "
	if selected {
		mark = "> "
	}
	rel := relTime(s.ModTime)
	turns := fmt.Sprintf("%d turns", s.Turns)
	model := s.Model
	if model == "" {
		model = "-"
	}
	// The fixed part is measured without color, so a bolded row does not shift
	// the columns the preview is supposed to start at.
	fixed := mark + s.ID + "  " + rel + "  " + turns + "  " + model + "  "
	text := mark + s.ID + "  " + rel + "  " + turns + "  " + model + "  " + s.First
	if s.First == "" {
		text = strings.TrimRight(fixed, " ")
	}
	if r := []rune(text); width > 0 && len(r) > width {
		// Trim the preview, not the whole row: an id and its age are what
		// distinguishes rows, so they survive and the prompt is what gives way.
		keep := width - len([]rune(fixed))
		if keep < 1 {
			return trimRunes(fixed, width)
		}
		text = fixed + trimRunes(s.First, keep)
	}
	if !selected {
		return text
	}
	return withColor(text, "\033[1m")
}

// match is the filtered listing: a case-insensitive substring match against the
// prompt, the id, and the model, in the caller's (newest-first) order. Matching
// the id and the model as well as the prompt is what makes the box useful on a
// workspace full of sessions that all start with "fix" — typing part of an id,
// or the model you remember running, finds the row.
func (p *sessionPickerView) match() []sessionInfo {
	if p.filter == "" {
		return p.all
	}
	needle := strings.ToLower(p.filter)
	var out []sessionInfo
	for _, s := range p.all {
		if strings.Contains(strings.ToLower(s.First), needle) ||
			strings.Contains(strings.ToLower(s.ID), needle) ||
			strings.Contains(strings.ToLower(s.Model), needle) {
			out = append(out, s)
		}
	}
	return out
}

// trimRunes cuts s to n runes, appending the ellipsis when it had to cut, and
// returns s unchanged when n is not a reduction.
func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// compile-time proof that the picker satisfies the harness's view contracts, so
// a signature drift in internal/lineedit is a build failure here rather than a
// picker that silently stops being interactive.
var (
	_ lineedit.View     = (*sessionPickerView)(nil)
	_ lineedit.Filterer = (*sessionPickerView)(nil)
	_ lineedit.Cursorer = (*sessionPickerView)(nil)
	_ lineedit.Selecter = (*sessionPickerView)(nil)
	_ lineedit.Accepter = (*sessionPickerView)(nil)
)
