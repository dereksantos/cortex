package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

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

// TestTurnEmptyUnsalvageableReturnsEmptyNotStale: round 1 carries tool_calls
// plus throwaway prose; every later round (the natural finish, the one
// reasoning-off retry of issue #149, and the salvage re-ask) comes back
// empty. The reply must be empty — not round 1's stale pre-tool prose — and
// the stop reason must say the turn ended empty.
func TestTurnEmptyUnsalvageableReturnsEmptyNotStale(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"I'll run this command.","tool_calls":[{"index":0,"id":"x","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		}
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)))
	}))
	defer srv.Close()

	cs := &CortexSession{Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	res, err := cs.Turn(context.Background(), "run echo hi")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if res.Reply != "" {
		t.Errorf("Reply = %q, want empty (nothing salvageable), not stale pre-tool text", res.Reply)
	}
	if res.StopReason != "empty-finalize" {
		t.Errorf("StopReason = %q, want empty-finalize", res.StopReason)
	}
	if calls != 4 {
		t.Errorf("model calls = %d, want 4 (tool round, empty finish, one reasoning-off retry, one salvage)", calls)
	}
}

// TestTurnReasoningFallbackWritesReceipt: the issue #149 end-to-end receipt —
// the natural finish comes back empty (the model's reasoning consumed the
// whole completion), the one-shot reasoning-off retry recovers an answer, and
// the turn persists a recovery.reasoning_fallback entry under
// .cortex/journal/recovery/. The fake backend answers the effort-off send
// with a non-empty reply and nothing else.
func TestTurnReasoningFallbackWritesReceipt(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			// Natural finish: no tool_calls, empty content, not clamped.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		}
		// The reasoning-off retry: the answer.
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"recovered answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)))
	}))
	defer srv.Close()

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	res, err := cs.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if res.Reply != "recovered answer" {
		t.Errorf("Reply = %q, want recovered answer", res.Reply)
	}
	if calls != 2 {
		t.Errorf("model calls = %d, want 2 (empty finish, one reasoning-off retry)", calls)
	}

	r, err := journal.NewReader(filepath.Join(cs.ContextDir(), "journal", "recovery"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var p *journal.ReasoningFallbackPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p2, perr := journal.ParseReasoningFallback(e); perr == nil {
			if p != nil {
				t.Fatalf("more than one recovery.reasoning_fallback entry")
			}
			p = p2
		}
	}
	if p == nil {
		t.Fatal("no recovery.reasoning_fallback entry under .cortex/journal/recovery/")
	}
	if p.Model != "m" || p.Role != "code" || p.Path != "natural" {
		t.Errorf("receipt = %+v, want model=m role=code path=natural", p)
	}
}

// TestTurnReasoningFallbackToolRoundReceipt: the issue #149 receipt's
// tool-calls variant. The empty natural finish is recovered by the
// reasoning-off retry's tool call (dispatched like an ordinary round; the
// following round answers), so the turn persists exactly ONE
// recovery.reasoning_fallback entry — attributed "tool-round" (the receipt's
// own stop-reason attribution, distinct from the run's final stop reason),
// outcome "tool_calls". This is the case the loop-level unit test pins at
// engine level and the doc-comment in internal/journal/recovery.go names.
func TestTurnReasoningFallbackToolRoundReceipt(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			// Natural finish: empty content, no tool_calls, not clamped.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		case 2:
			// The reasoning-off retry: a tool call, not prose.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		default:
			// The round after the tool result: the answer.
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"final answer"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		}
	}))
	defer srv.Close()

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}, Tools: toolSet}}
	res, err := cs.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if res.Reply != "final answer" {
		t.Errorf("Reply = %q, want final answer (the loop continued after the retry's tool round)", res.Reply)
	}
	if calls != 3 {
		t.Errorf("model calls = %d, want 3 (empty finish, off-retry with a tool call, final round)", calls)
	}

	r, err := journal.NewReader(filepath.Join(cs.ContextDir(), "journal", "recovery"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var p *journal.ReasoningFallbackPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p2, perr := journal.ParseReasoningFallback(e); perr == nil {
			if p != nil {
				t.Fatalf("more than one recovery.reasoning_fallback entry")
			}
			p = p2
		}
	}
	if p == nil {
		t.Fatal("no recovery.reasoning_fallback entry under .cortex/journal/recovery/")
	}
	if p.Model != "m" || p.Role != "code" || p.Path != "natural" {
		t.Errorf("receipt = %+v, want model=m role=code path=natural", p)
	}
	if p.Outcome != journal.OutcomeToolCalls {
		t.Errorf("receipt outcome = %q, want %q (the retry recovered the round with a tool call)",
			p.Outcome, journal.OutcomeToolCalls)
	}
	if p.StopReason != "tool-round" {
		t.Errorf("receipt stop_reason = %q, want tool-round (the receipt's own attribution; the run ended clean-finalize)",
			p.StopReason)
	}
}

// TestTurnRecoverableErrorJournalsAndRedacts is the issue #117 end-to-end
// receipt: a mid-turn send fails AFTER progress (first round 200-with-tool-call,
// second round HTTP 500), the loop recovers (StopReason error-recovered) and
// the turn SUCCEEDS — so err is nil and the provider's 500 + body would
// otherwise vanish behind the finalize answer. The fix records them in two
// places, both secrets-redacted:
//
//   - exactly one model.recovered_error entry under .cortex/journal/model/
//     with the HTTP status (503) and a detail that carries the body's message
//     — a DISTINCT type from model.failure (the unrecovered kind the healing
//     ladder journals), so the recovered turn is never reported as "FAILED
//     unrecovered";
//   - a one-line "backend error: 503 …" on the stdlib logger (the REPL
//     diverts that to .cortex/cortex.log; headless keeps it on stderr).
//
// The fake backend echoes the request's Authorization header in the 500 body
// (the 400-rejection leak class #117 names), so the test proves the key never
// survives to the entry OR the log line.
func TestTurnRecoverableErrorJournalsAndRedacts(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	const key = "sk-or-v1-1234567890abcdef"

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// First round: 200 with a tool call (progress made — the loop
			// has gathered context, so a later failure is recoverable).
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		}
		if calls == 2 {
			// Second round (after the tool result): HTTP 500 echoing the
			// request's Authorization header in the body — the leak the
			// redaction must catch. The SSE error path (wrapServerError)
			// surfaces this as "<name> (500): server error: <body>".
			body := fmt.Sprintf(`{"error":{"message":"boom: auth %s leaked"}}`, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, body)
			return
		}
		// Third round: the tools-withheld finalize (the recovery) succeeds.
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"recovered answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)))
	}))
	defer srv.Close()

	// Capture the stdlib logger's output (the "cortex.log" surface) so the
	// test asserts the key never reaches it. Restore the real output after.
	logBuf := &strings.Builder{}
	oldLogOut := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(oldLogOut) })

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL, APIKey: key,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}, Tools: toolSet}}
	res, turnErr := cs.Turn(context.Background(), "hi")
	if turnErr != nil {
		t.Fatalf("turn should SUCCEED (recovered), not error: %v", turnErr)
	}
	if res.StopReason != "error-recovered" {
		t.Fatalf("StopReason = %q, want error-recovered (the mid-turn 500 was recovered)", res.StopReason)
	}
	if res.LastError == nil {
		t.Fatal("LastError = nil, want the recovered send's error carried on the result")
	}
	if calls != 3 {
		t.Errorf("model calls = %d, want 3 (tool-call, 500, finalize)", calls)
	}

	// Exactly one model.recovered_error entry under .cortex/journal/model/,
	// with the status and a detail carrying the body's message — but NEVER
	// the key.
	r, err := journal.NewReader(filepath.Join(cs.ContextDir(), "journal", "model"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var recovered []*journal.ModelRecoveredErrorPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p, perr := journal.ParseModelRecoveredError(e); perr == nil {
			recovered = append(recovered, p)
		}
	}
	if len(recovered) != 1 {
		t.Fatalf("got %d model.recovered_error entries, want exactly 1 (got %+v)", len(recovered), recovered)
	}
	p := recovered[0]
	if p.Status != http.StatusServiceUnavailable {
		t.Errorf("entry status = %d, want 503 (the recovered send's HTTP status)", p.Status)
	}
	// The Model is the one whose send FIRST failed ("m" — the ladder's
	// send-scoped receipt names it). The ladder rebound the request to its
	// last tried candidate before giving up, so a record written from the
	// rebound request model would read "b/coder:free", not "m".
	if p.Role != roleCode || p.Model != "m" {
		t.Errorf("entry role/model = %q/%q, want %s/m (the model whose send first failed, per the ladder receipt)", p.Role, p.Model, roleCode)
	}
	if !strings.Contains(p.Detail, "boom") {
		t.Errorf("entry detail %q does not carry the 500 body's message", p.Detail)
	}
	if strings.Contains(p.Detail, key) {
		t.Errorf("entry detail %q leaked the API key — redaction failed", p.Detail)
	}

	// The stdlib-logger surface ("cortex.log"): one "backend error: …" line
	// carrying the redacted message (which includes the streaming path's
	// "stream (503)" status prefix), key never present.
	logText := logBuf.String()
	if n := strings.Count(logText, "backend error:"); n != 1 {
		t.Errorf("log has %d \"backend error:\" lines, want exactly 1\nlog:\n%s", n, logText)
	}
	if !strings.Contains(logText, "boom") {
		t.Errorf("log line %q does not carry the body's message", logText)
	}
	if !strings.Contains(logText, "stream (503)") {
		t.Errorf("log line %q does not carry the HTTP status the streaming path bakes into the message", logText)
	}
	if strings.Contains(logText, key) {
		t.Errorf("log %q leaked the API key — redaction failed", logText)
	}
}

// TestTurnHealedRecoveryJournalsRecoveredError is the issue #117 review round
// 3 pin for the MAIN production path: an OpenRouter backend with self-heal on,
// and a healable failure class (5xx). The healing ladder walks FIRST (the
// common case) — its candidates are all served but every one fails — so the
// ladder's own receipt (the old model.failure + failureJournaled) used to be
// the only record left, and the turn's model.recovered_error entry was skipped.
// The mislabel that resulted: a turn that SUCCEEDED showing as "FAILED
// unrecovered" on `cortex model` (renderRecentModelEvents' model.failure
// branch).
//
// The fix settles exactly ONE journal record per failed send, the KIND
// following the OUTCOME: a recovered turn produces exactly one
// model.recovered_error entry and NO model.failure. The fake server returns a
// tool call (progress), then 503 on the mid-turn send AND on every
// healing-ladder candidate send (all fail, so the ladder reports the failure
// to the loop), and 200 on the tools-withheld finalize (the recovery
// succeeds). The loop recovers (error-recovered).
func TestTurnHealedRecoveryJournalsRecoveredError(t *testing.T) {
	quickRetries(t)
	// Force the blocking send path: in a non-TTY test session, cs.send would
	// stream (and report streamed=true), which makes healingSender skip the
	// ladder (a streamed partial isn't re-sent). The blocking path reports
	// streamed=false, so the 503 reaches the ladder.
	t.Setenv("CORTEX_LOOP_STREAM", "0")
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	var calls int
	// Blocking JSON responses (CORTEX_LOOP_STREAM=0 forces the blocking path):
	// the SSE shape would be ignored by the blocking transport, so the
	// tool-call and finalize rounds must return chat-completions JSON, not SSE.
	toolCallJSON := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	answerJSON := `{"choices":[{"message":{"role":"assistant","content":"recovered answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			// First round: 200 with a tool call (progress made — the loop has
			// gathered context, so a later failure is recoverable).
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, toolCallJSON)
			return
		case 2, 3, 4:
			// The mid-turn send (call 2) AND every healing-ladder candidate
			// (calls 3, 4): HTTP 503 — the healable class that makes the
			// ladder walk.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"backend down"}}`)
			return
		default:
			// The tools-withheld finalize (call 5, the recovery): 200 with the
			// answer.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, answerJSON)
			return
		}
	}))
	defer srv.Close()

	// The OpenRouter Config with self-heal on and a healList stub that
	// advertises two :free candidates (both served, both fail — so the ladder
	// walks to its cap and reports the failure to the loop).
	selfHeal := true
	cfg := &Config{Backend: Backend{Type: "openrouter"}, Network: NetworkConfig{SelfHeal: &selfHeal}}
	cs := &CortexSession{workspace: ws, Config: cfg,
		Request: &AgentRequest{Model: "m", BaseURL: srv.URL, MaxAttempts: 1,
			Messages: []Message{{Role: RoleSystem, Content: "s"}}, Tools: toolSet}}
	cs.healList = func(context.Context) ([]llm.OpenRouterModel, error) {
		return []llm.OpenRouterModel{{ID: "a/coder:free", ContextLength: 32768}, {ID: "b/coder:free", ContextLength: 16384}}, nil
	}

	// Run the turn: the mid-turn send is 503 (one attempt, MaxAttempts=1 so the
	// transport doesn't retry internally), the ladder walks (both candidates
	// also 503), the loop recovers on the finalize (200). The turn SUCCEEDS.
	res, turnErr := cs.Turn(context.Background(), "hi")
	if turnErr != nil {
		t.Fatalf("turn should SUCCEED (recovered), not error: %v", turnErr)
	}
	if res.StopReason != "error-recovered" {
		t.Fatalf("StopReason = %q, want error-recovered (the mid-turn 503 was recovered)", res.StopReason)
	}

	// The journal records under .cortex/journal/model/ are:
	//   - one model.substitution per candidate the ladder switched to (the
	//     production behavior — each switch is journaled, like preflight's
	//     startup substitutions),
	//   - exactly ONE model.recovered_error for the recovered turn.
	// No model.failure: a recovered turn must NOT be recorded as a failure.
	// The ladder walked two candidates (a/coder:free, b/coder:free), so there
	// are 2 substitutions + 1 recovered_error = 3 records.
	all := modelJournalTypes(t, cs)
	if len(all) != 3 {
		t.Fatalf("got %d journal records under .cortex/journal/model/, want 3 (2 substitution + 1 recovered_error): %v", len(all), all)
	}
	var subCount, recoveredCount, failureCount int
	for _, typ := range all {
		switch typ {
		case journal.TypeModelSubstitution:
			subCount++
		case journal.TypeModelRecoveredError:
			recoveredCount++
		case journal.TypeModelFailure:
			failureCount++
		}
	}
	if subCount != 2 {
		t.Errorf("got %d model.substitution records, want 2 (one per ladder candidate): %v", subCount, all)
	}
	if recoveredCount != 1 {
		t.Errorf("got %d model.recovered_error records, want 1: %v", recoveredCount, all)
	}
	if failureCount != 0 {
		t.Errorf("got %d model.failure records, want 0 (a recovered turn must NOT be recorded as model.failure): %v", failureCount, all)
	}
}

// TestTurnHealedStudyFailureDoesNotSuppressCoderRecovery is the issue #117
// review round 3 pin for the suppression scope: a STUDY subagent's own
// healed-then-failed send (the study subagent goes through healingSender, so
// it walks the ladder too) must NOT suppress the coder's own later recovered
// error in the same turn. The old session-wide failureJournaled flag let any
// healingSender in the turn (including the study subagent's) set it, which hid
// the coder's own recovered error — the silent loss #117 is about.
//
// The receipt now rides the send-scoped marker on the error (heal.go's
// pendingFailure, via healJournaledError), so a subagent's healed-then-failed
// send carries its own marker and can't clobber the coder's. The test drives
// the coder turn with a study tool call (the production path through the
// tool dispatcher → RunSubagent → healingSender); the study subagent's send
// 503s (its ladder walks, fails, study errors), and the coder's own later
// send also 503s (its ladder walks, fails, loop recovers). The coder's
// recovered error must still be recorded.
func TestTurnHealedStudyFailureDoesNotSuppressCoderRecovery(t *testing.T) {
	quickRetries(t)
	// Force the blocking send path (same reason as
	// TestTurnHealedRecoveryJournalsRecoveredError): a streamed partial skips
	// the ladder, so the 503 must reach it via the blocking path.
	t.Setenv("CORTEX_LOOP_STREAM", "0")
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	var calls int
	// Blocking JSON responses (CORTEX_LOOP_STREAM=0 forces the blocking path):
	// the SSE shape would be ignored by the blocking transport, so the
	// tool-call and finalize rounds must return chat-completions JSON, not SSE.
	studyCallJSON := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"study","arguments":"{\"path\":\".\",\"goal\":\"x\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	answerJSON := `{"choices":[{"message":{"role":"assistant","content":"recovered answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	// The call sequence (MaxAttempts=1 for the coder; MaxSendAttempts=1 for the
	// study role — both ensure no internal retries):
	//   1: coder round 1 → 200, study tool call
	//   2: study subagent send → 503 (study's own failure)
	//   3: study ladder candidate (s/coder:free) → 503 (session-dead)
	//   4: study ladder candidate (a/coder:free) → 503 (session-dead)
	//   5: study ladder candidate (t/coder:free) → 503 (session-dead; the
	//      study's ladder walks healMaxCandidates=3, then errors)
	//   6: coder round 2 mid-turn send → 503 (the coder's own failure; its
	//      ladder skips the three session-dead picks)
	//   7: coder ladder candidate (b/coder:free) → 503 (ladder reports failure
	//      to the loop)
	//   8: tools-withheld finalize → 200 (the recovery)
	// The ladders share the session deadModels map: the study's walk
	// (s, a, t) poisons s/a/t, so the coder's walk takes only b/coder:free —
	// nextHealCandidate's discovery heuristic (coder-named :free, then largest
	// context) over the alive candidates. The distinct model names (study-m
	// vs m) only prevent the study's rebind from clobbering the coder's
	// Request.Model.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			fmt.Fprint(w, studyCallJSON)
			return
		}
		if calls == 8 {
			fmt.Fprint(w, answerJSON)
			return
		}
		// Calls 2-7 (study subagent, its ladder, coder round 2, coder's ladder):
		// all 503.
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"backend down"}}`)
	}))
	defer srv.Close()

	selfHeal := true
	cfg := &Config{Backend: Backend{Type: "openrouter"}, Network: NetworkConfig{SelfHeal: &selfHeal}}
	cs := &CortexSession{workspace: ws, Config: cfg,
		Request: &AgentRequest{Model: "m", BaseURL: srv.URL, MaxAttempts: 1,
			Messages: []Message{{Role: RoleSystem, Content: "s"}}, Tools: toolSet},
		Study: ModelSpec{Model: "study-m", Endpoint: srv.URL, MaxSendAttempts: 1}}
	cs.healList = func(context.Context) ([]llm.OpenRouterModel, error) {
		// All four ids are :free with "coder" in the name, so the curated
		// ladder never matches and nextHealCandidate always falls through to
		// the discovery heuristic: coder-named :free, then largest context —
		// s (32768) beats a (32768, later in the sort) and both beat t
		// (16384), leaving b (16384) last. The study's walk consumes s, a, t
		// (they become session-dead), so the coder's walk finds only b left.
		return []llm.OpenRouterModel{
			{ID: "s/coder:free", ContextLength: 32768},
			{ID: "t/coder:free", ContextLength: 16384},
			{ID: "a/coder:free", ContextLength: 32768},
			{ID: "b/coder:free", ContextLength: 16384},
		}, nil
	}

	// Run the coder turn: round 1 returns a study tool call (the loop dispatches
	// it — the study subagent's send 503s, the ladder walks, fails, study
	// errors), round 2's send is 503 (the coder's own failure — the ladder
	// finds both candidates session-dead, reports the failure to the loop), and
	// the tools-withheld finalize is 200 (the recovery succeeds). The turn
	// SUCCEEDS with error-recovered.
	res, turnErr := cs.Turn(context.Background(), "hi")
	if turnErr != nil {
		t.Fatalf("turn should SUCCEED (recovered), not error: %v", turnErr)
	}
	if res.StopReason != "error-recovered" {
		t.Fatalf("StopReason = %q, want error-recovered (the coder's mid-turn 503 was recovered)", res.StopReason)
	}

	// The coder's own recovered error must still be recorded — a
	// model.recovered_error entry with role=code. The study subagent's
	// healed-then-failed send (which also went through healingSender) must NOT
	// have suppressed it (the old session-wide flag's bug).
	recovered := modelRecoveredErrorPayloads(t, cs)
	if len(recovered) < 1 {
		t.Fatalf("got %d model.recovered_error entries, want at least 1 (the coder's own recovered error was suppressed by the study subagent's healed failure)", len(recovered))
	}
	var foundCode bool
	for _, p := range recovered {
		if p.Role == roleCode {
			foundCode = true
			break
		}
	}
	if !foundCode {
		t.Errorf("no model.recovered_error entry with role=%s — the coder's own recovered error is missing: %+v", roleCode, recovered)
	}
	// The coder's entry must carry the model whose send FIRST failed ("m",
	// per the ladder's send-scoped receipt) — not "m" rebound to the last
	// candidate tried (b/coder:free), which the request model would show.
	for _, p := range recovered {
		if p.Role == roleCode && p.Model != "m" {
			t.Errorf("coder model.recovered_error entry model = %q, want m (the model whose send first failed): %+v", p.Model, p)
		}
	}
}

// TestTurnHealedExhaustionJournalsUnrecoveredFailure is the issue #117 review
// round pin for the UNRECOVERED side of the one-record-per-failed-send rule
// (the half that moved from heal.go into turn.go when the ladder stopped
// journaling model.failure on the fly): a FIRST-round send failure (no
// progress yet) with the self-heal ladder exhausted makes runLoop return the
// error — and exactly ONE journal record must come out of it: one
// model.failure, zero model.recovered_error. The send-scoped receipt on the
// error (healJournaledError) is what settles that record here; with nothing
// recovered, the recovery record would be the wrong kind (and a double
// record would be the old session-wide-flag bug resurfacing).
func TestTurnHealedExhaustionJournalsUnrecoveredFailure(t *testing.T) {
	quickRetries(t)
	// Force the blocking send path (same reason as
	// TestTurnHealedRecoveryJournalsRecoveredError): a streamed partial skips
	// the ladder, so the 503 must reach it via the blocking path.
	t.Setenv("CORTEX_LOOP_STREAM", "0")
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	var calls int
	// Every request 503s starting with the first coder send: no progress is
	// ever made, so the loop cannot finalize from what it has — the first-
	// round failure path returns the error straight to the caller.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"backend down"}}`)
	}))
	defer srv.Close()

	selfHeal := true
	cfg := &Config{Backend: Backend{Type: "openrouter"}, Network: NetworkConfig{SelfHeal: &selfHeal}}
	cs := &CortexSession{workspace: ws, Config: cfg,
		Request: &AgentRequest{Model: "m", BaseURL: srv.URL, MaxAttempts: 1,
			Messages: []Message{{Role: RoleSystem, Content: "s"}}, Tools: toolSet}}
	cs.healList = func(context.Context) ([]llm.OpenRouterModel, error) {
		return []llm.OpenRouterModel{{ID: "a/coder:free", ContextLength: 32768}, {ID: "b/coder:free", ContextLength: 16384}}, nil
	}

	// First-round send: 503; the ladder walks a then b (both 503, both
	// session-dead); runLoop returns the unrecovered error.
	_, turnErr := cs.Turn(context.Background(), "hi")
	if turnErr == nil {
		t.Fatalf("turn should FAIL (unrecovered: first-round 503 with the ladder exhausted)")
	}

	// The model journal records are: one model.substitution per candidate the
	// ladder switched to (production behavior), and exactly ONE
	// model.failure — settled here in turn from the send-scoped receipt.
	// Zero model.recovered_error: nothing recovered.
	all := modelJournalTypes(t, cs)
	var subCount, recoveredCount, failureCount int
	for _, typ := range all {
		switch typ {
		case journal.TypeModelSubstitution:
			subCount++
		case journal.TypeModelRecoveredError:
			recoveredCount++
		case journal.TypeModelFailure:
			failureCount++
		}
	}
	if subCount != 2 {
		t.Errorf("got %d model.substitution records, want 2 (one per ladder candidate): %v", subCount, all)
	}
	if recoveredCount != 0 {
		t.Errorf("got %d model.recovered_error records, want 0 (nothing recovered): %v", recoveredCount, all)
	}
	if failureCount != 1 {
		t.Errorf("got %d model.failure records, want exactly 1 (the unrecovered first-round send): %v", failureCount, all)
	}
}

// TestErrorTurnStillAdvancesTurnCounter pins the turn bookkeeping against a
// regression (issue #117 review): the #117 change once moved cs.turns++ and
// the token/cost accounting BELOW the err-return, so an errored turn (a
// cancelled ctx, an unrecovered 503) would not advance the turn counter —
// its messages are already stamped cs.turns+1 and added to cs.ws as their own
// span, so the NEXT turn would reuse the same stamp and replayWorkingSet would
// merge the two turns on resume (snapshot restore failing), and the tokens
// and cost the provider actually billed for the failed turn would be dropped
// from the session totals. The counter and totals must settle for EVERY turn,
// errored or not.
func TestErrorTurnStillAdvancesTurnCounter(t *testing.T) {
	t.Chdir(t.TempDir())
	backend := newContextEvalBackend(t) // usage: 10 prompt / 3 completion per reply
	cs := newContextEvalSession(t, backend, 4000)

	// The first turn's send is canceled (ctx canceled) — the interrupted
	// path: runLoop returns the error without progress. The second turn
	// succeeds.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // turn 1's send is canceled before it happens
	if _, err := cs.Turn(ctx, "one"); err == nil {
		t.Fatalf("turn 1 should fail (canceled ctx)")
	}

	if _, err := cs.Turn(context.Background(), "two"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	if cs.turns != 2 {
		t.Fatalf("cs.turns = %d, want 2 (an errored turn still advances the counter — its messages are stamped cs.turns+1 and spanned in cs.ws, so a collision on the next turn would merge the two on resume)", cs.turns)
	}
	if cs.tokensIn != 10 {
		t.Errorf("cs.tokensIn = %d, want 10 (turn 2's billed usage — an errored turn that bills tokens must count them here too)", cs.tokensIn)
	}
	if cs.tokensOut != 3 {
		t.Errorf("cs.tokensOut = %d, want 3", cs.tokensOut)
	}
	// The session state snapshot (resume) must agree with the live session:
	// the replayed working set has one span per turn, so 2 turns — including
	// the canceled one's span — and the turn counter matches it.
	if cs.ws == nil {
		t.Fatalf("cs.ws is nil")
	}
	if got := cs.ws.TotalTurns(); got != 2 {
		t.Errorf("cs.ws.TotalTurns() = %d, want 2 (the canceled turn's span is in the working set, so the counter must agree)", got)
	}
}

// modelJournalTypes reads every entry under .cortex/journal/model/ and returns
// their types (so a test can assert the EXACT set of record kinds a turn
// produced — e.g. one model.recovered_error and no model.failure).
func modelJournalTypes(t *testing.T, cs *CortexSession) []string {
	t.Helper()
	r, err := journal.NewReader(filepath.Join(cs.ContextDir(), "journal", "model"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var got []string
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		got = append(got, e.Type)
	}
	return got
}

// modelRecoveredErrorPayloads reads every model.recovered_error entry under
// .cortex/journal/model/ and returns their parsed payloads (empty slice when
// there are none).
func modelRecoveredErrorPayloads(t *testing.T, cs *CortexSession) []*journal.ModelRecoveredErrorPayload {
	t.Helper()
	r, err := journal.NewReader(filepath.Join(cs.ContextDir(), "journal", "model"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	var got []*journal.ModelRecoveredErrorPayload
	for {
		e, err := r.Next()
		if e == nil || err != nil {
			break
		}
		if p, perr := journal.ParseModelRecoveredError(e); perr == nil {
			got = append(got, p)
		}
	}
	return got
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

// TestStartStopActivitySetsPhase pins startActivity's phase side effect: a
// running tool is busy time and should light the same "thinking" glyph as
// reasoning, without needing a live anchor attached (nil cs.live must not
// panic — the common case in headless/test sessions).
func TestStartStopActivitySetsPhase(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	if cs.phase != phaseIdle {
		t.Fatalf("setup: phase = %v, want phaseIdle", cs.phase)
	}
	cs.startActivity("bash(echo hi)")
	if cs.phase != phaseThinking {
		t.Errorf("phase after startActivity = %v, want phaseThinking", cs.phase)
	}
	// stopActivity only clears the anchor's status-row label (SetActivity(""))
	// — it does not itself change the phase (the next model round-trip or the
	// turn's own end-of-turn defer does that), so the phase should still read
	// busy right after a tool call ends.
	cs.stopActivity()
	if cs.phase != phaseThinking {
		t.Errorf("phase after stopActivity = %v, want phaseThinking (unchanged)", cs.phase)
	}
}

// TestTurnPhaseIdleAfterCompletion guards turn()'s own phase bookkeeping: it
// should enter phaseThinking at the start and — via its deferred setPhase —
// land back on phaseIdle once the turn (including a mid-turn tool call) fully
// resolves, even though nothing observes the phase while live.
func TestTurnPhaseIdleAfterCompletion(t *testing.T) {
	quickRetries(t)
	body := sseBody(
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo one\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":50}}`,
		`{"choices":[{"delta":{"role":"assistant","content":"done"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":600,"completion_tokens":10}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	cs := &CortexSession{Window: 128000, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "system"}}}}

	if _, err := cs.Turn(context.Background(), "test"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if cs.phase != phaseIdle {
		t.Errorf("phase after Turn() = %v, want phaseIdle", cs.phase)
	}
}

// fallbackTranscriptEntries re-reads the session JSONL on disk and returns the
// raw entries whose kind matches want ("" for the default message entries).
func fallbackTranscriptEntries(t *testing.T, cs *CortexSession, want string) []sessionEntry {
	t.Helper()
	path := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	var out []sessionEntry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		if e.Kind == want {
			out = append(out, e)
		}
	}
	return out
}

// TestTurnReasoningFallbackKeepsTranscriptConsistent: the issue #149
// off-retry recovers an empty finish with a tool call. The dropped empty
// assistant message must NOT outlive the retry in the resumable session
// log: reload with loadSession and assert there is no "assistant, then
// assistant" shape (and no mid-conversation system note) — the transcript is
// consistent with the wire conversation (assistant(tool_calls) → tool →
// assistant).
func TestTurnReasoningFallbackKeepsTranscriptConsistent(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			// Natural finish: empty, no tool calls (reasoning consumed the turn).
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		case 2:
			// The reasoning-off retry: a tool call (the work the deliberation hid).
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		default:
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":"recovered answer"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
		}
	}))
	defer srv.Close()

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript")
	}
	t.Cleanup(func() { cs.transcript.Close() })
	if _, err := cs.Turn(context.Background(), "fix the build"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// Reload the resumable log exactly as `cortex resume` does.
	msgs, _, _, err := loadSession(filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl"))
	if err != nil {
		t.Fatalf("loadSession: %v", err)
	}
	// No mid-conversation system message (the transcript note is kindNote,
	// which loadSession skips).
	for i, m := range msgs {
		if m.Role == RoleSystem && i != 0 {
			t.Errorf("loaded message %d is a non-leading system message: %+v (the note must not be resumable)", i, m)
		}
	}
	// No "assistant followed by assistant" shape, and no empty assistant at
	// all — the dropped empty finish did not survive in the log.
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Content) == "" && len(m.ToolCalls) == 0 {
			t.Errorf("loaded message %d is an empty assistant with no tool calls: the dropped empty finish survived in the log", i)
		}
		if i > 0 && msgs[i-1].Role == "assistant" {
			t.Errorf("loaded message %d is an assistant message directly after another assistant: the transcript is not consistent with the wire conversation", i)
		}
	}
}

// TestTurnReasoningFallbackNoteIsNotResumable: the issue #149 transcript note
// must be written under a distinct kindNote entry so `cortex resume`
// (loadSession) never loads it back as a message. It is visible in the JSONL
// (human-readable) but absent from the resumed wire conversation.
func TestTurnReasoningFallbackNoteIsNotResumable(t *testing.T) {
	quickRetries(t)
	root := t.TempDir()
	t.Chdir(root)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Write([]byte(sseBody(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			)))
			return
		}
		w.Write([]byte(sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"recovered answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)))
	}))
	defer srv.Close()

	cs := &CortexSession{workspace: ws, Request: &AgentRequest{Model: "m", BaseURL: srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript did not open a transcript")
	}
	t.Cleanup(func() { cs.transcript.Close() })
	if _, err := cs.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// The note is present in the JSONL under kindNote (human-readable).
	notes := fallbackTranscriptEntries(t, cs, kindNote)
	if len(notes) != 1 {
		t.Fatalf("got %d kindNote entries, want 1 (the fallback transcript note)", len(notes))
	}
	if !strings.Contains(notes[0].Content, "re-sent once with reasoning disabled") {
		t.Errorf("kindNote content = %q, want the reasoning-fallback note", notes[0].Content)
	}

	// And it is NOT loaded back as a message on resume.
	msgs, _, _, err := loadSession(filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl"))
	if err != nil {
		t.Fatalf("loadSession: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "re-sent once with reasoning disabled") {
			t.Errorf("loadSession returned the transcript note as a message: %+v", m)
		}
	}
}
