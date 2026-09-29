// plan_mode_test.go — issue #150: plan-then-execute step mode.
//
// parsePlan is pure (no session), so it is tested table-driven directly.
// TurnWithPlan is exercised end-to-end against a scripted backend that
// distinguishes the tools-less PLANNING turn from the tools-present STEP
// turns (a quiet session sends cs.Request, whose Tools field is nil only
// during the planning turn) and replays a different reply per turn — the
// same hand-built-CortexSession-plus-httptest pattern as turnTestBackend
// (serve_turn_test.go), not the low-level runLoop Sender seam.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parsePlan
// ---------------------------------------------------------------------------

func TestParsePlan(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  []string // nil when the reply is not a parseable 2+ step plan
	}{
		{
			name:  "three steps, exact shape",
			reply: "1. scaffold the module\n2. wire it into main\n3. add tests\n",
			want:  []string{"scaffold the module", "wire it into main", "add tests"},
		},
		{
			name:  "two steps is the floor",
			reply: "1. first\n2. second\n",
			want:  []string{"first", "second"},
		},
		{
			name:  "six steps is the cap, extras truncated",
			reply: "1. a\n2. b\n3. c\n4. d\n5. e\n6. f\n7. g\n8. h\n",
			want:  []string{"a", "b", "c", "d", "e", "f"},
		},
		{
			name:  "prose preamble and trailing summary are ignored",
			reply: "Here is the plan:\n1. do the thing\n2. then more\n\nThat covers it.\n",
			want:  []string{"do the thing", "then more"},
		},
		{
			name:  "blank lines between steps are skipped",
			reply: "1. first\n\n2. second\n",
			want:  []string{"first", "second"},
		},
		{
			name:  "single step falls back to nil",
			reply: "1. only one step\n",
			want:  nil,
		},
		{
			name:  "prose with no numbered list falls back to nil",
			reply: "I will first read the file, then edit it, then run the tests.\n",
			want:  nil,
		},
		{
			name:  "markdown list (no space after dot) is not a plan",
			reply: "1.first\n2.second\n",
			want:  nil,
		},
		{
			name:  "single-line answer (no numbered list) falls back to nil",
			reply: "scaffold the module, wire it into main, then add tests.\n",
			want:  nil,
		},
		{
			name: "full-width (non-ASCII) list markers are rejected, not parsed",
			// parsePlan's contract is ASCII "N. step" lines (the planning
			// instruction forces exactly that shape). A full-width-digit
			// marker like "１." — the common non-ASCII variant — does not match
			// the regex, so a plan written that way parses to 0 steps and the
			// caller falls back to a single plain turn rather than mis-splitting
			// on a marker it was never asked to understand.
			reply: "１. scaffold the module\n２. wire it into main\n３. add tests\n",
			want:  nil,
		},
		{
			name:  "indented sub-bullets are not counted",
			reply: "1. outer\n   - sub detail\n2. second\n",
			want:  []string{"outer", "second"},
		},
		{
			name:  "empty reply falls back to nil",
			reply: "",
			want:  nil,
		},
		{
			name:  "whitespace-only reply falls back to nil",
			reply: "   \n  \n  ",
			want:  nil,
		},
		{
			name:  "step text starting with a digit or dot is preserved",
			reply: "1. 3 new endpoints\n2. .gitignore update\n",
			want:  []string{"3 new endpoints", ".gitignore update"},
		},
		{
			name:  "step text starting with multiple digits is preserved",
			reply: "1. 404 handler\n2. 2x2 matrix\n",
			want:  []string{"404 handler", "2x2 matrix"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlan(tt.reply)
			if len(got) != len(tt.want) {
				t.Fatalf("parsePlan(%q) = %v (len %d), want len %d", tt.reply, got, len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parsePlan(%q)[%d] = %q, want %q", tt.reply, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParsePlanCapIsSix(t *testing.T) {
	var reply strings.Builder
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&reply, "%d. step %d\n", i, i)
	}
	got := parsePlan(reply.String())
	if len(got) != planStepCap {
		t.Fatalf("parsePlan truncated %d steps to %d, want cap %d", 9, len(got), planStepCap)
	}
}

// ---------------------------------------------------------------------------
// planTestBackend — a scripted backend that replays one reply per turn and
// records each request's tool count, so a test can tell the tools-less
// planning turn from the tools-present step turns.
// ---------------------------------------------------------------------------

type planTestBackend struct {
	mu        sync.Mutex
	replies   []string // reply[i] is the assistant content for the i-th request
	count     int
	haveTools []bool
	toolCount []int    // toolCount[i] is the number of tools the i-th request advertised
	lastUser  []string // lastUser[i] is the last user message in the i-th request
	// cancelAt, when set, invokes cancelCtx on the request with index cancelAt
	// (0-based, in arrival order) — the way a test simulates a user pressing
	// Ctrl-C mid-turn without racing a watcher goroutine. cancelCtx is the
	// cancel func for the context the test passes to TurnWithPlan; the backend
	// holds it so it can fire from the handler.
	cancelAt  int
	cancelCtx context.CancelFunc
	srv       *httptest.Server
}

func newPlanTestBackend(t *testing.T, replies ...string) *planTestBackend {
	t.Helper()
	b := &planTestBackend{replies: replies}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		tools, _ := req["tools"].([]any)
		var lastUserContent string
		if msgs, ok := req["messages"].([]any); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok && mm["role"] == "user" {
					if content, ok := mm["content"].(string); ok {
						lastUserContent = content
					}
				}
			}
		}
		b.mu.Lock()
		idx := b.count
		b.count++
		b.haveTools = append(b.haveTools, len(tools) > 0)
		b.toolCount = append(b.toolCount, len(tools))
		b.lastUser = append(b.lastUser, lastUserContent)
		b.mu.Unlock()

		// Simulate a user pressing Ctrl-C mid-turn: cancel the run's context on
		// the Nth request (0-based). The in-flight Send then observes
		// ctx.Err() == context.Canceled and returns it, exactly as a real
		// interrupt would.
		if b.cancelAt == idx && b.cancelCtx != nil {
			b.cancelCtx()
		}

		reply := ""
		if idx < len(b.replies) {
			reply = b.replies[idx]
		}
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, reply)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// requests returns the recorded tool-flags in request order: false for the
// tools-less planning turn, true for each step turn.
func (b *planTestBackend) requests() []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bool(nil), b.haveTools...)
}

func (b *planTestBackend) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// toolCounts returns the number of tools each request advertised, in order.
// A test uses this to prove the step turns carry the session's OWN filtered
// tool list (a strict subset here, so the full registry can't sneak in as a
// restore fallback).
func (b *planTestBackend) toolCounts() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.toolCount...)
}

// lastUserMessages returns each request's last user message, in order —
// the test's way of proving the step prompts carry the original task.
func (b *planTestBackend) lastUserMessages() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lastUser...)
}

// stepCount is the number of tools-present (STEP) requests — the planning
// turn is tools-less, so requests with tools are exactly the per-step
// turns. A test proves "later steps were never sent" by checking this stays
// below the planned step count after a failure.
func (b *planTestBackend) stepCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, hasTools := range b.haveTools {
		if hasTools {
			n++
		}
	}
	return n
}

// planTestSession builds a quiet, hand-built *CortexSession pointed at
// backend — the turnTestSessionFactory shape (serve_turn_test.go) minus the
// SessionManager, with a workspace rooted at root so runProjectCheck has a
// deterministic directory (no go.mod → the check is skipped, no execution).
// Its tool list is a strict SUBSET of toolSet: if TurnWithPlan ever restored
// the full registry (the #150 review bug) instead of the session's own list,
// the step requests would advertise all of toolSet and the per-request count
// checks below would fail.
func planTestSession(t *testing.T, b *planTestBackend, root string) *CortexSession {
	t.Helper()
	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = b.srv.URL
	cs.Request.Tools = toolSet[:2] // strict subset — see the note above
	// One attempt, no backoff: a test that injects a failure or cancels the
	// context (the StopOnFailure and CancelledContext tests) must see the
	// error surface IMMEDIATELY, not after the default 3-attempt × 500 ms
	// retry loop. Production uses the ModelSpec-driven defaults.
	cs.Request.MaxAttempts = 1
	cs.Request.Backoff = time.Millisecond
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace(%q): %v", root, err)
	}
	cs.workspace = ws
	cs.deleteRoot = root
	return cs
}

// ---------------------------------------------------------------------------
// TurnWithPlan — end-to-end
// ---------------------------------------------------------------------------

func TestTurnWithPlanHappyPath(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. scaffold the module\n2. wire it into main\n3. add tests\n", // planning turn (tools withheld)
		"step one done",   // step 1 turn
		"step two done",   // step 2 turn
		"step three done", // step 3 turn
	)
	cs := planTestSession(t, backend, root)

	res, err := cs.TurnWithPlan(context.Background(), "build a feature")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true (the planning turn produced a 3-step list)")
	}
	// One planning turn + three step turns = four requests; the FIRST is the
	// tools-less planning turn, the rest carry tools. Exactly N (3) step
	// turns ran — one per planned step — plus the one planning turn.
	if flags := backend.requests(); len(flags) != 4 || flags[0] || !flags[1] || !flags[2] || !flags[3] {
		t.Fatalf("request tool-flags = %v, want [false true true true]", flags)
	}
	// The session was seeded with a strict 2-tool subset. Every STEP request
	// must advertise exactly those 2 — not the full registry (the #150
	// restore bug) and not zero. This fails on the code that restored
	// toolSet instead of the session's own filtered list.
	counts := backend.toolCounts()
	if len(counts) != 4 {
		t.Fatalf("tool counts = %v, want 4 requests", counts)
	}
	if counts[0] != 0 {
		t.Errorf("planning turn advertised %d tools, want 0", counts[0])
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] != 2 {
			t.Errorf("step request %d advertised %d tools, want exactly 2 (the session's own list)", i, counts[i])
		}
	}
	// The session's own list must be intact when TurnWithPlan returns — the
	// later REPL turns reuse it.
	if got := len(cs.Request.Tools); got != 2 {
		t.Errorf("len(cs.Request.Tools) after TurnWithPlan = %d, want 2 (the session's own list)", got)
	}
	if n := backend.stepCount(); n != 3 {
		t.Fatalf("step sends = %d, want 3 (one per planned step)", n)
	}
	// Every step prompt must carry the ORIGINAL task, not just the step line:
	// demotion at turn boundaries can fold the planning turn (the only other
	// place the task text lived) into the outline (#94's failure mode).
	task := "build a feature"
	users := backend.lastUserMessages()
	if len(users) != 4 {
		t.Fatalf("recorded user messages = %d, want 4", len(users))
	}
	// The planning turn's prompt is the full instruction plus the task, so
	// check it *contains* the task rather than equaling it.
	if !strings.Contains(users[0], task) {
		t.Errorf("planning turn's prompt does not contain the task %q:\n%s", task, users[0])
	}
	for i := 1; i < len(users); i++ {
		if !strings.Contains(users[i], task) {
			t.Errorf("step %d prompt %q does not contain the original task %q", i, users[i], task)
		}
	}
	if len(res.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Status != stepDone {
			t.Errorf("step %d status = %v, want done", i+1, s.Status)
		}
	}
	for _, want := range []string{"1. [done]", "2. [done]", "3. [done]", "3/3 steps done"} {
		if !strings.Contains(res.Reply, want) {
			t.Errorf("reply missing %q:\n%s", want, res.Reply)
		}
	}
	// No command is discoverable in the empty temp root, so each done step's
	// note is the short skip summary — NOT a wall of raw test output (the
	// #150 review: a done step's report line must stay a single status line).
	for i, s := range res.Steps {
		if !strings.HasPrefix(s.Note, "check skipped:") {
			t.Errorf("step %d note = %q, want a short 'check skipped: …' summary, not raw output", i+1, s.Note)
		}
	}
}

func TestTurnWithPlanUnparseableFallsBackToSingleTurn(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"I will read the file, edit it, and run the tests. Done.", // planning turn (prose, not a list)
		"did the whole thing", // fallback single turn
	)
	cs := planTestSession(t, backend, root)

	res, err := cs.TurnWithPlan(context.Background(), "do the whole task")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if res.Planned {
		t.Fatal("Planned = true, want false (the planning reply was not a step list)")
	}
	if len(res.Steps) != 0 {
		t.Errorf("Steps = %v, want none for the fallback", res.Steps)
	}
	// Exactly two sends total: the tools-less planning turn and ONE fallback
	// single turn (tools present). No per-step turns ran — the unparseable
	// reply collapsed to a single plain turn, not a stepped plan.
	flags := backend.requests()
	if len(flags) != 2 || flags[0] || !flags[1] {
		t.Fatalf("request tool-flags = %v, want exactly [false true] (one planning send + one fallback send)", flags)
	}
	// The fallback reply is returned unaltered — TurnWithPlan did not wrap it
	// in a plan report (that only exists for a real stepped plan).
	if res.Reply != "did the whole thing" {
		t.Errorf("Reply = %q, want the fallback turn's unaltered reply", res.Reply)
	}
}

func TestTurnWithPlanStopOnFailure(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. first step\n2. second step\n3. third step\n", // planning turn
		"first done",    // step 1 (ok)
		"second broken", // step 2 — we force its check to fail
		// step 3 must never be requested.
	)
	cs := planTestSession(t, backend, root)

	// Force step 2's post-step check to fail (and step 1's to pass) by
	// swapping the session's check behavior per call. runProjectCheck is
	// called ONCE as a baseline before step 1 (call 1), then once after each
	// completed step (calls 2, 3, …). The baseline must PASS (a clean
	// baseline keeps the gate armed); call 2 = step 1's post-step check →
	// pass; call 3 = step 2's post-step check → fail. We override by stubbing
	// runProjectCheck via a package var.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		calls++
		switch calls {
		case 1:
			return "go test ./...", "ok", true, "ok" // baseline: clean, gate stays armed
		case 2:
			return "go test ./...", "ok", true, "ok" // step 1 post-step check: pass
		case 3:
			return "go test ./...", "boom", false, "check failed: boom" // step 2 post-step check: fail → stops
		}
		// Step 3: reached only if the run continued past step 2's failure (a bug).
		return "go test ./...", "ok", true, "ok"
	}

	res, err := cs.TurnWithPlan(context.Background(), "multi-part task")
	// A step's check failure now SURFACES as an error (the report tells the
	// reader where it stopped, and the error keeps a headless driver's exit
	// code non-zero) — the report is still returned alongside it.
	if err == nil {
		t.Fatal("TurnWithPlan returned nil error, want a non-nil error (step 2's check failed)")
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(res.Steps))
	}
	want := []stepStatus{stepDone, stepFailed, stepNotReached}
	for i, s := range res.Steps {
		if s.Status != want[i] {
			t.Errorf("step %d status = %v, want %v", i+1, s.Status, want[i])
		}
	}
	// Step 2 failed → its check note is surfaced; step 3 was never run.
	if !strings.Contains(res.Steps[1].Note, "check failed") {
		t.Errorf("step 2 note = %q, want the failed check's output", res.Steps[1].Note)
	}
	// The baseline check ran exactly once, BEFORE step 1; the gate stays armed
	// (baseline passed), so each completed step runs its own post-step check.
	// Total calls: 1 baseline + step1 (pass) + step2 (fail, stops the run).
	// If the baseline were missing, the failure would land on step 1 instead
	// of step 2 — so this also proves the baseline didn't swallow the signal.
	if calls != 3 {
		t.Errorf("runProjectCheck calls = %d, want 3 (1 baseline + step1 + step2)", calls)
	}
	if !strings.Contains(res.Reply, "2. [failed]") || !strings.Contains(res.Reply, "3. [not reached]") {
		t.Errorf("reply missing per-step status:\n%s", res.Reply)
	}
	// Only planning + step1 + step2 ran; step 3 was not reached — prove it
	// from the SEND side, not just the status labels: exactly two step
	// (tools-present) sends happened, so step 3 was never sent.
	if n := backend.stepCount(); n != 2 {
		t.Fatalf("step sends = %d, want 2 (step 3 was not reached and never sent)", n)
	}
	if n := backend.requestCount(); n != 3 {
		t.Fatalf("requests = %d, want 3 (one planning send + two step sends)", n)
	}
}

// TestTurnWithPlanBaselineFailureSkipsCheckGate covers the #150 review's
// baseline case: the project's own suite ALREADY fails (or times out) before
// the plan starts. The baseline runProjectCheck is called once up front; when
// it fails, the between-step gate is disabled for the whole run and every step
// still runs and is reported done (with a note naming why the check was
// skipped). Without the baseline, step 1's post-step check would fail and the
// whole run would stop there.
func TestTurnWithPlanBaselineFailureSkipsCheckGate(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. first step\n2. second step\n3. third step\n", // planning turn
		"first done",  // step 1 turn
		"second done", // step 2 turn
		"third done",  // step 3 turn
	)
	cs := planTestSession(t, backend, root)

	// The baseline (call 1) FAILS — the suite was already broken. With the
	// gate disabled by the baseline, NO per-step check may run, so the stub
	// must be called EXACTLY once (the baseline). If the baseline were missing
	// or the gate stayed armed, step 1's post-step check would be call 2 and
	// would fail too, stopping the run.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		calls++
		// The baseline reports the pre-existing failure: a real non-zero exit.
		return "go test ./...", "FAIL\n\tmodule [build failed]", false, "check failed: FAIL\n\tmodule [build failed]"
	}

	res, err := cs.TurnWithPlan(context.Background(), "multi-part task")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Status != stepDone {
			t.Errorf("step %d status = %v, want done (baseline failure must not gate the steps)", i+1, s.Status)
		}
		if !strings.Contains(s.Note, "check skipped: failing before plan") {
			t.Errorf("step %d note = %q, want 'check skipped: failing before plan (…)'", i+1, s.Note)
		}
	}
	// Only the baseline check ran; the between-step gate was disabled.
	if calls != 1 {
		t.Errorf("runProjectCheck calls = %d, want 1 (baseline only; gate disabled after a failing baseline)", calls)
	}
	// All three step turns ran (one planning + three steps = four requests).
	if n := backend.stepCount(); n != 3 {
		t.Fatalf("step sends = %d, want 3 (every step ran despite the failing baseline)", n)
	}
	if n := backend.requestCount(); n != 4 {
		t.Fatalf("requests = %d, want 4 (one planning + three step sends)", n)
	}
	for _, want := range []string{"1. [done]", "2. [done]", "3. [done]", "3/3 steps done"} {
		if !strings.Contains(res.Reply, want) {
			t.Errorf("reply missing %q:\n%s", want, res.Reply)
		}
	}
}

// TestTurnWithPlanCancelledContextInterrupts covers the #150 review's
// interrupt case: a cancelled context (Ctrl-C) during a STEP is an INTERRUPT,
// not a step failure. TurnWithPlan must return the per-step report so far and
// an error that satisfies errors.Is(err, context.Canceled), so the REPL's
// afterTurn and the headless --plan path can tell an interrupt apart from a
// genuine step failure.
//
// The backend is told to cancel the request context on the SECOND request
// (index 1) — the first STEP turn (request 0 is the tools-less planning turn).
// So the planning turn completes, step 1 is the interrupted one, and the test
// asserts step 1 is marked failed (with an 'interrupted' note) and the
// remaining steps are not reached.
func TestTurnWithPlanCancelledContextInterrupts(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. first step\n2. second step\n", // planning turn (request 0, tools-less)
		"first done",                      // step 1 turn (request 1) — cancelled
		// step 2 is never requested.
	)
	backend.cancelAt = 1 // cancel the run's context on request 1 (the first step)
	cs := planTestSession(t, backend, root)

	// The test owns the run's context and hands its cancel func to the backend,
	// which fires it on request 1 (the first step turn). The planning turn
	// (request 0) completes; step 1 is the interrupted one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.cancelCtx = cancel

	// No stub: the empty temp root has no go.mod, so runProjectCheck reports a
	// skip (ok) and never fails the run — only the context cancel stops it.
	res, err := cs.TurnWithPlan(ctx, "multi-part task")
	if err == nil {
		t.Fatal("TurnWithPlan returned nil error, want a context.Canceled error (interrupt)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want an error satisfying errors.Is(…, context.Canceled)", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true (the planning turn succeeded before the interrupt)")
	}
	// The report is still returned: step 1 failed (interrupted), step 2 not
	// reached.
	if len(res.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(res.Steps))
	}
	if res.Steps[0].Status != stepFailed {
		t.Errorf("step 1 status = %v, want failed (interrupted)", res.Steps[0].Status)
	}
	if !strings.Contains(res.Steps[0].Note, "interrupted") {
		t.Errorf("step 1 note = %q, want an 'interrupted: …' note", res.Steps[0].Note)
	}
	if res.Steps[1].Status != stepNotReached {
		t.Errorf("step 2 status = %v, want not reached", res.Steps[1].Status)
	}
	if res.Reply == "" {
		t.Error("Reply = empty, want the per-step report even on interrupt")
	}
	// Prove from the send side: the planning turn (request 0) and step 1
	// (request 1) were sent; step 2 was never sent (the cancel stopped the
	// run before it).
	if n := backend.requestCount(); n != 2 {
		t.Fatalf("requests = %d, want 2 (planning + step 1; step 2 never sent)", n)
	}
}
