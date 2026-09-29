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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
		b.mu.Lock()
		idx := b.count
		b.count++
		b.haveTools = append(b.haveTools, len(tools) > 0)
		b.mu.Unlock()

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
func planTestSession(t *testing.T, b *planTestBackend, root string) *CortexSession {
	t.Helper()
	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = b.srv.URL
	cs.Request.Tools = toolSet // non-nil so step turns are distinguishable from the planning turn
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
	if n := backend.stepCount(); n != 3 {
		t.Fatalf("step sends = %d, want 3 (one per planned step)", n)
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
	// swapping the session's check behavior per call. step 1 → ok, step 2 →
	// fail. We override by planting a failing go.mod-backed root for the
	// check: simplest deterministic path is to stub runProjectCheck via a
	// package var.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (string, bool, string) {
		calls++
		if calls == 1 {
			return "ok", true, "check ok (go test ./...)"
		}
		return "boom", false, "check failed: boom"
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
