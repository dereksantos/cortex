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

func shellCall(id, cmd string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: "run_shell", Arguments: `{"cmd":"` + cmd + `"}`},
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
	// Even pure repetition can't fire before the window is full — early
	// turns must never be punished.
	for i := 0; i < noProgressWindow-1; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("c%d", i), "a.go")})
	}
	if p.noProgress() {
		t.Errorf("noProgress() = true before window full; want false")
	}
}

// TestProgressTracker_DistinctReads_DoNotTriggerStop pins the fix: a
// window of reads against DIFFERENT files is productive exploration
// (each surfaces something new), not spinning. The earlier heuristic
// fired here whenever the session lacked a write — killing cross-file
// questions one turn before synthesis ("empty synthesis").
func TestProgressTracker_DistinctReads_DoNotTriggerStop(t *testing.T) {
	p := &progressTracker{}
	// More distinct reads than the window: still never fires, because
	// every turn introduces a new target.
	for i := 0; i < noProgressWindow*2; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("c%d", i), fmt.Sprintf("file%d.go", i))})
		if p.noProgress() {
			t.Fatalf("noProgress() = true at turn %d; want false — distinct reads are progress", i)
		}
	}
}

func TestProgressTracker_OneWriteResets_DoesNotStop(t *testing.T) {
	p := &progressTracker{}
	// A write is progress, and the following reads are all distinct —
	// the whole window is productive.
	p.recordTurn([]llm.ToolCall{writeCall("w0", "main.go")})
	paths := []string{"a.go", "b.go", "c.go", "d.go"}
	for i := 0; i < noProgressWindow-1; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), paths[i])})
	}
	if p.noProgress() {
		t.Errorf("noProgress() = true; want false when the window contains a write and distinct reads")
	}
}

// TestProgressTracker_RepeatedShell_DoesNotStop — a debug loop that
// re-runs tests (or any run_shell) keeps making progress; side effects
// are never spinning. The hard caps bound a genuinely stuck shell loop.
func TestProgressTracker_RepeatedShell_DoesNotStop(t *testing.T) {
	p := &progressTracker{}
	for i := 0; i < noProgressWindow*2; i++ {
		p.recordTurn([]llm.ToolCall{shellCall(fmt.Sprintf("s%d", i), "go test ./...")})
		if p.noProgress() {
			t.Fatalf("noProgress() = true at turn %d; want false — run_shell is side-effecting progress", i)
		}
	}
}

// TestProgressTracker_SameFileReadInCircle_TriggersStop — the genuine
// pathology: the model re-reads the exact same file (identical args)
// and learns nothing new. The first read is productive (novel), so the
// stop fires once the window fills with repeats — at turn
// noProgressWindow+1.
func TestProgressTracker_SameFileReadInCircle_TriggersStop(t *testing.T) {
	p := &progressTracker{}
	for i := 0; i < noProgressWindow; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), "main.go")})
	}
	if p.noProgress() {
		t.Errorf("noProgress() = true after %d reads; want false — the first read was novel", noProgressWindow)
	}
	// One more repeat pushes the novel first-read out of the window.
	p.recordTurn([]llm.ToolCall{readCall("rN", "main.go")})
	if !p.noProgress() {
		t.Errorf("noProgress() = false; want true — %d consecutive identical reads is spinning", noProgressWindow)
	}
}

// TestProgressTracker_TwoFileCircle_TriggersStop — alternating between
// two already-seen files surfaces nothing new after the first read of
// each. A whole-window novelty check catches the cycle.
func TestProgressTracker_TwoFileCircle_TriggersStop(t *testing.T) {
	p := &progressTracker{}
	files := []string{"a.go", "b.go"}
	fired := false
	for i := 0; i < noProgressWindow*2; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), files[i%2])})
		if p.noProgress() {
			fired = true
			break
		}
	}
	if !fired {
		t.Errorf("noProgress() never fired; want true — A/B/A/B re-reads are a circle")
	}
}

// TestProgressTracker_SamePathDifferentRanges_StaysProductive — reading
// different line ranges of one file carries distinct args, so mining a
// long file for several symbols is progress, not a circle.
func TestProgressTracker_SamePathDifferentRanges_StaysProductive(t *testing.T) {
	p := &progressTracker{}
	for i := 0; i < noProgressWindow*2; i++ {
		args := fmt.Sprintf(`{"path":"big.go","start":%d,"end":%d}`, i*40, i*40+40)
		p.recordTurn([]llm.ToolCall{{
			ID:       fmt.Sprintf("r%d", i),
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "read_file", Arguments: args},
		}})
		if p.noProgress() {
			t.Fatalf("noProgress() = true at turn %d; want false — distinct line ranges are new info", i)
		}
	}
}

// TestProgressTracker_NovelReadResetsStreak — a single new read in an
// otherwise-stuck run resets the no-progress window. The model that
// breaks out of a circle by reading something new is not stopped.
func TestProgressTracker_NovelReadResetsStreak(t *testing.T) {
	p := &progressTracker{}
	// Four repeats of the same file (one novel + three stuck), then a
	// genuinely new read.
	p.recordTurn([]llm.ToolCall{readCall("r0", "main.go")})
	for i := 1; i < noProgressWindow-1; i++ {
		p.recordTurn([]llm.ToolCall{readCall(fmt.Sprintf("r%d", i), "main.go")})
	}
	p.recordTurn([]llm.ToolCall{readCall("fresh", "other.go")})
	if p.noProgress() {
		t.Errorf("noProgress() = true; want false — a novel read broke the streak")
	}
}

func TestProgressTracker_NovelSearchCountsAsProgress(t *testing.T) {
	p := &progressTracker{}
	// Distinct search queries are information-gathering progress; the
	// tracker is tool-agnostic about what surfaces new info.
	for i := 0; i < noProgressWindow*2; i++ {
		p.recordTurn([]llm.ToolCall{{
			ID:       fmt.Sprintf("q%d", i),
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "cortex_search", Arguments: fmt.Sprintf(`{"query":"term-%d"}`, i)},
		}})
		if p.noProgress() {
			t.Fatalf("noProgress() = true at turn %d; want false — distinct searches are progress", i)
		}
	}
}

func TestLoop_NoProgress_StopsLoop_RespectsBudgetCeiling(t *testing.T) {
	// A model stuck re-reading the SAME file every turn. The first read
	// is novel (progress); once the window fills with identical repeats
	// the loop stops with ReasonNoProgress — well before defaultMaxTurns.
	reg := newRegistry(t)
	resps := make([]llm.ChatResult, 0, noProgressWindow+2)
	for i := 0; i < noProgressWindow+2; i++ {
		resps = append(resps, llm.ChatResult{
			Content:      "",
			ToolCalls:    []llm.ToolCall{readCall(fmt.Sprintf("c%d", i), "stuck.go")},
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
	// The novel first read keeps the window productive until it scrolls
	// off, so the stop fires one turn after the window fills.
	if res.Turns != noProgressWindow+1 {
		t.Errorf("Turns=%d; want %d (stop fires once repeats fill the window)", res.Turns, noProgressWindow+1)
	}
}

// TestLoop_DistinctReadsThenFinal_ReachesModelDone — the regression the
// fix targets: a read-heavy session that reads several DIFFERENT files
// then synthesizes must reach its final answer. The earlier guard fired
// ReasonNoProgress on the write-less window and killed the session one
// turn before the model could respond (the "empty synthesis" failure).
func TestLoop_DistinctReadsThenFinal_ReachesModelDone(t *testing.T) {
	reg := newRegistry(t)
	// More distinct reads than the window, then a final text message.
	resps := []llm.ChatResult{}
	for i := 0; i < noProgressWindow+2; i++ {
		resps = append(resps, llm.ChatResult{
			ToolCalls:    []llm.ToolCall{readCall(fmt.Sprintf("r%d", i), fmt.Sprintf("file%d.go", i))},
			FinishReason: "tool_calls",
		})
	}
	resps = append(resps, llm.ChatResult{Content: "here is the explanation", FinishReason: "stop"})

	loop := &Loop{
		Provider: &scriptedProvider{responses: resps},
		Registry: reg,
		System:   "test",
	}
	res, err := loop.Run(context.Background(), "explain the core files")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != ReasonModelDone {
		t.Errorf("Reason=%q; want %q — distinct-read exploration must complete", res.Reason, ReasonModelDone)
	}
	if res.Final != "here is the explanation" {
		t.Errorf("Final=%q; want final synthesis text", res.Final)
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
				{},                                      // first call: error returned
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

// rewriteHistoryWithSnapshot — pure-function unit tests for the
// in-place msgs rewrite the accumulator-bounded loop drives each turn.

func TestRewriteHistoryWithSnapshot_NoSnapshot_NoOp(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
	}
	got, n := rewriteHistoryWithSnapshot(&msgs, "", 1)
	if got {
		t.Errorf("rewrote=true on empty snapshot; want false")
	}
	if n != 2 || len(msgs) != 2 {
		t.Errorf("msgs len changed: got %d / %d, want 2", n, len(msgs))
	}
}

func TestRewriteHistoryWithSnapshot_InsertsWorkingMemoryAfterUser(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the thing"},
	}
	rewrote, _ := rewriteHistoryWithSnapshot(&msgs, "memory contents", 1)
	if !rewrote {
		t.Fatal("rewrote=false; want true")
	}
	if len(msgs) != 3 {
		t.Fatalf("len=%d; want 3 (system+user+working_memory)", len(msgs))
	}
	if msgs[2].Role != "user" || !strings.HasPrefix(msgs[2].Content, workingMemorySentinel) {
		t.Errorf("msgs[2] role=%q content=%q; want user + sentinel", msgs[2].Role, msgs[2].Content)
	}
	if !strings.Contains(msgs[2].Content, "memory contents") {
		t.Errorf("working memory content missing snapshot body; got %q", msgs[2].Content)
	}
}

func TestRewriteHistoryWithSnapshot_RefreshesExistingWorkingMemory(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "user", Content: workingMemorySentinel + "\n\nold memory"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{readCall("r0", "main.go")}},
		{Role: "tool", ToolCallID: "r0", Name: "read_file", Content: "file contents"},
	}
	rewrote, _ := rewriteHistoryWithSnapshot(&msgs, "fresh memory", 1)
	if !rewrote {
		t.Fatal("rewrote=false; want true")
	}
	if len(msgs) != 5 {
		t.Errorf("len=%d; want 5 (refresh in place, no insert)", len(msgs))
	}
	if !strings.Contains(msgs[2].Content, "fresh memory") {
		t.Errorf("working memory not refreshed; got %q", msgs[2].Content)
	}
	if strings.Contains(msgs[2].Content, "old memory") {
		t.Errorf("old memory still present; got %q", msgs[2].Content)
	}
}

func TestRewriteHistoryWithSnapshot_KeepsLastKToolResults_StubsOlder(t *testing.T) {
	// 3 (assistant tool_call, tool_result) pairs, keep=1 → only the
	// most recent tool_result keeps its content; the other two get
	// stubbed.
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{readCall("r0", "a")}},
		{Role: "tool", ToolCallID: "r0", Name: "read_file", Content: "AAA"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{readCall("r1", "b")}},
		{Role: "tool", ToolCallID: "r1", Name: "read_file", Content: "BBB"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{readCall("r2", "c")}},
		{Role: "tool", ToolCallID: "r2", Name: "read_file", Content: "CCC"},
	}
	if rewrote, _ := rewriteHistoryWithSnapshot(&msgs, "snap", 1); !rewrote {
		t.Fatal("rewrote=false; want true")
	}
	// New layout: system, user, working_memory, then 3 (assistant,
	// tool) pairs. Indices: [0]=system, [1]=user, [2]=wm, [3]=asst,
	// [4]=tool(AAA→stub), [5]=asst, [6]=tool(BBB→stub), [7]=asst,
	// [8]=tool(CCC; kept).
	if len(msgs) != 9 {
		t.Fatalf("len=%d; want 9", len(msgs))
	}
	if msgs[4].Content != toolResultStub {
		t.Errorf("msgs[4] not stubbed; got %q", msgs[4].Content)
	}
	if msgs[6].Content != toolResultStub {
		t.Errorf("msgs[6] not stubbed; got %q", msgs[6].Content)
	}
	if msgs[8].Content != "CCC" {
		t.Errorf("msgs[8] (latest) modified; got %q want CCC", msgs[8].Content)
	}
	// Assistant tool_call messages should be preserved verbatim — the
	// tool_call_id matching protocol requires it.
	for _, i := range []int{3, 5, 7} {
		if len(msgs[i].ToolCalls) == 0 {
			t.Errorf("msgs[%d] lost its tool_calls; want preserved", i)
		}
	}
}

func TestRewriteHistoryWithSnapshot_KeepKHigherThanHistory_NoStub(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{readCall("r0", "a")}},
		{Role: "tool", ToolCallID: "r0", Name: "read_file", Content: "AAA"},
	}
	rewrote, _ := rewriteHistoryWithSnapshot(&msgs, "snap", 5)
	if !rewrote {
		t.Fatal("rewrote=false")
	}
	if msgs[4].Content != "AAA" {
		t.Errorf("msgs[4] stubbed despite keep=5 > 1 turn of history; got %q", msgs[4].Content)
	}
}

// End-to-end: Loop with AccumulatorSnapshot wired runs a 3-turn
// session. Verify the SECOND call's msgs has the working-memory
// message inserted and the THIRD call's msgs has the first turn's
// tool_result stubbed while the most recent is kept.
func TestLoop_AccumulatorSnapshot_RewritesEachTurn(t *testing.T) {
	reg := newRegistry(t)
	snap := "the working memory describes everything"
	loop := &Loop{
		Provider: &scriptedProvider{responses: []llm.ChatResult{
			{ToolCalls: []llm.ToolCall{readCall("r0", "a.go")}, FinishReason: "tool_calls"},
			{ToolCalls: []llm.ToolCall{readCall("r1", "b.go")}, FinishReason: "tool_calls"},
			{ToolCalls: []llm.ToolCall{writeCall("w0", "out.go")}, FinishReason: "tool_calls"},
			{Content: "done", FinishReason: "stop"},
		}},
		Registry:            reg,
		System:              "test",
		AccumulatorSnapshot: func(_ context.Context) string { return snap },
		KeepRecentTurns:     1,
	}
	_, err := loop.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sp := loop.Provider.(*scriptedProvider)
	if len(sp.observedMsgs) < 3 {
		t.Fatalf("observedMsgs=%d; want >=3", len(sp.observedMsgs))
	}

	// Call 0: no rewrite (turn==0 short-circuits).
	for _, m := range sp.observedMsgs[0] {
		if strings.HasPrefix(m.Content, workingMemorySentinel) {
			t.Errorf("call 0 has working memory; should not on turn 0")
		}
	}

	// Call 1: working memory injected once.
	wmCount := 0
	for _, m := range sp.observedMsgs[1] {
		if strings.HasPrefix(m.Content, workingMemorySentinel) {
			wmCount++
			if !strings.Contains(m.Content, snap) {
				t.Errorf("call 1 working memory missing snapshot body; got %q", m.Content)
			}
		}
	}
	if wmCount != 1 {
		t.Errorf("call 1 working-memory count=%d; want 1", wmCount)
	}

	// Call 2: still exactly one working-memory msg (refreshed in place,
	// not duplicated); the first tool_result (r0) should be stubbed.
	call2 := sp.observedMsgs[2]
	wmCount = 0
	for _, m := range call2 {
		if strings.HasPrefix(m.Content, workingMemorySentinel) {
			wmCount++
		}
	}
	if wmCount != 1 {
		t.Errorf("call 2 working-memory count=%d; want 1 (refresh, not dup)", wmCount)
	}
	var firstStubbed, secondKept bool
	for _, m := range call2 {
		if m.Role == "tool" && m.ToolCallID == "r0" {
			firstStubbed = m.Content == toolResultStub
		}
		if m.Role == "tool" && m.ToolCallID == "r1" {
			secondKept = m.Content != toolResultStub
		}
	}
	if !firstStubbed {
		t.Errorf("call 2 first tool_result (r0) not stubbed")
	}
	if !secondKept {
		t.Errorf("call 2 most recent tool_result (r1) was stubbed; should be kept verbatim")
	}
}

// TurnZero never invokes AccumulatorSnapshot: there's no prior output
// to fold, so calling out to the snapshot would be wasted work and
// could spuriously inject empty working memory.
func TestLoop_AccumulatorSnapshot_SkippedOnTurnZero(t *testing.T) {
	reg := newRegistry(t)
	called := 0
	loop := &Loop{
		Provider: &scriptedProvider{responses: []llm.ChatResult{
			{Content: "immediate answer", FinishReason: "stop"},
		}},
		Registry: reg,
		System:   "test",
		AccumulatorSnapshot: func(_ context.Context) string {
			called++
			return "would be memory"
		},
	}
	_, err := loop.Run(context.Background(), "no tools needed")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 0 {
		t.Errorf("AccumulatorSnapshot called %d times on turn-0-only session; want 0", called)
	}
}
