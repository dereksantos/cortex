// issue161_engine_test.go — issue #161, step 3: pin the engine behavior for
// the two halves of the cap-hit fix, table-driven and stdlib-only.
//
// Half 1 — OnForcedFinalize (step 1): when a bound drags the run to its
// forced finalize, the seam fires exactly once with the run's stats as they
// stand at that moment (stop reason set, FinalizeForced true), and a
// non-empty note gets one more tools-withheld finalize round whose reply is
// APPENDED to the forced answer. An empty note, a nil hook, an empty reply,
// or a failed note-round send leaves the answer untouched. The seam never
// fires on a clean finalize.
//
// Half 2 — cap-approaching warning (step 2): when the model asks for tool
// calls with at most toolCapWarningRounds left, runLoop injects ONE
// "Harness note" naming the remaining count and asking the model to wrap up
// or clean up. The warning is absent when MaxIter is at or below the
// threshold, and on a clean finalize before the threshold.
//
// The table-driven structure: each test case names the scenario, the bounds,
// the scripted model behavior, and the expected assertions (stop reason,
// whether the seam fired, how many cap-warnings appeared on the wire, the
// expected content, and the expected hook stop reason).
//
// The existing TestForcedFinalizeHookSeam / TestCapApproachingWarning /
// TestForcedFinalizeOtherExits in forced_finalize_test.go remain as the
// detailed per-subtest locks; this file adds the TABLE-DRIVEN pins the
// issue calls for, covering the stop reasons those tests don't reach
// (no-progress, read-budget) and locking the once-per-turn / absent-on-clean
// / absent-below-threshold invariants in a single pass.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// --- forced-finalize seam: table-driven ---

// forcedFinalizeCase is one row of the forced-finalize seam table.
type forcedFinalizeCase struct {
	name        string
	bounds      Bounds
	toolRounds  int // how many tool-call rounds the scripted model issues
	answer      string
	noteReply   string // the model's reply to the hook's note ("" = empty reply)
	note        string // the hook's note ("" = empty note, nilHook = nil)
	nilHook     bool
	failNote    bool // the note-round send fails
	wantStop    string
	wantFired   bool // the hook was consulted at least once
	wantFires   int  // how many times the hook was consulted (0 or 1)
	wantContent string
}

// forcedFinalizeScriptedSender builds a SenderFunc that issues toolRounds
// distinct tool-call rounds, then returns answer as the forced finalize
// (and noteReply if a note round fires). If failNote, the note round returns
// an error.
func forcedFinalizeScriptedSender(toolRounds int, answer, noteReply string, failNote bool) SenderFunc {
	var i int
	return SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		i++
		if i <= toolRounds {
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		}
		// i == toolRounds+1 is the forced finalize; i == toolRounds+2 is the
		// note round (only reached when the hook returned a non-empty note).
		if failNote && i == toolRounds+2 {
			return nil, false, errors.New("note round failed")
		}
		if i == toolRounds+2 {
			return fakeResp(noteReply, nil, 5, 5), false, nil
		}
		return fakeResp(answer, nil, 5, 5), false, nil
	})
}

// TestForcedFinalizeSeamTable pins the OnForcedFinalize seam's contract
// across every stop reason the engine produces, table-driven.
func TestForcedFinalizeSeamTable(t *testing.T) {
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		return "OBS for " + call.Function.Arguments
	})

	cases := []forcedFinalizeCase{
		{
			name:        "max-iter: non-empty note appended",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 3},
			toolRounds:  3,
			answer:      "forced answer",
			note:        "leftover note",
			noteReply:   "the accounting",
			wantStop:    "max-iter",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer\n\nthe accounting",
		},
		{
			name:        "max-iter: empty note skipped",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 3},
			toolRounds:  3,
			answer:      "forced answer",
			note:        "",
			wantStop:    "max-iter",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer",
		},
		{
			name:        "max-iter: nil hook untouched",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 3},
			toolRounds:  3,
			answer:      "forced answer",
			nilHook:     true,
			wantStop:    "max-iter",
			wantFired:   false,
			wantFires:   0,
			wantContent: "forced answer",
		},
		{
			name:        "max-iter: empty reply untouched",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 3},
			toolRounds:  3,
			answer:      "forced answer",
			note:        "leftover note",
			noteReply:   "",
			wantStop:    "max-iter",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer",
		},
		{
			name:        "max-iter: failed note round untouched",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 3},
			toolRounds:  3,
			answer:      "forced answer",
			note:        "leftover note",
			noteReply:   "never reached",
			failNote:    true,
			wantStop:    "max-iter",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer",
		},
		{
			name:        "token-budget: non-empty note appended",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 100, TokenBudget: 14},
			toolRounds:  1,
			answer:      "forced answer",
			note:        "leftover note",
			noteReply:   "the accounting",
			wantStop:    "token-budget",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer\n\nthe accounting",
		},
		{
			name:        "read-budget: non-empty note appended",
			bounds:      Bounds{MaxTokens: 100, MaxIter: 100, ReadBudgetBytes: 10},
			toolRounds:  1,
			answer:      "forced answer",
			note:        "leftover note",
			noteReply:   "the accounting",
			wantStop:    "read-budget",
			wantFired:   true,
			wantFires:   1,
			wantContent: "forced answer\n\nthe accounting",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
			var msgs []Message
			appendMsg := func(m Message) {
				req.Messages = append(req.Messages, m)
				msgs = append(msgs, m)
			}
			send := forcedFinalizeScriptedSender(tc.toolRounds, tc.answer, tc.noteReply, tc.failNote)

			var hookCalls int
			var hookStop string
			ts := Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}
			if !tc.nilHook {
				ts.OnForcedFinalize = func(stats loopStats) string {
					hookCalls++
					hookStop = stats.StopReason
					return tc.note
				}
			}

			content, stats, err := runLoop(context.Background(), send, req, ts, tc.bounds, nil, appendMsg, nil)
			if err != nil {
				t.Fatalf("runLoop: %v", err)
			}
			if stats.StopReason != tc.wantStop {
				t.Fatalf("stop = %q, want %q", stats.StopReason, tc.wantStop)
			}
			if !stats.FinalizeForced {
				t.Fatal("FinalizeForced = false, want true (bound-forced exit)")
			}
			if tc.wantFired && hookCalls == 0 {
				t.Fatal("OnForcedFinalize was not consulted, want at least 1 call")
			}
			if hookCalls != tc.wantFires {
				t.Errorf("OnForcedFinalize calls = %d, want %d", hookCalls, tc.wantFires)
			}
			if tc.wantFired && hookStop != tc.wantStop {
				t.Errorf("hook saw stop=%q, want %q", hookStop, tc.wantStop)
			}
			if content != tc.wantContent {
				t.Errorf("content = %q, want %q", content, tc.wantContent)
			}
			// The hook's note is on the wire when it was non-empty.
			if tc.note != "" && !tc.nilHook {
				found := false
				for _, m := range msgs {
					if m.Role == RoleUser && strings.Contains(m.Content, tc.note) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("hook note %q not found on the wire", tc.note)
				}
			}
		})
	}
}

// TestForcedFinalizeCleanNoFire pins that the seam never fires on a clean
// finalize: the model answers without tool calls before any bound trips, and
// OnForcedFinalize is never consulted.
func TestForcedFinalizeCleanNoFire(t *testing.T) {
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		return "OBS for " + call.Function.Arguments
	})
	var hookCalls int
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	var msgs []Message
	appendMsg := func(m Message) {
		req.Messages = append(req.Messages, m)
		msgs = append(msgs, m)
	}
	var i int
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		i++
		if i == 1 {
			return fakeResp("", []ToolCall{readCall("c1", "a.go")}, 10, 4), false, nil
		}
		return fakeResp("clean answer", nil, 5, 5), false, nil
	})
	ts := Toolset{
		Tools:    []Tool{tools.ReadFile},
		Dispatch: disp,
		OnForcedFinalize: func(loopStats) string {
			hookCalls++
			return "note"
		},
	}
	content, stats, err := runLoop(context.Background(), send, req, ts,
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "clean-finalize" {
		t.Fatalf("stop = %q, want clean-finalize", stats.StopReason)
	}
	if hookCalls != 0 {
		t.Errorf("OnForcedFinalize calls = %d, want 0 (clean finalize never consults the hook)", hookCalls)
	}
	if content != "clean answer" {
		t.Errorf("content = %q, want the clean answer untouched", content)
	}
}

// --- cap-approaching warning: table-driven ---

// capWarningCase is one row of the cap-warning table.
type capWarningCase struct {
	name          string
	toolRounds    int
	maxIter       int
	wantStop      string
	wantWarnings  int // how many cap warnings on the wire
	wantRemaining int // the remaining count the warning names (0 = absent)
}

// TestCapWarningTable pins the cap-approaching warning's contract,
// table-driven: fires once with the right remaining count, absent below the
// threshold, absent on a clean finalize before the threshold, and once per
// turn even when many rounds remain after the first crossing.
func TestCapWarningTable(t *testing.T) {
	cases := []capWarningCase{
		{
			name:          "fires once with the right remaining count",
			toolRounds:    92,
			maxIter:       100,
			wantStop:      "clean-finalize",
			wantWarnings:  1,
			wantRemaining: 10,
		},
		{
			name:          "fires once even when many rounds remain after",
			toolRounds:    98,
			maxIter:       100,
			wantStop:      "clean-finalize",
			wantWarnings:  1,
			wantRemaining: 10,
		},
		{
			name:          "fires on the round that then forces finalize",
			toolRounds:    100,
			maxIter:       100,
			wantStop:      "max-iter",
			wantWarnings:  1,
			wantRemaining: 10,
		},
		{
			name:          "absent when MaxIter is at the threshold",
			toolRounds:    10,
			maxIter:       10,
			wantStop:      "max-iter",
			wantWarnings:  0,
			wantRemaining: 0,
		},
		{
			name:          "absent when MaxIter is below the threshold",
			toolRounds:    8,
			maxIter:       8,
			wantStop:      "max-iter",
			wantWarnings:  0,
			wantRemaining: 0,
		},
		{
			name:          "absent on a clean finalize before the threshold",
			toolRounds:    5,
			maxIter:       100,
			wantStop:      "clean-finalize",
			wantWarnings:  0,
			wantRemaining: 0,
		},
		{
			name:          "absent just past the threshold",
			toolRounds:    89,
			maxIter:       100,
			wantStop:      "clean-finalize",
			wantWarnings:  0,
			wantRemaining: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop, _, msgs := capRun(t, tc.toolRounds, tc.maxIter, "done")
			if stop != tc.wantStop {
				t.Fatalf("stop = %q, want %q", stop, tc.wantStop)
			}
			notes := harnessNotes(msgs)
			warnIdx := capWarningAt(notes)
			warnCount := 0
			if warnIdx >= 0 {
				warnCount = 1
			}
			if warnCount != tc.wantWarnings {
				t.Fatalf("cap warnings = %d, want %d (all harness notes: %v)", warnCount, tc.wantWarnings, notes)
			}
			if tc.wantWarnings > 0 {
				warnText := notes[warnIdx]
				wantPhrase := "you have " + ritoa(tc.wantRemaining) + " tool-call round(s) left"
				if !strings.Contains(warnText, wantPhrase) {
					t.Errorf("warning = %q, want it to name %q", warnText, wantPhrase)
				}
			}
		})
	}
}
