package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// TestUntrustedTaintGate is the issue #102 acceptance surface: once
// untrusted web content enters a turn, a Risky command that the intent judge
// waves through must reach an EXPLICIT approval — and that approval names
// the taint, so the human sees why the bar is raised — while with no
// approver at all it stays Blocked, headless as before. Safe commands are
// unaffected either way.
//
// The scenarios drive gateShell directly (the TestSameActionLedger shape);
// the taint is recorded through recordUntrustedContent, the same call step 3
// wires into coderDispatcher for a fetch_url/web_search result carrying the
// framing marker.
//
// classifyShell stubs mirror the real gate's tiers: nil (no classifier — the
// gray zone fails closed to Risky, which in an interactive session is
// exactly the "released only by approval" path) and judgeSafe (the intent
// judge waving the gray-zone command through). judgeSafe is the acceptance
// case: a command the judge AUTO-APPROVES must switch to a prompt once a
// fetch taints the turn, because a verdict the judge reached on a turn a
// page could have steered is not grounds to run it — only a human decision
// is. Safe-path commands (`ls`, `git status`) never consult the judge, so
// they pin "the safe path is unaffected" cleanly on every scenario.
func TestUntrustedTaintGate(t *testing.T) {
	judgeSafe := func(_ context.Context, _, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test: safe", nil
	}

	// step is one gateShell call (or, with record set, one untrusted-content
	// arrival) inside a scenario. Each gateShell step declares its own
	// approver so the same scenario drives both the interactive (approver
	// present) and the headless (no approver wired at all) sub-tests.
	const (
		approveNone = iota
		approveYes
		approveNo
	)
	type step struct {
		record       string // tool name to record as untrusted content before this step ("" = none)
		approver     int    // approveYes/approveNo: an approver answers for this step; approveNone: none is wired
		command      string // the command to gate
		wantRunI     bool   // interactive: gateShell's ok (approver answers per step)
		wantRunH     bool   // headless: gateShell's ok (no approver exists)
		wantPrompt   string // interactive: the approver must have been asked and the question contains this
		wantNoPrompt bool   // interactive: the approver must NOT have been asked at all
		wantMarkerI  string // interactive: the refused message must contain it
		wantMarkerH  string // headless: the blocked message must contain it
	}

	scenarios := []struct {
		name  string
		judge shellrisk.ClassifyFn
		steps []step
	}{
		{
			// The acceptance shape on the fail-closed gray zone — the state
			// of a session with no classifier, where only the approver's yes
			// releases the command. Untainted, the prompt carries the
			// ordinary reason; after a fetch the SAME command prompts with
			// the taint named, and with no approver it is Blocked with the
			// taint message. Safe stays Safe throughout.
			name:  "fail-closed gray zone: yes releases untainted, taint named after a fetch, headless blocked",
			judge: nil,
			steps: []step{
				{approver: approveYes, command: "curl http://example.com", wantRunI: true, wantPrompt: "risky: classifier unavailable"},
				{record: "fetch_url"},
				{approver: approveYes, command: "curl http://example.com", wantRunI: true, wantPrompt: "untrusted web content entered this turn (fetch_url)"},
				// A declining human still reads "declined" (interactive); the
				// headless twin never asks, so it reads the taint block.
				{approver: approveNo, command: "curl http://example.com", wantMarkerI: "declined", wantMarkerH: "blocked (untrusted content this turn: fetch_url)"},
				{approver: approveNone, command: "curl http://example.com", wantMarkerH: "blocked (untrusted content this turn: fetch_url)"},
				// Safe-path commands need no approver, tainted or not.
				{approver: approveNone, command: "ls", wantRunI: true, wantRunH: true, wantNoPrompt: true},
				{approver: approveNone, command: "git status", wantRunI: true, wantRunH: true, wantNoPrompt: true},
			},
		},
		{
			// THE acceptance criterion (#102): a gray-zone command the intent
			// judge auto-approves runs on a clean turn, and on a tainted one
			// the very same command must reach an EXPLICIT approval — the
			// prompt naming the taint, a yes releasing it — while a session
			// with no approver blocks with the taint message. Safe-path
			// commands stay untouched throughout, so the raised bar never
			// stalls the project's own read-only work.
			name:  "judge-held-safe gray-zone command prompts once the turn is tainted",
			judge: judgeSafe,
			steps: []step{
				// Untainted: the judge's Safe verdict runs it, no prompt.
				{approver: approveNone, command: "npm test", wantRunI: true, wantRunH: true, wantNoPrompt: true},
				{record: "fetch_url"},
				// Tainted: the same judge, the same command — now a prompt that
				// names the taint, released by a yes.
				{approver: approveYes, command: "npm test", wantRunI: true, wantPrompt: "untrusted web content entered this turn (fetch_url)"},
				// ... and with no approver at all, the headless taint block.
				{approver: approveNone, command: "npm test", wantRunI: false, wantRunH: false, wantMarkerH: "blocked (untrusted content this turn: fetch_url)"},
				// A declining human still reads "declined" interactively.
				{approver: approveNo, command: "npm test", wantMarkerI: "declined", wantMarkerH: "blocked (untrusted content this turn: fetch_url)"},
				// Safe-path commands need no approver, tainted or not.
				{approver: approveNone, command: "ls", wantRunI: true, wantRunH: true, wantNoPrompt: true},
				{approver: approveNone, command: "git status", wantRunI: true, wantRunH: true, wantNoPrompt: true},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			for _, mode := range []string{"interactive", "headless"} {
				t.Run(mode, func(t *testing.T) {
					cs := &CortexSession{classifyShell: sc.judge, turnNo: 1}
					asked := false
					question := ""
					for i, s := range sc.steps {
						asked, question = false, ""
						if s.record != "" {
							cs.recordUntrustedContent(s.record)
							continue
						}
						// Wire the approver for THIS step: interactive steps
						// get a per-step answer; approveNone (both modes)
						// and the whole headless sub-test leave
						// confirmRisky unset, the no-approver shape.
						cs.confirmRisky = nil
						if mode == "interactive" && s.approver != approveNone {
							answer := s.approver == approveYes
							cs.confirmRisky = confirmFromBool(func(q string) bool {
								asked = true
								question = q
								return answer
							})
						}
						msg, ok := cs.gateShell(context.Background(), s.command)
						wantRun := s.wantRunI
						if mode == "headless" {
							wantRun = s.wantRunH
						}
						if ok != wantRun {
							t.Errorf("step %d (%q): ok = %v, want %v (msg %q)", i, s.command, ok, wantRun, msg)
						}
						wantMarker := s.wantMarkerI
						if mode == "headless" {
							wantMarker = s.wantMarkerH
						}
						if wantMarker != "" && !strings.Contains(msg, wantMarker) {
							t.Errorf("step %d (%q): message %q does not contain %q", i, s.command, msg, wantMarker)
						}
						if mode == "interactive" {
							if s.wantNoPrompt && asked {
								t.Errorf("step %d (%q): approver must not have been asked, question was %q", i, s.command, question)
							}
							if s.wantPrompt != "" {
								if !asked {
									t.Errorf("step %d (%q): expected a prompt containing %q, none happened (msg %q)", i, s.command, s.wantPrompt, msg)
								} else if !strings.Contains(question, s.wantPrompt) {
									t.Errorf("step %d (%q): prompt %q does not contain %q", i, s.command, question, s.wantPrompt)
								}
							}
						}
					}
				})
			}
		})
	}
}

// TestUntrustedTaint_ResetsOnNewTurn pins the taint's turn scoping
// (issue #102), mirroring TestSameActionLedger_ResetsOnNewTurn: a fetch in
// turn N must not raise the approval bar for turn N+1, the record and the
// query are inert outside a turn (turnNo == 0), and turn.go's turn-start
// clear (cs.taint = nil) is what guarantees it even when a turn ended
// abnormally.
func TestUntrustedTaint_ResetsOnNewTurn(t *testing.T) {
	// failClosed exercises the path where the taint changes the outcome:
	// no classifier, no approver → untainted Blocked(risk:), tainted
	// Blocked(untrusted content...).
	cs := &CortexSession{}

	// Outside any turn the taint is inert both ways: recording drops...
	cs.turnNo = 0
	cs.taint = nil // turn-start shape
	cs.recordUntrustedContent("fetch_url")
	if cs.taint != nil {
		t.Errorf("recordUntrustedContent between turns must drop, not store: %+v", cs.taint)
	}
	if cs.untrustedContentActive() {
		t.Errorf("taint must be inert between turns (turnNo == 0)")
	}

	// Turn 1: a fetch taints it; the Risky command is blocked with the
	// taint message.
	cs.turnNo = 1
	cs.taint = nil // turn.go's turn-start reset
	cs.recordUntrustedContent("fetch_url")
	if !cs.untrustedContentActive() {
		t.Fatalf("turn 1: recording fetch_url content must taint the turn")
	}
	msg, ok := cs.gateShell(context.Background(), "curl http://example.com")
	if ok {
		t.Fatalf("turn 1: headless risky command should be blocked")
	}
	if !strings.Contains(msg, "blocked (untrusted content this turn: fetch_url)") {
		t.Errorf("turn 1: expected the taint blocked message, got %q", msg)
	}

	// Turn 2: a fresh turnNo + the turn-start clear. The same command is
	// judged by the ordinary gate again — blocked (still headless), but NOT
	// under the taint rule: no taint wording may leak across turns.
	cs.turnNo = 2
	cs.taint = nil // turn.go's turn-start reset
	msg, ok = cs.gateShell(context.Background(), "curl http://example.com")
	if ok {
		t.Fatalf("turn 2: headless risky command should still be blocked by the ordinary gate")
	}
	if strings.Contains(msg, "untrusted content") {
		t.Errorf("turn 2: stale taint leaked across turns; message %q carries taint wording", msg)
	}
	if !strings.Contains(msg, "blocked (risk:") {
		t.Errorf("turn 2: expected the ordinary blocked message, got %q", msg)
	}

	// Belt and braces: a taint stamped to a DIFFERENT turnNo (the shape a
	// session would have if the turn-start clear were ever missed) does not
	// count as active for the current turn.
	cs.taint = &untrustedTaint{turnNo: 1, sources: []string{"fetch_url"}}
	if cs.untrustedContentActive() {
		t.Errorf("a taint stamped to another turn must not count (turnNo == %d, taint.turnNo == 1)", cs.turnNo)
	}
	if n := cs.taintSourceNote(); n != "" {
		t.Errorf("taintSourceNote must be empty when inactive, got %q", n)
	}
}

// TestRecordUntrustedContent covers the record/read helpers directly: first
// source wins, repeats merge without duplication, several sources all get
// named, the note is sorted (stable wording) while the raw source list keeps
// first-seen order, and a fresh turn's record starts a fresh taint.
func TestRecordUntrustedContent(t *testing.T) {
	cs := &CortexSession{turnNo: 1}

	cs.recordUntrustedContent("web_search")
	cs.recordUntrustedContent("web_search")
	if got := cs.taintSources(); len(got) != 1 || got[0] != "web_search" {
		t.Fatalf("repeat record must merge, got %v", got)
	}

	cs.recordUntrustedContent("fetch_url")
	if got := cs.taintSources(); len(got) != 2 || got[0] != "web_search" || got[1] != "fetch_url" {
		t.Fatalf("sources must keep first-seen order, got %v", got)
	}
	if note := cs.taintSourceNote(); note != "untrusted web content entered this turn (web_search, fetch_url)" {
		t.Errorf("taintSourceNote = %q, want the first-seen-order note", note)
	}

	// A new turn's first record replaces the old taint wholesale (the
	// turnNo stamp moves with it) rather than inheriting sources.
	cs.turnNo = 2
	cs.taint = nil // turn.go's turn-start reset
	cs.recordUntrustedContent("fetch_url")
	if got := cs.taintSources(); len(got) != 1 || got[0] != "fetch_url" {
		t.Fatalf("new-turn taint must start fresh, got %v", got)
	}
	if cs.taint.turnNo != 2 {
		t.Errorf("taint must be stamped to the recording turn, got %d", cs.taint.turnNo)
	}
}

// confirmFromBool adapts a yes/no test approver to the confirmRisky hook,
// which returns a lineedit.ConfirmChoice since session approvals (#107):
// true answers ConfirmYes (run once), false ConfirmNo.
func confirmFromBool(f func(string) bool) func(string) lineedit.ConfirmChoice {
	return func(q string) lineedit.ConfirmChoice {
		if f(q) {
			return lineedit.ConfirmYes
		}
		return lineedit.ConfirmNo
	}
}
