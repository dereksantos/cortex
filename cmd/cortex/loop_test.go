package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

var errFake = errors.New("fake send failure")

// fakeResp builds a minimal *AgentResponse with one assistant choice.
func fakeResp(content string, calls []ToolCall, in, out int) *AgentResponse {
	return &AgentResponse{
		Choices: []Choice{{Message: Message{Role: "assistant", Content: content, ToolCalls: calls}}},
		Usage:   Usage{PromptTokens: in, CompletionTokens: out},
	}
}

// TestAccountUsageReasoningTokens covers item 3's plumbing: accountUsage
// sums each response's reasoning-token usage into loopStats, zero when a
// response doesn't report it (Usage.ReasoningTokens() defaults to 0 when
// CompletionTokensDetails is nil).
func TestAccountUsageReasoningTokens(t *testing.T) {
	s := &loopStats{}
	withReasoning := &AgentResponse{Usage: Usage{
		PromptTokens: 10, CompletionTokens: 50,
		CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 30},
	}}
	withoutReasoning := &AgentResponse{Usage: Usage{PromptTokens: 5, CompletionTokens: 5}}

	accountUsage(s, withReasoning, 0)
	if s.ReasoningTokens != 30 {
		t.Fatalf("ReasoningTokens after first response = %d, want 30", s.ReasoningTokens)
	}
	accountUsage(s, withoutReasoning, 0)
	if s.ReasoningTokens != 30 {
		t.Errorf("ReasoningTokens after second (unreported) response = %d, want 30 (unchanged)", s.ReasoningTokens)
	}
}

func readCall(id, path string) ToolCall {
	args, _ := json.Marshal(map[string]any{"path": path})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionReadFile, Arguments: string(args)}}
}

// rolesOf renders a message slice's role sequence for failure diagnostics.
func rolesOf(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(m.Role)
	}
	return "[" + b.String() + "]"
}

// TestCoderLoopCharacterization locks the coder loop's behavior — the message
// sequence, dispatch order, token accounting, and clean-finalize stop — against
// a SenderFunc fake, with zero network. It is written BEFORE the Resolve→Turn
// fold and must stay green through it: runLoop is authored to reproduce exactly
// what today's Resolve does on this scenario (two tool rounds, then an answer).
func TestCoderLoopCharacterization(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{
		{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "go"},
	}}
	var recorded []Message
	appendMsg := func(m Message) {
		req.Messages = append(req.Messages, m)
		recorded = append(recorded, m)
	}

	// The fake model: round 0 asks for one read; round 1 answers.
	var round int
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		defer func() { round++ }()
		switch round {
		case 0:
			return fakeResp("", []ToolCall{readCall("c1", "go.mod")}, 10, 4), false, nil
		default:
			return fakeResp("final answer", nil, 12, 6), false, nil
		}
	})

	var dispatched []string
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		dispatched = append(dispatched, call.Function.Name)
		return "OBS:" + call.Function.Name
	})

	ts := Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}
	content, stats, err := runLoop(context.Background(), send, req,
		ts, Bounds{MaxTokens: 1000, MaxIter: 100}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	if content != "final answer" {
		t.Errorf("content = %q, want %q", content, "final answer")
	}
	if stats.StopReason != "clean-finalize" {
		t.Errorf("stop reason = %q, want clean-finalize", stats.StopReason)
	}
	if stats.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", stats.Iterations)
	}
	// Tokens summed across both round-trips; LastPromptTokens is the most recent.
	if stats.InputTokens != 22 || stats.OutputTokens != 10 {
		t.Errorf("tokens = %d in / %d out, want 22/10", stats.InputTokens, stats.OutputTokens)
	}
	if stats.LastPromptTokens != 12 {
		t.Errorf("last prompt tokens = %d, want 12", stats.LastPromptTokens)
	}
	if stats.Reads != 1 {
		t.Errorf("reads = %d, want 1", stats.Reads)
	}
	// One tool was dispatched, in order.
	if len(dispatched) != 1 || dispatched[0] != tools.FunctionReadFile {
		t.Errorf("dispatched = %v, want [read_file]", dispatched)
	}
	// Message sequence appended by the loop: assistant(tool_calls) → tool(result)
	// → assistant(answer). The API ordering invariant.
	wantRoles := []string{"assistant", RoleTool, "assistant"}
	if len(recorded) != len(wantRoles) {
		t.Fatalf("recorded %d messages, want %d: %+v", len(recorded), len(wantRoles), recorded)
	}
	for i, r := range wantRoles {
		if recorded[i].Role != r {
			t.Errorf("message %d role = %q, want %q", i, recorded[i].Role, r)
		}
	}
	if recorded[1].Content != "OBS:read_file" || recorded[1].ToolCallID != "c1" {
		t.Errorf("tool result = %+v, want OBS:read_file/c1", recorded[1])
	}
}

// TestCoderLoopNoProgressFinalizes locks the no-progress guard + nudge + forced
// finalize: a model that re-issues the byte-identical batch is nudged one short
// of the cap, broken at the cap, and finalized (tools withheld) — never run to
// the iteration ceiling.
func TestCoderLoopNoProgressFinalizes(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var sends int
	var sawNoTools bool
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		if r.Tools == nil { // finalize round — tools withheld
			sawNoTools = true
			return fakeResp("forced answer", nil, 1, 1), false, nil
		}
		return fakeResp("", []ToolCall{readCall("c", "x")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "same" })

	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "no-progress" || !stats.FinalizeForced {
		t.Errorf("stop = %q forced=%v, want no-progress/true", stats.StopReason, stats.FinalizeForced)
	}
	if !sawNoTools || content != "forced answer" {
		t.Errorf("finalize did not fire: content=%q sawNoTools=%v", content, sawNoTools)
	}
	// maxRepeatedToolCalls loop sends + 1 finalize, far below MaxIter.
	if sends != maxRepeatedToolCalls+1 {
		t.Errorf("sends = %d, want %d (cap + finalize)", sends, maxRepeatedToolCalls+1)
	}
	// The nudge was injected one repeat short of the cap.
	var nudges int
	for _, m := range req.Messages {
		if m.Role == RoleUser && m.Content == noProgressNudge {
			nudges++
		}
	}
	if nudges != 1 {
		t.Errorf("nudges = %d, want 1", nudges)
	}
}

// TestRunLoopNoProgress is the issue #132 no-progress guard, table-driven:
// the guard compares BATCHES — this round's batch (its signature and the
// dispatch observations joined) against the PREVIOUS round's batch — so the
// observation it reads is always the same call's, never a sibling call's or
// another round's. Each case script-calls one batch per round and records what
// the guard did in each round (nudge and/or finalize) plus where the turn
// ended. A finalize lands on the round after the LAST scripted batch (the
// forced finalize's send has tools withheld).
func TestRunLoopNoProgress(t *testing.T) {
	type step struct {
		obs   string // the round's dispatch observation
		nudge bool   // the nudge was injected after this round
		warn  bool   // the cap-approaching warning was injected after this round
		stop  string // the loop broke at this round with this stop reason
	}
	cases := []struct {
		name       string
		steps      []step
		maxIter    int
		wantStop   string
		wantFinal  bool // a forced finalize ran (tools withheld)
		wantToolRd int  // rounds that dispatched a tool batch
	}{
		{
			// Identical batch, unchanged observation: a plain re-read. No
			// progress from the second batch on (first repeat → streak 2 →
			// nudge; second repeat → streak 3 → finalize).
			name: "identical call, unchanged observation",
			steps: []step{
				{obs: "same"},
				{obs: "same", nudge: true},
				{obs: "same", stop: "no-progress"},
			},
			maxIter:    100,
			wantStop:   "no-progress",
			wantFinal:  true,
			wantToolRd: 3,
		},
		{
			// Identical batch whose observation changes EVERY round: each
			// re-read yields new information (progress), so the streak
			// resets every round and the guard never finalizes — the turn
			// runs to the iteration cap.
			name: "identical call, observation changes every round",
			steps: []step{
				{obs: "a"}, {obs: "b"}, {obs: "c"}, {obs: "d"}, {obs: "e"},
			},
			maxIter:    5,
			wantStop:   "max-iter",
			wantFinal:  true,
			wantToolRd: 5,
		},
		{
			// Identical batch, unchanged observation, with the cap-approaching
			// warning (issue #161) appended after the tool results mid-streak:
			// MaxIter is just above toolCapWarningRounds, so the warning fires
			// on the LAST round, after dispatch. The guard must be immune to
			// the extra user message it appends: an identical call whose
			// observation is unchanged is no progress across the warning
			// round — nudge at the first repeat, finalize at the second — and
			// the round's observation stays its own dispatch result, not ""
			// (the pre-fix transcript tail-slice took the warning in place of
			// the tool result and reset the streak).
			name: "identical call, unchanged observation, across the cap warning",
			steps: []step{
				{obs: "same"},
				{obs: "same", nudge: true},
				{obs: "same", warn: true, stop: "no-progress"},
			},
			maxIter:    toolCapWarningRounds + 2,
			wantStop:   "no-progress",
			wantFinal:  true,
			wantToolRd: 3,
		},
		{
			// Identical batch returning an error: no progress from the FIRST
			// repeat (the nudge fires on the first repeat; the one after
			// finalizes). On main this run got three repeats and a finalize
			// with no nudge. Each round's error keeps a distinct class so the
			// stuck detector (the same error class recurring) never fires
			// first: the no-progress guard is under test alone.
			name: "identical call returning an error",
			steps: []step{
				{obs: "Error: no such file alpha"},
				{obs: "Error: no such file beta", nudge: true},
				{obs: "Error: no such file gamma", stop: "no-progress"},
			},
			maxIter:    100,
			wantStop:   "no-progress",
			wantFinal:  true,
			wantToolRd: 3,
		},
		{
			// A different call's error (round 1) must not poison the guard for
			// the next call: the guard only reads the PREVIOUS batch's own
			// observation. Round 2 (first sighting of the new call) starts a
			// streak; round 3 (its first repeat, unchanged obs) is no progress
			// (streak 2 → nudge); round 4 (second repeat) reaches the cap and
			// finalizes.
			name: "different call errored earlier, then a repeat",
			steps: []step{
				{obs: "Error: b failed"},
				{obs: "ok"},
				{obs: "ok", nudge: true},
				{obs: "ok", stop: "no-progress"},
			},
			maxIter:    100,
			wantStop:   "no-progress",
			wantFinal:  true,
			wantToolRd: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
			appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
			var round int
			var sawNoTools bool
			send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
				if r.Tools == nil { // the forced finalize
					sawNoTools = true
					return fakeResp("forced answer", nil, 1, 1), false, nil
				}
				// Distinct IDs, same path: the signature is name+args, so the
				// same path is the identical batch the guard is about. (The
				// tool-call ID stays unique to this response: reused IDs would
				// masquerade as the previous round's batch on the wire.)
				call := readCall(fmt.Sprintf("c%d", round), "f")
				if tc.name == "different call errored earlier, then a repeat" && round == 0 {
					call = readCall("c0", "b") // round 1 is a DIFFERENT call
				}
				round++
				return fakeResp("", []ToolCall{call}, 1, 1), false, nil
			})
			disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return tc.steps[round-1].obs })

			_, stats, err := runLoop(context.Background(), send, req,
				Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
				Bounds{MaxTokens: 100, MaxIter: tc.maxIter}, nil, appendMsg, nil)
			if err != nil {
				t.Fatalf("runLoop: %v", err)
			}
			if stats.StopReason != tc.wantStop {
				t.Fatalf("stop = %q, want %q", stats.StopReason, tc.wantStop)
			}
			if (stats.FinalizeForced && sawNoTools) != tc.wantFinal {
				t.Errorf("forced finalize ran = %v (tools withheld seen = %v), want %v",
					stats.FinalizeForced, sawNoTools, tc.wantFinal)
			}
			if round != tc.wantToolRd {
				t.Errorf("tool rounds dispatched = %d, want %d", round, tc.wantToolRd)
			}
			// Per-round expectations: a nudge after each round flagged in the
			// script, and the stop lands on the round flagged (that batch was
			// dispatched; the next send is the forced finalize, tools withheld).
			var nudges, wantNudges int
			for _, m := range req.Messages {
				if m.Role == RoleUser && m.Content == noProgressNudge {
					nudges++
				}
			}
			var capWarnings int
			for _, m := range req.Messages {
				if m.Role == RoleUser && strings.HasPrefix(m.Content, "Harness note: you have ") {
					capWarnings++
				}
			}
			for i, s := range tc.steps {
				if s.nudge {
					wantNudges++
				}
				if s.warn {
					if i != tc.wantToolRd-1 {
						t.Errorf("cap warning landed on tool round %d, want round %d", i+1, tc.wantToolRd)
					}
					if capWarnings != 1 {
						t.Errorf("cap warnings = %d, want 1", capWarnings)
					}
				}
				if s.stop != "" && i != tc.wantToolRd-1 {
					t.Errorf("stop %q landed on tool round %d, want round %d", s.stop, i+1, tc.wantToolRd)
				}
			}
			if nudges != wantNudges {
				t.Errorf("nudges = %d, want %d", nudges, wantNudges)
			}
		})
	}
}

// TestRunLoopBytesBudgetFinalizes locks the ReadBudgetBytes ceiling: accumulated
// tool output past the budget forces finalize (the subagent path's guard).
func TestRunLoopBytesBudgetFinalizes(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			return fakeResp("done", nil, 1, 1), false, nil
		}
		i++
		// Each round a DIFFERENT call so the no-progress guard never fires first.
		return fakeResp("", []ToolCall{readCall("c", strings.Repeat("a", i))}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return strings.Repeat("x", 5000) })

	_, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 100, ReadBudgetBytes: 8000}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "read-budget" {
		t.Errorf("stop = %q, want read-budget", stats.StopReason)
	}
	if stats.ReadBytes < 8000 {
		t.Errorf("read bytes = %d, want >= 8000", stats.ReadBytes)
	}
}

// TestRunLoopMaxIterFinalizes locks the MaxIter ceiling: a model that keeps
// issuing DIFFERENT calls (so no-progress never fires) is stopped at the
// iteration cap and finalized — never run unbounded.
func TestRunLoopMaxIterFinalizes(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			return fakeResp("forced", nil, 1, 1), false, nil
		}
		i++
		return fakeResp("", []ToolCall{readCall("c", strings.Repeat("a", i))}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 4}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "max-iter" || !stats.FinalizeForced {
		t.Errorf("stop = %q forced=%v, want max-iter/true", stats.StopReason, stats.FinalizeForced)
	}
	if stats.Iterations != 4 {
		t.Errorf("iterations = %d, want 4 (the cap)", stats.Iterations)
	}
	if content != "forced" {
		t.Errorf("content = %q, want forced", content)
	}
}

// TestRunLoopMidLoopErrorFinalizes locks the resilience fix: a model-call failure
// AFTER progress has been made finalizes from the gathered context (tools
// withheld) rather than losing the whole run — the dodge for a proxy that rejects
// a tool-call round's grammar mid-study. A first-send failure still aborts.
func TestRunLoopMidLoopErrorFinalizes(t *testing.T) {
	t.Run("mid-loop error finalizes", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var round int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil { // the finalize round (tools withheld) succeeds
				return fakeResp("recovered answer", nil, 1, 1), false, nil
			}
			defer func() { round++ }()
			if round == 0 {
				return fakeResp("", []ToolCall{readCall("c", "x")}, 1, 1), false, nil
			}
			return nil, false, errFake // the second tool-call round fails
		})
		disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("mid-loop error should finalize, not abort: %v", err)
		}
		if content != "recovered answer" || stats.StopReason != "error-recovered" {
			t.Errorf("content=%q stop=%q, want recovered/error-recovered", content, stats.StopReason)
		}
		// Issue #117: the recovered run carries the failing send's error, so
		// the caller can log and print it instead of it vanishing behind the
		// finalize answer.
		if stats.LastError == nil {
			t.Fatal("LastError = nil on an error-recovered run, want the failing send's error")
		}
		if stats.LastError != errFake {
			t.Errorf("LastError = %v, want the failing send's error (%v)", stats.LastError, errFake)
		}
	})
	t.Run("first-send error aborts", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
			return nil, false, errFake
		})
		_, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: DispatchFunc(func(context.Context, ToolCall) string { return "" })},
			Bounds{MaxTokens: 100, MaxIter: 100}, nil, func(m Message) { req.Messages = append(req.Messages, m) }, nil)
		if err == nil || stats.StopReason != "error" {
			t.Errorf("a first-send failure must abort: err=%v stop=%q", err, stats.StopReason)
		}
		// Issue #117: an UNRECOVERED failure returns the error itself and
		// carries nothing on LastError — the recovered path is the only
		// writer of that field.
		if stats.LastError != nil {
			t.Errorf("LastError = %v on an unrecovered abort, want nil (the error is returned, not carried)", stats.LastError)
		}
	})
	t.Run("recovered run's finalize prompt carries the error", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		var recorded []Message
		appendMsg := func(m Message) {
			req.Messages = append(req.Messages, m)
			recorded = append(recorded, m)
		}
		var round int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil { // the finalize round (tools withheld) succeeds
				return fakeResp("recovered answer", nil, 1, 1), false, nil
			}
			defer func() { round++ }()
			if round == 0 {
				return fakeResp("", []ToolCall{readCall("c", "x")}, 1, 1), false, nil
			}
			return nil, false, errFake // the second tool-call round fails
		})
		disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
		_, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("mid-loop error should finalize, not abort: %v", err)
		}
		if stats.StopReason != "error-recovered" || stats.LastError == nil {
			t.Fatalf("stop=%q lasterr=%v, want error-recovered with the error carried", stats.StopReason, stats.LastError)
		}
		// The finalize prompt names the CAUSE of the stop ("a backend error
		// that interrupted the run") — what today's finalizePromptFor does —
		// while the engine carries the provider error ITSELF on stats.LastError
		// (above) so the caller can log and print the status and body. That
		// separation is the fix: the model's framing stays cause-neutral, the
		// diagnoseable detail rides the stats, not the prompt.
		last := lastUserMessage(t, recorded)
		if !strings.Contains(last, "a backend error that interrupted the run") {
			t.Errorf("finalize prompt %q does not name the cause", last)
		}
		if strings.Contains(last, errFake.Error()) {
			t.Errorf("finalize prompt %q carries the raw provider error; it must stay cause-neutral (the detail rides stats.LastError)", last)
		}
	})
}

// TestRunLoopSalvagesEmptyClampedFinalize locks the spiral-salvage fix: when the
// forced finalize returns EMPTY because the model burned its whole completion
// budget thinking (the max-tokens clamp), the engine re-asks ONCE with a hard
// brevity floor and returns the salvaged answer — the dodge for a reasoning model
// (north) that over-deliberates a finalize and emits no prose. A non-empty
// clamped answer is also salvaged into a concise rewrite; a non-clamped answer
// must NOT trigger the retry (the gate stays off for healthy runs).
func TestRunLoopSalvagesEmptyClampedFinalize(t *testing.T) {
	t.Run("empty clamped finalize is salvaged", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		// Effort pinned off so the issue #149 natural-finish off-retry is
		// skipped and this subtest exercises exactly what it was named for:
		// the forced finalize empty at the clamp, recovered by ONE salvage
		// re-ask (finalizes == 2).
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var finalizes int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil { // a finalize round (tools withheld)
				finalizes++
				if finalizes == 1 {
					return fakeResp("", nil, 1, 100), false, nil // clamp: out == MaxTokens, empty
				}
				return fakeResp("salvaged answer", nil, 1, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "salvaged answer" {
			t.Errorf("content = %q, want salvaged answer", content)
		}
		if stats.StopReason != "salvaged-finalize" {
			t.Errorf("stop = %q, want salvaged-finalize", stats.StopReason)
		}
		if finalizes != 2 {
			t.Errorf("finalize calls = %d, want 2 (one empty + one salvage)", finalizes)
		}
		if stats.ReasoningFallback {
			t.Errorf("ReasoningFallback = true, want false (effort off: no off-retry fired)")
		}
		if stats.SalvagedUnclamped {
			t.Errorf("SalvagedUnclamped = true, want false (this recovery was clamped)")
		}
	})
	t.Run("natural empty clamped finish is salvaged", func(t *testing.T) {
		// The live failure mode: the model returns NO tool calls with EMPTY content
		// because it spiraled to the token clamp on a normal turn (out == MaxTokens).
		// The natural-finish path must salvage it, not return "".
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil { // the salvage re-ask
				return fakeResp("salvaged answer", nil, 1, 5), false, nil
			}
			return fakeResp("", nil, 1, 100), false, nil // empty + clamped (out == MaxTokens), no tool calls
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "salvaged answer" || stats.StopReason != "salvaged-finalize" {
			t.Errorf("content=%q stop=%q, want salvaged answer/salvaged-finalize", content, stats.StopReason)
		}
	})
	t.Run("natural empty clamped finish is recovered by the reasoning-off retry", func(t *testing.T) {
		// The issue #149 path: the role runs with reasoning ON (the code role's
		// default), so an empty natural finish triggers the one-shot
		// reasoning-off retry before any prompt-based salvage. Here the retry
		// answers, clamped.
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Effort.Level == llm.EffortOff {
				return fakeResp("salvaged answer", nil, 1, 100), false, nil // the off-retry answers, clamped (out == MaxTokens)
			}
			return fakeResp("", nil, 1, 100), false, nil // empty + clamped (out == MaxTokens), no tool calls → the off-retry fires
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "salvaged answer" || stats.StopReason != "salvaged-finalize" {
			t.Errorf("content=%q stop=%q, want salvaged answer/salvaged-finalize", content, stats.StopReason)
		}
		if !stats.ReasoningFallback {
			t.Errorf("ReasoningFallback = false, want true (recovered by the off-retry)")
		}
		if stats.SalvagedUnclamped {
			t.Errorf("SalvagedUnclamped = true, want false (the off-retry's answer was clamped)")
		}
	})
	t.Run("empty clamped finish can salvage from explicit tool candidate", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		// The role runs with reasoning OFF so step 1's natural-finish off-retry
		// is skipped: the empty natural finish falls straight through to the
		// prompt-based salvage chain (salvageEmptyFinalize fails empty at
		// clamp, then salvageObservationFinalize recovers from the tool
		// candidate). Without this gate the off-retry would fire first and
		// answer "salvaged answer" before the observation candidate is ever
		// consulted.
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil { // both finalize and salvage re-ask fail empty at clamp
				return fakeResp("", nil, 1, 100), false, nil
			}
			if len(r.Messages) == 1 {
				return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
			}
			return fakeResp("", nil, 1, 100), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string {
			return "Root-file candidate hits:\nx:1:AGENTS.md\nsummary: AGENTS.md=3\nmost_frequent_candidate: AGENTS.md"
		})
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if !strings.Contains(content, "AGENTS.md") || stats.StopReason != "salvaged-finalize" || !stats.Salvaged {
			t.Errorf("content=%q stop=%q salvaged=%v, want AGENTS.md/salvaged-finalize/true", content, stats.StopReason, stats.Salvaged)
		}
	})
	t.Run("non-empty clamped finalize is rewritten", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var finalizes int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil {
				finalizes++
				if finalizes == 1 {
					return fakeResp("runaway but factual answer", nil, 1, 100), false, nil // clamp + non-empty
				}
				return fakeResp("concise answer", nil, 1, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "concise answer" {
			t.Errorf("content = %q, want concise answer", content)
		}
		if stats.StopReason != "salvaged-finalize" || !stats.Salvaged {
			t.Errorf("stop=%q salvaged=%v, want salvaged-finalize/true", stats.StopReason, stats.Salvaged)
		}
		if finalizes != 2 {
			t.Errorf("finalize calls = %d, want 2 (one clamped + one rewrite)", finalizes)
		}
	})
	t.Run("natural non-empty clamped finish is rewritten", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var sends int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			sends++
			if r.Tools == nil {
				return fakeResp("concise answer", nil, 1, 5), false, nil
			}
			return fakeResp("runaway but factual answer", nil, 1, 100), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "concise answer" || stats.StopReason != "salvaged-finalize" || !stats.Salvaged {
			t.Errorf("content=%q stop=%q salvaged=%v, want concise answer/salvaged-finalize/true", content, stats.StopReason, stats.Salvaged)
		}
		if sends != 2 {
			t.Errorf("sends = %d, want 2 (one clamped + one rewrite)", sends)
		}
	})
	t.Run("non-clamped finalize does not retry", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var finalizes int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil {
				finalizes++
				return fakeResp("clean answer", nil, 1, 99), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, _, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "clean answer" || finalizes != 1 {
			t.Errorf("content=%q finalizes=%d, want clean answer/1", content, finalizes)
		}
	})
}

// TestRunLoopSalvagesNaturalEmptyUnclampedFinish: an empty final message with
// no tool calls and no clamp must still trigger the salvage.
func TestRunLoopSalvagesNaturalEmptyUnclampedFinish(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // the salvage re-ask
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		}
		return fakeResp("", nil, 1, 5), false, nil // empty, NOT clamped (out << MaxTokens), no tool calls
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.MaxTokensClamped {
		t.Fatalf("test setup bug: response should not read as clamped")
	}
	if content != "salvaged answer" || stats.StopReason != "salvaged-finalize" {
		t.Errorf("content=%q stop=%q, want salvaged answer/salvaged-finalize", content, stats.StopReason)
	}
	if !stats.SalvagedUnclamped {
		t.Errorf("SalvagedUnclamped = false, want true (recovery was unclamped)")
	}
}

// TestFinalizeLoopSalvagesNaturalEmptyUnclampedFinish is the same case via
// the forced-finalize path (max-iter/stuck/read-budget) instead of a natural
// mid-loop finish — both call sites share the fix.
func TestFinalizeLoopSalvagesNaturalEmptyUnclampedFinish(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var finalizes int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // forced finalize, tools withheld
			finalizes++
			if finalizes == 1 {
				return fakeResp("", nil, 1, 5), false, nil // empty, NOT clamped (out << MaxTokens)
			}
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		}
		return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if !stats.FinalizeForced {
		t.Errorf("FinalizeForced = false, want true (max-iter should force finalize)")
	}
	if stats.MaxTokensClamped {
		t.Fatalf("test setup bug: response should not read as clamped")
	}
	if content != "salvaged answer" || stats.StopReason != "salvaged-finalize" {
		t.Errorf("content=%q stop=%q, want salvaged answer/salvaged-finalize", content, stats.StopReason)
	}
	if !stats.SalvagedUnclamped {
		t.Errorf("SalvagedUnclamped = false, want true")
	}
	if finalizes != 2 {
		t.Errorf("finalize calls = %d, want 2 (one empty + one salvage)", finalizes)
	}
}

// salvageReasoningRetryScripted is the base Sender for the reasoning-fallback
// tests (issue #149): the first send returns an EMPTY answer with no tool
// calls (the live failure mode, unclamped), the second — the reasoning-off
// retry — answers "recovered answer" and records the wire fields it was sent
// with, and the third (reached only when the retry is skipped or fails) is the
// prompt-based salvage re-ask (tools withheld) answering "salvaged answer".
func salvageReasoningRetryScripted(retrySawOff, retryCarriedTools *bool, sendCount *int) Sender {
	return SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		*sendCount++
		switch *sendCount {
		case 1:
			return fakeResp("", nil, 1, 5), false, nil // empty, no tool calls, no clamp
		case 2:
			// In the recovery subtests this is the reasoning-off retry (tools
			// carried); in the off-role subtest the retry is skipped, so this
			// send is the prompt-based salvage re-ask (tools withheld). Report
			// the wire fields only when tools are actually carried.
			if r.Tools != nil {
				*retrySawOff = r.Effort.Level == llm.EffortOff && r.ChatTemplateKwargs["enable_thinking"] == false
				*retryCarriedTools = true
			}
			if r.Tools != nil {
				return fakeResp("recovered answer", nil, 1, 8), false, nil
			}
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		default:
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		}
	})
}

// TestRunLoopSalvagesEmptyFinishWithReasoningOffRetry covers issue #149:
// an empty finish (no content, no tool calls) from a role whose reasoning is
// ON is recovered by re-sending the SAME request once with reasoning pinned
// off, before the prompt-based salvage chain is consulted; the retry's result
// is used as the answer. Table-driven over the role's effort: any non-off
// level triggers exactly one retry, and an explicitly off role never does —
// it falls through to the existing salvage chain untouched.
func TestRunLoopSalvagesEmptyFinishWithReasoningOffRetry(t *testing.T) {
	tests := []struct {
		name         string
		effort       llm.EffortLevel
		wantRecovery bool
	}{
		{name: "reasoning on", effort: llm.EffortOn, wantRecovery: true},
		{name: "reasoning high", effort: llm.EffortHigh, wantRecovery: true},
		{name: "unset (model default)", effort: llm.EffortUnset, wantRecovery: true},
		{name: "reasoning off", effort: llm.EffortOff, wantRecovery: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
			applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: tt.effort})
			appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

			sends := 0
			var retrySawOff, retryCarriedTools bool
			send := salvageReasoningRetryScripted(&retrySawOff, &retryCarriedTools, &sends)
			disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
			content, stats, err := runLoop(context.Background(), send, req,
				Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
				Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
			if err != nil {
				t.Fatalf("runLoop: %v", err)
			}
			// The one-shot mutation must restore the role's configured effort.
			if req.Effort.Level != tt.effort {
				t.Errorf("req.Effort after runLoop = %+v, want restored to %q", req.Effort, tt.effort)
			}
			if !tt.wantRecovery {
				if stats.ReasoningFallback {
					t.Errorf("ReasoningFallback = true, want false (reasoning-off role never retries)")
				}
				// Empty finish, no retry: the existing prompt-based salvage
				// chain runs (empty → salvage re-ask, which answers).
				if content != "salvaged answer" {
					t.Errorf("content = %q, want salvaged answer (existing salvage chain, no retry)", content)
				}
				if sends != 2 {
					t.Errorf("sends = %d, want 2 (empty finish + salvage re-ask)", sends)
				}
				return
			}
			if content != "recovered answer" {
				t.Errorf("content = %q, want recovered answer (the retry's result is used)", content)
			}
			if sends != 2 {
				t.Errorf("sends = %d, want 2 (exactly one reasoning-off retry)", sends)
			}
			if !retryCarriedTools {
				t.Error("the retry send did not carry the role's tools (same request re-sent, not a finalize)")
			}
			if !retrySawOff {
				t.Error("the retry send did not go out with reasoning pinned off")
			}
			if stats.StopReason != "salvaged-finalize" || !stats.Salvaged || !stats.ReasoningFallback {
				t.Errorf("stop=%q salvaged=%v reasoningFallback=%v, want salvaged-finalize/true/true",
					stats.StopReason, stats.Salvaged, stats.ReasoningFallback)
			}
			if stats.ReasoningFallbackOutcome != journal.OutcomeAnswer {
				t.Errorf("ReasoningFallbackOutcome = %q, want %q (the retry answered with prose)",
					stats.ReasoningFallbackOutcome, journal.OutcomeAnswer)
			}
			if !stats.SalvagedUnclamped {
				t.Errorf("SalvagedUnclamped = false, want true (the empty finish was not clamped)")
			}
		})
	}
}

// TestRunLoopReasoningOffRetryFallsThroughWhenRetryIsEmpty: if the
// reasoning-off retry ALSO comes back empty (or errors), the existing salvage
// chain (salvageEmptyFinalize) still runs — the retry is one attempt, not a
// retry storm, and it never masks a later successful salvage.
func TestRunLoopReasoningOffRetryFallsThroughWhenRetryIsEmpty(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	sends := 0
	var retrySawOff bool
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		switch sends {
		case 1:
			return fakeResp("", nil, 1, 5), false, nil // empty finish, reasoning on
		case 2:
			retrySawOff = r.Effort.Level == llm.EffortOff
			return fakeResp("", nil, 1, 5), false, nil // retry also empty
		case 3:
			// The salvage re-ask (tools withheld) finally answers.
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		default:
			t.Fatalf("send %d: unexpected extra send", sends)
			return nil, false, nil
		}
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if sends != 3 {
		t.Errorf("sends = %d, want 3 (empty + one failed retry + salvage re-ask)", sends)
	}
	if !retrySawOff {
		t.Error("the retry send did not go out with reasoning pinned off")
	}
	if content != "salvaged answer" {
		t.Errorf("content = %q, want salvaged answer (fall-through to existing salvage)", content)
	}
	if stats.ReasoningFallback {
		t.Errorf("ReasoningFallback = true, want false (the retry recovered nothing)")
	}
	if stats.StopReason != "salvaged-finalize" || !stats.Salvaged {
		t.Errorf("stop=%q salvaged=%v, want salvaged-finalize/true", stats.StopReason, stats.Salvaged)
	}
	if req.Effort.Level != llm.EffortOn {
		t.Errorf("req.Effort after runLoop = %+v, want restored to on", req.Effort)
	}
}

// TestRunLoopReasoningOffRetryToolCallsDispatch: the issue #149 off-retry
// re-sends the SAME request — tools still advertised. When the retry returns
// tool calls (the model had work to do once the deliberation channel closed),
// the loop must treat it like a normal tool round: dispatch the calls and
// append their results, then let the next round answer — not append a bare
// assistant(tool_calls) message and ignore it.
func TestRunLoopReasoningOffRetryToolCallsDispatch(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	sends := 0
	var retrySawOff, retryCarriedTools bool
	var dispatched []string
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		switch sends {
		case 1:
			return fakeResp("", nil, 1, 5), false, nil // empty finish, reasoning on
		case 2:
			// The off-retry: tools must be carried (the same request), and it
			// answers with a tool call — the work the deliberation hid.
			retrySawOff = r.Effort.Level == llm.EffortOff && r.ChatTemplateKwargs["enable_thinking"] == false
			retryCarriedTools = r.Tools != nil
			return fakeResp("", []ToolCall{readCall("c1", "go.mod")}, 1, 8), false, nil
		case 3:
			return fakeResp("recovered answer", nil, 1, 8), false, nil // normal round after the tool result
		default:
			t.Fatalf("send %d: unexpected extra send", sends)
			return nil, false, nil
		}
	})
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		dispatched = append(dispatched, call.Function.Name)
		return "OBS:" + call.Function.Name
	})
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 5}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if sends != 3 {
		t.Fatalf("sends = %d, want 3 (empty finish, off-retry with tool calls, final round)", sends)
	}
	if !retrySawOff {
		t.Error("the retry send did not go out with reasoning pinned off")
	}
	if !retryCarriedTools {
		t.Error("the retry send did not carry the role's tools (the retry must be the same request)")
	}
	if len(dispatched) != 1 || dispatched[0] != tools.FunctionReadFile {
		t.Errorf("dispatched = %v, want [read_file] (the retry's tool call must be dispatched like a normal round)", dispatched)
	}
	if content != "recovered answer" {
		t.Errorf("content = %q, want recovered answer (the loop continued after dispatching the retry's calls)", content)
	}
	if stats.StopReason != "clean-finalize" {
		t.Errorf("stop = %q, want clean-finalize (the loop continued; the retry was a tool round, not an answer)", stats.StopReason)
	}
	// The fallback DID fire and recover the turn (with tool calls), so it is
	// recorded for telemetry even though the loop continued rather than
	// returning an answer: flag set, outcome = tool_calls.
	if !stats.ReasoningFallback {
		t.Errorf("ReasoningFallback = false, want true (the off-retry recovered the round with tool calls)")
	}
	if stats.ReasoningFallbackOutcome != journal.OutcomeToolCalls {
		t.Errorf("ReasoningFallbackOutcome = %q, want %q", stats.ReasoningFallbackOutcome, journal.OutcomeToolCalls)
	}
}

// TestRunLoopReasoningOffRetryRecoversXMLToolCalls: a Qwen-style model (the
// exact target of #149) can answer the reasoning-off retry with native XML
// tool-call markup in its content instead of structured tool_calls. The
// recovery must run the SAME XML recovery the main round applies — parse the
// calls out of the content, dispatch them, and NOT return the raw markup as
// the turn's final prose.
func TestRunLoopReasoningOffRetryRecoversXMLToolCalls(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	sends := 0
	var retrySawOff bool
	var dispatched []string
	xml := "<tool_call>\n<function=read_file>\n<parameter=path>\ngo.mod\n</parameter>\n</function>\n</tool_call>"
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		switch sends {
		case 1:
			return fakeResp("", nil, 1, 5), false, nil // empty finish, reasoning on
		case 2:
			// The off-retry: the Qwen model answers with native XML tool-call
			// markup in its content, NOT structured tool_calls.
			retrySawOff = r.Effort.Level == llm.EffortOff
			return fakeResp(xml, nil, 1, 8), false, nil
		case 3:
			return fakeResp("recovered answer", nil, 1, 8), false, nil // normal round after the tool result
		default:
			t.Fatalf("send %d: unexpected extra send", sends)
			return nil, false, nil
		}
	})
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		dispatched = append(dispatched, call.Function.Name)
		return "OBS:" + call.Function.Name
	})
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 5}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if !retrySawOff {
		t.Error("the retry send did not go out with reasoning pinned off")
	}
	if len(dispatched) != 1 || dispatched[0] != tools.FunctionReadFile {
		t.Errorf("dispatched = %v, want [read_file] (the retry's XML tool call must be dispatched, not returned as prose)", dispatched)
	}
	if content != "recovered answer" {
		t.Errorf("content = %q, want recovered answer — NOT the raw <tool_call> markup", content)
	}
	if strings.Contains(content, "function=read_file") || strings.Contains(content, "tool_call") {
		t.Errorf("content leaked raw tool-call markup: %q", content)
	}
	// system, assistant(tool_calls), tool(result), assistant — no doubled or
	// leftover empty assistant (the dropped empty finish did not survive).
	if len(req.Messages) != 4 {
		t.Fatalf("len(req.Messages) = %d, want 4 (system, assistant(tool_calls), tool, assistant): %v", len(req.Messages), rolesOf(req.Messages))
	}
	// The retry's assistant message carries the parsed call (not the raw markup).
	assistant := req.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 ||
		assistant.ToolCalls[0].Function.Name != tools.FunctionReadFile {
		t.Errorf("assistant message = %+v, want one parsed read_file tool call", assistant)
	}
	if strings.Contains(assistant.Content, "function=read_file") {
		t.Errorf("assistant content still carries raw markup: %q", assistant.Content)
	}
	if toolMsg := req.Messages[2]; toolMsg.Role != RoleTool {
		t.Errorf("message 2 role = %q, want a tool result following the assistant(tool_calls)", toolMsg.Role)
	}
	if finalMsg := req.Messages[3]; finalMsg.Role != "assistant" || finalMsg.Content != "recovered answer" {
		t.Errorf("final message = %+v, want the assistant's recovered answer", finalMsg)
	}
	// The fallback fired and recovered the round with tool calls, so the engine
	// records it (flag + outcome) for telemetry, exactly as TestRunLoopReasoningOffRetryToolCallsDispatch pins.
	if !stats.ReasoningFallback || stats.ReasoningFallbackOutcome != journal.OutcomeToolCalls {
		t.Errorf("ReasoningFallback=%v outcome=%q, want true/%q (the off-retry recovered the round with tool calls)",
			stats.ReasoningFallback, stats.ReasoningFallbackOutcome, journal.OutcomeToolCalls)
	}
}

// TestRunLoopNonEmptyFinishDoesNotRetryWithReasoningOff: a non-empty answer
// from a reasoning-on role triggers no retry at all — the recovery is gated
// on the empty branch of the loop.
func TestRunLoopNonEmptyFinishDoesNotRetryWithReasoningOff(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	sends := 0
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		return fakeResp("a normal answer", nil, 1, 5), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "a normal answer" {
		t.Errorf("content = %q, want a normal answer", content)
	}
	if sends != 1 {
		t.Errorf("sends = %d, want 1 (no retry for a non-empty reply)", sends)
	}
	if stats.ReasoningFallback {
		t.Errorf("ReasoningFallback = true, want false")
	}
	if stats.StopReason != "clean-finalize" {
		t.Errorf("stop = %q, want clean-finalize", stats.StopReason)
	}
}

// TestFinalizeLoopUnclampedEmptyWithNoCandidateReturnsEmpty: when every
// finalize attempt comes back empty, runLoop still returns cleanly —
// exactly two calls, no retry storm.
func TestFinalizeLoopUnclampedEmptyWithNoCandidateReturnsEmpty(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var finalizes int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			finalizes++
			return fakeResp("", nil, 1, 5), false, nil // always empty, never clamped
		}
		return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" }) // no candidate pattern
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "" {
		t.Errorf("content = %q, want empty (nothing salvageable)", content)
	}
	if stats.StopReason != "max-iter" {
		t.Errorf("stop = %q, want max-iter (no salvage succeeded to relabel it)", stats.StopReason)
	}
	if finalizes != 2 {
		t.Errorf("finalize calls = %d, want exactly 2 (bounded: one empty + one failed salvage, no retry storm)", finalizes)
	}
}

// TestRunLoopRestoresToolsAfterFailedEmptySalvage: the salvage re-ask withholds
// tools; when it also comes back empty, runLoop must still hand the caller's
// long-lived request back with its tools, or the coder's next turn runs with
// none. It must also label the turn empty-finalize, not clean-finalize.
func TestRunLoopRestoresToolsAfterFailedEmptySalvage(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	send := SenderFunc(func(context.Context, *AgentRequest) (*AgentResponse, bool, error) {
		return fakeResp("", nil, 1, 5), false, nil // always empty, never clamped
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	ts := Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}
	req.Tools = ts.Tools
	content, stats, err := runLoop(context.Background(), send, req, ts,
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "" || stats.StopReason != "empty-finalize" {
		t.Errorf("content=%q stop=%q, want empty/empty-finalize", content, stats.StopReason)
	}
	if len(req.Tools) != len(ts.Tools) {
		t.Errorf("req.Tools = %d tools after failed salvage, want %d restored", len(req.Tools), len(ts.Tools))
	}
}

// TestReFinalizePromptIsHonest pins the salvage ask's wording: it now fires on
// every empty finish (not just clamped ones), so it must not assert a cause
// that may be false, and it must carry the same honesty clause as
// rewriteClampedPrompt so an unfinished coding turn isn't narrated as done.
func TestReFinalizePromptIsHonest(t *testing.T) {
	for _, want := range []string{"Describe only what you actually completed", "unfinished"} {
		if !strings.Contains(reFinalizePrompt, want) {
			t.Errorf("reFinalizePrompt missing %q: %q", want, reFinalizePrompt)
		}
	}
	for _, banned := range []string{"everything you need", "whole budget thinking"} {
		if strings.Contains(reFinalizePrompt, banned) {
			t.Errorf("reFinalizePrompt still asserts %q: %q", banned, reFinalizePrompt)
		}
	}
}

// TestRunLoopDetectsStuckErrorLoop reproduces the live coder hang: the model
// alternates a no-op edit (always "Error: …identical; nothing to change") with a
// read (always succeeds). The byte-identical no-progress guard only sees
// CONSECUTIVE identical batches, so the interleaved read resets it and it NEVER
// trips — the harness thrashes to MaxIter. The error-class detector must catch the
// recurring error (across the interleaved reads), inject an actionable redirect,
// and escalate to a "stuck" finalize.
func TestRunLoopDetectsStuckErrorLoop(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // finalize round
			return fakeResp("gave up", nil, 1, 1), false, nil
		}
		if i++; i%2 == 1 {
			return fakeResp("", []ToolCall{{ID: "e", Type: "function", Function: FunctionCall{
				Name: tools.FunctionEditFile, Arguments: `{"path":"x"}`}}}, 1, 1), false, nil
		}
		return fakeResp("", []ToolCall{readCall("r", "x")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, c ToolCall) string {
		if c.Function.Name == tools.FunctionEditFile {
			return "Error: x edit 1: old_string and new_string are identical; nothing to change"
		}
		return "@x:1-5 lines"
	})
	_, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile, tools.EditFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 12}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "stuck" {
		t.Errorf("stop = %q, want \"stuck\" — the error-class detector should fire on the recurring edit error", stats.StopReason)
	}
	if stats.Iterations >= 12 {
		t.Errorf("ran to MaxIter %d — harness thrashed instead of redirecting", stats.Iterations)
	}
	foundHint := false
	for _, m := range req.Messages {
		if m.Role == RoleUser && strings.Contains(strings.ToLower(m.Content), "already applied") {
			foundHint = true
		}
	}
	if !foundHint {
		t.Error("no actionable redirect (the off-ramp hint) was injected before giving up")
	}
}

// TestStuckHint table-pins the stuckHint off-ramp for each recurring error
// class (issue #142): a known class must yield its ACTIONABLE redirect (the
// off-ramp a weak model can't infer from the raw error), and an unrecognized
// class must fall back to the generic "stop and report" wrap-up. The checks
// are on loose keywords — the principle, not the exact wording — so a future
// rephrase keeps the test green as long as the redirect survives, matching the
// loose-keyword style of the prompt tests.
func TestStuckHint(t *testing.T) {
	tests := []struct {
		name   string
		class  string
		wantIn []string // substrings that must appear in the hint
		notIn  []string // substrings that must NOT appear
	}{
		{
			name:   "no-op edit: confirm it is already applied",
			class:  "Error: x edit 1: old_string and new_string are identical; nothing to change",
			wantIn: []string{"already applied", "read_file", "confirm"},
		},
		{
			name:   "edit old_string not found: match the file exactly",
			class:  "Error: x edit 1: old_string not found in the file",
			wantIn: []string{"EXACTLY", "old_string", "Re-read"},
		},
		{
			// The issue #142 case: a recurring NOT-FOUND error class. The
			// class is the leading "Error: …" line with numbers neutralized
			// (errorClass), so "does not exist" must be present to trigger
			// the locate-first off-ramp.
			name:   "recurring not-found: stop guessing, outline/grep the workspace root",
			class:  "Error: missing.go does not exist. To locate the right path, use outline or grep.",
			wantIn: []string{"does not exist", "do not guess", "outline", "grep", "workspace root"},
		},
		{
			// The real not-found error read_file/grep/outline return (from
			// pathNotFoundError in internal/tools) must classify into the
			// locate-first off-ramp — pinning the end-to-end class→hint
			// mapping, not just a synthetic string.
			name:   "read_file/grep/outline not-found error: locate-first off-ramp",
			class:  "Error: /abs/missing.go does not exist. To locate the right path, use outline (list a directory) or grep (search contents) instead of guessing. The workspace root is /w; all tool paths are relative to it.",
			wantIn: []string{"does not exist", "do not guess", "workspace root", "re-issue"},
		},
		{
			// A non-not-found error must NOT get the locate-first redirect —
			// the off-ramp must stay class-specific, not bleed into unrelated
			// errors.
			name:   "unrecognized class: generic wrap-up, not the locate-first redirect",
			class:  "Error: something unrelated went wrong",
			wantIn: []string{"stop and report"},
			notIn:  []string{"do not guess paths"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := stuckHint(tt.class)
			lower := strings.ToLower(hint)
			for _, sub := range tt.wantIn {
				if !strings.Contains(lower, strings.ToLower(sub)) {
					t.Errorf("hint missing %q; got:\n%q", sub, hint)
				}
			}
			for _, sub := range tt.notIn {
				if strings.Contains(lower, strings.ToLower(sub)) {
					t.Errorf("hint should not contain %q; got:\n%q", sub, hint)
				}
			}
		})
	}
}

// TestRunLoopDetectsNotfoundStuck is the end-to-end acceptance test for the
// issue #142 stuck-hint: a model that keeps hitting a NOT-EXISTING path (each
// read_file returning the oriented not-found error) must get the locate-first
// off-ramp injected on the first repeat (stuckThreshold) and be escalated to a
// "stuck" finalize — instead of the harness thrashing to MaxIter. This is the
// same shape as TestRunLoopDetectsStuckErrorLoop (which covers the no-op-edit
// class) but pins the not-found class through the full errorClass → errSeen →
// stuckHint path.
func TestRunLoopDetectsNotfoundStuck(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // finalize round
			return fakeResp("gave up", nil, 1, 1), false, nil
		}
		// Alternate a failing read (always the not-found error) with a
		// successful read, so the byte-identical no-progress guard (which only
		// sees CONSECUTIVE identical batches) is reset by the interleaved
		// success and NEVER trips — forcing the error-class detector to do the
		// work, exactly like TestRunLoopDetectsStuckErrorLoop.
		if i++; i%2 == 1 {
			return fakeResp("", []ToolCall{readCall("e", "missing.go")}, 1, 1), false, nil
		}
		return fakeResp("", []ToolCall{readCall("r", "real.go")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, c ToolCall) string {
		if strings.Contains(c.Function.Arguments, "missing.go") {
			return "Error: missing.go does not exist. To locate the right path, use outline or grep instead of guessing."
		}
		return "@real.go:1-5 lines"
	})
	_, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 12}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "stuck" {
		t.Errorf("stop = %q, want \"stuck\" — the error-class detector should fire on the recurring not-found error", stats.StopReason)
	}
	if stats.Iterations >= 12 {
		t.Errorf("ran to MaxIter %d — harness thrashed instead of redirecting", stats.Iterations)
	}
	foundHint := false
	for _, m := range req.Messages {
		if m.Role == RoleUser && strings.Contains(strings.ToLower(m.Content), "do not guess") {
			foundHint = true
		}
	}
	if !foundHint {
		t.Error("no locate-first off-ramp (the not-found stuck hint) was injected before giving up")
	}
}

// TestRunLoopBlockingSubagentPath proves the subagent path with no second real
// caller: runLoop driven by the real blockingSender over an httptest server (no
// model) runs a 2-round tool loop, accounts tokens, drives the Progress sink,
// and clean-finalizes — exactly the path RunSubagent will use.
func TestRunLoopBlockingSubagentPath(t *testing.T) {
	quickRetries(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"the digest"}}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	cs := &CortexSession{}
	req := requestFor(ModelSpec{Model: "m", Endpoint: srv.URL}, "sys", "seed", []Tool{tools.ReadFile}, 1000, llm.DialectTemplateKwargs)
	var seen []string
	disp := DispatchFunc(func(_ context.Context, c ToolCall) string {
		seen = append(seen, c.Function.Name)
		return "FILE BODY"
	})
	var progress []string
	content, stats, err := runLoop(context.Background(), cs.blockingSender(), req,
		Toolset{Tools: req.Tools, Dispatch: disp},
		Bounds{MaxTokens: 1000, MaxIter: 10, ReadBudgetBytes: 96000},
		func(line string) { progress = append(progress, line) },
		func(m Message) { req.Messages = append(req.Messages, m) }, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "the digest" || stats.StopReason != "clean-finalize" {
		t.Errorf("content=%q stop=%q, want 'the digest'/clean-finalize", content, stats.StopReason)
	}
	if stats.Reads != 1 || len(seen) != 1 || seen[0] != tools.FunctionReadFile {
		t.Errorf("reads=%d seen=%v, want 1 read_file", stats.Reads, seen)
	}
	if stats.InputTokens != 11 || stats.OutputTokens != 5 {
		t.Errorf("tokens = %d in / %d out, want 11/5", stats.InputTokens, stats.OutputTokens)
	}
	if len(progress) != 1 { // one breadcrumb for the one tool call
		t.Errorf("progress lines = %d, want 1: %v", len(progress), progress)
	}
}

// TestRequestForSetsMaxTokens proves no request path is unbounded: requestFor
// always stamps a finite max_tokens — the passed ceiling when >0, else the
// role/default fallback. The regression guard for the 2026-06-28 north runaway.
func TestRequestForSetsMaxTokens(t *testing.T) {
	spec := ModelSpec{Model: "m", Endpoint: "http://x"}
	t.Run("explicit", func(t *testing.T) {
		r := requestFor(spec, "sys", "seed", nil, 5000, llm.DialectTemplateKwargs)
		if r.MaxTokens != 5000 {
			t.Errorf("max_tokens = %d, want 5000", r.MaxTokens)
		}
	})
	t.Run("fallback when zero", func(t *testing.T) {
		r := requestFor(spec, "sys", "seed", nil, 0, llm.DialectTemplateKwargs)
		if r.MaxTokens <= 0 {
			t.Errorf("max_tokens = %d, want a positive fallback (never unbounded)", r.MaxTokens)
		}
		if r.MaxTokens != defaultAgentMaxTokens {
			t.Errorf("max_tokens = %d, want default %d", r.MaxTokens, defaultAgentMaxTokens)
		}
	})
	t.Run("role override", func(t *testing.T) {
		r := requestFor(ModelSpec{Model: "m", MaxTokens: 4321}, "sys", "seed", nil, 0, llm.DialectTemplateKwargs)
		if r.MaxTokens != 4321 {
			t.Errorf("max_tokens = %d, want role override 4321", r.MaxTokens)
		}
	})
	t.Run("temperature default and override", func(t *testing.T) {
		if got := requestFor(spec, "sys", "seed", nil, 0, llm.DialectTemplateKwargs).Temperature; got != defaultTemperature {
			t.Errorf("temperature = %v, want default %v", got, defaultTemperature)
		}
		temp := 0.25
		if got := requestFor(ModelSpec{Model: "m", Temperature: &temp}, "sys", "seed", nil, 0, llm.DialectTemplateKwargs).Temperature; got != temp {
			t.Errorf("temperature = %v, want override %v", got, temp)
		}
	})
}

// TestSenderCancelClosesConnection proves a mid-flight cancel propagates to the
// client: with a hung server, cancelling the ctx returns the Sender PROMPTLY
// (closing its socket) because Send builds its HTTP request with the call ctx
// (http.NewRequestWithContext). This is the cancel signal the engine relies on.
// We deliberately do NOT assert the server observed the close: a non-streaming
// server blocked in its handler may never notice the client left — that is the
// exact LiteLLM-disconnect failure mode the mandatory max_tokens backstop exists
// for, and an ops concern, not an eval-verifiable one (docs/engine-unification.md).
func TestSenderCancelClosesConnection(t *testing.T) {
	quickRetries(t)
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		// Hang until the client cancels OR the test releases us, so srv.Close()
		// never blocks on a stuck handler regardless of disconnect propagation.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL, MaxTokens: 100,
		Messages: []Message{{Role: RoleUser, Content: "hi"}}}}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-started
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, _, err := cs.blockingSender().Send(ctx, cs.Request)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from the cancelled request, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send did not return promptly after cancel — ctx not threaded into the HTTP request")
	}
}

// TestDeliberationClamped covers the P4 detection function
// (docs/thinking-models.md §4): finish_reason "length" AND either the
// reasoning/completion split shows reasoning dominated (>= 80%), or —
// unreported — empty content with a non-empty reasoning trace.
func TestDeliberationClamped(t *testing.T) {
	tests := []struct {
		name string
		res  *AgentResponse
		want bool
	}{
		{
			name: "length finish, reasoning dominates the split: clamped",
			res: &AgentResponse{
				Choices: []Choice{{FinishReason: "length", Message: Message{Content: "a partial an"}}},
				Usage:   Usage{CompletionTokens: 100, CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 90}},
			},
			want: true,
		},
		{
			name: "length finish, reasoning below the fraction: not clamped",
			res: &AgentResponse{
				Choices: []Choice{{FinishReason: "length", Message: Message{Content: "a long finished answer"}}},
				Usage:   Usage{CompletionTokens: 100, CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 10}},
			},
			want: false,
		},
		{
			name: "length finish, no split reported, empty content + reasoning trace: clamped",
			res: &AgentResponse{
				Choices: []Choice{{FinishReason: "length", Reasoning: "still thinking about it"}},
				Usage:   Usage{CompletionTokens: 100},
			},
			want: true,
		},
		{
			name: "length finish, no split reported, content present: not clamped",
			res: &AgentResponse{
				Choices: []Choice{{FinishReason: "length", Message: Message{Content: "an answer"}, Reasoning: "some reasoning"}},
				Usage:   Usage{CompletionTokens: 100},
			},
			want: false,
		},
		{
			name: "finish_reason stop: never clamped regardless of usage",
			res: &AgentResponse{
				Choices: []Choice{{FinishReason: "stop"}},
				Usage:   Usage{CompletionTokens: 100, CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 99}},
			},
			want: false,
		},
		{
			name: "no choices: false",
			res:  &AgentResponse{Choices: nil},
			want: false,
		},
		{
			name: "nil response: false",
			res:  nil,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliberationClamped(tt.res); got != tt.want {
				t.Errorf("deliberationClamped() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSalvageDisablesEffortOnDeliberationClamp covers P4's detection wiring:
// a deliberation-clamped forced finalize is attributed on stats
// (DeliberationClamped) and the salvage re-ask goes out effort-off,
// restoring afterward (the same one-shot pattern as the existing
// temperature jitter). P5a (TestFinalizeSendsAlwaysGoEffortOff below)
// generalizes the effort-off mutation itself to EVERY finalize/salvage send
// unconditionally, so this test's own assertions no longer distinguish
// "clamped" from "not" for the mutation — only for the DeliberationClamped
// attribution, which stays meaningful for eval/telemetry purposes.
func TestSalvageDisablesEffortOnDeliberationClamp(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var salvageSawEffortOff bool
	var finalizes int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			finalizes++
			if finalizes == 1 {
				// The forced finalize round itself hits the deliberation-clamp
				// signature: length finish, reasoning ate ~90% of the completion,
				// empty visible content.
				return &AgentResponse{
					Choices: []Choice{{FinishReason: "length", Message: Message{Content: ""}}},
					Usage:   Usage{CompletionTokens: 100, CompletionTokensDetails: &completionTokensDetails{ReasoningTokens: 90}},
				}, false, nil
			}
			// The salvage re-ask: observe the wire fields it was actually sent with.
			salvageSawEffortOff = r.Effort.Level == llm.EffortOff && r.ChatTemplateKwargs["enable_thinking"] == false
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		}
		return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "salvaged answer" {
		t.Errorf("content = %q, want salvaged answer", content)
	}
	if !stats.DeliberationClamped {
		t.Error("stats.DeliberationClamped = false, want true")
	}
	if !salvageSawEffortOff {
		t.Error("the salvage re-ask did not go out with effort off")
	}
	if req.Effort.Level != llm.EffortOn {
		t.Errorf("req.Effort after runLoop = %+v, want restored to on", req.Effort)
	}
}

// TestFinalizeSendsAlwaysGoEffortOff covers P5a: docs/thinking-models.md §5
// generalizes P4's clamp-gated salvage fix to EVERY finalize send
// (tools withheld), unconditionally — including the primary finalizeLoop ask
// itself, which P4 never touched, and a salvage re-ask that follows a
// perfectly healthy (non-deliberation) clamp. Effort is always restored to
// its pre-finalize value once runLoop returns.
func TestFinalizeSendsAlwaysGoEffortOff(t *testing.T) {
	t.Run("the primary finalize ask goes out effort-off with no clamp involved at all", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

		var finalizeSawEffortOff bool
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil {
				finalizeSawEffortOff = r.Effort.Level == llm.EffortOff && r.ChatTemplateKwargs["enable_thinking"] == false
				return fakeResp("a clean forced answer", nil, 1, 10), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, _, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 1}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "a clean forced answer" {
			t.Errorf("content = %q, want a clean forced answer", content)
		}
		if !finalizeSawEffortOff {
			t.Error("the primary finalize ask did not go out with effort off")
		}
		if req.Effort.Level != llm.EffortOn {
			t.Errorf("req.Effort after runLoop = %+v, want restored to on", req.Effort)
		}
	})

	t.Run("a non-deliberation clamp's salvage re-ask ALSO goes out effort-off now", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

		var salvageEffort llm.Effort
		var finalizes int
		send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
			if r.Tools == nil {
				finalizes++
				if finalizes == 1 {
					// Clamped (out == MaxTokens) but NOT a deliberation clamp: no
					// finish_reason "length", no reasoning-token report.
					return fakeResp("a verbose but non-empty answer", nil, 1, 100), false, nil
				}
				salvageEffort = r.Effort
				return fakeResp("concise answer", nil, 1, 5), false, nil
			}
			return fakeResp("", []ToolCall{readCall("c", "p")}, 1, 1), false, nil
		})
		disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
		content, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 2}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if content != "concise answer" {
			t.Errorf("content = %q, want concise answer", content)
		}
		if stats.DeliberationClamped {
			t.Error("stats.DeliberationClamped = true, want false (no length finish_reason) — attribution stays narrow")
		}
		if salvageEffort.Level != llm.EffortOff {
			t.Errorf("salvage re-ask effort = %+v, want off (P5a: every finalize send, unconditionally)", salvageEffort)
		}
		if req.Effort.Level != llm.EffortOn {
			t.Errorf("req.Effort after runLoop = %+v, want restored to on", req.Effort)
		}
	})
}

// stuckErrorScripted builds the scripted stuck-error-loop Sender +
// Dispatch shared by the escalation tests below: it alternates a failing
// edit_file (always "…identical; nothing to change") with a succeeding
// read_file, driving the SAME recurring-error-class path
// TestRunLoopDetectsStuckErrorLoop exercises, and records the Effort level
// each tool-call round (req.Tools != nil) actually sent.
func stuckErrorScripted(observed *[]llm.EffortLevel) (Sender, AgentDispatcher) {
	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // finalize round
			return fakeResp("gave up", nil, 1, 1), false, nil
		}
		*observed = append(*observed, r.Effort.Level)
		if i++; i%2 == 1 {
			return fakeResp("", []ToolCall{{ID: "e", Type: "function", Function: FunctionCall{
				Name: tools.FunctionEditFile, Arguments: `{"path":"x"}`}}}, 1, 1), false, nil
		}
		return fakeResp("", []ToolCall{readCall("r", "x")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, c ToolCall) string {
		if c.Function.Name == tools.FunctionEditFile {
			return "Error: x edit 1: old_string and new_string are identical; nothing to change"
		}
		return "@x:1-5 lines"
	})
	return send, disp
}

// TestStuckGuardEscalatesEffortOnce covers P5c: opt-in (Bounds.EscalateEffort,
// mirroring tools.enable_effort_escalation) one-shot effort escalation
// alongside the stuck guard's existing temperature jitter. Off by default
// (the zero value): the same scripted stuck run leaves effort untouched
// unless EscalateEffort is set. When set, exactly the one post-redirect
// re-sample carries the escalated "high" effort, and it's restored
// immediately after — same one-shot pattern as the temperature jitter it
// rides alongside. An explicitly suppressed effort (off) never escalates —
// the redirect must not override a role's "don't reason" policy.
func TestStuckGuardEscalatesEffortOnce(t *testing.T) {
	t.Run("disabled by default: effort untouched through a stuck run", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var observed []llm.EffortLevel
		send, disp := stuckErrorScripted(&observed)
		_, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile, tools.EditFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 12}, nil, appendMsg, nil) // EscalateEffort left false
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if stats.StopReason != "stuck" {
			t.Fatalf("stop = %q, want stuck", stats.StopReason)
		}
		for i, lvl := range observed {
			if lvl != llm.EffortOn {
				t.Errorf("round %d effort = %q, want on (escalation disabled)", i, lvl)
			}
		}
	})

	t.Run("enabled: exactly the post-redirect re-sample escalates to high, then restores", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var observed []llm.EffortLevel
		send, disp := stuckErrorScripted(&observed)
		_, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile, tools.EditFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 12, EscalateEffort: true}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if stats.StopReason != "stuck" {
			t.Fatalf("stop = %q, want stuck", stats.StopReason)
		}
		highCount := 0
		for _, lvl := range observed {
			if lvl == llm.EffortHigh {
				highCount++
			} else if lvl != llm.EffortOn {
				t.Errorf("unexpected effort level observed: %q", lvl)
			}
		}
		if highCount != 1 {
			t.Errorf("high-effort rounds = %d, want exactly 1 (one-shot)", highCount)
		}
		if req.Effort.Level != llm.EffortOn {
			t.Errorf("req.Effort after runLoop = %+v, want restored to on", req.Effort)
		}
	})

	t.Run("an explicitly off effort never escalates, even when enabled", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
		applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
		var observed []llm.EffortLevel
		send, disp := stuckErrorScripted(&observed)
		_, stats, err := runLoop(context.Background(), send, req,
			Toolset{Tools: []Tool{tools.ReadFile, tools.EditFile}, Dispatch: disp},
			Bounds{MaxTokens: 100, MaxIter: 12, EscalateEffort: true}, nil, appendMsg, nil)
		if err != nil {
			t.Fatalf("runLoop: %v", err)
		}
		if stats.StopReason != "stuck" {
			t.Fatalf("stop = %q, want stuck", stats.StopReason)
		}
		for i, lvl := range observed {
			if lvl != llm.EffortOff {
				t.Errorf("round %d effort = %q, want off (explicit off must never escalate)", i, lvl)
			}
		}
	})
}

// TestRunLoopRestoresToolsAfterFailedClampedSalvage: a natural NON-empty
// finish whose completion hit the clamp (out == MaxTokens) runs
// salvageClampedFinalize, which withholds the tools for its re-send. When
// that re-send comes back empty (or errors), runLoop must still hand the
// caller's long-lived request back with its tools, or the coder's next turn
// runs with none — the regression the #149 restructure introduced when it
// dropped the pre-clean-finalize restore.
func TestRunLoopRestoresToolsAfterFailedClampedSalvage(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil { // the clamped-finalize re-ask: comes back empty
			return fakeResp("", nil, 1, 5), false, nil
		}
		// Natural NON-empty finish, clamped: out == MaxTokens, no tool calls.
		return fakeResp("a long answer that ran into the cap", nil, 1, 100), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	ts := Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}
	content, stats, err := runLoop(context.Background(), send, req, ts,
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "a long answer that ran into the cap" {
		t.Errorf("content = %q, want the clamped answer (the failed salvage falls through to clean-finalize)", content)
	}
	if stats.StopReason != "clean-finalize" {
		t.Errorf("stop = %q, want clean-finalize", stats.StopReason)
	}
	if req.Tools == nil || len(req.Tools) != len(ts.Tools) {
		t.Errorf("req.Tools after failed clamped salvage = %v, want the toolset restored (%d tools) for the caller's next turn",
			req.Tools, len(ts.Tools))
	}
}

// TestRunLoopReasoningFallbackReceiptFiresOncePerGenuineRecovery: the
// issue #149 receipt is round-local. Round 1's empty finish is recovered by
// the reasoning-off retry's tool calls (one genuine recovery → one receipt).
// A LATER round (3) comes back empty again and its retry errors (send
// failure → nil): the receipt must NOT fire a second time (it recovered
// nothing, and the run-level flag must not be read as a per-round gate),
// and the run's stats must keep recording the earlier genuine recovery.
// With the old flag-gated receipt, this scenario wrote a second, false
// receipt.
func TestRunLoopReasoningFallbackReceiptFiresOncePerGenuineRecovery(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var receipts int
	var receiptOutcomes []string
	sends := 0
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		switch sends {
		case 1:
			return fakeResp("", nil, 1, 5), false, nil // round 1: empty finish, reasoning on
		case 2:
			// Round 1's off-retry: recovers with a tool call (genuine recovery #1).
			return fakeResp("", []ToolCall{readCall("c1", "go.mod")}, 1, 8), false, nil
		case 3:
			// After the tool result, round 3 comes back empty again.
			return fakeResp("", nil, 1, 5), false, nil
		case 4:
			// Round 3's off-retry ERRORS: the retry recovered nothing.
			return nil, false, errors.New("backend down")
		case 5:
			// Fall-through salvage re-ask (tools withheld): recovers the turn.
			return fakeResp("salvaged answer", nil, 1, 5), false, nil
		default:
			t.Fatalf("send %d: unexpected extra send", sends)
			return nil, false, nil
		}
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	ts := Toolset{
		Tools:    []Tool{tools.ReadFile},
		Dispatch: disp,
		OnReasoningFallback: func(stats loopStats) {
			receipts++
			receiptOutcomes = append(receiptOutcomes, stats.ReasoningFallbackOutcome)
		},
	}
	content, stats, err := runLoop(context.Background(), send, req, ts,
		Bounds{MaxTokens: 100, MaxIter: 5}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "salvaged answer" {
		t.Errorf("content = %q, want salvaged answer (round 3's fall-through salvage)", content)
	}
	if receipts != 1 {
		t.Errorf("OnReasoningFallback fired %d times, want exactly 1 (round 1's genuine recovery; round 3's failed retry must not write a second receipt)",
			receipts)
	}
	if len(receiptOutcomes) == 1 && receiptOutcomes[0] != journal.OutcomeToolCalls {
		t.Errorf("receipt outcome = %q, want %q (the single genuine recovery was the tool-call one)",
			receiptOutcomes[0], journal.OutcomeToolCalls)
	}
	// The run-level flag keeps the earlier genuine recovery: a failed retry
	// in a later round must not clear it.
	if !stats.ReasoningFallback {
		t.Errorf("ReasoningFallback = false, want true (round 1's recovery stands for the run's stats)")
	}
	if stats.ReasoningFallbackOutcome != journal.OutcomeToolCalls {
		t.Errorf("ReasoningFallbackOutcome = %q, want %q (never overwritten by the later failed retry)",
			stats.ReasoningFallbackOutcome, journal.OutcomeToolCalls)
	}
	if stats.StopReason != "salvaged-finalize" {
		t.Errorf("stop = %q, want salvaged-finalize (round 3's prompt-based salvage)", stats.StopReason)
	}
}

// TestRunLoopFinalizeHookRoundAcrossFinishes pins how issue #141's
// FinalizeHook round (finalizeHookRound) composes with every way runLoop can
// finish naturally after the #149 reasoning-off retry landed beside it:
//
//   - a clean finish gets one extra tools-withheld round whose reply is
//     APPENDED to the real answer (#145 round 4), never substituted;
//   - an empty finish recovered by the #149 off-retry gets the SAME round
//     after the retry — the recovered prose is the model's natural answer,
//     so a reasoning-on model must still be told what its turn did to the
//     tests — and the #149 receipt still fires exactly once;
//   - an empty finish nothing could salvage still gets the round (the
//     position #145 gave the hook), so the note's answer is the turn's answer;
//   - an empty note costs no send at all.
//
// In every case the role's tools are restored on req for the caller's next
// turn, and the note round itself carries no tools.
func TestRunLoopFinalizeHookRoundAcrossFinishes(t *testing.T) {
	const note = "Harness note: tests changed: foo_test.go lost TestFoo"
	tests := []struct {
		name         string
		effort       llm.EffortLevel
		replies      []string // scripted assistant contents, one per send
		hookNote     string
		wantAnswer   string
		wantSends    int
		wantStop     string
		wantFallback int  // OnReasoningFallback calls
		wantNoteSent bool // the note reached the model as the last message of a tools-withheld send
	}{
		{
			name:         "clean finish appends the note round's reply",
			effort:       llm.EffortOff,
			replies:      []string{"summary of the work", "I removed TestFoo because it was obsolete."},
			hookNote:     note,
			wantAnswer:   "summary of the work\n\nI removed TestFoo because it was obsolete.",
			wantSends:    2,
			wantStop:     "clean-finalize",
			wantNoteSent: true,
		},
		{
			name:         "reasoning-off retry recovery also gets the note round",
			effort:       llm.EffortOn,
			replies:      []string{"", "recovered answer", "I removed TestFoo because it was obsolete."},
			hookNote:     note,
			wantAnswer:   "recovered answer\n\nI removed TestFoo because it was obsolete.",
			wantSends:    3,
			wantStop:     "salvaged-finalize",
			wantFallback: 1,
			wantNoteSent: true,
		},
		{
			name:         "unsalvageable empty finish still gets the note round",
			effort:       llm.EffortOff,
			replies:      []string{"", "", "I removed TestFoo because it was obsolete."},
			hookNote:     note,
			wantAnswer:   "I removed TestFoo because it was obsolete.",
			wantSends:    3,
			wantStop:     "clean-finalize",
			wantNoteSent: true,
		},
		{
			name:       "empty note costs no extra send",
			effort:     llm.EffortOff,
			replies:    []string{"summary of the work"},
			hookNote:   "",
			wantAnswer: "summary of the work",
			wantSends:  1,
			wantStop:   "clean-finalize",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
			applyEffort(req, llm.DialectTemplateKwargs, llm.Effort{Level: tt.effort})
			appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

			sends := 0
			noteSent := false
			send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
				sends++
				if sends > len(tt.replies) {
					t.Fatalf("send %d: unexpected extra send", sends)
				}
				if last := r.Messages[len(r.Messages)-1]; last.Role == RoleUser && last.Content == tt.hookNote && tt.hookNote != "" {
					if r.Tools != nil {
						t.Errorf("send %d: the note round carried tools, want them withheld", sends)
					}
					noteSent = true
				}
				return fakeResp(tt.replies[sends-1], nil, 1, 5), false, nil
			})
			hookCalls, fallbacks := 0, 0
			ts := Toolset{
				Tools:               []Tool{tools.ReadFile},
				Dispatch:            DispatchFunc(func(context.Context, ToolCall) string { return "obs" }),
				FinalizeHook:        func() string { hookCalls++; return tt.hookNote },
				OnReasoningFallback: func(loopStats) { fallbacks++ },
			}
			content, stats, err := runLoop(context.Background(), send, req, ts,
				Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
			if err != nil {
				t.Fatalf("runLoop: %v", err)
			}
			if content != tt.wantAnswer {
				t.Errorf("content = %q, want %q", content, tt.wantAnswer)
			}
			if sends != tt.wantSends {
				t.Errorf("sends = %d, want %d", sends, tt.wantSends)
			}
			if stats.StopReason != tt.wantStop {
				t.Errorf("stop = %q, want %q", stats.StopReason, tt.wantStop)
			}
			if hookCalls != 1 {
				t.Errorf("FinalizeHook consulted %d times, want exactly 1", hookCalls)
			}
			if fallbacks != tt.wantFallback {
				t.Errorf("OnReasoningFallback fired %d times, want %d", fallbacks, tt.wantFallback)
			}
			if noteSent != tt.wantNoteSent {
				t.Errorf("note sent = %v, want %v", noteSent, tt.wantNoteSent)
			}
			if len(req.Tools) != len(ts.Tools) {
				t.Errorf("req.Tools after runLoop = %v, want the toolset restored (%d tools)", req.Tools, len(ts.Tools))
			}
			if req.Effort.Level != tt.effort {
				t.Errorf("req.Effort after runLoop = %+v, want restored to %q", req.Effort, tt.effort)
			}
		})
	}
}

// TestHasToolCallMarkup covers the issue #230 markup detector: it must fire
// on Qwen XML and Hermes function_calls markup, and stay silent on plain
// prose.
func TestHasToolCallMarkup(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "clean prose", content: "I fixed the bug in loop.go.", want: false},
		{name: "empty content", content: "", want: false},
		{
			name:    "Qwen XML markup",
			content: "Done. <tool_call><function=bash>\n<parameter=command>\ngo test ./...\n</parameter>\n</function></tool_call>",
			want:    true,
		},
		{
			name:    "Hermes function_calls markup",
			content: `<function_calls>{"name": "bash", "arguments": {"command": "ls"}}</function_calls>`,
			want:    true,
		},
		{name: "prose with a word that looks like markup but is not", content: "The <tool_call> tag is not a real tool call.", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasToolCallMarkup(tt.content); got != tt.want {
				t.Errorf("hasToolCallMarkup(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// TestLooksTruncated covers the issue #230 truncation detector: it must fire
// on a clear mid-sentence cutoff (a dangling conjunction, preposition,
// relative pronoun, or article/possessive) and stay silent on a complete
// answer.
//
// The dangling-word list deliberately does NOT include "so", "then", or "as":
// the reviewer's #230 note observed that matching them flags ordinary complete
// replies ("I think so", "I fixed it, then ran the tests", "the fix as a
// comment"). The trade-off is that a truncation landing on one of those words
// is not flagged (it reads as a complete reply, and the turn ends without a
// re-ask) — accepted, because the list's job is to catch the obvious
// mid-sentence cutoffs, not every possible tail.
func TestLooksTruncated(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   bool
	}{
		{name: "empty answer", answer: "", want: false},
		{name: "ends with period", answer: "I fixed the bug.", want: false},
		{name: "ends with question mark", answer: "Is that done?", want: false},
		{name: "ends with exclamation", answer: "Done!", want: false},
		{name: "ends with closing paren", answer: "Fixed the bug (in loop.go).", want: false},
		{name: "ends with closing quote", answer: `Fixed the "bug".`, want: false},
		{name: "ends with colon", answer: "Fixed the bug:", want: false},
		{name: "ends with semicolon", answer: "Fixed the bug;", want: false},
		{name: "ends with comma", answer: "Fixed the bug,", want: false},
		{name: "ends with hyphen", answer: "Fixed the bug -", want: false},
		{name: "dangling conjunction: and", answer: "I fixed the bug and", want: true},
		{name: "dangling preposition: to", answer: "I was going to", want: true},
		{name: "dangling article: the", answer: "Let me restate my complete final answer for the", want: true},
		{name: "dangling possessive: my", answer: "Let me restate my", want: true},
		{name: "complete reply ending with a letter", answer: "final answer", want: false},
		{name: "complete reply ending with a noun", answer: "I fixed the bug in the loop engine", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksTruncated(tt.answer); got != tt.want {
				t.Errorf("looksTruncated(%q) = %v, want %v", tt.answer, got, tt.want)
			}
		})
	}
}

// TestSanitizeFinalAnswerStripsMarkup covers the issue #230 sanitizer's
// markup strip: a final answer with leaked Qwen XML or Hermes markup has it
// removed, leaving the prose. A markup-only answer becomes empty (the caller
// handles the empty-salvage chain).
// TestSanitizeFinalAnswerStripsMarkup covers the issue #230 sanitizer: a
// final answer with leaked Qwen XML or Hermes markup has it stripped,
// leaving the prose. A markup-only answer becomes empty (the caller's
// empty-salvage chain then repairs it). The function is PURE — no send, no
// salvage — so the salvage decision (empty or truncated) is the caller's.
func TestSanitizeFinalAnswerStripsMarkup(t *testing.T) {
	tests := []struct {
		name      string
		answer    string
		want      string
		wantTrunc bool // the sanitized result is flagged truncated (caller triggers the clamped salvage)
	}{
		{
			name:   "clean prose passes through",
			answer: "I fixed the bug in loop.go.",
			want:   "I fixed the bug in loop.go.",
		},
		{
			name:   "empty answer passes through",
			answer: "",
			want:   "",
		},
		{
			name:   "Qwen XML markup stripped, prose kept",
			answer: "Done.\n<tool_call><function=bash>\n<parameter=command>\ngo test ./...\n</parameter>\n</function></tool_call>",
			want:   "Done.",
		},
		{
			name: "Hermes markup stripped, prose kept",
			answer: `All done.
<function_calls>{"name": "bash", "arguments": {"command": "ls"}}</function_calls>`,
			want: "All done.",
		},
		{
			name:   "markup-only answer becomes empty",
			answer: "<tool_call><function=bash>\n<parameter=command>\nls\n</parameter>\n</function></tool_call>",
			want:   "",
		},
		{
			name:      "truncated answer is flagged (caller rewrites it)",
			answer:    "I fixed the bug and",
			want:      "I fixed the bug and",
			wantTrunc: true,
		},
		{
			name:   "complete answer is not flagged",
			answer: "I was about to say the thing.",
			want:   "I was about to say the thing.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFinalAnswer(tt.answer)
			if got != tt.want {
				t.Errorf("sanitizeFinalAnswer(%q) = %q, want %q", tt.answer, got, tt.want)
			}
			// The pure sanitizer performs no salvage: the truncated flag is
			// what tells the caller to run its clamped-salvage repair.
			if gotTrunc := looksTruncated(got); gotTrunc != tt.wantTrunc {
				t.Errorf("looksTruncated(sanitizeFinalAnswer(%q)) = %v, want %v", tt.answer, gotTrunc, tt.wantTrunc)
			}
		})
	}
}

// TestRunLoopCleanFinalizeStripsMarkup covers the issue #230 fix in
// runLoop's finalize path: a natural finish whose content carries leaked
// Qwen XML markup has the markup stripped from the returned answer. The
// markup is recovered and dispatched as a tool round (the pre-#230
// behavior the reviewer asked to restore — "go back to
// recoverTextToolCalls"), so the turn ends via the no-progress guard
// (the recovered batch is a repeat of the prior round's call), not via
// clean-finalize. The returned content is stripped of the raw markup.
func TestRunLoopCleanFinalizeStripsMarkup(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var sends int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		// Send 1: the model asks for a read (a tool round).
		if sends == 1 {
			return fakeResp("", []ToolCall{readCall("c1", "go.mod")}, 1, 1), false, nil
		}
		// Send 2: natural finish with leaked Qwen XML markup. The markup is
		// recovered and dispatched as a tool round (pre-#230 behavior); the
		// turn then ends via the no-progress guard (the recovered batch is a
		// repeat of the prior round's call).
		return fakeResp("Done. <tool_call><function=bash>\n<parameter=command>\nls\n</parameter>\n</function></tool_call>", nil, 1, 5), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 5}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	// The returned content is stripped of the raw markup. The prose "Done."
	// is what remains (the markup was recovered and dispatched as a tool
	// round, then the turn ended via the no-progress guard).
	if hasToolCallMarkup(content) {
		t.Error("content still carries tool-call markup after sanitization")
	}
	// The turn ended via the no-progress guard (the recovered batch is a
	// repeat of the prior round's call), not via clean-finalize.
	if stats.StopReason != "no-progress" {
		t.Errorf("stop = %q, want no-progress", stats.StopReason)
	}
}

// TestRunLoopCleanFinalizeSalvagesTruncated covers the issue #230 fix in
// runLoop's clean-finalize path: a natural finish whose content looks cut
// off mid-sentence (no terminal punctuation, not clamped by token count)
// triggers a salvage re-ask that rewrites the answer.
func TestRunLoopCleanFinalizeSalvagesTruncated(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var sends int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		// First send: tool round (the model asks for a read).
		if sends == 1 {
			return fakeResp("", []ToolCall{readCall("c1", "go.mod")}, 1, 1), false, nil
		}
		// Second send: natural finish, truncated: dangling conjunction,
		// no clamp.
		if sends == 2 {
			return fakeResp("I fixed the bug and", nil, 1, 5), false, nil
		}
		// Third send: the salvage re-ask (tools withheld) rewrites the
		// answer.
		return fakeResp("A clean, complete final answer.", nil, 1, 5), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "A clean, complete final answer." {
		t.Errorf("content = %q, want the salvaged rewrite", content)
	}
	if sends != 3 {
		t.Errorf("sends = %d, want 3 (tool round + truncated finish + salvage)", sends)
	}
	if stats.StopReason != "salvaged-finalize" {
		t.Errorf("stop = %q, want salvaged-finalize", stats.StopReason)
	}
}

// TestFinalizeLoopStripsMarkup covers the issue #230 fix in finalizeLoop
// (forced finalize): a forced-finalize reply with leaked markup has it
// stripped.
func TestFinalizeLoopStripsMarkup(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	// The forced finalize reply carries leaked Hermes markup.
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		return fakeResp("Summary here.\n<function_calls>{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}</function_calls>", nil, 1, 5), false, nil
	})
	stats := &loopStats{StopReason: "max-iter", FinalizeForced: true}
	content, _, err := finalizeLoop(context.Background(), send, req, "finalize prompt", stats, appendMsg, "")
	if err != nil {
		t.Fatalf("finalizeLoop: %v", err)
	}
	if content != "Summary here." {
		t.Errorf("content = %q, want %q (markup stripped)", content, "Summary here.")
	}
	if hasToolCallMarkup(content) {
		t.Error("content still carries tool-call markup after sanitization")
	}
}

// TestFinalizeLoopMarkupOnlySalvagesEmpty covers the issue #230 fix in
// finalizeLoop: a forced-finalize reply that is markup-only (no prose)
// becomes empty after stripping, and the empty-salvage chain recovers it.
func TestFinalizeLoopMarkupOnlySalvagesEmpty(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var sends int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		if sends == 1 {
			// Forced finalize reply: markup only, no prose.
			return fakeResp("<function_calls>{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}</function_calls>", nil, 1, 5), false, nil
		}
		// Empty-salvage re-ask: recovers the turn.
		return fakeResp("Recovered summary.", nil, 1, 5), false, nil
	})
	stats := &loopStats{StopReason: "max-iter", FinalizeForced: true}
	content, st, err := finalizeLoop(context.Background(), send, req, "finalize prompt", stats, appendMsg, "")
	if err != nil {
		t.Fatalf("finalizeLoop: %v", err)
	}
	if content != "Recovered summary." {
		t.Errorf("content = %q, want %q (empty salvage after markup strip)", content, "Recovered summary.")
	}
	if !st.Salvaged {
		t.Error("stats.Salvaged = false, want true (the empty salvage recovered the turn)")
	}
}

// TestRunLoopStructuredRoundStripsMarkupInContent covers the issue #230 fix
// for the recorded 082119.jsonl:281 shape: a tool round that already carries
// STRUCTURED tool calls, whose content ALSO carries leaked tool-call markup.
// The markup in the content is stripped so it doesn't leak into the
// transcript or the final answer, while the structured calls are dispatched
// as normal.
func TestRunLoopStructuredRoundStripsMarkupInContent(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }

	var sends int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		sends++
		if sends == 1 {
			// A structured tool round whose content ALSO carries leaked
			// markup (the 082119.jsonl:281 shape).
			return &AgentResponse{Choices: []Choice{{
				Index: 0,
				Message: Message{
					Role:      "assistant",
					Content:   "Let me check.\n<tool_call><function=grep>\n<parameter=pattern>\nfoo\n</parameter>\n</function></tool_call>",
					ToolCalls: []ToolCall{readCall("c1", "f")},
				},
			}}, Usage: Usage{PromptTokens: 1, CompletionTokens: 1}}, false, nil
		}
		// Final answer.
		return fakeResp("Done.", nil, 1, 5), false, nil
	})
	disp := DispatchFunc(func(context.Context, ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 5}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if content != "Done." {
		t.Errorf("content = %q, want %q", content, "Done.")
	}
	// The structured round's content must have its markup stripped in the
	// transcript — the model's message is appended to req.Messages with the
	// markup removed, so a resumed session never replays the raw markup.
	for _, m := range req.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if hasToolCallMarkup(m.Content) {
				t.Errorf("assistant message with tool calls still carries markup: %q", m.Content)
			}
			if m.Content != "Let me check." {
				t.Errorf("assistant message content = %q, want %q (markup stripped)", m.Content, "Let me check.")
			}
		}
	}
	if stats.StopReason != "clean-finalize" {
		t.Errorf("stop = %q, want clean-finalize", stats.StopReason)
	}
}

// TestSummaryIssueSurface covers the issue #230 `summary_issue` surface on
// the headless turn path: a final reply the engine's sanitizer could not
// repair (truncated, and the salvage re-ask also failed) is flagged on
// TurnResult.SummaryIssue, and the --json output carries it under
// "summary_issue". A clean reply leaves the flag empty.
//
// The "markup reply" case uses the Qwen3-Coder shape: a natural finish whose
// content carries leaked <tool_call> markup.
// (The Hermes <function_calls> shape is the same defect class but goes
// through a different parser path.) The engine's sanitizeFinalAnswer strips
// the recoverable markup, leaving the prose "Done." as the clean final
// answer — the reply the model meant, minus the leaked internal
// representation. The sanitizer REPAIRED the reply, so the flag is empty
// (the reply is clean). The flag is set when the SANITIZED reply is still
// malformed (e.g. a truncated reply the salvage re-ask also failed to
// rewrite); a reply the sanitizer successfully cleaned is clean and leaves
// the flag empty.
//
// The dispatcher is overridden to a no-op: the markup in the reply is a
// sanitizer defect, not a pending tool call, so the engine must NOT execute
// it; the override makes the test's intent explicit and keeps the test from
// running a real shell command.
func TestSummaryIssueSurface(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantIssue  string
		wantAbsent bool
	}{
		{name: "clean reply", reply: "I fixed the bug in loop.go.", wantIssue: "", wantAbsent: true},
		{
			name:       "markup reply",
			reply:      "Done. <tool_call><function=bash>\n<parameter=command>\nls\n</parameter>\n</function></tool_call>",
			wantIssue:  "",
			wantAbsent: true,
		},
		{
			name:      "truncated reply",
			reply:     "I fixed the bug and",
			wantIssue: "truncated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Hermetic: a temp dir for the session's .cortex/ (transcript,
			// memory) and a throwaway home for config / journal, so the
			// test doesn't write into the workspace's real .cortex/.
			t.Chdir(t.TempDir())
			cs := newMemSession(t)
			cs.Request = &AgentRequest{Model: "m", BaseURL: "http://localhost:0", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
			cs.senderOverride = SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
				return fakeResp(tc.reply, nil, 1, 5), false, nil
			})
			// No-op dispatcher: the markup in the reply is a sanitizer
			// defect, not a pending call — the engine must not execute it.
			cs.coderDispatcherOverride = func() AgentDispatcher {
				return DispatchFunc(func(context.Context, ToolCall) string { return "noop" })
			}
			cs.StartTranscript()
			if cs.transcript == nil {
				t.Fatal("StartTranscript failed")
			}
			t.Cleanup(func() { cs.Close() })
			res, err := cs.Turn(context.Background(), "do the thing")
			if err != nil {
				t.Fatalf("Turn: %v", err)
			}
			if tc.wantAbsent {
				if res.SummaryIssue != "" {
					t.Errorf("SummaryIssue = %q, want empty (clean reply)", res.SummaryIssue)
				}
			} else if res.SummaryIssue != tc.wantIssue {
				t.Errorf("SummaryIssue = %q, want %q", res.SummaryIssue, tc.wantIssue)
			}
		})
	}
}
