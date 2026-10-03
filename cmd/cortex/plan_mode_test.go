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

// TestPlanStepPromptCarriesTaskAndPrinciple proves the per-step prompt
// (issue #178) keeps the exact "Overall task: / Plan step N of M:" shape the
// happy-path test asserts AND restates the verify-before-fix principle
// (prompt.go's verifyBeforeFixPrinciple — the same const the base system
// prompt carries), so each step turn (tools present) carries both even after
// the planning turn is demoted out of the window. The check is on the
// PRINCIPLE, not on recipe phrases: the principle says confirm the problem
// exists before fixing it, and that a reported problem that doesn't
// reproduce is finished by reporting it with the evidence. It ALSO carries
// the no-repro output-shape convention (noReproMarker) that turns that
// principle's "saying so" into words noReproNote can recognize.
func TestPlanStepPromptCarriesTaskAndPrinciple(t *testing.T) {
	got := planStepPrompt("reproduce and fix the flaky test", 2, 4, "reproduce the bug, then fix it", nil)
	// The exact two-line shape the existing end-to-end tests assert on.
	for _, want := range []string{"Overall task: reproduce and fix the flaky test", "Plan step 2 of 4: reproduce the bug, then fix it"} {
		if !strings.Contains(got, want) {
			t.Errorf("planStepPrompt missing %q:\n%s", want, got)
		}
	}
	// The verify-before-fix principle is restated for the step turn — check
	// the PRINCIPLE (verify-before-fix + honest no-repro outcome), not exact
	// recipe wording.
	if !strings.Contains(got, verifyBeforeFixPrinciple) {
		t.Errorf("planStepPrompt does not restate the verify-before-fix principle (verifyBeforeFixPrinciple):\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "confirm a problem exists before fixing") {
		t.Errorf("planStepPrompt does not state the verify-before-fix principle (confirm before fix):\n%s", got)
	}
	// THE connect-through point (issue #178): the step prompt carries the
	// no-repro output-shape convention, so a real model that concludes the
	// bug doesn't reproduce writes it in the shape noReproNote recognizes.
	if !strings.Contains(got, noReproMarker) {
		t.Errorf("planStepPrompt does not carry the no-repro output-shape convention (noReproMarker):\n%s", got)
	}
	// And the convention and the probe agree on the same phrase: the marker
	// the model is told to use is the prefix noReproNote anchors on, so a
	// reply that follows the convention is recognized (round-2 review: the
	// probe was unreachable for a real model before this).
	if got := noReproNote("Not reproduced: go test ./... passes"); got != "Not reproduced: go test ./... passes" {
		t.Errorf("noReproNote did not recognize a reply that follows the %q convention: got %q", noReproMarker, got)
	}
}

// TestPlanStepPromptCarriesEarlierNotes proves issue #178's connect-through
// for plan steps: a step's prompt carries the EARLIER done steps' notes, so
// a later "fix it" step sees that the earlier step found the reported bug
// does not reproduce, instead of running blind to it. The no-repro note (the
// first step's own evidence) must appear in the second step's prompt; a
// first step (no earlier steps) carries no "Earlier steps:" section at all.
func TestPlanStepPromptCarriesEarlierNotes(t *testing.T) {
	first := planStepPrompt("reproduce and fix the bug", 1, 2, "reproduce the bug", nil)
	if strings.Contains(first, "Earlier steps:") {
		t.Errorf("the first step's prompt must not carry an 'Earlier steps:' section:\n%s", first)
	}
	second := planStepPrompt("reproduce and fix the bug", 2, 2, "fix it",
		[]string{"not reproduced: the test passes on the current code (go test ./... → ok, 0 failures)", "check passed (go test ./...)"})
	if !strings.Contains(second, "Earlier steps:") {
		t.Errorf("the second step's prompt must carry the earlier steps' section:\n%s", second)
	}
	// THE point: the earlier no-repro note reaches the fix step.
	if !strings.Contains(second, "not reproduced: the test passes on the current code") {
		t.Errorf("the second step's prompt must carry the earlier step's no-repro note:\n%s", second)
	}
	if !strings.Contains(second, "check passed (go test ./...)") {
		t.Errorf("the second step's prompt must also carry the other earlier done note:\n%s", second)
	}
	// Notes are numbered, one per line, in order.
	if !strings.Contains(second, "1. not reproduced: the test passes") || !strings.Contains(second, "2. check passed (go test ./...)") {
		t.Errorf("the earlier notes must be numbered lines in order:\n%s", second)
	}
}

// TestNoReproNote is a table-driven probe of the anchored no-repro verdict
// (issue #178): a step reply that LEADS with the "not reproduced" verdict
// yields its evidence as the step's note; a reply that merely echoes prompt
// wording — "if the bug does not reproduce …" — or states that it first
// failed to reproduce but then DID, yields "". The match is anchored to the
// verdict (a line starting with "not reproduced"), so an echo of the rule
// without a verdict can't be misread as a no-repro outcome.
func TestNoReproNote(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  string // "" = no note
	}{
		{name: "explicit no-repro verdict with evidence", reply: "not reproduced: the test passes on the current code (go test ./... → ok, 0 failures)", want: "not reproduced: the test passes on the current code (go test ./... → ok, 0 failures)"},
		{name: "no-repro verdict after a preamble line", reply: "I ran the suite twice against the current code.\nnot reproduced: both runs pass (go test ./... → ok).", want: "I ran the suite twice against the current code.\nnot reproduced: both runs pass (go test ./... → ok)."},
		{name: "echo of the rule without a verdict", reply: "If the bug does not reproduce, I would report it with the evidence, but I checked and it does.", want: ""},
		{name: "could not reproduce at first, then did and fixed it", reply: "I could not reproduce at first, then reproduced it with -race and fixed it.", want: ""},
		{name: "no verdict (a plain done)", reply: "done", want: ""},
		{name: "a done step's reply with no no-repro verdict yields no note", reply: "Done — the fix is in and the tests pass.", want: ""},
		{name: "empty reply", reply: "", want: ""},
		{name: "whitespace reply", reply: "   \n", want: ""},
		// The marker convention the step prompt tells the model to follow
		// (noReproMarker): a reply that BEGINS with the capitalized
		// "Not reproduced:" verdict is the real-model case the probe must
		// recognize — the case-insensitive, line-anchored match handles the
		// capital N.
		{name: "the marker convention a step prompt instructs (capitalized lead)", reply: "Not reproduced: the reported crash does not occur — `go test -race ./...` passes on the current code.", want: "Not reproduced: the reported crash does not occur — `go test -race ./...` passes on the current code."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noReproNote(tt.reply); got != tt.want {
				t.Errorf("noReproNote(%q) = %q, want %q", tt.reply, got, tt.want)
			}
		})
	}
}

// TestPlanPromptsCarryNoReproRule proves the prompts each turn actually sends
// (issue #178) restate the verify-before-fix principle: the PLANNING prompt
// (plan shape + task, no tools) and each STEP prompt (overall task + principle,
// tools present). It drives TurnWithPlan through the existing planTestBackend
// seam and inspects lastUserMessages — the recorded per-request user prompts —
// so it checks the real bytes a turn carries, not just the instruction
// constant. The check is on the PRINCIPLE (verify-before-fix + honest
// no-repro outcome), not on the recipe phrases a procedural rule would use.
func TestPlanPromptsCarryNoReproRule(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. reproduce the bug\n2. fix it\n", // planning turn (tools withheld)
		"repro done",                        // step 1
		"fix done",                          // step 2
	)
	cs := planTestSession(t, backend, root)

	if _, err := cs.TurnWithPlan(context.Background(), "reproduce and fix a bug"); err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	users := backend.lastUserMessages()
	if len(users) != 3 {
		t.Fatalf("recorded prompts = %d, want 3 (one planning + two step)", len(users))
	}

	// (a) table: which prompt, and what it must carry.
	// verifyBeforeFixPrinciple is the SAME const the base system prompt
	// carries (prompt.go), so both planning and step turns see the identical
	// wording; noReproMarker (the output-shape convention) rides in BOTH
	// prompts too, so the probe's "not reproduced" anchor has a real-model
	// counterpart.
	tests := []struct {
		name     string
		prompt   string
		contains []string
	}{
		{
			name:     "planning prompt carries the verify-before-fix principle and the no-repro marker",
			prompt:   users[0],
			contains: []string{verifyBeforeFixPrinciple, "confirm a problem exists before fixing", noReproMarker},
		},
		{
			name:     "step prompt carries the original task, the principle, and the no-repro marker",
			prompt:   users[1],
			contains: []string{"Overall task:", "Plan step 1 of 2:", "reproduce and fix a bug", verifyBeforeFixPrinciple, "confirm a problem exists before fixing", noReproMarker},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Case-insensitive: the principle's "Confirm a problem exists…" is
			// capitalized in the prompt (mid-sentence), and the probe must not
			// depend on that casing.
			lp := strings.ToLower(tt.prompt)
			for _, want := range tt.contains {
				if !strings.Contains(lp, strings.ToLower(want)) {
					t.Errorf("prompt does not contain %q:\n%s", want, tt.prompt)
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
		//
		// DETERMINISM: cancel, then HOLD the response — block until the
		// client's request context (r.Context()) is done, i.e. the client has
		// given up on this request. Returning without writing would NOT hold
		// it: net/http would still send an empty 200, and the client could
		// race that empty-body response against the cancel. With the
		// handler blocked on the canceled context, the client's in-flight
		// Do(ctx) is forced to return context.Canceled exactly as a real
		// interrupt would — no reply is ever read, so the interrupt is
		// deterministic.
		if b.cancelAt == idx && b.cancelCtx != nil {
			b.cancelCtx()
			// Hold the response until the client goes away (its request
			// context fires once the canceled run context is observed), so the
			// client cannot read any response body before the cancel.
			<-r.Context().Done()
			return
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
		// Each step turn (tools present) must ALSO restate the
		// verify-before-fix principle (issue #178) — the base system prompt
		// carries it, but each step prompt restates it so it survives demotion
		// of the planning turn.
		if !strings.Contains(strings.ToLower(users[i]), strings.ToLower("confirm a problem exists before fixing")) {
			t.Errorf("step %d prompt %q does not restate the verify-before-fix principle", i, users[i])
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
	// Each skip note carries EXACTLY ONE "check skipped: " prefix: the note
	// is the check's own (already prefixed), not a re-prefixed one.
	for i, s := range res.Steps {
		if strings.Count(s.Note, "check skipped:") != 1 {
			t.Errorf("step %d note = %q, want exactly one 'check skipped: …' prefix", i+1, s.Note)
		}
		if s.Note != "check skipped: no test/build command found for this project" {
			t.Errorf("step %d note = %q, want %q", i+1, s.Note, "check skipped: no test/build command found for this project")
		}
	}
}

// TestTurnWithPlanNoReproStepIsDone covers issue #178 step 3b: a step whose
// job was to reproduce a reported bug — and which, on the current code, does
// NOT reproduce it — must be reported DONE (not failed: no speculative fix was
// built and nothing broke), and its per-step report line must carry the
// no-repro note (the model's own "not reproduced" reply) so the reader sees
// the step was a verification, not a silently skipped fix.
func TestTurnWithPlanNoReproStepIsDone(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. reproduce the reported bug\n2. fix it\n",                                           // planning turn (tools withheld)
		"not reproduced: the test passes on the current code (go test ./... → ok, 0 failures)", // step 1 — verification, no bug to fix
		"fix done", // step 2
	)
	cs := planTestSession(t, backend, root)

	// Force the checks to pass deterministically: a clean baseline (gate armed)
	// and clean post-step checks. Without a stub, the empty temp root yields a
	// skip note, which would mask the no-repro note we're proving below.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		return "go test ./...", "ok", true, "ok"
	}

	res, err := cs.TurnWithPlan(context.Background(), "reproduce and fix the reported bug")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(res.Steps))
	}
	// THE point of the fix: the no-repro step is DONE, not failed.
	if res.Steps[0].Status != stepDone {
		t.Fatalf("step 1 status = %v, want done (a non-reproducible bug is reported, not a speculative-fix failure)", res.Steps[0].Status)
	}
	// The no-repro note (the model's own reply) is carried in the per-step
	// report so the reader sees the verification outcome, not a bare "done".
	if !strings.Contains(res.Steps[0].Note, "not reproduced") {
		t.Errorf("step 1 note = %q, want the no-repro note from the step's reply", res.Steps[0].Note)
	}
	// And it surfaces in the rendered final report line.
	if !strings.Contains(res.Reply, "1. [done]") || !strings.Contains(res.Reply, "not reproduced") {
		t.Errorf("final report missing the no-repro step's outcome:\n%s", res.Reply)
	}
	// Step 2 was reached, and — THE connect-through point (issue #178) — its
	// prompt carried step 1's no-repro note, so the fix step ran with the
	// knowledge that the bug never reproduced instead of blind to it.
	if res.Steps[1].Status != stepDone {
		t.Errorf("step 2 status = %v, want done", res.Steps[1].Status)
	}
	step2 := backend.lastUserMessages()[2]
	if !strings.Contains(step2, "Earlier steps:") || !strings.Contains(step2, "not reproduced") {
		t.Errorf("step 2's prompt must carry step 1's no-repro note (earlier steps):\n%s", step2)
	}
}

// TestTurnWithPlanNoReproNoteSurvivesFailingBaseline covers issue #178's
// dropped-note case: when the BASELINE check fails (the suite was already
// broken — the usual state when a bug has been reported), the between-step
// gate is disabled, and a step whose reply reports the bug does NOT
// reproduce must still carry that note — the no-repro evidence is not thrown
// away just because no check can gate the run.
func TestTurnWithPlanNoReproNoteSurvivesFailingBaseline(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. reproduce the reported bug\n2. fix it\n",                                           // planning turn (tools withheld)
		"not reproduced: the test passes on the current code (go test ./... → ok, 0 failures)", // step 1 — verification, no bug to fix
		"fix done", // step 2
	)
	cs := planTestSession(t, backend, root)

	// The baseline (call 1) FAILS — the suite was already broken before the
	// plan started. With the gate disabled, NO per-step check may run.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		calls++
		return "go test ./...", "FAIL", false, "check failed: FAIL"
	}

	res, err := cs.TurnWithPlan(context.Background(), "reproduce and fix the reported bug")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(res.Steps))
	}
	// THE point of the fix: the no-repro note SURVIVES a failing baseline —
	// it is not replaced by the baseline-skip note.
	if res.Steps[0].Status != stepDone {
		t.Errorf("step 1 status = %v, want done (a non-reproducible bug is reported, not a failure)", res.Steps[0].Status)
	}
	if !strings.Contains(res.Steps[0].Note, "not reproduced") {
		t.Errorf("step 1 note = %q, want the no-repro note (a failing baseline must not drop it)", res.Steps[0].Note)
	}
	if strings.Contains(res.Steps[0].Note, "failing before plan") {
		t.Errorf("step 1 note = %q, must not be the baseline-skip note (the no-repro evidence wins)", res.Steps[0].Note)
	}
	// Only the baseline check ran (the gate was disabled by it).
	if calls != 1 {
		t.Errorf("runProjectCheck calls = %d, want 1 (baseline only; gate disabled after a failing baseline)", calls)
	}
	// The no-repro note is also carried to step 2's prompt (earlier notes).
	step2 := backend.lastUserMessages()[2]
	if !strings.Contains(step2, "Earlier steps:") || !strings.Contains(step2, "not reproduced") {
		t.Errorf("step 2's prompt must carry step 1's no-repro note:\n%s", step2)
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

// TestTurnWithPlanCheckTimeoutIsSkipNotPass covers the #150 review's
// timeout case: a check that TIMES OUT (ok=true, cmdLine non-empty, note
// "check skipped: …") must NOT be reported as "check passed (cmd)" — a
// suite that didn't finish tells us nothing about the step, and a report
// line that names the command as passed misleads the reader about what
// actually ran. The step still completes (done); its note is the check's
// own skip summary.
func TestTurnWithPlanCheckTimeoutIsSkipNotPass(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. first step\n2. second step\n", // planning turn
		"first done",                      // step 1 turn
		"second done",                     // step 2 turn
	)
	cs := planTestSession(t, backend, root)

	// The baseline (call 1) is clean; every post-step check (calls 2, 3) is
	// the timeout-skip shape runProjectCheck produces when the check's
	// context hits its deadline: ok=true, cmdLine EMPTY (a skip never names
	// a command — the fix), and a "check skipped: …" note. A step that
	// keyed its report on cmdLine alone would mis-report this as a pass.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		calls++
		if calls == 1 {
			return "go test ./...", "ok", true, "ok" // baseline: clean, gate stays armed
		}
		// Timeout-skip shape: ok=true, cmdLine empty, skip note.
		return "", "", true, "check skipped: go test ./... (context deadline exceeded)"
	}

	res, err := cs.TurnWithPlan(context.Background(), "multi-part task")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Status != stepDone {
			t.Errorf("step %d status = %v, want done (a timed-out check skips, it does not fail the step)", i+1, s.Status)
		}
		// THE point of the fix: a timed-out check is never reported as
		// having passed, with or without a command named.
		if strings.Contains(s.Note, "check passed") {
			t.Errorf("step %d note = %q, must NOT contain 'check passed' (the check timed out)", i+1, s.Note)
		}
		if strings.Count(s.Note, "check skipped:") != 1 {
			t.Errorf("step %d note = %q, want exactly one 'check skipped: …' prefix", i+1, s.Note)
		}
	}
	// 1 baseline + 2 post-step checks = 3; the gate stayed armed the whole
	// run (a timeout is not a failing baseline), so every step ran its own
	// check.
	if calls != 3 {
		t.Errorf("runProjectCheck calls = %d, want 3 (1 baseline + 2 post-step)", calls)
	}
	if n := backend.stepCount(); n != 2 {
		t.Errorf("step sends = %d, want 2 (both steps ran)", n)
	}
}

// TestTurnWithPlanBaselineTimeoutDisarmsGate covers the #150 review's
// baseline blind spot: a baseline check that TIMES OUT (not fails) must
// disable the between-step gate for the whole run, exactly like a failing
// baseline. Without the fix, a timed-out baseline left the gate armed, so
// every step then waited out another full check timeout just to be
// mis-reported — a slow suite stacked one 90 s timeout on top of another.
func TestTurnWithPlanBaselineTimeoutDisarmsGate(t *testing.T) {
	root := t.TempDir()
	backend := newPlanTestBackend(t,
		"1. first step\n2. second step\n", // planning turn
		"first done",                      // step 1 turn
		"second done",                     // step 2 turn
	)
	cs := planTestSession(t, backend, root)

	// The baseline (call 1) TIMES OUT — the same skip shape the timeout
	// branch produces: ok=true (so a naive !ok check can't catch it),
	// cmdLine empty (a skip never names a command — the fix), and a
	// "check skipped: …" note. After the fix, such a baseline disarms the
	// gate for the whole run.
	orig := runProjectCheckStub
	t.Cleanup(func() { runProjectCheckStub = orig })
	var calls int
	runProjectCheckStub = func(cs *CortexSession, ctx context.Context) (cmdLine, out string, ok bool, note string) {
		calls++
		return "", "", true, "check skipped: go test ./... (context deadline exceeded)"
	}

	res, err := cs.TurnWithPlan(context.Background(), "multi-part task")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatal("Planned = false, want true")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Status != stepDone {
			t.Errorf("step %d status = %v, want done (a timed-out baseline must not gate the steps)", i+1, s.Status)
		}
		if strings.Contains(s.Note, "check passed") {
			t.Errorf("step %d note = %q, must NOT contain 'check passed' (no check completed)", i+1, s.Note)
		}
		if strings.Count(s.Note, "check skipped:") != 1 {
			t.Errorf("step %d note = %q, want exactly one 'check skipped: …' prefix", i+1, s.Note)
		}
	}
	// ONLY the baseline check ran: the timed-out baseline disarmed the gate,
	// so no per-step check ran. If the gate stayed armed (the pre-fix
	// behavior), step 1's post-step check would be call 2 and step 2's call
	// 3 — three calls instead of one.
	if calls != 1 {
		t.Errorf("runProjectCheck calls = %d, want 1 (baseline only; gate disarmed after a timed-out baseline)", calls)
	}
	// Both step turns ran (one planning + two steps = three requests).
	if n := backend.stepCount(); n != 2 {
		t.Errorf("step sends = %d, want 2 (both steps ran despite the timed-out baseline)", n)
	}
	if n := backend.requestCount(); n != 3 {
		t.Errorf("requests = %d, want 3 (one planning + two step sends)", n)
	}
}
