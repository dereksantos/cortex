// indemote_test.go — issue #171, step 3: the session-side in-turn demotion
// adapter (cmd/cortex/indemote.go). The pure selection policy is tested in
// internal/cache (indemote_test.go); here we lock the SESSION adapter: the
// policy it resolves (the same watermarks turn demotion uses, disabled → nil)
// and what applyInTurnDemotion does to the live request in place — under
// budget it is a byte-for-byte no-op, over budget it stubs the oldest tool
// results (keepRecent stay verbatim) while preserving Role/ToolCallID and
// writing a recall-citable stub whose citation resolves to the message's
// transcript index. The transcript is never touched, so recall of the stub's
// citation returns the original content.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/tools"
)

// indemoteToolResult builds a minimal assistant+tool-result pair: an assistant
// message that issues one tool call, then the tool's result (the message this
// feature may stub). The call's ActivityLabel is the "tool(args)" the stub's
// label should carry.
func indemoteToolResult(i, size int) (assistant, result Message) {
	path := "f" + ritoa(i) + ".go"
	argsJSON := `{"path":"` + path + `"}`
	assistant = Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_" + ritoa(i), Type: "function", Function: FunctionCall{Name: "read_file", Arguments: argsJSON}}}}
	result = Message{Role: RoleTool, Content: strings.Repeat("x", size), ToolCallID: "call_" + ritoa(i)}
	return
}

// indemoteSession builds a bare session with a fixed window and the default
// (nil) Config — which resolves to the standard W/2, W/3 watermarks and
// in-turn demotion ENABLED, exactly like a real session before the first turn.
func indemoteSession(window int) *CortexSession {
	cs := &CortexSession{Window: window, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	return cs
}

func TestTurnInTurnBudget(t *testing.T) {
	t.Run("default (nil Config) resolves the same watermarks as turn demotion", func(t *testing.T) {
		cs := indemoteSession(120000)
		p := cs.turnInTurnBudget()
		if p == nil {
			t.Fatal("policy is nil, want enabled by default (context.in_turn_demotion absent → on)")
		}
		if p.HighWM != 60000 { // 120000/2
			t.Errorf("HighWM = %d, want 60000 (W/2, the turn-demotion high watermark)", p.HighWM)
		}
		if p.LowWM != 40000 { // 120000/3
			t.Errorf("LowWM = %d, want 40000 (W/3, the turn-demotion drain watermark)", p.LowWM)
		}
		if p.KeepRecent != inTurnKeepRecentDefault {
			t.Errorf("KeepRecent = %d, want %d (default)", p.KeepRecent, inTurnKeepRecentDefault)
		}
		if p.SessionID != "test-session" {
			t.Errorf("SessionID = %q, want the session's id for the stub's recall citation", p.SessionID)
		}
	})

	t.Run("context.in_turn_demotion:false disables → nil policy", func(t *testing.T) {
		cs := indemoteSession(120000)
		disabled := false
		cs.Config = &Config{Context: ContextConfig{InTurnDemotion: &disabled}}
		if p := cs.turnInTurnBudget(); p != nil {
			t.Errorf("policy = %+v, want nil when in_turn_demotion is false", p)
		}
	})

	t.Run("nil Config still resolves (enabled by default)", func(t *testing.T) {
		cs := &CortexSession{Window: 120000, SessionID: "x", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}}
		if cs.Config != nil {
			t.Fatal("fixture Config should be nil")
		}
		if p := cs.turnInTurnBudget(); p == nil {
			t.Error("policy is nil for a nil Config, want enabled by default")
		}
	})
}

// TestApplyInTurnDemotionUnderBudgetIsNoOp locks the byte-for-byte
// guarantee: a turn whose tool results are under the high watermark goes out
// untouched — Content, Role, and ToolCallID all unchanged.
func TestApplyInTurnDemotionUnderBudgetIsNoOp(t *testing.T) {
	cs := indemoteSession(120000) // highWM 60000
	turnStart := len(cs.Request.Messages)
	// 8 results of 1000 chars ≈ 2000 tokens — well under the 60000 high watermark.
	for i := 0; i < 8; i++ {
		a, r := indemoteToolResult(i, 1000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	before := cloneMessages(cs.Request.Messages)
	cs.applyInTurnDemotion(cs.Request, turnStart)
	if !messagesEqual(cs.Request.Messages, before) {
		t.Errorf("under budget, the request was mutated; want byte-for-byte unchanged (got %d messages, some content changed)", len(cs.Request.Messages))
	}
}

// TestApplyInTurnDemotionOverBudgetStubsOldestFirst locks the over-budget path:
// the oldest tool results are stubbed (keepRecent stay verbatim), the stub
// carries a recall citation naming the message's transcript index, and
// Role/ToolCallID are preserved.
func TestApplyInTurnDemotionOverBudgetStubsOldestFirst(t *testing.T) {
	cs := indemoteSession(120000) // highWM 60000, lowWM 40000, keepRecent 6
	turnStart := len(cs.Request.Messages)
	// 20 results of 12000 chars = 3000 tokens each, 60000 total — right at the
	// edge; push well over by using 30 results (90000 tokens).
	const n = 30
	for i := 0; i < n; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)

	// Find the tool-result messages and which were stubbed. A stubbed message
	// has Content starting with "[demoted:".
	stubbedCount := 0
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		m := cs.Request.Messages[i]
		if m.Role != RoleTool {
			continue
		}
		if strings.HasPrefix(m.Content, "[demoted:") {
			stubbedCount++
			// The stub must carry a recall citation naming THIS message's index.
			wantCite := "@session/test-session#m" + ritoa(i) + "-" + ritoa(i+1)
			if !strings.Contains(m.Content, "recall "+wantCite) {
				t.Errorf("stubbed message %d citation = %q, want it to name %q (its own transcript index)", i, m.Content, wantCite)
			}
			// Role and ToolCallID must be preserved so the result still pairs
			// with the assistant's tool call.
			if m.Role != RoleTool {
				t.Errorf("stubbed message %d role = %q, want %q (Role preserved)", i, m.Role, RoleTool)
			}
			if m.ToolCallID == "" {
				t.Errorf("stubbed message %d lost its ToolCallID (must stay paired with the assistant call)", i)
			}
		}
	}
	if stubbedCount == 0 {
		t.Fatal("no tool results stubbed, but the turn is far over the high watermark")
	}
	// Oldest-first: the stubbed tool results must form a contiguous PREFIX of
	// the turn's tool results (the oldest k are stubbed, the rest verbatim).
	// Scan the tool results in order; once we hit the first verbatim one,
	// every later one must also be verbatim (no stub after a verbatim).
	toolIdx := make([]int, 0, n)
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	seenVerbatim := false
	for j, idx := range toolIdx {
		isStub := strings.HasPrefix(cs.Request.Messages[idx].Content, "[demoted:")
		if seenVerbatim && isStub {
			t.Errorf("tool result at position %d (index %d) is stubbed but an earlier tool result (position < %d) was verbatim; stubbing must be oldest-first (a contiguous prefix of the tool results)", j, idx, j)
		}
		if !isStub {
			seenVerbatim = true
		}
	}
}

// TestApplyInTurnDemotionKeepRecentStayVerbatim locks that the most recent
// keepRecent tool results are never stubbed even when the turn is over budget.
func TestApplyInTurnDemotionKeepRecentStayVerbatim(t *testing.T) {
	cs := indemoteSession(120000) // keepRecent 6
	turnStart := len(cs.Request.Messages)
	const n = 30
	for i := 0; i < n; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)

	// Collect the indices of the tool results, then assert the last keepRecent
	// of them are NOT stubbed.
	var toolIdx []int
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	for k := 0; k < inTurnKeepRecentDefault; k++ {
		idx := toolIdx[len(toolIdx)-1-k]
		if strings.HasPrefix(cs.Request.Messages[idx].Content, "[demoted:") {
			t.Errorf("the %d-th most recent tool result (index %d) was stubbed, but the last keepRecent=%d must stay verbatim", k, idx, inTurnKeepRecentDefault)
		}
	}
}

// TestApplyInTurnDemotionTranscriptUntouched locks the invariant that makes
// recall work: applyInTurnDemotion mutates only the WIRE copy
// (cs.Request.Messages). The transcript (the lossless record, written by
// Append → writeTranscript) is never touched, so Recall of a stub's citation
// returns the ORIGINAL content, not the stub.
func TestApplyInTurnDemotionTranscriptUntouched(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{workspace: ws, Window: 120000, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	// Open the transcript file directly (Append writes through cs.transcript).
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	cs.transcript = f
	// Append the system message first so the transcript's index 0 is the
	// system message, matching Request.Messages.
	cs.writeTranscript(cs.Request.Messages[0])
	turnStart := len(cs.Request.Messages)
	const n = 30
	for i := 0; i < n; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Append(a)
		cs.Append(r)
	}
	// Record the original content of the FIRST tool result.
	var firstResultIdx int
	var firstOrig string
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			firstResultIdx = i
			firstOrig = cs.Request.Messages[i].Content
			break
		}
	}
	if firstOrig == "" {
		t.Fatal("fixture: no tool result found before demotion")
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)

	// The wire copy of the first tool result is now a stub.
	if !strings.HasPrefix(cs.Request.Messages[firstResultIdx].Content, "[demoted:") {
		t.Fatalf("wire copy of message %d = %q..., want a [demoted: stub (the turn is over budget)", firstResultIdx, first200(cs.Request.Messages[firstResultIdx].Content))
	}
	// But Recall of the stub's citation returns the ORIGINAL content, because
	// the transcript (written before demotion) still holds it. Recall resolves
	// against SessionsDir()+SessionID.jsonl = the file we just wrote.
	cite := "@session/test-session#m" + ritoa(firstResultIdx) + "-" + ritoa(firstResultIdx+1)
	got, err := cs.Recall(cite)
	if err != nil {
		t.Fatalf("Recall(%s): %v", cite, err)
	}
	if !strings.Contains(got, firstOrig) {
		t.Errorf("Recall of the stub's citation did not return the original content; transcript was mutated by demotion. want it to contain the original %d-char content, got: %q", len(firstOrig), first200(got))
	}
	if strings.Contains(got, "[demoted:") {
		t.Errorf("Recall returned the STUB (%q...), not the original — the transcript must keep the original content", first200(got))
	}
}

// TestApplyInTurnDemotionTurnEndConsumersSeeOriginals locks the turn-side of
// issue #171 item 5: when a turn's tool result was an "Error: …" failure and
// in-turn demotion stubbed it on the wire, the turn-END consumers read the
// ORIGINAL content, never the stub — (a) the turn-end outline entry labels the
// call [err] (the stub's content would start with "[demoted:" and look like a
// success to turnOutlineEntry's "Error:" check), and (b) captureTurn's
// journal artifacts are built from the original content (web_search/
// fetch_url lines are the real tool output, not stub text). For a turn
// demotion never touched, the original view is identical to the wire copy.
func TestApplyInTurnDemotionTurnEndConsumersSeeOriginals(t *testing.T) {
	cs := indemoteSession(120000) // highWM 60000, lowWM 40000
	turnStart := len(cs.Request.Messages)
	errResult := "Error: exit 1\n" + strings.Repeat("t", 4000) + "\n" + strings.Repeat("u", 4000)
	for i := 0; i < 30; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	// The OLDEST tool result is the one the drain stubs first — make it the
	// failed call.
	var errIdx int
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			errIdx = i
			cs.Request.Messages[i].Content = errResult
			break
		}
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)
	// Fixture sanity: the failed result must be stubbed on the wire (it is the
	// oldest tool result, and the turn is far over the high watermark).
	if !strings.HasPrefix(cs.Request.Messages[errIdx].Content, "[demoted:") {
		t.Fatalf("fixture: the failed tool result at index %d was not stubbed on the wire", errIdx)
	}

	// (a) The turn-end outline entry must label the failed call [err], reading
	// through the originals (a stubbed "[demoted: …" result would otherwise be
	// marked [ok] by turnOutlineEntry's "Error:" check).
	entry := turnOutlineEntry(1, cache.TurnSpan{Start: turnStart, End: len(cs.Request.Messages)}, cs.turnOriginalSpan(cs.Request, turnStart, len(cs.Request.Messages)), cs.SessionID)
	found := false
	for _, a := range entry.Actions {
		if strings.Contains(a, "[err]") {
			found = true
		}
		if strings.Contains(a, "[ok]") && !found && errIdx < len(cs.Request.Messages) {
			// The FIRST action is the oldest result's call: it must be [err],
			// not [ok]. (Later actions may legitimately be [ok].)
			t.Errorf("the failed call's outline action is %q, want it labeled [err] (the wire copy was a stub; the outline must read the original)", a)
		}
	}
	if !found {
		t.Errorf("no outline action labeled [err] for the failed tool result; actions: %v", entry.Actions)
	}

	// (b) captureTurn's artifacts come from the ORIGINAL content: the journal
	// summary's answer/tool text is the original, never the stub.
	view := cs.turnOriginalSpan(cs.Request, turnStart, len(cs.Request.Messages))
	_, answer := turnArtifacts(view)
	_ = answer
	results := toolResultsByID(view)
	origForCall, ok := results["call_0"]
	if !ok {
		t.Fatal("fixture: no tool result indexed for call_0 in the original view")
	}
	if origForCall != errResult {
		t.Errorf("captureTurn's view of the failed result = %q..., want the ORIGINAL content (Error: …), not the stub", first200(origForCall))
	}
	if strings.Contains(origForCall, "[demoted:") {
		t.Errorf("captureTurn's view contains the STUB — journal artifacts must be built from the original content")
	}

	// The original view restores the originals at every stubbed index and
	// leaves every other message byte-for-byte identical to the wire copy
	// (only the drained — oldest-first — results are stubbed, so their
	// indices are exactly the ones with a "[demoted:" stub on the wire).
	for i, m := range cs.Request.Messages[turnStart:] {
		if !strings.HasPrefix(m.Content, "[demoted:") {
			if view[i].Content != m.Content {
				t.Errorf("original view message %d differs from the wire copy, want byte-for-byte (only stubbed indices are restored)", i)
			}
		}
	}
}

// TestApplyInTurnDemotionTwiceKeepsFirstStub locks the session-side half of
// the re-stub bug: a later send in the same turn, over the high watermark
// again, must NOT rebuild an already-stubbed message from its stub text —
// the first stub's "240 lines, error: …" (original line count and error
// status) survives the second call byte-for-byte, while the drain moves on to
// the next-oldest verbatim results.
func TestApplyInTurnDemotionTwiceKeepsFirstStub(t *testing.T) {
	cs := indemoteSession(120000) // highWM 60000, lowWM 40000
	kr := inTurnKeepRecentDefault - 1
	cs.Config = &Config{Context: ContextConfig{InTurnKeepRecent: &kr}} // keep 5
	turnStart := len(cs.Request.Messages)
	errResult := "Error: exit 1\n" + strings.Repeat("e", 9000) // 93 lines
	// 24 results of 12000 chars = 72000 tokens — over the 60k highWM; the
	// failed result (index 2, the oldest) is the first one the drain stubs.
	for i := 0; i < 24; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	// The oldest tool result is a failed call (the "Error: …" shape the
	// dispatcher returns). 24 results total, keepRecent 5 → the failed result
	// (index 2) is the oldest candidate.
	var errIdx int
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			errIdx = i
			cs.Request.Messages[i].Content = errResult
			break
		}
	}

	// First send: over budget → the oldest verbatim results are stubbed, the
	// failed one among them.
	cs.applyInTurnDemotion(cs.Request, turnStart)
	firstStub := cs.Request.Messages[errIdx].Content
	if !strings.HasPrefix(firstStub, "[demoted:") {
		t.Fatalf("fixture: after the first call the oldest result at index %d is not a stub: %q", errIdx, first200(firstStub))
	}
	// The first stub must carry the ORIGINAL line count and the error status.
	wantLines := ritoa(countLines(errResult))
	if !strings.Contains(firstStub, "→ "+wantLines+" lines, error: ") {
		t.Fatalf("first stub lost the original's line count/error status: %q", first200(firstStub))
	}

	// Grow the turn (a later round of tool results) so the turn is over the
	// high watermark AGAIN — after the first call the turn is ~46k (at
	// lowWM); 3 more results of 36000 chars = 27000 tokens push it back over
	// the 60k highWM — and call the hook the second time, the situation a
	// later send in the same turn hits.
	for i := 0; i < 3; i++ {
		a, r := indemoteToolResult(100+i, 36000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)

	// THE assertion: the first stub is byte-for-byte unchanged after the
	// second call. The regression rebuilt it from the stub text — countLines
	// (stub)=1 and isErr (stub)=false — turning "error: Error: exit 1" into
	// "1 lines, ok": wrong information about a failed call, and a wasted drain
	// slot.
	if got := cs.Request.Messages[errIdx].Content; got != firstStub {
		t.Errorf("second call re-stubbed the first stub: got %q, want the unchanged first stub %q", first200(got), first200(firstStub))
	}
	if got := cs.Request.Messages[errIdx].Content; strings.Contains(got, "1 lines, ok") {
		t.Errorf("the re-stubbed message reads as a successful one-line result: %q", first200(got))
	}
}

// --- issue #180: whole-prompt in-turn demotion tests ---

// indemoteSessionWithWS builds a session with a WorkingSet so the whole-prompt
// plan can resolve the previous turn's span via cs.ws.FrontierMsg(). The
// WorkingSet is initialized with the given base index (usually 1, the index
// after the system message).
func indemoteSessionWithWS(window int, base int) *CortexSession {
	cs := &CortexSession{Window: window, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	cs.ws = cs.newWorkingSet(base)
	return cs
}

// TestApplyInTurnDemotionSmallSessionUnchanged locks the byte-for-byte
// guarantee for a small session: when the whole prompt is well under the high
// watermark, nothing is stubbed — the request goes out untouched.
func TestApplyInTurnDemotionSmallSessionUnchanged(t *testing.T) {
	cs := indemoteSessionWithWS(120000, 1) // highWM 60000
	turnStart := len(cs.Request.Messages)
	// A modest turn: 4 results of 1000 chars ≈ 1000 tokens — well under 60000.
	for i := 0; i < 4; i++ {
		a, r := indemoteToolResult(i, 1000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	before := cloneMessages(cs.Request.Messages)
	cs.applyInTurnDemotion(cs.Request, turnStart)
	if !messagesEqual(cs.Request.Messages, before) {
		t.Errorf("small session, the request was mutated; want byte-for-byte unchanged")
	}
}

// TestApplyInTurnDemotionDisabledNothingStubbed locks that when
// context.in_turn_demotion is false, applyInTurnDemotion is a complete no-op
// even when the whole prompt is far over the high watermark.
func TestApplyInTurnDemotionDisabledNothingStubbed(t *testing.T) {
	cs := indemoteSessionWithWS(120000, 1) // highWM 60000
	disabled := false
	cs.Config = &Config{Context: ContextConfig{InTurnDemotion: &disabled}}
	turnStart := len(cs.Request.Messages)
	// 30 results of 12000 chars = 90000 tokens — far over the 60000 highWM.
	for i := 0; i < 30; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}
	before := cloneMessages(cs.Request.Messages)
	cs.applyInTurnDemotion(cs.Request, turnStart)
	if !messagesEqual(cs.Request.Messages, before) {
		t.Errorf("in_turn_demotion:false, the request was mutated; want byte-for-byte unchanged (disabled = complete no-op)")
	}
}

// TestApplyInTurnDemotionWholePromptEarlierTrigger locks the issue #180 fix:
// the whole-prompt plan fires when the WHOLE prompt (prefix + tail + current
// turn) exceeds the high watermark, even when the current turn ALONE is under
// it. The previous turn's large tail pushes the total over; the turn-only
// plan would see the current turn alone as under budget and do nothing.
func TestApplyInTurnDemotionWholePromptEarlierTrigger(t *testing.T) {
	cs := indemoteSessionWithWS(120000, 1) // highWM 60000, lowWM 40000, keepRecent 6
	// Simulate a previous turn: 20 results of 12000 chars each = 60000 tokens.
	// These are in the hydrated tail (the previous turn's span).
	prevMsgs := []Message{}
	for i := 0; i < 20; i++ {
		a, r := indemoteToolResult(i, 12000)
		prevMsgs = append(prevMsgs, a, r)
	}
	cs.Request.Messages = append(cs.Request.Messages, prevMsgs...)
	// Register the previous turn in the WorkingSet so FrontierMsg() points to
	// the start of the current turn.
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: len(cs.Request.Messages)})

	// Current turn: 4 results of 1000 chars ≈ 1000 tokens — well under 60000
	// alone, but the whole prompt (60000 + 1000 + system) is over 60000.
	turnStart := len(cs.Request.Messages)
	for i := 0; i < 4; i++ {
		a, r := indemoteToolResult(100+i, 1000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}

	cs.applyInTurnDemotion(cs.Request, turnStart)

	// The whole-prompt plan must have stubbed SOMETHING (the previous turn's
	// large results, after the current turn's 4 candidates are exhausted —
	// but with keepRecent 6 and only 4 current-turn results, all 4 are kept
	// verbatim, so the stubs come from the previous turn).
	stubbedInPrev := 0
	for i := 1; i < turnStart; i++ {
		if cs.Request.Messages[i].Role == RoleTool && strings.HasPrefix(cs.Request.Messages[i].Content, "[demoted:") {
			stubbedInPrev++
		}
	}
	if stubbedInPrev == 0 {
		t.Fatal("no previous-turn results stubbed; the whole prompt is over highWM (prev 60000 + cur 1000) but the turn alone (1000) is under — the whole-prompt plan must have fired")
	}
}

// TestApplyInTurnDemotionWireEstimateCountsOnlyTheSentPart locks the issue
// #180 regression: the whole-prompt total is built from what wireMessages
// actually sends. A session whose working set has several DEMOTED turns
// (frontier > base) holding large tool results, plus a small hydrated tail and
// a small current turn whose real wire size is under the high watermark, must
// send untouched — even though the stored message log is far over the high
// watermark, because the demoted region rides on the wire as the outline
// block (counted in the fixed part), never as the raw demoted messages.
func TestApplyInTurnDemotionWireEstimateCountsOnlyTheSentPart(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{workspace: ws, Window: 120000, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	cs.ws = cs.newWorkingSet(1)
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	cs.transcript = f
	cs.writeTranscript(cs.Request.Messages[0])

	// Three completed turns of 20 results × 8000 chars each: ~40000 tokens
	// EACH, ~120000 total — far over the 60000 highWM as a stored log, but the
	// newest (still-hydrated) turn alone is ~40000, so the real wire — that
	// one hydrated turn plus the small tail and current turn — stays just
	// under the high watermark while the stored log stays far over it.
	for turn := 0; turn < 3; turn++ {
		spanStart := len(cs.Request.Messages)
		cs.Append(Message{Role: RoleUser, Content: "turn " + ritoa(turn)})
		for i := 0; i < 20; i++ {
			a, r := indemoteToolResult(turn*100+i, 8000)
			cs.Append(a)
			cs.Append(r)
		}
		cs.ws.AddTurn(cache.TurnSpan{Start: spanStart, End: len(cs.Request.Messages), Tokens: estTurnTokens(cs.Request.Messages[spanStart:])})
	}

	// Demote the older turns: frontier = 2 > base = 1, so the stored log holds
	// ~120000 tokens of demoted history behind the frontier (DemoteBatch always
	// keeps the newest turn hydrated, so the batch drains 3 turns → 2 turns).
	batch := cs.ws.DemoteBatch()
	if len(batch) != 2 {
		t.Fatalf("DemoteBatch demoted %d turns, want 2 (turns 0 and 1 must be behind the frontier)", len(batch))
	}
	for i, span := range batch {
		cs.outline = append(cs.outline, turnOutlineEntry(1+i, span, cs.Request.Messages[span.Start:span.End], cs.SessionID))
	}
	cs.Request.OutlineBlock = cs.renderOutlineBlock()
	cs.Request.PrefixEnd = cs.ws.Base()
	cs.Request.TailFrom = cs.ws.FrontierMsg()

	// A small hydrated tail: one completed turn of 4 results × 1000 chars
	// (≈1000 tokens).
	hydrStart := len(cs.Request.Messages)
	cs.Append(Message{Role: RoleUser, Content: "hydrate"})
	for i := 0; i < 4; i++ {
		a, r := indemoteToolResult(500+i, 1000)
		cs.Append(a)
		cs.Append(r)
	}
	cs.ws.AddTurn(cache.TurnSpan{Start: hydrStart, End: len(cs.Request.Messages), Tokens: estTurnTokens(cs.Request.Messages[hydrStart:])})
	cs.Request.TailFrom = cs.ws.FrontierMsg()

	// Current turn: 2 results × 1000 chars ≈ 500 tokens.
	turnStart := len(cs.Request.Messages)
	for i := 0; i < 2; i++ {
		a, r := indemoteToolResult(600+i, 1000)
		cs.Append(a)
		cs.Append(r)
	}

	// Fixture sanity: the stored log must be far over the high watermark, and
	// the WIRE (what composeWire actually sends) must be under it.
	stored := estTurnTokens(cs.Request.Messages)
	if stored <= 60000 {
		t.Fatalf("fixture: stored log is %d tokens, want > 60000 (the demoted history must keep the stored log far over the high watermark)", stored)
	}
	wire := estTurnTokens(cs.Request.wireMessages())
	if wire >= 60000 {
		t.Fatalf("fixture: wire is %d tokens, want < 60000 (the demoted region must not ride the wire)", wire)
	}

	before := cloneMessages(cs.Request.Messages)
	cs.applyInTurnDemotion(cs.Request, turnStart)
	if !messagesEqual(cs.Request.Messages, before) {
		t.Errorf("real wire size (%d tokens) is under the high watermark, but the request was mutated — the plan counted the stored log's demoted history (the #180 regression: the total never drops to the low watermark, so every send stubs and invalidates the prompt cache)", wire)
	}
}

// TestApplyInTurnDemotionNeverStubsTheWirePrefix locks the second issue #180
// regression: the fallback for an empty hydrated tail (cs.ws nil, or a
// frontier at turnStart) must degrade to a turn-only plan — it must NOT widen
// the stubbable region to [0, turnStart). Tool-role messages sitting before
// the tail (for example a session whose ws base was set lazily after earlier
// turns ran) are never stub candidates, even when the whole prompt is far
// over the high watermark.
func TestApplyInTurnDemotionNeverStubsTheWirePrefix(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{workspace: ws, Window: 120000, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	cs.transcript = f
	cs.writeTranscript(cs.Request.Messages[0])

	// Tool-role messages BEFORE the tail: no demoted region here (TailFrom =
	// 0 → prefixEnd = 1, nothing demoted), so index 1 is pre-tail content in
	// the shape of a session whose ws base was set lazily after earlier turns
	// ran — the #180 regression made exactly these a stub candidate.
	a, r := indemoteToolResult(0, 12000)
	cs.Append(a)
	cs.Append(r)
	prefixResultIdx := len(cs.Request.Messages) - 1
	prefixOrig := cs.Request.Messages[prefixResultIdx].Content

	// A big current turn: 30 results × 12000 chars = 90000 tokens — far over
	// the 60000 highWM, so the plan fires and stubs the turn's own oldest
	// results (keepRecent 6 stays verbatim).
	turnStart := len(cs.Request.Messages)
	for i := 0; i < 30; i++ {
		a, r := indemoteToolResult(100+i, 12000)
		cs.Append(a)
		cs.Append(r)
	}

	// (a) cs.ws is nil: the fallback must be turn-only, never [0, turnStart).
	if cs.ws != nil {
		t.Fatal("fixture: cs.ws must be nil")
	}
	cs.applyInTurnDemotion(cs.Request, turnStart)

	if cs.Request.Messages[prefixResultIdx].Content != prefixOrig {
		t.Errorf("the pre-tail tool result at index %d was stubbed with cs.ws nil — the fallback span must be empty (turn-only), not [0, turnStart): got %q", prefixResultIdx, first200(cs.Request.Messages[prefixResultIdx].Content))
	}
	// Sanity: the turn's own oldest results WERE stubbed (the plan fired).
	stubbed := 0
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool && strings.HasPrefix(cs.Request.Messages[i].Content, "[demoted:") {
			stubbed++
		}
	}
	if stubbed == 0 {
		t.Fatalf("fixture: the over-budget current turn stubbed nothing — the plan must have fired")
	}

	// (b) cs.ws present with a frontier span [frontier, turnStart) that is
	// non-empty but holds no tool results: the previous span's candidates are
	// exhausted (nothing to stub there), so the drain covers only the current
	// turn — the pre-tail result must stay untouched either way.
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: turnStart, Tokens: estTurnTokens(cs.Request.Messages[1:turnStart])})
	if cs.ws.FrontierMsg() != 1 {
		t.Fatalf("fixture: FrontierMsg() = %d, want 1 (a hydrated tail spanning [1, turnStart))", cs.ws.FrontierMsg())
	}
	before := cloneMessages(cs.Request.Messages)
	cs.applyInTurnDemotion(cs.Request, turnStart)
	if cs.Request.Messages[prefixResultIdx].Content != prefixOrig {
		t.Errorf("the pre-tail tool result at index %d was stubbed with an empty frontier span — the fallback span must be empty (turn-only), not [0, turnStart): got %q", prefixResultIdx, first200(cs.Request.Messages[prefixResultIdx].Content))
	}
	if !messagesEqual(cs.Request.Messages, before) {
		t.Errorf("the second call mutated the request, want byte-for-byte (everything stubbable was already stubbed)")
	}
}

// TestApplyInTurnDemotionResumeLargeTurnStaysUnderWindow is the issue #180
// acceptance test: resume a session whose last turn is over the high
// watermark; the first request of the NEXT turn must be under the window
// (at or under the low watermark once candidates are exhausted), and recall of
// a stubbed previous-turn citation must return the original content. It goes
// through the real ResumeTranscript path (replayWorkingSet, the persisted
// state, the restored outline block) — not ws.AddTurn.
func TestApplyInTurnDemotionResumeLargeTurnStaysUnderWindow(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	// Write the original session: system + one user turn of 20 results ×
	// 12000 chars = 60000 tokens — over the 60000 high watermark (W/2).
	orig := &CortexSession{workspace: ws, Window: 120000, SessionID: "large-turn", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	orig.ws = orig.newWorkingSet(1)
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), orig.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	orig.transcript = f
	orig.writeTranscript(orig.Request.Messages[0])
	orig.turnNo = 1
	orig.Append(Message{Role: RoleUser, Content: "read the big files"})
	for i := 0; i < 20; i++ {
		a, r := indemoteToolResult(i, 12000)
		orig.Append(a)
		orig.Append(r)
	}
	orig.Append(Message{Role: "assistant", Content: "done with the big files"})
	orig.ws.AddTurn(cache.TurnSpan{Start: 1, End: len(orig.Request.Messages), Tokens: estTurnTokens(orig.Request.Messages[1:])})
	orig.writeSessionState()
	f.Close()

	// Resume: ReplayWorkingSet rebuilds the working set from the transcript,
	// the persisted state restores the frontier, and the outline block +
	// PrefixEnd/TailFrom are set from the restored state (the real resume
	// path the issue asks for).
	resumed := &CortexSession{workspace: ws, Window: 120000, Request: &AgentRequest{Model: "m"}}
	if err := resumed.ResumeTranscript("large-turn"); err != nil {
		t.Fatalf("ResumeTranscript: %v", err)
	}
	defer resumed.transcript.Close()
	if resumed.ws == nil || resumed.ws.FrontierMsg() != 1 || resumed.ws.TotalTurns() != 1 {
		t.Fatalf("fixture: resumed working set = frontier %v, turns %v, want frontier 1 with 1 turn (nothing demoted yet)", resumed.ws.FrontierMsg(), resumed.ws.TotalTurns())
	}
	// The resumed last turn alone must be over the high watermark — the
	// shape the issue describes.
	if tok := estTurnTokens(resumed.Request.Messages[1:]); tok <= 60000 {
		t.Fatalf("fixture: the resumed last turn is %d tokens, want > 60000 (over the high watermark)", tok)
	}

	// Start the next turn's first send: user input + a small current turn
	// (2 results × 1000 chars ≈ 500 tokens). The whole prompt is now ≈61.5k
	// — over the high watermark, under the window (120000).
	turnStart := len(resumed.Request.Messages)
	resumed.Append(Message{Role: RoleUser, Content: "small next question"})
	for i := 0; i < 2; i++ {
		a, r := indemoteToolResult(100+i, 1000)
		resumed.Append(a)
		resumed.Append(r)
	}
	resumed.Request.EphemeralSystem = "# memory index (small)"

	// Pre-stub sanity: the WIRE must be over the high watermark before the
	// hook runs (otherwise the test does not exercise the drain).
	wireBefore := estTurnTokens(resumed.Request.wireMessages())
	if wireBefore <= 60000 {
		t.Fatalf("fixture: wire before demotion is %d tokens, want > 60000", wireBefore)
	}

	resumed.applyInTurnDemotion(resumed.Request, turnStart)

	// (1) The resulting request must be under the window — at or under the
	// low watermark once candidates are exhausted (the previous turn's 14
	// stubbable results drain the prompt far below lowWM 40000).
	wireAfter := estTurnTokens(resumed.Request.wireMessages())
	if wireAfter > 120000 {
		t.Fatalf("the first send of the next turn carries %d tokens, over the 120000 window", wireAfter)
	}
	if wireAfter > 40000 {
		t.Errorf("the first send of the next turn carries %d tokens, over the low watermark 40000 — the drain must reach the low watermark when candidates remain", wireAfter)
	}
	// (2) Something in the previous turn was stubbed (the current turn's 2
	// results are both within keepRecent 6, so the stubs come from the tail).
	prevStubIdx := -1
	for i := 1; i < turnStart; i++ {
		m := resumed.Request.Messages[i]
		if m.Role == RoleTool && strings.HasPrefix(m.Content, "[demoted:") {
			prevStubIdx = i
			break
		}
	}
	if prevStubIdx < 0 {
		t.Fatal("no previous-turn result was stubbed; the whole prompt was over the high watermark and the current turn's results are all within keepRecent")
	}
	// (3) Recall of the stub's citation returns the ORIGINAL content (the
	// transcript was written before the stub, so recall never sees the
	// stub) — the losslessness half of the acceptance test.
	cite := fmtCitation(resumed.SessionID, prevStubIdx)
	if !strings.Contains(resumed.Request.Messages[prevStubIdx].Content, "recall "+cite) {
		t.Errorf("stub at index %d does not carry its citation %q", prevStubIdx, cite)
	}
	got, err := resumed.Recall(cite)
	if err != nil {
		t.Fatalf("Recall(%s): %v", cite, err)
	}
	if strings.Contains(got, "[demoted:") {
		t.Errorf("Recall of %s returned the STUB, not the original — the transcript must keep the original content", cite)
	}
	if !strings.Contains(got, strings.Repeat("x", 12000)) {
		t.Errorf("Recall of %s did not return the original 12000-char content", cite)
	}
}

// TestApplyInTurnDemotionResumeLargeTail locks the resume scenario: after a
// resume, the previous turn comes back verbatim in the hydrated tail and is
// large. The whole-prompt plan stubs the previous turn's results (after the
// current turn's candidates) to bring the prompt back under the low watermark.
func TestApplyInTurnDemotionResumeLargeTail(t *testing.T) {
	cs := indemoteSessionWithWS(120000, 1) // highWM 60000, lowWM 40000, keepRecent 6
	// Previous turn (the hydrated tail): 20 results of 12000 chars = 60000 tokens.
	prevMsgs := []Message{}
	for i := 0; i < 20; i++ {
		a, r := indemoteToolResult(i, 12000)
		prevMsgs = append(prevMsgs, a, r)
	}
	cs.Request.Messages = append(cs.Request.Messages, prevMsgs...)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: len(cs.Request.Messages)})

	// Current turn: 8 results of 10000 chars = 20000 tokens.
	// Whole prompt: 60000 + 20000 + system ≈ 80000 > highWM 60000.
	// Drain: 2 current-turn candidates (8 - keepRecent 6) × ~2500 tokens saved
	// = ~5000 → still over. Then previous-turn candidates (14 of 20) × ~3000
	// tokens saved → reaches lowWM 40000.
	turnStart := len(cs.Request.Messages)
	for i := 0; i < 8; i++ {
		a, r := indemoteToolResult(100+i, 10000)
		cs.Request.Messages = append(cs.Request.Messages, a, r)
	}

	cs.applyInTurnDemotion(cs.Request, turnStart)

	// The current turn's oldest results must be stubbed first (keepRecent 6
	// keeps the newest 6 verbatim).
	curToolIdx := []int{}
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool {
			curToolIdx = append(curToolIdx, i)
		}
	}
	// The first 2 current-turn results (8 - keepRecent 6 = 2 candidates) must
	// be stubbed.
	for j := 0; j < 2; j++ {
		idx := curToolIdx[j]
		if !strings.HasPrefix(cs.Request.Messages[idx].Content, "[demoted:") {
			t.Errorf("current-turn result %d (index %d) not stubbed; the whole-prompt plan stubs current-turn oldest first", j, idx)
		}
	}
	// The previous turn's oldest results must also be stubbed (after the
	// current turn's candidates are exhausted).
	prevStubbed := 0
	for i := 1; i < turnStart; i++ {
		if cs.Request.Messages[i].Role == RoleTool && strings.HasPrefix(cs.Request.Messages[i].Content, "[demoted:") {
			prevStubbed++
		}
	}
	if prevStubbed == 0 {
		t.Errorf("no previous-turn results stubbed; the drain should continue into the previous turn after the current turn's candidates")
	}
}

// TestApplyInTurnDemotionRecallStubbedPrevTurnResult locks that a stubbed
// previous-turn result's recall citation resolves to the original content
// (the transcript was written before the stub, so recall returns the
// original, not the stub).
func TestApplyInTurnDemotionRecallStubbedPrevTurnResult(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{workspace: ws, Window: 120000, SessionID: "test-session", Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}}}
	cs.ws = cs.newWorkingSet(1)
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	cs.transcript = f
	cs.writeTranscript(cs.Request.Messages[0])

	// Previous turn: 20 results of 12000 chars = 60000 tokens.
	for i := 0; i < 20; i++ {
		a, r := indemoteToolResult(i, 12000)
		cs.Append(a)
		cs.Append(r)
	}
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: len(cs.Request.Messages)})

	// Current turn: 4 results of 1000 chars.
	turnStart := len(cs.Request.Messages)
	for i := 0; i < 4; i++ {
		a, r := indemoteToolResult(100+i, 1000)
		cs.Append(a)
		cs.Append(r)
	}

	cs.applyInTurnDemotion(cs.Request, turnStart)

	// Find a stubbed previous-turn result on the wire.
	prevStubIdx := -1
	prevStubOrig := ""
	for i := 1; i < turnStart; i++ {
		m := cs.Request.Messages[i]
		if m.Role == RoleTool && strings.HasPrefix(m.Content, "[demoted:") {
			prevStubIdx = i
			prevStubOrig = cs.inTurnOriginals[i]
			break
		}
	}
	if prevStubIdx < 0 {
		t.Fatal("no previous-turn result was stubbed; the test cannot exercise the recall path")
	}
	if prevStubOrig == "" {
		t.Fatal("no original recorded for the stubbed previous-turn result")
	}
	// Recall the stub's citation: it must return the ORIGINAL content.
	cite := fmtCitation(cs.SessionID, prevStubIdx)
	if !strings.Contains(cs.Request.Messages[prevStubIdx].Content, "recall "+cite) {
		t.Errorf("stub at index %d does not carry its citation %q", prevStubIdx, cite)
	}
	got, err := cs.Recall(cite)
	if err != nil {
		t.Fatalf("Recall(%s): %v", cite, err)
	}
	if strings.Contains(got, "[demoted:") {
		t.Errorf("Recall of %s returned the STUB, not the original — the transcript must keep the original content", cite)
	}
	if !strings.Contains(got, prevStubOrig) {
		t.Errorf("Recall of %s did not return the original content", cite)
	}
}

// --- helpers ---

// ritoa is int→string for citation/index formatting in fixtures (the
// package's own strconv import would be fine too, but a local helper keeps the
// test file's imports minimal and the intent obvious).
func ritoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// first200 truncates s to 200 runes for test diagnostics.
func first200(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}

// cloneMessages deep-copies a Message slice (ToolCalls included) for
// before/after comparisons.
func cloneMessages(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if m.ToolCalls != nil {
			nc := make([]ToolCall, len(m.ToolCalls))
			copy(nc, m.ToolCalls)
			out[i].ToolCalls = nc
		}
	}
	return out
}

// messagesEqual reports whether two Message slices are byte-for-byte equal
// (Content, Role, ToolCallID, and ToolCalls' names/args).
func messagesEqual(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Content != b[i].Content || a[i].Role != b[i].Role || a[i].ToolCallID != b[i].ToolCallID {
			return false
		}
		if len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
		for j := range a[i].ToolCalls {
			if a[i].ToolCalls[j].ID != b[i].ToolCalls[j].ID || a[i].ToolCalls[j].Function.Name != b[i].ToolCalls[j].Function.Name {
				return false
			}
		}
	}
	return true
}

// --- step 3b: the turn-end consumers across MULTIPLE turns ---
//
// The tests above call the adapter (applyInTurnDemotion) and the consumer
// (turnOutlineEntry) directly, which is the right granularity for the policy —
// but it can't see the WIRING in turn.go: how the demote-then-send block
// reads each span's originals and when the originals are dropped. The two
// regression shapes that wiring has to keep out of every outline entry are:
//
//   1. an UNBOUNDED original view — turnOriginalSpan(req, start) with no end
//      would copy every message from the span's start to the CURRENT end of
//      the log, so a turn demoted two or more turns later would collect all
//      later turns' tool calls in its actions and take the latest turn's
//      reply as its ReplyHead;
//   2. a per-turn reset of the originals map — applyInTurnDemotion used to
//      drop every entry when a new turn started, but DemoteBatch drains the
//      oldest turns from the hydrated tail, usually several turns after they
//      ran. By then a stubbed "Error: …" result's original is gone, the
//      outline reads the one-line wire stub, and a failed call outlines as
//      [ok].
//
// This test drives the REAL turn path (cs.Turn → cs.turn → the DemoteBatch
// block in turn.go) across several turns with a fake Sender — the same
// seam the engine's BeforeSend hook uses — and asserts the rendered outline.

// multiTurnScriptedSender returns a fake Sender that replays scripted
// responses, one per request, until the script is exhausted (the last
// response is then repeated). No network: the turn's coderSender is
// replaced wholesale via the session's test-only senderOverride seam.
func multiTurnScriptedSender(responses []*AgentResponse) Sender {
	var i int
	return SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := responses[i]
		if i < len(responses)-1 {
			i++
		}
		return r, false, nil
	})
}

// multiTurnScriptedSession builds a scripted, transcript-backed session whose
// coder's round-trip is driven by script (the fake Sender), not a live
// backend. Window 12000 (highWM 6000, lowWM 4000 — W/2, W/3) with in-turn
// demotion ENABLED (nil Config) and keepRecent 3, so turn 1's batch of 6
// 4000-token results drains its oldest three on the FIRST send — the same
// shape a real session hits, on the smallest fixture that forces it. The
// transcript is opened so captureTurn (and Recall) work as in production.
func multiTurnScriptedSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    12000, // highWM 6000, lowWM 4000 (W/2, W/3)
		SessionID: "multi-turn-session",
		Config:    &Config{Context: ContextConfig{InTurnKeepRecent: intPtr(3)}},
		Request:   &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	cs.transcript = f
	cs.writeTranscript(cs.Request.Messages[0])
	cs.senderOverride = multiTurnScriptedSender(script)
	return cs
}

// TestTurnEndConsumersSeeOriginalsAcrossTurns is the turn-level lock for the
// outline half of issue #171 item 5: a failed tool result that in-turn
// demotion stubbed on the wire in turn 1 must still outline as [err] when
// turn 1 is demoted two or more turns later — and every demoted entry must
// hold only its own span's actions and its own reply (no later turn's tool
// calls or reply may leak in). It drives the REAL turn path (DemoteBatch →
// turnOutlineEntry in turn.go) with a fake Sender, so both wiring
// regressions above are caught:
//
//   - turn 1 stubs an "Error: …" result (oldest-first drain); turns 2 and 3
//     then run; at turn 4's start DemoteBatch drains turn 1 — two turns after
//     it ran — and its outline entry must read the ORIGINAL (the stub's
//     "[demoted: …" text would read as a success → [ok]);
//   - the same entry must list only turn 1's call and turn 1's reply: the
//     turn-2/turn-3 tool calls and the latest reply must not appear in it.
func TestTurnEndConsumersSeeOriginalsAcrossTurns(t *testing.T) {
	t.Chdir(t.TempDir())                                       // workspace root so the turn's coderDispatcher has a valid workspace
	errResult := "Error: exit 1\n" + strings.Repeat("t", 4000) // the failed call's ORIGINAL result
	// Turn 1: a batch of 6 tool results at 12000 chars each (3000 tokens each,
	// 18000 total — over the 6000 highWM; keepRecent 3 → the oldest three,
	// including the failed one, are stubbed on the FIRST send of the turn).
	turn1Calls := []ToolCall{}
	var turn1Msgs []Message
	for i := 0; i < 6; i++ {
		id := "t1call_" + ritoa(i)
		path := "t1f" + ritoa(i) + ".go"
		turn1Calls = append(turn1Calls, readCall(id, path))
		turn1Msgs = append(turn1Msgs,
			Message{Role: RoleTool, Content: strings.Repeat("x", 12000), ToolCallID: id})
	}
	// The OLDEST tool result is the failed call — the first one the drain stubs.
	turn1Msgs[0].Content = errResult
	// Turns 2 and 3: one distinct call each, then their own replies.
	// turn 3's reply is also the LATEST assistant reply in the log — the value
	// an unbounded original view would wrongly give turn 1's ReplyHead.
	script := []*AgentResponse{
		respWithCalls(turn1Calls), // turn 1 round 1: issue the 30 calls
		respWithAnswer("t1-reply-marker done"),
		respWithCalls([]ToolCall{readCall("t2call_0", "t2f0.go")}), // turn 2
		respWithAnswer("t2-reply-marker done"),
		respWithCalls([]ToolCall{readCall("t3call_0", "t3f0.go")}), // turn 3
		respWithAnswer("t3-reply-marker done"),
		respWithAnswer("t4-reply-marker done"), // turn 4: plain answer, no tool calls
	}
	cs := multiTurnScriptedSession(t, script)
	// The REAL turn path (turn.go's Toolset) is driven by the script above.
	// Replace the tool dispatcher so the scripted read_file calls return the
	// fixture's tool results instead of touching real files — the sender is
	// already scripted (senderOverride), so the only live seam left is the
	// dispatcher's file access. This keeps the test honest about the wire
	// (cs.Request.Messages) and the transcript, which are what the outline
	// reads, without depending on the contents of real files.
	origDispatcher := cs.coderDispatcherOverride
	cs.coderDispatcherOverride = func() AgentDispatcher {
		return DispatchFunc(func(_ context.Context, call ToolCall) string {
			for _, m := range turn1Msgs {
				if m.ToolCallID == call.ID {
					return m.Content
				}
			}
			return "ok"
		})
	}
	defer func() { cs.coderDispatcherOverride = origDispatcher }()

	if _, err := cs.Turn(context.Background(), "turn 1: read the big files"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	// Fixture sanity: the failed result must actually be stubbed on the wire
	// (it is the oldest of six results over the high watermark).
	stubbedErr := false
	for _, m := range cs.Request.Messages {
		if m.Role == RoleTool && m.ToolCallID == "t1call_0" && strings.HasPrefix(m.Content, "[demoted:") {
			stubbedErr = true
		}
	}
	if !stubbedErr {
		t.Fatal("fixture: turn 1's failed result was never stubbed on the wire — the test would not exercise the outline-from-originals path")
	}
	// Fixture sanity: after turn 1, the tail (turn 1 alone) must still be over
	// the 6000 highWM, so the demote-then-send block at turn 2's start
	// demotes turn 1 — the span is then rebuilt from the originals while the
	// stubs still ride the wire.
	if tok := cs.ws.TailTokens(); tok <= 6000 {
		t.Fatalf("fixture: turn 1's tail is %d tokens, <= the 6000 highWM — it would never demote and the outline path is not exercised", tok)
	}

	if _, err := cs.Turn(context.Background(), "turn 2"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if _, err := cs.Turn(context.Background(), "turn 3"); err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	// Turn 4's start runs the DemoteBatch: turn 1 (over the high watermark at
	// that point) is demoted to the outline — TWO turns after it ran, so the
	// per-turn originals reset (the regression) would have dropped turn 1's
	// originals long before this point.
	if _, err := cs.Turn(context.Background(), "turn 4"); err != nil {
		t.Fatalf("turn 4: %v", err)
	}
	if cs.ws.Demoted() < 1 {
		t.Fatalf("Demoted() = %d, want >= 1: the scripted turns were over the watermark, turn 1 must have demoted at turn 4's start", cs.ws.Demoted())
	}

	// Find turn 1's outline entry (its user content is the turn-1 input).
	var entry *cache.OutlineEntry
	for i := range cs.outline {
		if strings.Contains(cs.outline[i].User, "turn 1") {
			entry = &cs.outline[i]
			break
		}
	}
	if entry == nil {
		t.Fatalf("turn 1's span was not demoted into the outline (entries: %+v)", cs.outline)
	}

	// (a) The failed call must be labeled [err] — read from the ORIGINAL
	// "Error: …" result, not the one-line wire stub (which would read as [ok]).
	errAction := ""
	for _, a := range entry.Actions {
		if strings.Contains(a, "[err]") {
			errAction = a
		}
	}
	if errAction == "" {
		t.Errorf("turn 1's failed call is not labeled [err] in the outline — its original was lost by turn-end demotion (the stub's text reads as a success); actions: %v", entry.Actions)
	}
	if strings.Contains(strings.Join(entry.Actions, " "), "t2f0.go") || strings.Contains(strings.Join(entry.Actions, " "), "t3f0.go") {
		t.Errorf("turn 1's entry carries a LATER turn's tool call in its actions (unbounded original view leaked later spans); actions: %v", entry.Actions)
	}
	// (b) Turn 1's entry must hold ONLY its own actions: its 30 turn-1 calls
	// (call ids are distinct per turn, so a count is exact) and none of the
	// later turns'.
	if got, want := len(entry.Actions), 6; got != want {
		t.Errorf("turn 1's entry has %d actions, want %d (its own calls only); actions: %v", got, want, entry.Actions)
	}
	// (c) Its ReplyHead must be turn 1's OWN reply — not the latest turn's
	// reply, which an unbounded original view would take from the end of the
	// log.
	if entry.ReplyHead != "t1-reply-marker done" {
		t.Errorf("turn 1's ReplyHead = %q, want %q (its own reply; the unbounded view would have taken the latest turn's reply)", entry.ReplyHead, "t1-reply-marker done")
	}

	// (d) The later demoted entries (turns 2/3, if the batch drained them too)
	// must each hold only their own single call — the same span-boundedness
	// guarantee for every entry in the batch, not just the first.
	for i := range cs.outline {
		e := &cs.outline[i]
		if e == entry {
			continue
		}
		// Every action in the entry must name only ONE turn's file — an
		// action mixing file names from two turns' spans is a leak from an
		// unbounded original view.
		for _, a := range e.Actions {
			var owner string
			for _, own := range []string{"t1f", "t2f", "t3f"} {
				if strings.Contains(a, own) {
					if owner != "" && owner != own {
						t.Errorf("entry %d mixes turns in one action: %q", e.Turn, a)
					}
					owner = own
				}
			}
		}
		// Each single-call entry (turns 2/3) holds exactly one action and its
		// own reply head — no later turn's reply in it.
		if len(e.Actions) == 1 {
			switch {
			case strings.Contains(e.Actions[0], "t2f"):
				if e.ReplyHead != "t2-reply-marker done" {
					t.Errorf("turn 2's entry ReplyHead = %q, want its own reply %q (unbounded view leak)", e.ReplyHead, "t2-reply-marker done")
				}
			case strings.Contains(e.Actions[0], "t3f"):
				if e.ReplyHead != "t3-reply-marker done" {
					t.Errorf("turn 3's entry ReplyHead = %q, want its own reply %q (unbounded view leak)", e.ReplyHead, "t3-reply-marker done")
				}
			}
		}
	}
	// (e) The originals for the DEMOTED span are dropped (the entry has read
	// them; keeping them would be a memory leak in a long session). A turn
	// that was never stubbed has no originals to keep, so this assertion
	// holds vacuously for it — the per-turn-reset regression is caught by
	// the [err] assertion (a): an unbounded or cleared map would leave the
	// stub's text in the outline and the [err] check would fail.
	var t1Span cache.TurnSpan
	foundSpan := false
	for _, s := range cs.ws.TurnSpans() {
		if s.End-s.Start > 5 { // turn 1 is the only oversized span
			t1Span = s
			foundSpan = true
		}
	}
	if !foundSpan {
		t.Fatalf("turn 1's span not found in the working set: %+v", cs.ws.TurnSpans())
	}
	if cs.inTurnOriginals != nil {
		left := 0
		for k := range cs.inTurnOriginals {
			if k >= t1Span.Start && k < t1Span.End {
				left++
			}
		}
		if left != 0 {
			t.Errorf("%d original entries survive inside a DEMOTED span's indices (they must be dropped when the entry is built); left in %+v: %d", left, t1Span, left)
		}
	}
}

// respWithCalls builds an AgentResponse whose single choice is an assistant
// message with the given tool calls and no content.
func respWithCalls(calls []ToolCall) *AgentResponse {
	return &AgentResponse{Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}}
}

// respWithAnswer builds an AgentResponse whose single choice is an assistant
// message with plain content (no tool calls) — the turn's finalize.
func respWithAnswer(answer string) *AgentResponse {
	return &AgentResponse{Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: answer}, FinishReason: "stop"}}}
}

// --- step 5: end-to-end runLoop with a fake Sender ---
//
// TestRunLoopInTurnDemotionKeepsEverySendUnderWindow is the end-to-end lock
// for issue #171: drive the REAL runLoop engine with the coder's BeforeSend
// seam wired to the real session adapter (cs.applyInTurnDemotion), a fake
// Sender that issues a long turn of large tool results, and a fake dispatcher
// that returns large observations. It proves the four guarantees the step
// calls for, through the actual loop (not the adapter in isolation):
//
//  1. EVERY request the sender receives is under the window — a turn whose
//     accumulated tool output would otherwise overflow the window is kept
//     under it on every send by in-turn demotion.
//  2. Stubs carry correct citations — a demoted tool result's one-line stub
//     names its own transcript index (@session/<id>#m<i>-<i+1>).
//  3. ToolCallID/Role pairing is preserved on the wire — every tool-result
//     message the sender sees keeps Role==tool and a ToolCallID that matches
//     the assistant's tool call (a broken pairing would be rejected by the
//     provider).
//  4. The transcript contains every original result — the lossless record
//     (written by Append → writeTranscript BEFORE demotion) holds the original
//     observation for every tool call, never the stub, so recall works.
func TestRunLoopInTurnDemotionKeepsEverySendUnderWindow(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	const window = 12000 // highWM 6000, lowWM 4000 (W/2, W/3), keepRecent 2
	// keepRecent=2 so that even a modest batch of 3 large results can be
	// demoted (oldest-first, keeping only the newest 2 verbatim). This also
	// exercises step 4's context.in_turn_keep_recent end to end.
	kr := 2
	cs := &CortexSession{
		workspace: ws,
		Window:    window,
		SessionID: "test-session",
		Config:    &Config{Context: ContextConfig{InTurnKeepRecent: &kr}},
		Request:   &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	// Open the transcript (Append writes through cs.transcript → writeTranscript).
	f, err := openTranscript(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	cs.transcript = f
	// The transcript's index 0 is the system message, matching Request.Messages.
	cs.writeTranscript(cs.Request.Messages[0])

	// The turn's user message (appended before the loop, like turn.go).
	cs.Append(Message{Role: RoleUser, Content: "run the long turn"})
	// turnStart bounds BeforeSend to this turn's own messages, exactly like
	// turn.go's `ts.BeforeSend = func(req){ cs.applyInTurnDemotion(req, turnStart) }`.
	turnStart := len(cs.Request.Messages)

	// Each tool result is 12000 chars ≈ 3000 tokens (TokensOf = chars/4), so a
	// single round of 3 results is 9000 tokens — well over the 6000 high
	// watermark and the 4000 low watermark, forcing demotion on the next send.
	const resultSize = 12000
	// The fake model's script: round 1 issues three read_file calls, round 2
	// issues two more, round 3 finalizes (no tool calls). Each round's calls
	// have DISTINCT ids/paths so the no-progress guard never trips.
	type roundScript struct {
		calls  []ToolCall
		answer string
	}
	script := []roundScript{
		{calls: []ToolCall{readCall("c0", "f0.go"), readCall("c1", "f1.go"), readCall("c2", "f2.go")}},
		{calls: []ToolCall{readCall("c3", "f3.go"), readCall("c4", "f4.go")}},
		{answer: "done, all files read"},
	}
	// expectedOrigins records each dispatched tool result's original content,
	// keyed by the call id, for the transcript check.
	origins := map[string]string{}
	var round int
	// sentReqs captures a DEEP CLONE of the request on EVERY send — the
	// request object is mutated in place by later iterations (and by the
	// BeforeSend hook), so the clone is the only way to assert what each
	// individual send actually carried.
	var sentReqs [][]Message
	// requestTokens is the same estimate currentContextSize uses (sum of
	// Content bytes + tool-call name/args bytes, through TokensOf).
	requestTokens := func(msgs []Message) int {
		sum := 0
		for _, m := range msgs {
			sum += len(m.Content)
			for _, call := range m.ToolCalls {
				sum += len(call.Function.Name) + len(call.Function.Arguments)
			}
		}
		return cache.TokensOf(sum)
	}
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		// Record a deep clone of the request exactly as this send sees it.
		sentReqs = append(sentReqs, cloneMessages(r.Messages))
		if round >= len(script) {
			return fakeResp(script[len(script)-1].answer, nil, 1, 1), false, nil
		}
		s := script[round]
		round++
		if len(s.calls) == 0 {
			return fakeResp(s.answer, nil, 1, 1), false, nil
		}
		return fakeResp("", s.calls, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, call ToolCall) string {
		obs := strings.Repeat("x", resultSize) + call.ID
		origins[call.ID] = obs
		return obs
	})

	_, stats, err := runLoop(context.Background(), send, cs.Request,
		Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp, BeforeSend: func(req *AgentRequest) { cs.applyInTurnDemotion(req, turnStart) }},
		Bounds{MaxTokens: 100, MaxIter: 100}, nil, cs.Append, nil)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if stats.StopReason != "clean-finalize" {
		t.Fatalf("stop reason = %q, want clean-finalize (the fake model answered with no tool calls)", stats.StopReason)
	}
	if len(sentReqs) < 2 {
		t.Fatalf("the sender saw %d requests, want at least 2 (multiple sends — the turn must overflow across rounds)", len(sentReqs))
	}

	// (1) EVERY request the sender received is under the window.
	for i, req := range sentReqs {
		if tok := requestTokens(req); tok >= window {
			t.Errorf("send #%d carried %d tokens, >= the window (%d) — in-turn demotion failed to keep a send under the window", i+1, tok, window)
		}
	}

	// (2)+(3) On the LATER sends (after at least one round of large results),
	// the demoted tool results are stubs with correct citations and preserved
	// Role/ToolCallID pairing. Find the first send that carries a stub and
	// verify every tool-result message in every send.
	stubSeen := false
	for i, req := range sentReqs {
		// Collect the assistant tool-call ids this send's assistant messages
		// issued, so we can verify each tool result's ToolCallID pairs with one.
		calledIDs := map[string]bool{}
		for _, m := range req {
			for _, c := range m.ToolCalls {
				calledIDs[c.ID] = true
			}
		}
		for j, m := range req {
			if m.Role != RoleTool {
				continue
			}
			// (3a) Role preserved.
			if m.Role != RoleTool {
				t.Errorf("send #%d message %d: tool result lost its role", i+1, j)
			}
			// (3b) ToolCallID preserved and pairs with an assistant call.
			if m.ToolCallID == "" {
				t.Errorf("send #%d message %d: tool result lost its ToolCallID (would be rejected by the provider)", i+1, j)
			} else if !calledIDs[m.ToolCallID] {
				t.Errorf("send #%d message %d: ToolCallID %q does not pair with any assistant tool call in the same request", i+1, j, m.ToolCallID)
			}
			if strings.HasPrefix(m.Content, "[demoted:") {
				stubSeen = true
				// (2) The stub carries a citation naming THIS message's own
				// transcript index: @session/<id>#m<j>-<j+1>.
				wantCite := "@session/test-session#m" + ritoa(j) + "-" + ritoa(j+1)
				if !strings.Contains(m.Content, "recall "+wantCite) {
					t.Errorf("send #%d stub at index %d: citation = %q, want it to name %q (its own transcript index)", i+1, j, m.Content, wantCite)
				}
			}
		}
	}
	if !stubSeen {
		t.Fatal("no stub was sent on the wire, but the turn overflowed the high watermark — in-turn demotion never fired")
	}

	// (4) The transcript contains every original result (never a stub). Read the
	// transcript file back and check each dispatched origin is present verbatim
	// and no [demoted: stub leaked into the record.
	data, err := os.ReadFile(filepath.Join(ws.SessionsDir(), cs.SessionID+".jsonl"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	transcript := string(data)
	if strings.Contains(transcript, "[demoted:") {
		t.Errorf("the transcript contains a [demoted: stub — the lossless record must keep the original content, not the wire stub")
	}
	for id, orig := range origins {
		// The full original observation (12000 'x' chars + the id) must appear
		// verbatim in the transcript — the lossless record keeps the original
		// content, never the stub. (The body has no JSON-escaping special
		// chars, so it is written verbatim inside the JSONL string.)
		if !strings.Contains(transcript, orig) {
			t.Errorf("the transcript is missing the full original observation for call %s", id)
		}
	}
	// Also assert via the Recall path (the point of recall-citable stubs): a
	// stub's citation resolves to the original content, not the stub.
	// Find a stubbed index in the LAST send and recall it.
	last := sentReqs[len(sentReqs)-1]
	for j, m := range last {
		if m.Role == RoleTool && strings.HasPrefix(m.Content, "[demoted:") {
			cite := "@session/test-session#m" + ritoa(j) + "-" + ritoa(j+1)
			got, err := cs.Recall(cite)
			if err != nil {
				t.Fatalf("Recall(%s): %v", cite, err)
			}
			if strings.Contains(got, "[demoted:") {
				t.Errorf("Recall of %s returned the STUB, not the original content", cite)
			}
			if !strings.Contains(got, strings.Repeat("x", resultSize)) {
				t.Errorf("Recall of %s did not return the original %d-char observation", cite, resultSize)
			}
			break
		}
	}
}

// TestRecallStubCitationRoundTrip is the focused step-6 lock: a single stub's
// recall citation, resolved against a REAL transcript (StartTranscript +
// Recall, not a hand-rolled file), returns the original verbatim content. This
// is the guarantee that makes the whole in-turn-demotion feature useful — the
// wire shrinks a result to a one-line stub, but recall of the stub's citation
// brings the full original back. It uses StartTranscript (the same path a live
// session uses to open its transcript) and Recall (the same resolver the
// recall tool calls), with the stub's ACTUAL citation shape (the half-open
// single-message #m<i>-<i+1> that fmtCitation / citationFor produce — a bare
// #m<i>-<i> is an empty range Recall rejects as out-of-range).
func TestRecallStubCitationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    12000, // highWM 6000, lowWM 4000
		// keepRecent=2 so a 3-result batch can be demoted (oldest 1 stubbed).
		Config:  &Config{Context: ContextConfig{InTurnKeepRecent: intPtr(2)}},
		Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	// StartTranscript opens the transcript for this session and writes the
	// existing messages (the system message) to it, aligning the transcript's
	// message indices with cs.Request.Messages. cs.SessionID is set to the
	// generated id.
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	t.Cleanup(func() { cs.transcript.Close() })
	if cs.SessionID == "" {
		t.Fatal("StartTranscript did not assign a session id")
	}
	// A turn: user input + an assistant issuing one read_file, + the (large)
	// tool result. Each Append writes the ORIGINAL to the transcript.
	const resultSize = 12000                    // ≈ 3000 tokens
	original := strings.Repeat("O", resultSize) // 'O' so it is unambiguous vs the stub
	turnStart := len(cs.Request.Messages)
	cs.Append(Message{Role: RoleUser, Content: "read f0"})
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{readCall("c0", "f0.go")}})
	cs.Append(Message{Role: RoleTool, Content: original, ToolCallID: "c0"})

	// Demote the turn in place: the wire (cs.Request.Messages) gets a stub, the
	// transcript already holds the original. With one tool result and
	// keepRecent=2, there is nothing old enough to stub (len<=keepRecent) — so
	// use a second result to push past keepRecent: append a second large result.
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{readCall("c1", "f1.go")}})
	original2 := strings.Repeat("P", resultSize)
	cs.Append(Message{Role: RoleTool, Content: original2, ToolCallID: "c1"})

	// Now two tool results (each ≈3000 tokens) = 6000 = highWM (not strictly
	// over). Push one more to force demotion past the high watermark.
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{readCall("c2", "f2.go")}})
	original3 := strings.Repeat("Q", resultSize)
	cs.Append(Message{Role: RoleTool, Content: original3, ToolCallID: "c2"})

	cs.applyInTurnDemotion(cs.Request, turnStart)

	// Find the stubbed message (the oldest tool result) on the wire.
	stubIdx := -1
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool && strings.HasPrefix(cs.Request.Messages[i].Content, "[demoted:") {
			stubIdx = i
			break
		}
	}
	if stubIdx < 0 {
		// With keepRecent=2 and 3 tool results, the oldest (result 0) must be
		// stubbed. A silent no-op demotion must not masquerade as a passing
		// round-trip, so fail clearly here.
		t.Fatalf("no tool result was stubbed on the wire (turn is over the high watermark, keepRecent=2, 3 results) — in-turn demotion never fired; the round-trip cannot be tested")
	}
	// The stubbed message's original content is the FIRST tool result
	// (original, all 'O' — it is the oldest and therefore the one stubbed).
	// Verify the wire copy is now a stub.
	if !strings.HasPrefix(cs.Request.Messages[stubIdx].Content, "[demoted:") {
		t.Fatalf("wire copy of message %d = %q..., want a [demoted: stub", stubIdx, first200(cs.Request.Messages[stubIdx].Content))
	}
	// The stub's citation must be the half-open single-message range for its
	// own index: #m<stubIdx>-<stubIdx+1>. Extract it from the stub content and
	// verify it names that exact range.
	stubCite := fmtCitation(cs.SessionID, stubIdx)
	if !strings.Contains(cs.Request.Messages[stubIdx].Content, "recall "+stubCite) {
		t.Errorf("stub at index %d does not carry its own citation %q; stub content: %q", stubIdx, stubCite, cs.Request.Messages[stubIdx].Content)
	}
	// The point of the round-trip: recall of the stub's citation returns the
	// ORIGINAL verbatim content (all 'O'), never the stub.
	got, err := cs.Recall(stubCite)
	if err != nil {
		t.Fatalf("Recall(%s): %v", stubCite, err)
	}
	if strings.Contains(got, "[demoted:") {
		t.Errorf("Recall of %s returned the STUB, not the original — the transcript must keep the original content", stubCite)
	}
	if !strings.Contains(got, original) {
		t.Errorf("Recall of %s did not return the original %d-char content (the run of %q); got: %q", stubCite, resultSize, "O", first200(got))
	}
	// And recall of the citation for a VERBATIM (non-stubbed) result returns
	// that original too — the resolver works uniformly, not just for stubs.
	verbatimIdx := -1
	for i := turnStart; i < len(cs.Request.Messages); i++ {
		if cs.Request.Messages[i].Role == RoleTool && !strings.HasPrefix(cs.Request.Messages[i].Content, "[demoted:") {
			verbatimIdx = i
			break
		}
	}
	if verbatimIdx >= 0 {
		if v, err := cs.Recall(fmtCitation(cs.SessionID, verbatimIdx)); err != nil {
			t.Fatalf("Recall of a verbatim result's citation: %v", err)
		} else if strings.Contains(v, "[demoted:") {
			t.Errorf("Recall of the verbatim result at index %d returned a stub", verbatimIdx)
		}
	}
}
