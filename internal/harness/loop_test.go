package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// scriptedProvider replays a queued sequence of ChatResults so the
// loop can be tested without a real LLM. Each Generate* call pops
// the next response; running out of responses fails the test.
type scriptedProvider struct {
	responses []llm.ChatResult
	// errors, when non-nil at the matching index, are returned instead
	// of responses[i]. Pads with nil for indices that should succeed.
	errors []error
	// observedMsgs[i] is the msgs slice handed to call i; tests that
	// care whether history was trimmed before the retry inspect this.
	observedMsgs [][]llm.ChatMessage
	idx          int
}

func (p *scriptedProvider) GenerateWithTools(_ context.Context, msgs []llm.ChatMessage, _ []llm.ToolSpec, _ any) (llm.ChatResult, llm.GenerationStats, error) {
	if p.idx >= len(p.responses) {
		return llm.ChatResult{}, llm.GenerationStats{}, fmt.Errorf("scriptedProvider exhausted at idx=%d", p.idx)
	}
	// Snapshot msgs (the loop reuses the slice header across calls).
	snap := make([]llm.ChatMessage, len(msgs))
	copy(snap, msgs)
	p.observedMsgs = append(p.observedMsgs, snap)
	r := p.responses[p.idx]
	var err error
	if p.idx < len(p.errors) {
		err = p.errors[p.idx]
	}
	p.idx++
	if err != nil {
		return llm.ChatResult{}, llm.GenerationStats{}, err
	}
	return r, llm.GenerationStats{InputTokens: 100, OutputTokens: 50}, nil
}
func (p *scriptedProvider) LastCostUSD() float64 { return 0.001 }
func (p *scriptedProvider) Model() string        { return "scripted" }

// readCall builds a tool-call shape the loop's tracker classifies as
// a read. Args carry the path so identical paths across turns trip
// the same-files-only condition.
func readCall(id, path string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"` + path + `"}`},
	}
}

func writeCall(id, path string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: "write_file", Arguments: `{"path":"` + path + `","content":"x"}`},
	}
}

// newRegistry wires read/write/list/shell with a fresh workdir so
// Dispatch doesn't blow up; tests don't care about outputs.
func newRegistry(t *testing.T) *ToolRegistry {
	t.Helper()
	dir := t.TempDir()
	reg := NewToolRegistry()
	reg.Register(NewReadFileTool(dir))
	reg.Register(NewWriteFileTool(dir, reg))
	reg.Register(NewListDirTool(dir))
	reg.Register(NewRunShellTool(dir, reg))
	return reg
}

func TestProgressTracker_NotEnoughTurnsYet(t *testing.T) {
	p := &progressTracker{}
	for i := 0; i < noProgressWindow-1; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("c%d", i), "a.go")})
	}
	if p.noProgress() {
		t.Errorf("noProgress() = true before window full; want false")
	}
}

func TestProgressTracker_AllReadsNoWrite_TriggersStop(t *testing.T) {
	p := &progressTracker{}
	// Window of pure-read turns; varying paths so the "same files"
	// condition does NOT fire — this isolates the "no write_file/
	// run_shell" condition.
	paths := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	for i := 0; i < noProgressWindow; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("c%d", i), paths[i%len(paths)])})
	}
	if !p.noProgress() {
		t.Errorf("noProgress() = false; want true after %d read-only turns", noProgressWindow)
	}
}

func TestProgressTracker_OneWriteResets_DoesNotStop(t *testing.T) {
	p := &progressTracker{}
	// First turn writes, subsequent turns read different files — the
	// window has at least one write so condition 1 fails, and reads
	// differ so condition 2 fails.
	p.recordTurn([]llm.ToolCall{writeCall("w0", "main.go")})
	paths := []string{"a.go", "b.go", "c.go", "d.go"}
	for i := 0; i < noProgressWindow-1; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), paths[i])})
	}
	if p.noProgress() {
		t.Errorf("noProgress() = true; want false when window contains a write")
	}
}

func TestProgressTracker_SameFileReadInCircle_TriggersStop(t *testing.T) {
	p := &progressTracker{}
	// Every turn in the window reads exactly the same file. Both
	// conditions fire here (no writes + identical read targets) —
	// the test pins the same-file-in-a-circle pathology.
	for i := 0; i < noProgressWindow; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), "main.go")})
	}
	if !p.noProgress() {
		t.Errorf("noProgress() = false; want true after window of identical reads")
	}
}

func TestProgressTracker_TurnOrderIndependent(t *testing.T) {
	// Two reads in the same turn should hash to the same readTargets
	// regardless of arrival order.
	p1 := &progressTracker{}
	p2 := &progressTracker{}
	for i := 0; i < noProgressWindow; i++ {
		p1.recordTurn([]llm.ToolCall{readCall("a", "x.go"), readCall("b", "y.go")})
		p2.recordTurn([]llm.ToolCall{readCall("b", "y.go"), readCall("a", "x.go")})
	}
	if p1.turnShapes[0].readTargets != p2.turnShapes[0].readTargets {
		t.Errorf("readTargets not order-independent: %q vs %q", p1.turnShapes[0].readTargets, p2.turnShapes[0].readTargets)
	}
}

func TestLoop_NoProgress_StopsLoop_RespectsBudgetCeiling(t *testing.T) {
	// Build a script of noProgressWindow turns, each issuing a single
	// read_file (no writes, no run_shell). After the window fills the
	// loop should stop with ReasonNoProgress — well before defaultMaxTurns.
	reg := newRegistry(t)
	resps := make([]llm.ChatResult, 0, noProgressWindow+1)
	for i := 0; i < noProgressWindow+1; i++ {
		resps = append(resps, llm.ChatResult{
			Content: "",
			ToolCalls: []llm.ToolCall{readCall(
				fmt.Sprintf("c%d", i),
				fmt.Sprintf("file%d.go", i),
			)},
			FinishReason: "tool_calls",
		})
	}
	loop := &Loop{
		Provider: &scriptedProvider{responses: resps},
		Registry: reg,
		System:   "test",
	}
	res, err := loop.Run(context.Background(), "explore the repo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonNoProgress {
		t.Fatalf("Reason=%q; want %q", res.Reason, ReasonNoProgress)
	}
	if res.Turns != noProgressWindow {
		t.Errorf("Turns=%d; want %d (stop fires after the window fills)", res.Turns, noProgressWindow)
	}
}

func TestLoop_ProductiveSession_ReachesModelDone(t *testing.T) {
	// A short productive script: two read+write turns, then the model
	// emits a final text message with no tool calls. The no-progress
	// tracker should NOT fire — write_file is in the window.
	reg := newRegistry(t)
	loop := &Loop{
		Provider: &scriptedProvider{responses: []llm.ChatResult{
			{
				ToolCalls:    []llm.ToolCall{readCall("r0", "main.go")},
				FinishReason: "tool_calls",
			},
			{
				ToolCalls:    []llm.ToolCall{writeCall("w0", "out.go")},
				FinishReason: "tool_calls",
			},
			{Content: "done", FinishReason: "stop"},
		}},
		Registry: reg,
		System:   "test",
	}
	res, err := loop.Run(context.Background(), "write out.go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonModelDone {
		t.Errorf("Reason=%q; want %q", res.Reason, ReasonModelDone)
	}
	if !strings.Contains(res.Final, "done") {
		t.Errorf("Final=%q; want substring 'done'", res.Final)
	}
}

func TestLoop_ContextOverflow_RetriesOnceAndLearnsNctx(t *testing.T) {
	// First call returns a typed ContextOverflowError; second call
	// (the retry) succeeds and the loop should reach ReasonModelDone.
	overflow := &llm.ContextOverflowError{
		Message:         "chatterbox: server error: request (4946 tokens) exceeds the available context size (4096 tokens)",
		AvailableTokens: 4096,
		RequestedTokens: 4946,
	}
	reg := newRegistry(t)

	// Build PriorMessages whose token estimate (with the 4-char
	// heuristic) clearly exceeds 70% of 4096 = ~2867. 16 messages × ~500
	// tokens = ~8k; the retry trim must drop messages to fit.
	bulky := make([]llm.ChatMessage, 0, 16)
	for i := 0; i < 8; i++ {
		bulky = append(bulky, llm.ChatMessage{Role: "user", Content: strings.Repeat("xx ", 700)})
		bulky = append(bulky, llm.ChatMessage{Role: "assistant", Content: strings.Repeat("yy ", 700)})
	}

	var notifies []string
	loop := &Loop{
		Provider: &scriptedProvider{
			responses: []llm.ChatResult{
				{},                                    // first call: error returned
				{Content: "done", FinishReason: "stop"}, // retry: succeeds
			},
			errors: []error{overflow, nil},
		},
		Registry:      reg,
		System:        "test",
		PriorMessages: bulky,
		Notify: func(kind string, _ any) {
			notifies = append(notifies, kind)
		},
	}
	res, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonModelDone {
		t.Fatalf("Reason=%q; want %q (the loop should recover via retry)", res.Reason, ReasonModelDone)
	}
	if loop.ContextWindowTokens != 4096 {
		t.Errorf("ContextWindowTokens=%d; want 4096 (learned from overflow error)", loop.ContextWindowTokens)
	}
	// Both retry + trim events should have fired.
	saw := func(kind string) bool {
		for _, k := range notifies {
			if k == kind {
				return true
			}
		}
		return false
	}
	if !saw("coding.context_overflow_retry") {
		t.Errorf("missing coding.context_overflow_retry; got %v", notifies)
	}
	if !saw("coding.history_trimmed") {
		t.Errorf("missing coding.history_trimmed; got %v", notifies)
	}
}

func TestLoop_ContextOverflow_DoesNotRetryTwice(t *testing.T) {
	// Both attempts fail with overflow — the loop must give up after
	// the single retry, not loop indefinitely.
	overflow := &llm.ContextOverflowError{
		Message:         "request (8000 tokens) exceeds the available context size (4096 tokens)",
		AvailableTokens: 4096,
		RequestedTokens: 8000,
	}
	reg := newRegistry(t)
	loop := &Loop{
		Provider: &scriptedProvider{
			responses: []llm.ChatResult{{}, {}},
			errors:    []error{overflow, overflow},
		},
		Registry: reg,
		System:   "test",
	}
	res, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonError {
		t.Errorf("Reason=%q; want %q", res.Reason, ReasonError)
	}
}

func TestLoop_ProactiveTrimWhenNctxKnown(t *testing.T) {
	// Set ContextWindowTokens up front. Hand the loop bulky priors
	// that exceed 0.85 of the window; the proactive trim should fire
	// before the first call.
	reg := newRegistry(t)
	bulky := make([]llm.ChatMessage, 0, 8)
	for i := 0; i < 8; i++ {
		bulky = append(bulky, llm.ChatMessage{Role: "user", Content: strings.Repeat("a ", 500)})
		bulky = append(bulky, llm.ChatMessage{Role: "assistant", Content: strings.Repeat("b ", 500)})
	}

	var (
		notifies []string
		callIdx  int
	)
	provider := &scriptedProvider{
		responses: []llm.ChatResult{
			{Content: "done", FinishReason: "stop"},
		},
	}
	loop := &Loop{
		Provider:            provider,
		Registry:            reg,
		System:              "sys",
		PriorMessages:       bulky,
		ContextWindowTokens: 1000, // small budget → must trim
		Notify: func(kind string, _ any) {
			notifies = append(notifies, kind)
		},
	}
	res, err := loop.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonModelDone {
		t.Errorf("Reason=%q; want %q", res.Reason, ReasonModelDone)
	}
	_ = callIdx
	sawTrim := false
	for _, k := range notifies {
		if k == "coding.history_trimmed" {
			sawTrim = true
			break
		}
	}
	if !sawTrim {
		t.Errorf("missing coding.history_trimmed; got %v", notifies)
	}
	// Sanity: the msgs handed to the provider should be smaller than
	// the un-trimmed total.
	if len(provider.observedMsgs) == 0 {
		t.Fatal("no calls observed")
	}
	if got := len(provider.observedMsgs[0]); got >= len(bulky)+2 {
		t.Errorf("trim did not shrink msgs (saw %d, untrimmed would be %d)", got, len(bulky)+2)
	}
}
