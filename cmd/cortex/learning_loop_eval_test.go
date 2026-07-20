// learning_loop_eval_test.go is the Δ (deterministic) layer of the learning
// loop's eval (docs/learning-loop.md, gated on docs/think-dream-eval.md's
// bar unchanged): scripted-sender tests over the REAL memory store and
// REAL journal/cursor machinery, proving the mechanism's invariants hold —
// no live model, mirrors context_pivot_eval_test.go's pivotEvalBackend
// style (a small httptest server scripting canned tool-call responses).
//
//	go test ./cmd/cortex -run LearningLoop
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/events"
)

// seedCaptureTurn appends one capture.event entry via the REAL capture
// package (internal/capture, the same path session_runtime.go's captureTurn
// uses) — not a hand-rolled journal line — so the Δ layer proves the
// mechanism against the real writer, not a fixture that merely looks like
// it. cs.EnableMemory() must have been called first (wires cs.capturer).
func seedCaptureTurn(t *testing.T, cs *CortexSession, prompt, outcome string) {
	t.Helper()
	if cs.capturer == nil {
		t.Fatal("seedCaptureTurn: cs.capturer is nil — call cs.EnableMemory() first")
	}
	if err := cs.capturer.CaptureEvent(&events.Event{
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": prompt},
		ToolResult: outcome,
	}); err != nil {
		t.Fatalf("seedCaptureTurn: %v", err)
	}
}

// newLearnEvalSession builds a *CortexSession rooted at t.Chdir's temp
// project dir, with memory + capture wired (EnableMemory) and Study pointed
// at the scripted backend — everything RunLearningPass/runSubagentStats
// needs, nothing else.
func newLearnEvalSession(t *testing.T, backendURL string) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	cs := &CortexSession{quiet: true, Study: ModelSpec{Model: "learn-m", Endpoint: backendURL}}
	cs.EnableMemory()
	return cs
}

// learnScriptedBackend is a small round-numbered httptest server: respond(n)
// registers what round n replies with (a tool-call batch or a final
// content string); rounds beyond the registered set finalize with
// "NO_INSIGHT" so an unscripted tail doesn't hang the test. wireAt(n)
// returns the n'th request's message wire for assertions.
type learnScriptedBackend struct {
	srv   *httptest.Server
	calls int32
	mu    struct {
		wires [][]Message
	}
	responses map[int32]func(w http.ResponseWriter)
}

func newLearnScriptedBackend(t *testing.T) *learnScriptedBackend {
	t.Helper()
	b := &learnScriptedBackend{responses: map[int32]func(w http.ResponseWriter){}}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		b.mu.wires = append(b.mu.wires, req.Messages)
		n := atomic.AddInt32(&b.calls, 1)
		if fn, ok := b.responses[n]; ok {
			fn(w)
			return
		}
		writeFinalReply(w, "NO_INSIGHT")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *learnScriptedBackend) url() string { return b.srv.URL }

// toolCallRound scripts round n as a single tool-call batch.
func (b *learnScriptedBackend) toolCallRound(n int32, calls ...ToolCall) {
	b.responses[n] = func(w http.ResponseWriter) {
		callsJSON, _ := json.Marshal(calls)
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":%s},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, callsJSON)
	}
}

// finalRound scripts round n as a final (no-tool-call) reply.
func (b *learnScriptedBackend) finalRound(n int32, content string) {
	b.responses[n] = func(w http.ResponseWriter) { writeFinalReply(w, content) }
}

func writeFinalReply(w http.ResponseWriter, content string) {
	textJSON, _ := json.Marshal(content)
	fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, textJSON)
}

func (b *learnScriptedBackend) callCount() int32 { return atomic.LoadInt32(&b.calls) }

func (b *learnScriptedBackend) wireAt(n int) []Message {
	if n < 1 || n > len(b.mu.wires) {
		return nil
	}
	return b.mu.wires[n-1]
}

func memoryWriteCall(id, name, content string) ToolCall {
	args, _ := json.Marshal(map[string]any{"name": name, "content": content})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionMemoryWrite, Arguments: string(args)}}
}

func bashCall(id, command string) ToolCall {
	args, _ := json.Marshal(map[string]any{"command": command})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionBash, Arguments: string(args)}}
}

func writeFileCall(id, path, content string) ToolCall {
	args, _ := json.Marshal(map[string]any{"path": path, "content": content})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionWriteFile, Arguments: string(args)}}
}

// --- Δ1: scanCaptureWindow reads exactly the entries past the cursor ------

func TestLearningLoopScanCaptureWindowSinceCursor(t *testing.T) {
	cs := newLearnEvalSession(t, "")

	seedCaptureTurn(t, cs, "fix the flaky test", "edited: retry_test.go | ran: go test ./...")
	seedCaptureTurn(t, cs, "what does the retry helper do", "→ it backs off linearly between attempts")

	entries, tail, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if tail != entries[len(entries)-1].Offset {
		t.Errorf("tail = %d, want the last entry's offset %d", tail, entries[len(entries)-1].Offset)
	}
	if entries[0].Prompt != "fix the flaky test" {
		t.Errorf("entries[0].Prompt = %q, want %q", entries[0].Prompt, "fix the flaky test")
	}
	if !strings.Contains(entries[1].Result, "backs off linearly") {
		t.Errorf("entries[1].Result = %q, want it to carry the captured outcome", entries[1].Result)
	}

	// Nothing scanned yet — the cursor hasn't moved.
	cursor := journal.OpenCursor(learnCursorDir(cs))
	if off, _ := cursor.Get(); off != 0 {
		t.Errorf("cursor = %d before any pass runs, want 0 (scanning must not itself advance the cursor)", off)
	}
}

// --- Δ2/Δ3: a scripted memory_write lands as a real note, and a second     --
// pass over the same window scans nothing (the cursor advanced + the run   --
// is idempotent) -------------------------------------------------------------

func TestLearningLoopScriptedMemoryWriteLandsRealNoteAndAdvancesCursor(t *testing.T) {
	backend := newLearnScriptedBackend(t)
	cs := newLearnEvalSession(t, backend.url())
	seedCaptureTurn(t, cs, "by the way we standardized on linear backoff for retries here", "→ noted, continuing with the actual task")

	backend.toolCallRound(1, memoryWriteCall("c1", "retry-backoff-standard", "The project standardized on linear backoff for retry logic."))
	backend.finalRound(2, "saved a note about the retry-backoff standard")

	result, err := RunLearningPass(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("RunLearningPass: %v", err)
	}
	if result.EntriesScanned != 1 {
		t.Errorf("EntriesScanned = %d, want 1", result.EntriesScanned)
	}
	if result.NotesWritten != 1 {
		t.Errorf("NotesWritten = %d, want 1", result.NotesWritten)
	}

	// The note is REAL: readable straight off disk via the same store the
	// coder's own memory_read/memory_search tools use, and INDEX.md was
	// regenerated (internal/memory.Store.Write's contract).
	body, err := cs.MemoryRead("retry-backoff-standard")
	if err != nil {
		t.Fatalf("MemoryRead: %v", err)
	}
	if !strings.Contains(body, "linear backoff") {
		t.Errorf("note body = %q, want it to contain the saved fact", body)
	}
	idxPath := filepath.Join(cs.ContextDir(), "memory", "INDEX.md")
	if _, err := os.Stat(idxPath); err != nil {
		t.Errorf("INDEX.md not regenerated: %v", err)
	}

	// The cursor advanced: re-scanning the SAME journal window now finds
	// nothing.
	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow after pass: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries after the pass = %d, want 0 (cursor should have advanced past them)", len(entries))
	}

	// Re-running the whole pass over the same (now-empty) window scans
	// nothing and never calls the model again — the bounded-cost property:
	// zero new entries is cheaper than a bounded model call, not just
	// idempotent with one.
	callsBefore := backend.callCount()
	result2, err := RunLearningPass(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("RunLearningPass (re-run): %v", err)
	}
	if result2.EntriesScanned != 0 || result2.NotesWritten != 0 {
		t.Errorf("re-run result = %+v, want a zero result", result2)
	}
	if backend.callCount() != callsBefore {
		t.Errorf("re-run made %d more model round-trips, want 0 (nothing new to scan)", backend.callCount()-callsBefore)
	}
}

// --- Δ3: the NO_INSIGHT escape writes nothing but still advances the      --
// cursor — the common case must not re-scan the same window forever --------

func TestLearningLoopNoInsightWritesNothingButAdvancesCursor(t *testing.T) {
	backend := newLearnScriptedBackend(t)
	cs := newLearnEvalSession(t, backend.url())
	seedCaptureTurn(t, cs, "rename a local variable for clarity", "edited: foo.go")

	backend.finalRound(1, "NO_INSIGHT")

	result, err := RunLearningPass(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("RunLearningPass: %v", err)
	}
	if result.EntriesScanned != 1 {
		t.Errorf("EntriesScanned = %d, want 1", result.EntriesScanned)
	}
	if result.NotesWritten != 0 {
		t.Errorf("NotesWritten = %d, want 0 (NO_INSIGHT)", result.NotesWritten)
	}
	if got := result.Report(); !strings.Contains(got, "nothing worth saving") {
		t.Errorf("Report() = %q, want it to say nothing worth saving", got)
	}

	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow after NO_INSIGHT pass: %v", err)
	}
	if len(entries) != 0 {
		t.Error("cursor did not advance after a NO_INSIGHT pass — a future run would re-scan the same window forever")
	}
}

// --- Δ4/bounds: a scripted call to a forbidden tool is refused, not       --
// executed — Learn's allowlist is outline/grep/read_file/memory_read/       --
// memory_search/memory_write ONLY, enforced by the same dispatcherFor      --
// door-guard Study and Agent use ----------------------------------------

func TestLearningLoopForbiddenToolsAreRefused(t *testing.T) {
	backend := newLearnScriptedBackend(t)
	cs := newLearnEvalSession(t, backend.url())
	seedCaptureTurn(t, cs, "clean up the build directory", "ran: rm -rf build")

	// Round 1: the model tries bash (not in Learn's toolset) and write_file
	// (also not offered) in the same batch; round 2 finalizes.
	backend.toolCallRound(1, bashCall("c1", "rm -rf /"), writeFileCall("c2", "notes.md", "hi"))
	backend.finalRound(2, "NO_INSIGHT")

	result, err := RunLearningPass(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("RunLearningPass: %v", err)
	}
	if result.NotesWritten != 0 {
		t.Errorf("NotesWritten = %d, want 0", result.NotesWritten)
	}

	wire := backend.wireAt(2)
	if wire == nil {
		t.Fatal("no wire captured for round 2")
	}
	var sawBashRefusal, sawWriteRefusal bool
	for _, m := range wire {
		if m.Role != RoleTool {
			continue
		}
		lower := strings.ToLower(m.Content)
		if m.ToolCallID == "c1" && strings.Contains(lower, "not available here") {
			sawBashRefusal = true
		}
		if m.ToolCallID == "c2" && strings.Contains(lower, "not available here") {
			sawWriteRefusal = true
		}
	}
	if !sawBashRefusal {
		t.Errorf("bash call was not refused as unavailable; wire = %+v", wire)
	}
	if !sawWriteRefusal {
		t.Errorf("write_file call was not refused as unavailable; wire = %+v", wire)
	}

	// The refused bash command must never actually have run — no
	// side effect to check against beyond the refusal text itself, since
	// dispatcherFor's allowlist check short-circuits before tools.Execute is
	// ever called for a disallowed name (Learn's dispatcher, mirroring
	// Study's, per internal/tools/study.go's dispatcherFor).
}

// --- bounds hold: MaxIter caps a model that never stops calling tools ----

func TestLearningLoopIterationCapHolds(t *testing.T) {
	backend := newLearnScriptedBackend(t)
	cs := newLearnEvalSession(t, backend.url())
	seedCaptureTurn(t, cs, "investigate the slow endpoint", "ran: profiling, inconclusive")

	// Every round asks for an allowed-but-pointless outline call (budget
	// varied per round so the no-progress guard's byte-identical-batch
	// check never trips first), and never finalizes on its own — proving
	// MaxIter (not model good behavior, and not the no-progress guard) is
	// what bounds the run.
	var round int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&round, 1)
		args, _ := json.Marshal(map[string]any{"path": ".", "budget": 1000 + int(n)})
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"function","function":{"name":"outline","arguments":%s}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, mustJSON(t, string(args)))
	}))
	t.Cleanup(srv.Close)
	cs.Study.Endpoint = srv.URL

	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("setup error: expected 1 seeded entry, got %d", len(entries))
	}
	seed := learnSeed("", entries, "")

	_, stats, err := cs.runSubagentStats(context.Background(), tools.Learn, seed)
	if err != nil {
		t.Fatalf("runSubagentStats: %v", err)
	}
	bounds := cs.Config.subagentBounds(tools.Learn.Role, tools.Learn.Bounds)
	if stats.Iterations > bounds.MaxIter {
		t.Errorf("Iterations = %d, want <= MaxIter %d", stats.Iterations, bounds.MaxIter)
	}
	if stats.StopReason != "max-iter" {
		t.Errorf("StopReason = %q, want %q (a model that never stops must be bound-forced to finalize)", stats.StopReason, "max-iter")
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- a subagent-level error leaves the cursor where it was: a failed pass --
// is retried next time, not silently skipped ------------------------------

func TestLearningLoopSubagentErrorLeavesCursorUnadvanced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cs := newLearnEvalSession(t, srv.URL)
	seedCaptureTurn(t, cs, "deploy the service", "ran: deploy script, failed midway")

	if _, err := RunLearningPass(context.Background(), cs, ""); err == nil {
		t.Fatal("RunLearningPass: want an error when the backend is unreachable")
	}

	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries after a failed pass = %d, want 1 (the window must still be there to retry)", len(entries))
	}
}

// --- loop-kind wiring: RunLoopFiring branches to the Learn engine for a --
// kind:"learn" spec, same journaled-outcome contract as a coder-turn loop --

func learnKindTestSessionFactory(t *testing.T, backendURL string) sessionFactory {
	t.Helper()
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request(), Study: ModelSpec{Model: "learn-m", Endpoint: backendURL}}
		return cs
	}
}

func TestRunLoopFiringLearnKindRunsLearnPassAndJournalsReport(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := t.TempDir()

	// Seed the PROJECT's journal directly (runLearnFiring resolves
	// ContextDir off the applied workspace, not the process CWD).
	seedCS := &CortexSession{quiet: true}
	if ws, err := NewWorkspace(root); err == nil {
		seedCS.workspace = ws
	} else {
		t.Fatalf("NewWorkspace: %v", err)
	}
	seedCS.EnableMemory()
	seedCaptureTurn(t, seedCS, "we use table-driven tests here by convention", "→ acknowledged, continuing")

	backend := newLearnScriptedBackend(t)
	backend.toolCallRound(1, memoryWriteCall("c1", "test-convention", "The project uses table-driven tests by convention."))
	backend.finalRound(2, "saved the table-driven-tests convention")

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	spec := loops.Spec{Name: "nightly-learn", Project: "blog", Kind: loops.KindLearn}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, learnKindTestSessionFactory(t, backend.url())); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", got.Outcome, journal.LoopOutcomeSuccess)
	}
	if !strings.Contains(got.Reason, "1 entries scanned") && !strings.Contains(got.Reason, "1 entry") {
		// Report()'s exact plural wording ("entries" even for n=1) — assert
		// substantively rather than pin the exact string here.
		if !strings.Contains(got.Reason, "note") {
			t.Errorf("Reason = %q, want it to summarize the learn pass (entries/notes)", got.Reason)
		}
	}
	if got.ChangeRef != "" {
		t.Errorf("ChangeRef = %q, want empty — a learn firing writes memory notes, not source files, so there is nothing to land as a `cortex change`", got.ChangeRef)
	}

	// The note landed in the PROJECT's memory store.
	body, err := os.ReadFile(filepath.Join(root, ".cortex", "memory", "test-convention.md"))
	if err != nil {
		t.Fatalf("read saved note: %v", err)
	}
	if !strings.Contains(string(body), "table-driven") {
		t.Errorf("note body = %q, want it to contain the saved fact", string(body))
	}
}

func TestRunLoopFiringLearnKindUnknownProjectJournalsFailed(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	reg := &fakeRegistry{}
	spec := loops.Spec{Name: "nightly-learn", Project: "nope", Kind: loops.KindLearn}

	if err := RunLoopFiring(context.Background(), spec, reg, nil, learnKindTestSessionFactory(t, "")); err != nil {
		t.Fatalf("RunLoopFiring: %v", err)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Outcome != journal.LoopOutcomeFailed {
		t.Errorf("Outcome = %q, want %q", entries[0].Outcome, journal.LoopOutcomeFailed)
	}
	if !strings.Contains(entries[0].Reason, "nope") {
		t.Errorf("Reason = %q, want it to mention the unresolved project name", entries[0].Reason)
	}
}
