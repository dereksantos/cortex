package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/cmd/loop/tools"
)

// fakeResp builds a minimal *AgentResponse with one assistant choice.
func fakeResp(content string, calls []ToolCall, in, out int) *AgentResponse {
	return &AgentResponse{
		Choices: []Choice{{Message: Message{Role: "assistant", Content: content, ToolCalls: calls}}},
		Usage:   Usage{PromptTokens: in, CompletionTokens: out},
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
		ts, Bounds{MaxTokens: 1000, MaxIter: 100}, nil, appendMsg)
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
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, appendMsg)
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
		Bounds{MaxTokens: 100, MaxIter: 100, ReadBudgetBytes: 8000}, nil, appendMsg)
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

// TestRequestForSetsMaxTokens proves no request path is unbounded: requestFor
// always stamps a finite max_tokens — the passed ceiling when >0, else the
// role/default fallback. The regression guard for the 2026-06-28 north runaway.
func TestRequestForSetsMaxTokens(t *testing.T) {
	spec := ModelSpec{Model: "m", Endpoint: "http://x"}
	t.Run("explicit", func(t *testing.T) {
		r := requestFor(spec, "sys", "seed", nil, 5000)
		if r.MaxTokens != 5000 {
			t.Errorf("max_tokens = %d, want 5000", r.MaxTokens)
		}
	})
	t.Run("fallback when zero", func(t *testing.T) {
		r := requestFor(spec, "sys", "seed", nil, 0)
		if r.MaxTokens <= 0 {
			t.Errorf("max_tokens = %d, want a positive fallback (never unbounded)", r.MaxTokens)
		}
		if r.MaxTokens != defaultAgentMaxTokens {
			t.Errorf("max_tokens = %d, want default %d", r.MaxTokens, defaultAgentMaxTokens)
		}
	})
	t.Run("role override", func(t *testing.T) {
		r := requestFor(ModelSpec{Model: "m", MaxTokens: 4321}, "sys", "seed", nil, 0)
		if r.MaxTokens != 4321 {
			t.Errorf("max_tokens = %d, want role override 4321", r.MaxTokens)
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
