// forced_finalize_test.go — issue #161, step 1: the engine's forced-finalize
// receipt seam. When a bound (max-iter, token-budget, …) drags the run to its
// forced finalize, the turn never took the clean-finalize path, so
// Toolset.FinalizeHook (the testwatch and turn-end-lint receipts, via
// turn.go) never ran. This file locks the mechanical half of the fix at the
// ENGINE level, driven against runLoop with a SenderFunc fake (zero network,
// the loop_test.go idiom — runScripted from finalize_eval_test.go supplies the
// scripted sender and records every appended message):
//
//   - OnForcedFinalize fires exactly once on the forced-finalize exit, with
//     the run's stats as they stand at that moment (stop reason set,
//     FinalizeForced true);
//   - a non-empty note gets one more tools-withheld finalize round whose
//     reply is APPENDED to the forced answer — never replaces it;
//   - an empty note, a nil hook, an empty reply, or a failed note-round send
//     leaves the forced answer untouched, byte for byte;
//   - the seam never fires on a clean finalize (the run's normal exit path);
//   - the tools the caller advertised are restored after the round.
//
// The session-side wiring (turn.go consulting the testwatch / turn-end-lint
// receipts for its note) is the next step; this file pins the contract that
// wiring rides on.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestForcedFinalizeHookSeam locks the OnForcedFinalize seam's engine
// contract, one subtest per contract clause.
func TestForcedFinalizeHookSeam(t *testing.T) {
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		return "OBS for " + call.Function.Arguments
	})
	bounds := Bounds{MaxTokens: 100, MaxIter: 2}

	t.Run("non-empty note gets one appended round", func(t *testing.T) {
		var got loopStats
		var calls int
		var sentTools [][]Tool
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		var msgs []Message
		appendMsg := func(m Message) {
			req.Messages = append(req.Messages, m)
			msgs = append(msgs, m)
		}
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			sentTools = append(sentTools, r.Tools)
			i++
			if i == 3 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			if i == 4 {
				return fakeResp("the leftover accounting", nil, 5, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		})
		ts := Toolset{
			Tools:    []Tool{tools.ReadFile},
			Dispatch: disp,
			OnForcedFinalize: func(stats loopStats) string {
				calls++
				got = stats
				return "harness leftover note"
			},
		}
		content, stats, err := runLoop(context.Background(), send, req, ts, bounds, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if stats.StopReason != "max-iter" || !stats.FinalizeForced {
			t.Fatalf("stop = %q forced=%v, want max-iter/true (the script trips the cap)", stats.StopReason, stats.FinalizeForced)
		}
		// Consulted exactly once, with the run's stats as they stand at the
		// forced-finalize exit: stop reason set, forced flag up.
		if calls != 1 {
			t.Fatalf("OnForcedFinalize calls = %d, want exactly 1", calls)
		}
		if got.StopReason != "max-iter" || !got.FinalizeForced {
			t.Errorf("hook saw stop=%q forced=%v, want max-iter/true", got.StopReason, got.FinalizeForced)
		}
		// The note's reply is APPENDED to the forced answer, never replaces it.
		want := "the forced answer\n\nthe leftover accounting"
		if content != want {
			t.Errorf("content = %q, want the forced answer with the note round's reply appended: %q", content, want)
		}
		// One extra round-trip: 2 tool rounds + 1 forced finalize + 1 note
		// round. The note round is tools-withheld, like the finalize's.
		if i != 4 {
			t.Errorf("round-trips = %d, want 4 (two tool rounds + forced finalize + note round)", i)
		}
		if len(sentTools) != 4 {
			t.Fatalf("sentTools = %d entries, want 4", len(sentTools))
		}
		if len(sentTools[3]) != 0 {
			t.Errorf("note round carried %d tools, want none (tools-withheld round)", len(sentTools[3]))
		}
		// The advertised tools are restored on the caller's long-lived
		// request after the forced-finalize exit.
		if len(req.Tools) != 1 {
			t.Errorf("req.Tools after runLoop = %d, want the advertised set restored", len(req.Tools))
		}
		// The transcript mirrors the wire: the note is a real user message,
		// answered by an assistant message.
		var lastUser, noteReply string
		for _, m := range msgs {
			if m.Role == RoleUser {
				lastUser = m.Content
			}
		}
		if !strings.Contains(lastUser, "harness leftover note") {
			t.Errorf("last user message = %q, want the hook's note on the wire", lastUser)
		}
		if len(msgs) == 0 {
			t.Fatal("no messages appended")
		}
		for j := len(msgs) - 1; j >= 0; j-- {
			if msgs[j].Role == "assistant" {
				noteReply = msgs[j].Content
				break
			}
		}
		if noteReply != "the leftover accounting" {
			t.Errorf("last assistant message = %q, want the note round's reply", noteReply)
		}
	})

	t.Run("empty note leaves the answer untouched", func(t *testing.T) {
		var calls int
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i == 3 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		})
		ts := Toolset{
			Tools:    []Tool{tools.ReadFile},
			Dispatch: disp,
			OnForcedFinalize: func(loopStats) string {
				calls++
				return ""
			},
		}
		content, _, err := runLoop(context.Background(), send, req, ts, bounds, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if calls != 1 {
			t.Errorf("OnForcedFinalize calls = %d, want 1 (consulted once even when the note is empty)", calls)
		}
		if content != "the forced answer" {
			t.Errorf("content = %q, want the forced answer untouched (empty note = no extra round)", content)
		}
		if i != 3 {
			t.Errorf("round-trips = %d, want 3 (no note round when the note is empty)", i)
		}
	})

	t.Run("nil hook leaves the answer untouched", func(t *testing.T) {
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i == 3 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		})
		content, _, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}, bounds, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "the forced answer" {
			t.Errorf("content = %q, want the forced answer untouched (nil hook = today's behavior)", content)
		}
		if i != 3 {
			t.Errorf("round-trips = %d, want 3 (no note round for a nil hook)", i)
		}
	})

	t.Run("empty note-round reply leaves the answer untouched", func(t *testing.T) {
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i == 3 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			if i == 4 {
				return fakeResp("", nil, 5, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		})
		ts := Toolset{
			Tools:            []Tool{tools.ReadFile},
			Dispatch:         disp,
			OnForcedFinalize: func(loopStats) string { return "harness leftover note" },
		}
		content, _, err := runLoop(context.Background(), send, req, ts, bounds, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "the forced answer" {
			t.Errorf("content = %q, want the forced answer untouched (an empty note-round reply appends nothing)", content)
		}
	})

	t.Run("failed note-round send leaves the answer untouched", func(t *testing.T) {
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i == 3 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			if i == 4 {
				return nil, false, errors.New("note round failed")
			}
			return fakeResp("", []ToolCall{readCall("c"+ritoa(i), "f"+ritoa(i)+".go")}, 10, 4), false, nil
		})
		ts := Toolset{
			Tools:            []Tool{tools.ReadFile},
			Dispatch:         disp,
			OnForcedFinalize: func(loopStats) string { return "harness leftover note" },
		}
		content, _, err := runLoop(context.Background(), send, req, ts, bounds, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v (a failed note round must not fail the turn)", err)
		}
		if content != "the forced answer" {
			t.Errorf("content = %q, want the forced answer untouched (a failed note round degrades to silence)", content)
		}
	})

	t.Run("clean finalize never consults the hook", func(t *testing.T) {
		var calls int
		script := []scriptStep{
			{resp: fakeResp("", []ToolCall{readCall("c1", "a.go")}, 10, 4)},
			{resp: fakeResp("clean answer", nil, 5, 5)},
		}
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i-1 < len(script) {
				return script[i-1].resp, false, nil
			}
			return script[len(script)-1].resp, false, nil
		})
		ts := Toolset{
			Tools:    []Tool{tools.ReadFile},
			Dispatch: disp,
			OnForcedFinalize: func(loopStats) string {
				calls++
				return "harness leftover note"
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
		if calls != 0 {
			t.Errorf("OnForcedFinalize calls = %d, want 0 (the seam fires only on the forced-finalize exit)", calls)
		}
		if content != "clean answer" {
			t.Errorf("content = %q, want the clean answer untouched", content)
		}
		if i != 2 {
			t.Errorf("round-trips = %d, want 2 (no note round on a clean finalize)", i)
		}
	})
}

// capRun drives runLoop with a scripted model that issues one DISTINCT
// tool call per round (readCall, so the no-progress and stuck guards never
// fire), then finalizes with answer if the loop is still running when the
// script runs out of tool rounds. It returns the run's stop reason and the
// messages appended. maxIter is the tool-call cap under test.
// capRun drives runLoop with a scripted model that issues one DISTINCT
// tool call per round (readCall, so the no-progress and stuck guards never
// fire), then finalizes with answer if the loop is still running when the
// script runs out of tool rounds. It returns the run's stop reason, the
// content (the turn's final answer), and the messages appended. maxIter is
// the tool-call cap under test.
func capRun(t *testing.T, toolRounds int, maxIter int, answer string) (string, string, []Message) {
	t.Helper()
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	var msgs []Message
	appendMsg := func(m Message) {
		req.Messages = append(req.Messages, m)
		msgs = append(msgs, m)
	}
	var round int
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		if round < toolRounds {
			id := "c" + ritoa(round)
			round++
			return fakeResp("", []ToolCall{readCall(id, "f"+ritoa(round)+".go")}, 10, 4), false, nil
		}
		return fakeResp(answer, nil, 5, 5), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		return "OBS for " + call.Function.Arguments
	})
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: maxIter}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	return stats.StopReason, content, msgs
}

// harnessNotes returns the RoleUser "Harness note: …" messages a run
// appended, with the seed user message excluded (it has no "Harness note"
// prefix, so it is excluded by the check anyway).
func harnessNotes(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == RoleUser && strings.HasPrefix(m.Content, "Harness note") {
			out = append(out, m.Content)
		}
	}
	return out
}

// capWarningAt returns the index of the first cap-approaching warning
// among notes (the "tool-call round(s) left" note), or -1.
func capWarningAt(notes []string) int {
	for i, n := range notes {
		if strings.Contains(n, "tool-call round(s) left before the per-turn tool-call limit") {
			return i
		}
	}
	return -1
}

// TestCapApproachingWarning is the issue #161 step-2 contract: when the
// model asks for tool calls with at most toolCapWarningRounds left, runLoop
// injects ONE "Harness note" naming the remaining count and asking the model
// to wrap up or clean up (remove debug prints, delete scratch files, leave
// tests honest) — so the cap no longer arrives without warning. The no-op
// cases are pinned too: a run that never crosses the threshold, a clean
// finalize before it, and a cap below the threshold.
func TestCapApproachingWarning(t *testing.T) {
	t.Run("fires once with the right remaining count", func(t *testing.T) {
		// 92 tool rounds on a 100-round cap: the warning fires at i=91
		// (remaining = 100-91+1 = 10, the first round where remaining <= 10),
		// then the model finalizes at i=92.
		stop, _, msgs := capRun(t, 92, 100, "wrapped up")
		if stop != "clean-finalize" {
			t.Fatalf("stop = %q, want clean-finalize", stop)
		}
		notes := harnessNotes(msgs)
		if w := capWarningAt(notes); w != 0 || len(notes) != 1 {
			t.Fatalf("harness notes = %d (warning at %d), want exactly one, the cap warning", len(notes), w)
		}
		if !strings.Contains(notes[0], "you have 10 tool-call round(s) left") {
			t.Errorf("warning = %q, want it to name 10 remaining rounds", notes[0])
		}
		for _, phrase := range []string{
			"remove debug prints",
			"delete scratch files",
			"state plainly which ones fail",
		} {
			if !strings.Contains(notes[0], phrase) {
				t.Errorf("warning = %q, want the cleanup ask (missing %q)", notes[0], phrase)
			}
		}
	})

	t.Run("fires only at the threshold, not before", func(t *testing.T) {
		// 89 tool rounds on a 100-round cap: the last tool round is i=88,
		// remaining = 100-88+1 = 13 > 10 — still past the threshold, so no
		// warning fires; the model finalizes without one.
		stop, _, msgs := capRun(t, 89, 100, "done quietly")
		if stop != "clean-finalize" {
			t.Fatalf("stop = %q, want clean-finalize", stop)
		}
		if notes := harnessNotes(msgs); len(notes) != 0 {
			t.Fatalf("harness notes = %v, want none (remaining 11 is past the warning threshold)", notes)
		}
	})

	t.Run("fires once even when many rounds remain after", func(t *testing.T) {
		// 98 tool rounds on a 100-round cap: the warning fires at i=91
		// (remaining 10) and NOT again at i=92…99 (remaining 9…2) —
		// the "10 remaining" count is the one the model gets (once per turn).
		stop, _, msgs := capRun(t, 98, 100, "finished close to the cap")
		if stop != "clean-finalize" {
			t.Fatalf("stop = %q, want clean-finalize", stop)
		}
		notes := harnessNotes(msgs)
		if len(notes) != 1 {
			t.Fatalf("harness notes = %d, want exactly one (once per turn)", len(notes))
		}
		if !strings.Contains(notes[0], "you have 10 tool-call round(s) left") {
			t.Errorf("warning = %q, want the 10-remaining count (the first crossing)", notes[0])
		}
	})

	t.Run("fires on the round that then forces finalize", func(t *testing.T) {
		// 100 tool rounds on a 100-round cap: the warning fires at i=91
		// (remaining 10), the loop exhausts the cap, and the forced finalize
		// runs — the warning did its job, the model was told.
		stop, _, msgs := capRun(t, 100, 100, "forced")
		if stop != "max-iter" {
			t.Fatalf("stop = %q, want max-iter", stop)
		}
		notes := harnessNotes(msgs)
		if len(notes) != 1 || !strings.Contains(notes[0], "you have 10 tool-call round(s) left") {
			t.Fatalf("harness notes = %v, want exactly one cap warning naming 10 remaining", notes)
		}
	})

	t.Run("no warning when MaxIter is below the threshold", func(t *testing.T) {
		// 8 tool rounds on an 8-round cap: every round is "within 10" of the
		// cap, so the warning is suppressed (it would be every round) and
		// the cap still forces finalize.
		stop, _, msgs := capRun(t, 8, 8, "forced")
		if stop != "max-iter" {
			t.Fatalf("stop = %q, want max-iter", stop)
		}
		if notes := harnessNotes(msgs); len(notes) != 0 {
			t.Fatalf("harness notes = %v, want none (MaxIter 8 is at or below the threshold 10)", notes)
		}
	})

	t.Run("a forced exit before the threshold stays quiet", func(t *testing.T) {
		// 5 tool rounds, then the model finalizes, on a 100-round cap: the
		// run ends clean long before the warning threshold — no note.
		stop, _, msgs := capRun(t, 5, 100, "early finish")
		if stop != "clean-finalize" {
			t.Fatalf("stop = %q, want clean-finalize", stop)
		}
		if notes := harnessNotes(msgs); len(notes) != 0 {
			t.Fatalf("harness notes = %v, want none (the run ended well before the cap)", notes)
		}
	})
}

// TestForcedFinalizeOtherExits covers the remaining seam contract clause:
// the other bound-forced exits route through the same seam — token-budget
// trips on spend, not round count, and its forced answer gets the note
// round too.
func TestForcedFinalizeOtherExits(t *testing.T) {
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		return "OBS for " + call.Function.Arguments
	})

	t.Run("token-budget exit fires the seam", func(t *testing.T) {
		var calls int
		var i int
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			i++
			if i == 2 {
				return fakeResp("the forced answer", nil, 5, 5), false, nil
			}
			if i == 3 {
				return fakeResp("the leftover accounting", nil, 5, 5), false, nil
			}
			// 8 in + 4 out on round 1 crosses a 10-token budget.
			return fakeResp("", []ToolCall{readCall("c1", "a.go")}, 8, 4), false, nil
		})
		ts := Toolset{
			Tools:    []Tool{tools.ReadFile},
			Dispatch: disp,
			OnForcedFinalize: func(stats loopStats) string {
				calls++
				if stats.StopReason != "token-budget" {
					t.Errorf("hook saw stop=%q, want token-budget", stats.StopReason)
				}
				return "harness leftover note"
			},
		}
		content, stats, err := runLoop(context.Background(), send, req, ts,
			Bounds{MaxTokens: 100, MaxIter: 100, TokenBudget: 10}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if stats.StopReason != "token-budget" {
			t.Fatalf("stop = %q, want token-budget", stats.StopReason)
		}
		if calls != 1 {
			t.Errorf("OnForcedFinalize calls = %d, want 1", calls)
		}
		want := "the forced answer\n\nthe leftover accounting"
		if content != want {
			t.Errorf("content = %q, want the note round's reply appended: %q", content, want)
		}
	})
}
