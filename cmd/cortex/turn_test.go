package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/journal"
)

// lastAssistantText is what a headless Turn caller relays back to its transport,
// so it must return the model's actual prose — the final assistant message with
// content — and skip tool-call-only (empty-content) assistant messages.
func TestLastAssistantText(t *testing.T) {
	tests := []struct {
		name string
		msgs []Message
		want string
	}{
		{
			name: "empty turn",
			msgs: nil,
			want: "",
		},
		{
			name: "single assistant answer",
			msgs: []Message{
				{Role: RoleUser, Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
			want: "hello",
		},
		{
			name: "skips tool-call-only assistant message",
			msgs: []Message{
				{Role: RoleUser, Content: "read the file"},
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "1"}}}, // no content
				{Role: RoleTool, ToolCallID: "1", Content: "file body"},
				{Role: "assistant", Content: "here is what it says"},
			},
			want: "here is what it says",
		},
		{
			name: "returns the LAST assistant prose, not the first",
			msgs: []Message{
				{Role: "assistant", Content: "let me check"},
				{Role: RoleTool, ToolCallID: "1", Content: "result"},
				{Role: "assistant", Content: "final answer"},
			},
			want: "final answer",
		},
		{
			name: "whitespace-only content is not a reply",
			msgs: []Message{
				{Role: "assistant", Content: "real answer"},
				{Role: "assistant", Content: "   \n"},
			},
			want: "real answer",
		},
		{
			name: "ignores trailing tool result",
			msgs: []Message{
				{Role: "assistant", Content: "answer before tool"},
				{Role: RoleTool, ToolCallID: "9", Content: "tool output that is not a reply"},
			},
			want: "answer before tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastAssistantText(tt.msgs); got != tt.want {
				t.Errorf("lastAssistantText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// contextSessionEntries re-reads the just-written session transcript and
// returns every "context" entry's sample — the diagnostic record this test
// exists to prove is actually on disk, not just held in memory.
func contextSessionEntries(t *testing.T, cs *CortexSession) []contextSample {
	t.Helper()
	path := filepath.Join(sessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	var out []contextSample
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		if e.Kind == kindContext && e.Context != nil {
			out = append(out, *e.Context)
		}
	}
	return out
}

// TestTurnWritesContextSamples closes the diagnostic gap: a session's real,
// provider-billed prompt-token usage was previously only held transiently in
// cs.LastPromptTokens (or the live REPL gauge) and never persisted, so a past
// session could only be diagnosed against the estTurnTokens/TailTokens
// char/4 heuristic — never against what the model actually saw. Every model
// round-trip must now leave a "context" entry with the real usage.prompt_tokens
// alongside the estimate and the watermarks in force at that instant.
func TestTurnWritesContextSamples(t *testing.T) {
	t.Chdir(t.TempDir())
	backend := newContextEvalBackend(t) // usage.prompt_tokens=10 per scripted reply
	// Window 4000 -> tail watermarks high=2000/low=1333 (docs/context-architecture.md).
	cs := newContextEvalSession(t, backend, 4000)

	if _, err := cs.Turn(context.Background(), "hello"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	samples := contextSessionEntries(t, cs)
	if len(samples) != 1 {
		t.Fatalf("got %d context samples for one no-tool-call turn, want 1", len(samples))
	}
	s := samples[0]
	if s.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", s.Iteration)
	}
	if s.LastPromptTokens != 10 {
		t.Errorf("LastPromptTokens = %d, want 10 (the backend's scripted usage.prompt_tokens)", s.LastPromptTokens)
	}
	if s.Window != 4000 {
		t.Errorf("Window = %d, want 4000", s.Window)
	}
	if s.HighWatermark != 2000 {
		t.Errorf("HighWatermark = %d, want 2000 (window/2)", s.HighWatermark)
	}
	if s.TailTokensEst < 0 {
		t.Errorf("TailTokensEst = %d, want >= 0", s.TailTokensEst)
	}
	if s.MaxTokens <= 0 {
		t.Errorf("MaxTokens = %d, want > 0", s.MaxTokens)
	}

	// A second turn accrues a second sample with an independent iteration
	// counter (not a running total across turns).
	if _, err := cs.Turn(context.Background(), "again"); err != nil {
		t.Fatalf("Turn 2: %v", err)
	}
	samples = contextSessionEntries(t, cs)
	if len(samples) != 2 {
		t.Fatalf("got %d context samples after two turns, want 2", len(samples))
	}
	if samples[1].Iteration != 1 {
		t.Errorf("turn 2 Iteration = %d, want 1 (resets per turn)", samples[1].Iteration)
	}
}

// --- Capture (Tier 1) ------------------------------------------------------

func TestTurnArtifacts(t *testing.T) {
	t.Run("extracts edited files, commands, and the final answer", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleUser, Content: "fix the bug and test it"},
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"main.go"}`}},
				{Function: FunctionCall{Name: FunctionBash, Arguments: `{"command":"go test ./..."}`}},
			}},
			{Role: RoleTool, Content: "ok"},
			{Role: "assistant", Content: "Done — fixed and tested."},
		}
		outcome, answer := turnArtifacts(msgs)
		for _, want := range []string{"edited: main.go", "ran: go test ./..."} {
			if !strings.Contains(outcome, want) {
				t.Errorf("outcome %q missing %q", outcome, want)
			}
		}
		if answer != "Done — fixed and tested." {
			t.Errorf("answer = %q, want the final assistant message", answer)
		}
	})

	t.Run("read-only turn has empty outcome but keeps the answer", func(t *testing.T) {
		msgs := []Message{
			{Role: RoleUser, Content: "how does auth work?"},
			{Role: "assistant", Content: "It uses JWT."},
		}
		outcome, answer := turnArtifacts(msgs)
		if outcome != "" {
			t.Errorf("read-only outcome should be empty, got %q", outcome)
		}
		if answer != "It uses JWT." {
			t.Errorf("answer = %q", answer)
		}
	})

	t.Run("repeated edits to one file are de-duplicated", func(t *testing.T) {
		msgs := []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"a.go"}`}},
			}},
			{Role: "assistant", ToolCalls: []ToolCall{
				{Function: FunctionCall{Name: FunctionEditFile, Arguments: `{"path":"a.go"}`}},
			}},
		}
		outcome, _ := turnArtifacts(msgs)
		if strings.Count(outcome, "a.go") != 1 {
			t.Errorf("file should appear once, got %q", outcome)
		}
	})
}

// --- Session metrics (6a) --------------------------------------------------

func TestSessionSummary(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now().Add(-90 * time.Second)}
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.injections = 5, 52000, 8000, 9, 6
	s := cs.sessionSummary()
	for _, want := range []string{"5 turns", "52k in", "8k out", "9 captured", "6 memory injections"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
}

func TestTurnAccumulatesTokens(t *testing.T) {
	quickRetries(t)
	srv := httptest.NewServer(sseHandler(sseBody(
		`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
	)))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	if _, err := cs.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if cs.tokensIn != 12 || cs.tokensOut != 3 {
		t.Errorf("accumulated tokens = %d in / %d out, want 12/3", cs.tokensIn, cs.tokensOut)
	}
}

func TestTurnDemotesOldTurnsToOutline(t *testing.T) {
	quickRetries(t)
	var got [][]Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Messages)
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
		)))
	}))
	defer srv.Close()

	// Window 60 → demotion watermarks high=30/low=20 tokens (newWorkingSet).
	cs := &CortexSession{Window: 60, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}

	// Turn 1 is ~40 tokens (162 chars / 4): over the high watermark, but the
	// most-recent-turn invariant blocks demoting the only turn. Turn 2 doubles
	// the tail; at turn 3 start, turn 1 demotes (turn 2 stays: same invariant).
	for _, input := range []string{strings.Repeat("alpha ", 27), strings.Repeat("bravo ", 27), "charlie"} {
		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("turn %q: %v", input[:5], err)
		}
	}

	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want 3", len(got))
	}
	wire := got[2]
	if wire[0].Content != "s" {
		t.Errorf("wire[0] = %q, want the system message", wire[0].Content)
	}
	if wire[1].Role != RoleUser || !strings.HasPrefix(wire[1].Content, outlineHeader) {
		t.Errorf("wire[1] should be the outline zone, got role=%q content=%q", wire[1].Role, wire[1].Content)
	}
	if !strings.Contains(wire[1].Content, "alpha") || !strings.Contains(wire[1].Content, "t1 · user:") {
		t.Errorf("outline should carry the demoted turn 1 entry, got %q", wire[1].Content)
	}
	for i, m := range wire[2:] {
		if strings.Contains(m.Content, "alpha") {
			t.Errorf("wire[%d] still carries raw turn-1 content after demotion", i+2)
		}
	}
	hydrated := false
	for _, m := range wire[2:] {
		if strings.Contains(m.Content, "bravo") {
			hydrated = true
		}
	}
	if !hydrated {
		t.Error("turn 2 should still ride the wire verbatim")
	}
	kept := false
	for _, m := range cs.Request.Messages {
		if strings.Contains(m.Content, "alpha") {
			kept = true
		}
	}
	if !kept {
		t.Error("demotion must be wire-only: the stored log keeps turn 1 verbatim")
	}
	if cs.Request.TailFrom <= 1 {
		t.Errorf("TailFrom = %d, want > 1 after demotion", cs.Request.TailFrom)
	}
}

// The inner loop must break when the model re-issues the byte-identical
// tool-call batch, rather than spinning to maxToolIterations. The model in the
// 2026-06-14 transcript made the same grep 68 times before the cap.
func TestTurnStopsRepeatedToolCalls(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	var calls int
	body := sseBody(
		// Always ask for the same harmless allowlisted command.
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"x","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	if _, err := cs.Turn(context.Background(), "go"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	// Guard fires at maxRepeatedToolCalls identical batches, then one forced
	// finalize. This fixture keeps returning tool_calls even with tools
	// withheld, so the empty-answer salvage fires once more (still empty)
	// before giving up.
	if calls < maxRepeatedToolCalls || calls > maxRepeatedToolCalls+2 {
		t.Errorf("model called %d times, want ~%d (guard should break the loop)", calls, maxRepeatedToolCalls)
	}
	if calls >= maxToolIterations {
		t.Errorf("guard failed: ran to the iteration cap (%d)", calls)
	}
}

// TestTurnReturnsSalvagedAnswerNotStalePreToolText: round 1 answers with
// tool_calls plus throwaway prose; round 2 is a natural, unclamped empty
// finish; round 3 (salvage) supplies the real answer. Reply must be round
// 3's answer, not round 1's stale prose.
func TestTurnReturnsSalvagedAnswerNotStalePreToolText(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			// Tool call, plus throwaway prose that must NOT survive as the reply.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"I'll run this command.","tool_calls":[{"index":0,"id":"x","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		case 2:
			// Natural finish: no tool_calls, empty content, not clamped.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		default:
			// The salvage re-ask.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"Confirmed: hi was printed."}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":5}}`,
			)))
		}
	}))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	res, err := cs.Turn(context.Background(), "run echo hi")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if res.Reply != "Confirmed: hi was printed." {
		t.Errorf("Reply = %q, want the salvaged answer, not stale pre-tool-call text", res.Reply)
	}
}

func TestEmitSessionMetrics(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
	cs.StartTranscript()
	t.Cleanup(func() {
		if cs.transcript != nil {
			cs.transcript.Close()
		}
	})
	cs.turns, cs.tokensIn, cs.tokensOut, cs.captures, cs.injections, cs.injectedChars = 3, 1200, 340, 2, 1, 400

	cs.emitSessionMetrics()

	r, err := journal.NewReader(filepath.Join(contextDir(), "journal", "eval"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var got []*journal.EvalCellResultPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p, perr := journal.ParseEvalCellResult(e); perr == nil {
			got = append(got, p)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d eval.cell_result entries, want 1", len(got))
	}
	p := got[0]
	if p.Harness != "loop" || p.RunID != cs.SessionID || p.ScenarioID != "repl-session" {
		t.Errorf("identity wrong: harness=%q run=%q scenario=%q", p.Harness, p.RunID, p.ScenarioID)
	}
	if p.TokensIn != 1200 || p.TokensOut != 340 || p.AgentTurnsTotal != 3 {
		t.Errorf("metrics wrong: in=%d out=%d turns=%d", p.TokensIn, p.TokensOut, p.AgentTurnsTotal)
	}
	if p.InjectedContextTokens != 100 { // 400 chars / 4
		t.Errorf("injected tokens = %d, want 100", p.InjectedContextTokens)
	}
	if p.ContextStrategy != "none" { // memory store nil in this test
		t.Errorf("context strategy = %q, want none", p.ContextStrategy)
	}
	if !strings.Contains(p.Notes, "injections=1") || !strings.Contains(p.Notes, "captures=2") {
		t.Errorf("notes = %q", p.Notes)
	}
}

// TestEmitSessionMetricsThinkingAttribution covers item 3: the resolved
// thinking config and accumulated reasoning-token count land in the emitted
// eval.cell_result row.
func TestEmitSessionMetricsThinkingAttribution(t *testing.T) {
	tests := []struct {
		name            string
		kwargs          map[string]any
		reasoningTokens int
		wantThinking    string
	}{
		{
			name:            "thinking explicitly suppressed",
			kwargs:          map[string]any{"enable_thinking": false},
			reasoningTokens: 512,
			wantThinking:    "off",
		},
		{
			name:            "no suppression: default on",
			kwargs:          nil,
			reasoningTokens: 0,
			wantThinking:    "on",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
			cs.Request.ChatTemplateKwargs = tt.kwargs
			cs.StartTranscript()
			t.Cleanup(func() {
				if cs.transcript != nil {
					cs.transcript.Close()
				}
			})
			cs.reasoningTokens = tt.reasoningTokens

			cs.emitSessionMetrics()

			r, err := journal.NewReader(filepath.Join(contextDir(), "journal", "eval"))
			if err != nil {
				t.Fatalf("reader: %v", err)
			}
			defer r.Close()
			e, err := r.Next()
			if err != nil || e == nil {
				t.Fatalf("expected one entry, got err=%v entry=%v", err, e)
			}
			p, perr := journal.ParseEvalCellResult(e)
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			if p.Thinking != tt.wantThinking {
				t.Errorf("Thinking = %q, want %q", p.Thinking, tt.wantThinking)
			}
			if p.ReasoningTokens != tt.reasoningTokens {
				t.Errorf("ReasoningTokens = %d, want %d", p.ReasoningTokens, tt.reasoningTokens)
			}
		})
	}
}

// An unpersisted session (no SessionID) emits nothing rather than erroring.
func TestEmitSessionMetricsUnpersistedNoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request(), sessionStart: time.Now()}
	cs.emitSessionMetrics() // must not panic; SessionID == "" → skip
	if _, err := os.Stat(filepath.Join(contextDir(), "journal", "eval")); err == nil {
		t.Error("unpersisted session should not write an eval entry")
	}
}

func TestFoldOutline(t *testing.T) {
	newFoldSession := func() *CortexSession {
		cs := &CortexSession{Window: 800, Request: &AgentRequest{}} // budget = 800/8 = 100 tokens
		for i := 1; i <= 4; i++ {
			cs.outline = append(cs.outline, cache.OutlineEntry{Turn: i, User: strings.Repeat(fmt.Sprintf("entry%d ", i), 40), Citation: fmt.Sprintf("@session/s#m%d-%d", i, i+1)})
		}
		return cs
	}

	t.Run("over budget folds the oldest half", func(t *testing.T) {
		var recordedContent string
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			recordedContent = content
			return "FOLDED [@session/s#m1-2]", true, nil
		}
		defer func() { foldSummarize = orig }()

		cs := newFoldSession()
		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		// The stub digest kept entry 1's citation but dropped entry 2's; the
		// citation guard must restore the missing one.
		if !strings.HasPrefix(cs.outlineFolded, "FOLDED [@session/s#m1-2]") {
			t.Errorf("cs.outlineFolded = %q, want prefix %q", cs.outlineFolded, "FOLDED [@session/s#m1-2]")
		}
		if !strings.Contains(cs.outlineFolded, "[@session/s#m2-3]") {
			t.Errorf("cs.outlineFolded = %q, want the dropped citation [@session/s#m2-3] restored", cs.outlineFolded)
		}
		if len(cs.outline) != 2 {
			t.Errorf("len(cs.outline) = %d, want 2", len(cs.outline))
		}
		if len(cs.outline) >= 2 {
			if cs.outline[0].Turn != 3 || cs.outline[1].Turn != 4 {
				t.Errorf("remaining turns = %d, %d, want 3, 4", cs.outline[0].Turn, cs.outline[1].Turn)
			}
		}
		if !strings.Contains(recordedContent, "entry1") || !strings.Contains(recordedContent, "entry2") {
			t.Errorf("recordedContent missing entry1 or entry2")
		}
		if strings.Contains(recordedContent, "entry4") {
			t.Errorf("recordedContent should not contain entry4")
		}

		rendered := cs.renderOutlineBlock()
		if !strings.HasPrefix(rendered, outlineHeader) {
			t.Errorf("renderOutlineBlock() prefix does not match outlineHeader")
		}
		if !strings.Contains(rendered, "FOLDED") || !strings.Contains(rendered, "entry4") {
			t.Errorf("renderOutlineBlock() should contain both 'FOLDED' and 'entry4'")
		}
	})

	t.Run("under budget never calls the summarizer", func(t *testing.T) {
		called := false
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			called = true
			return "", true, nil
		}
		defer func() { foldSummarize = orig }()

		cs := &CortexSession{Window: 800, Request: &AgentRequest{}}
		cs.outline = append(cs.outline, cache.OutlineEntry{Turn: 1, User: "tiny", Citation: "@session/s#m1-2"})

		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		if called {
			t.Errorf("foldSummarize should not have been called")
		}
		if cs.outlineFolded != "" {
			t.Errorf("cs.outlineFolded = %q, want empty", cs.outlineFolded)
		}
	})

	t.Run("summarizer failure leaves the outline intact", func(t *testing.T) {
		orig := foldSummarize
		foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
			return "", false, fmt.Errorf("boom")
		}
		defer func() { foldSummarize = orig }()

		cs := newFoldSession()
		initialLen := len(cs.outline)

		ctx := context.Background()
		cs.foldOutlineIfNeeded(ctx)

		if cs.outlineFolded != "" {
			t.Errorf("cs.outlineFolded = %q, want empty", cs.outlineFolded)
		}
		if len(cs.outline) != initialLen {
			t.Errorf("len(cs.outline) = %d, want %d (unchanged)", len(cs.outline), initialLen)
		}
	})
}

// TestTurnContextGaugeUpdatesMidTurn verifies that the context gauge updates
// during tool execution, not just after model responses.
func TestTurnContextGaugeUpdatesMidTurn(t *testing.T) {
	quickRetries(t)
	// Server that returns multiple tool calls in sequence
	var calls int
	body := sseBody(
		// First response: first tool call
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo one\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":50}}`,
		// Second response: second tool call
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo two\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":600,"completion_tokens":50}}`,
		// Final response: no more tool calls
		`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":700,"completion_tokens":10}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{Window: 128000, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "system"}}}}

	// Before turn starts, LastPromptTokens should be 0
	if cs.LastPromptTokens != 0 {
		t.Errorf("before turn: LastPromptTokens = %d, want 0", cs.LastPromptTokens)
	}

	// Execute turn with tool calls
	if _, err := cs.Turn(context.Background(), "test"); err != nil {
		t.Fatalf("turn: %v", err)
	}

	// After turn, LastPromptTokens should reflect the final model response
	if cs.LastPromptTokens != 700 {
		t.Errorf("after turn: LastPromptTokens = %d, want 700 (final model response)", cs.LastPromptTokens)
	}

	// The context gauge should have updated during tool execution
	// Check that currentContextSize is being computed (it should be > 0)
	current := cs.currentContextSize()
	if current <= 0 {
		t.Errorf("currentContextSize = %d, want > 0", current)
	}

	// Verify the prompt reflects the current context. Default style is the
	// two-zone gauge (contextbar.go's gaugeZones, replacing the old exact
	// "LastPromptTokens/window" scalar) — check its "<headK>|<tailK>"
	// structure renders; exact figures are pinned precisely in
	// contextbar_test.go.
	// Zone A/divider/zone B are each colored separately (coloredGauge), so
	// an ANSI reset sits between them — strip color before matching the text.
	prompt := cs.Prompt()
	wantZones := humanK(cs.headTokens()) + zoneDivider + humanK(cs.ws.TailTokens())
	if !strings.Contains(stripANSI(prompt), wantZones) {
		t.Errorf("Prompt() = %q, expected the two-zone gauge %q", prompt, wantZones)
	}

	// repl.gauge = "blocks" still renders the fixed-spatial bracket bar.
	cs.Config = &Config{Repl: ReplConfig{Gauge: "blocks"}}
	if bar := cs.Prompt(); !strings.Contains(bar, "[") || !strings.Contains(bar, "|") || !strings.Contains(bar, "]") {
		t.Errorf("Prompt() with repl.gauge=blocks = %q, expected the bar structure ([head|tail...])", bar)
	}

	// repl.gauge = "numeric" still renders a scalar "used/window" form, now
	// off the bar's own head(zone A)+tail(zone B) figures rather than
	// LastPromptTokens — renderContextBar is a pure function of
	// head/tail/window/cells/style (contextbar.go) with no access to the
	// provider's billed LastPromptTokens, which /context's header line still
	// shows verbatim, unchanged, from real usage.
	cs.Config = &Config{Repl: ReplConfig{Gauge: "numeric"}}
	want := humanK(cs.headTokens()+cs.ws.TailTokens()) + "/" + humanK(cs.windowSize())
	if numeric := cs.Prompt(); !strings.Contains(numeric, want) {
		t.Errorf("Prompt() with repl.gauge=numeric = %q, want to contain %q", numeric, want)
	}
}
