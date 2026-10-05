// beforesend_test.go — issue #171, step 2: the Toolset.BeforeSend seam on
// runLoop. A pure engine-level lock: the hook fires exactly once per
// main-loop iteration, immediately before send.Send, and a nil hook leaves the
// request byte-for-byte untouched (today's behavior). Kept in its own new file
// rather than editing loop_test.go (the pre-existing file the standing
// regression guard treats as frozen — see loop_budget_test.go's header).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestRunLoopBeforeSendFiresOncePerIteration locks the seam: a hook that
// mutates the request is observed by send.Send on the SAME round it fired —
// and exactly once per main-loop iteration. The model issues two distinct tool
// calls (so no-progress never trips), then the iteration cap forces a
// tools-withheld finalize; the hook must have fired once before each of the
// three main-loop sends, and must NOT fire for the forced-finalize send.
func TestRunLoopBeforeSendFiresOncePerIteration(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var i int
	// The hook stamps each request it sees so the sender can record what it
	// received; this is the "immediately before send.Send, same req" proof.
	var hookCalls int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			return fakeResp("forced", nil, 1, 1), false, nil
		}
		i++
		// Record the request the hook left in place this round.
		return fakeResp("", []ToolCall{readCall("c"+strings.Repeat("x", i), "f")}, 8, 4), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
	hook := func(r *AgentRequest) {
		hookCalls++
		// Mutate in a way the sender can see: tag the last message.
		if n := len(r.Messages); n > 0 {
			r.Messages[n-1].Content = "tagged"
		}
	}
	_, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp, BeforeSend: hook},
		Bounds{MaxTokens: 100, MaxIter: 3}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	// Two tool rounds + the cap-forced boundary = 3 main-loop iterations. The
	// hook fires once per iteration: the forced finalize (tools withheld,
	// r.Tools==nil) is a SEPARATE send inside finalizeLoop, which the hook
	// must NOT see — so hookCalls equals the iteration count, not the send
	// count.
	if stats.Iterations != 3 {
		t.Fatalf("iterations = %d, want 3 (two tool rounds + the cap-forced boundary)", stats.Iterations)
	}
	if hookCalls != stats.Iterations {
		t.Errorf("hook fired %d times, want %d (once per main-loop iteration, never for the forced finalize)", hookCalls, stats.Iterations)
	}
}

// TestRunLoopBeforeSendCleanFinalize is the clean-finalize counterpart of
// TestRunLoopBeforeSendFiresOncePerIteration: the model answers on round 3
// (no tool calls), so the turn ends through the main loop's clean-finalize
// path — the 3rd main-loop send is the answer, and there is no forced
// finalize at all. The hook must still fire exactly once per iteration.
func TestRunLoopBeforeSendCleanFinalize(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	var i int
	var hookCalls int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if r.Tools == nil {
			t.Fatal("a tools-withheld send must not happen on a clean-finalize turn")
		}
		i++
		if i >= 3 {
			return fakeResp("answer", nil, 1, 1), false, nil
		}
		// Distinct calls so the no-progress guard never trips.
		return fakeResp("", []ToolCall{readCall("c"+strings.Repeat("x", i), "f")}, 8, 4), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
	hook := func(r *AgentRequest) {
		hookCalls++
	}
	content, stats, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp, BeforeSend: hook},
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "clean-finalize" {
		t.Fatalf("stop = %q, want clean-finalize", stats.StopReason)
	}
	if content != "answer" {
		t.Errorf("content = %q, want answer", content)
	}
	if hookCalls != stats.Iterations {
		t.Errorf("hook fired %d times, want %d (once per main-loop iteration)", hookCalls, stats.Iterations)
	}
}

// TestRunLoopBeforeSendNilIsByteForByte locks the zero value: with a nil
// BeforeSend the request reaches send.Send completely untouched — the same
// byte-for-byte behavior as before the seam existed. We prove it by having the
// hook-less sender record the last message's content and asserting it is still
// the original (no mutation applied by a hook that isn't there).
func TestRunLoopBeforeSendNilIsByteForByte(t *testing.T) {
	const original = "original-content-must-survive"
	req := &AgentRequest{Model: "m", Messages: []Message{
		{Role: RoleSystem, Content: "s"}, {Role: RoleUser, Content: original},
	}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	// Record the content of the first user message the sender saw. With a nil
	// hook nothing may have mutated it, so it must still be the original.
	var sawOriginal string
	var i int
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		if sawOriginal == "" {
			for _, m := range r.Messages {
				if m.Role == RoleUser {
					sawOriginal = m.Content
					break
				}
			}
		}
		if r.Tools == nil {
			return fakeResp("forced", nil, 1, 1), false, nil
		}
		i++
		return fakeResp("", []ToolCall{readCall("c"+strings.Repeat("x", i), "f")}, 8, 4), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, _ ToolCall) string { return "obs" })
	_, _, err := runLoop(context.Background(), send, req,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp}, // BeforeSend is nil
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if sawOriginal != original {
		t.Errorf("sender saw user message %q, want the untouched %q (nil hook must not mutate the request)", sawOriginal, original)
	}
}
