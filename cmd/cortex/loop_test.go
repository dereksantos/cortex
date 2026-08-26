package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	})
	t.Run("natural empty clamped finish is salvaged", func(t *testing.T) {
		// The live failure mode: the model returns NO tool calls with EMPTY content
		// because it spiraled to the token clamp on a normal turn (out == MaxTokens).
		// The natural-finish path must salvage it, not return "".
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
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
	t.Run("empty clamped finish can salvage from explicit tool candidate", func(t *testing.T) {
		req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
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

// TestRunLoopSalvagesNaturalEmptyUnclampedFinish locks down the live failure
// mode this fix addresses: a hybrid-reasoning model returns an empty final
// message with NO tool calls and does NOT hit the token clamp — its answer
// landed in a reasoning channel the blocking path never parses, or it just
// stopped (docs/thinking-models.md's blocking-path gap). Before this fix the
// salvage was gated on stats.MaxTokensClamped (false here), so this empty
// answer was accepted as-is and turn.go's caller (lastAssistantText) then
// silently resurfaced a stale PRE-tool-call assistant message as if it were
// the finished answer. The salvage must fire on any empty final answer,
// clamped or not.
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
}

// TestFinalizeLoopSalvagesNaturalEmptyUnclampedFinish is the forced-finalize
// counterpart to TestRunLoopSalvagesNaturalEmptyUnclampedFinish above: the
// SAME empty-but-not-clamped failure mode, but hit via the forced-finalize
// path (max-iter/stuck/read-budget) rather than a natural mid-loop finish.
// Both call sites share the fix, so both need the coverage.
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
	if finalizes != 2 {
		t.Errorf("finalize calls = %d, want 2 (one empty + one salvage)", finalizes)
	}
}

// TestFinalizeLoopUnclampedEmptyWithNoCandidateReturnsEmpty proves the widened
// gate is still bounded: when EVERY finalize attempt (initial + the one
// salvage re-ask) comes back empty and there's no frequency-candidate pattern
// to fall back on, runLoop returns cleanly with an empty answer — exactly two
// finalize network calls, no error, no infinite retry.
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
