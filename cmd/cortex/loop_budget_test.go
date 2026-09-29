// loop_budget_test.go — M6.4 (GOAL.md §6, D11): the engine-level half of the
// per-loop-firing budget caps, testing Bounds.TokenBudget directly against
// runLoop. Kept in its own new file rather than appending to the
// genesis-present loop_test.go (GOAL.md §2's standing regression guard
// treats any edit to a pre-existing test file as a violation needing a
// Decisions entry; a new file sidesteps that entirely). Reuses loop_test.go's
// fakeResp/readCall helpers — same package, no duplication needed. The
// RunLoopFiring-level half (spec.MaxTurns/MaxTokens wired through
// TurnWithBudget, and the "failed: budget" journal outcome) lives in
// loop_run_test.go.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestRunLoopTokenBudgetFinalizes locks Bounds.TokenBudget: a model that
// keeps issuing DIFFERENT tool calls (so no-progress and MaxIter never fire
// first) is stopped once cumulative input+output usage crosses the budget —
// well before the (much higher) MaxIter ceiling — proving the cap holds on
// SPEND, not just round count.
func TestRunLoopTokenBudgetFinalizes(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			return fakeResp("forced", nil, 1, 1), false, nil
		}
		i++
		// 8 in + 4 out per round: crosses a 10-token budget on round 1.
		return fakeResp("", []ToolCall{readCall("c", strings.Repeat("a", i))}, 8, 4), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp},
		Bounds{MaxTokens: 100, MaxIter: 100, TokenBudget: 10}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "token-budget" || !stats.FinalizeForced {
		t.Errorf("stop = %q forced=%v, want token-budget/true", stats.StopReason, stats.FinalizeForced)
	}
	if stats.Iterations != 1 {
		t.Errorf("iterations = %d, want 1 (budget crossed on the first round)", stats.Iterations)
	}
	if content != "forced" {
		t.Errorf("content = %q, want forced", content)
	}
}
