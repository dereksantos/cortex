package main

// Step 3 of issue #220: the checklist fact (checklistMissingItems, prompt.go)
// rides the measurement-only turn receipt's existing surfaces (TurnResult.
// Receipt and the kindNote transcript entry). For a plain turn it is computed
// at the turn's END (after computeReceipt) from the turn's input (the task)
// and its reply (the model's final answer). In plan-then-execute it is
// measured ONCE per run, deterministically at the run's END — no model turn:
// the run's own rendered per-step report (every step's line — step text +
// note — rides in it) is checked off the run's task, and the missing items
// land on PlanRunResult.Receipt through the same joined-receipt surface as
// the per-step blocks — whether or not any step ran tools, and on a failed
// or interrupted run too (the measurement rides the run's deferred stamp,
// which every return path goes through). An item is accounted for when its
// words appear in a step's text or note. No step's prompt
// carries the checklist (a step that accounted for the whole task's
// checklist would flag the items other steps own). The receipt stays
// measurement-only: it never changes the turn's outcome or the reply.
//
// Driven through the REAL turn path (cs.Turn / cs.TurnWithBudget /
// cs.TurnWithPlan) with a scripted model (the senderOverride test-only seam),
// zero network. The real dispatcher runs (no coderDispatcherOverride), so the
// write_file call in the plain-turn and plan cases actually creates the file
// on disk and the receipt's files-changed fact measures it — the checklist
// fact is measured off the same reply the turn produced, alongside the other
// #219 facts.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// receiptChecklistSession builds a scripted, transcript-backed session rooted
// at a temp dir (t.Chdir) so relative tool paths resolve there. The real
// dispatcher runs (no override) — write_file creates the file, the receipt's
// files-changed fact measures it. The session's workspace is NOT a git repo
// (t.TempDir is not a git repo), so the files-changed fact is empty; the
// checklist fact (this step's subject) is the one being pinned.
func receiptChecklistSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if err := os.MkdirAll(ws.SessionsDir(), 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    20000,
		SessionID: "checklist-receipt-test",
		Request:   &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	t.Cleanup(func() { cs.Close() })
	cs.senderOverride = multiTurnScriptedSender(script)
	return cs
}

// checklistTask is a task that carries a two-item checklist (the shape
// taskPrompt extracts): both items are present, so a reply that names both
// verbatim accounts for them (no checklist fact), and a reply that names
// only one leaves the other missing (one checklist line on the receipt).
const checklistTask = "Add a helper and its tests.\n- [ ] add the helper\n- [ ] add the tests\n"

// TestTurnReceiptChecklistIsPresentOnPlainTurn is the issue #220 step-3
// plain-turn e2e: a tools-ran turn whose task carries a checklist and whose
// reply accounts for only ONE item (the other is missing) lands the checklist
// fact on the receipt — the "checklist (not accounted for in the reply):"
// section names the missing item, the receipt rides TurnResult.Receipt (the
// existing #219 surface), the kindNote transcript entry carries it (the
// existing #219 surface), and the reply is UNTOUCHED (measurement-only: the
// receipt never rewrites the model's answer).
func TestTurnReceiptChecklistIsPresentOnPlainTurn(t *testing.T) {
	// Script: round 0 = write_file (the turn ran a tool — the receipt is
	// computed for a tools-ran turn), round 1 = the final answer that
	// accounts for "add the helper" but leaves "add the tests" entirely
	// unmentioned (not even a "deferred"/"not done" account — the item's
	// word never appears, so it is genuinely missing).
	script := []*AgentResponse{
		respWithCalls([]ToolCall{writeFileCall("w1", "helper.go", "package main\n\nfunc helper() {}\n")}),
		respWithAnswer("Added the helper (helper.go:1)."),
	}
	cs := receiptChecklistSession(t, script)
	res, err := cs.Turn(context.Background(), checklistTask)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The reply is untouched: measurement-only means the receipt never
	// rewrites the model's answer — it is the verbatim scripted reply.
	if res.Reply != "Added the helper (helper.go:1)." {
		t.Fatalf("Reply = %q, want the verbatim scripted answer (measurement-only: the receipt must not touch the reply)", res.Reply)
	}
	// The receipt rides TurnResult.Receipt (the existing #219 surface) and
	// carries the checklist section naming the MISSING item — "add the tests"
	// (the reply never mentions it, in any form). The item the reply DID
	// account for ("add the helper") must NOT appear as missing.
	if res.Receipt == "" {
		t.Fatal("TurnResult.Receipt empty for a tools-ran turn with a checklist — the checklist fact did not land")
	}
	if !strings.Contains(res.Receipt, "checklist (not accounted for in the reply):") {
		t.Fatalf("TurnResult.Receipt =\n%s\nwant the checklist section", res.Receipt)
	}
	if !strings.Contains(res.Receipt, "  - add the tests") {
		t.Errorf("TurnResult.Receipt =\n%s\nwant the missing item \"add the tests\" named", res.Receipt)
	}
	if strings.Contains(res.Receipt, "  - add the helper") {
		t.Errorf("TurnResult.Receipt =\n%s\nmust NOT name \"add the helper\" as missing (the reply accounts for it)", res.Receipt)
	}
	// The kindNote transcript entry carries the receipt (the existing #219
	// surface): the receipt survives on the on-disk record, transcript-only.
	if cs.transcript == nil {
		t.Fatal("transcript not started")
	}
	path := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	var sawNote, sawVerbatim bool
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad transcript line %q: %v", line, err)
		}
		if e.Kind == kindNote && strings.Contains(e.Content, "turn receipt:") && strings.Contains(e.Content, "checklist (not accounted for in the reply):") {
			sawNote = true
		}
		if e.Kind == kindMessage && e.Role == "assistant" && e.Content == res.Reply {
			sawVerbatim = true
		}
	}
	if !sawNote {
		t.Errorf("no kindNote transcript entry carrying the checklist receipt")
	}
	if !sawVerbatim {
		t.Errorf("no verbatim assistant reply %q in the transcript", res.Reply)
	}
}

// TestTurnReceiptChecklistLandsOnForcedMaxIterTurn is the issue #220 step-3
// forced-max-iter e2e: when a bound (max-iter) cuts a turn off, the turn never
// took the clean-finalize path — yet the checklist fact must STILL land on
// the receipt (computed at the turn's END, after computeReceipt, BEFORE the
// unrecovered-error return, like the other #219 facts). The scripted model
// issues tool rounds until the cap trips (MaxIter=2); the forced-finalize
// answer is the turn's reply, and it accounts for "add the helper" (it names
// the helper) but never mentions "add the tests" — so the tests item is the
// one missing from the receipt. The point the test pins is that a turn cut
// off at a bound still carries the checklist fact on its receipt.
func TestTurnReceiptChecklistLandsOnForcedMaxIterTurn(t *testing.T) {
	// Script: round 0 = write_file (tool round 1), round 1 = read_file
	// (tool round 2), round 2 = the forced-finalize answer (the cap trips
	// here — MaxIter=2 means the loop runs i=0..1, then stop=max-iter and
	// finalizeLoop sends the forced-finalize prompt). The forced-finalize
	// answer is the turn's reply (content): it names the helper (accounting
	// for "add the helper") but never the tests item.
	script := []*AgentResponse{
		respWithCalls([]ToolCall{writeFileCall("w1", "helper.go", "package main\n\nfunc helper() {}\n")}),
		respWithCalls([]ToolCall{readFileCall("r1", "helper.go")}),
		respWithAnswer("partial work, the helper is started"),
	}
	cs := receiptChecklistSession(t, script)
	res, err := cs.TurnWithBudget(context.Background(), checklistTask, 2, 0)
	if err != nil {
		t.Fatalf("TurnWithBudget: %v", err)
	}
	if res.StopReason != "max-iter" {
		t.Fatalf("stop = %q, want max-iter (the cap must trip)", res.StopReason)
	}
	// The receipt rides TurnResult.Receipt and carries the checklist section.
	// The forced-finalize reply accounts for "add the helper" (it names the
	// helper) but never mentions "add the tests" — so the tests item is the
	// one missing, and the helper item is NOT reported.
	if res.Receipt == "" {
		t.Fatal("TurnResult.Receipt empty for a forced-max-iter tools-ran turn with a checklist — the checklist fact must land even when the turn is cut off")
	}
	if !strings.Contains(res.Receipt, "checklist (not accounted for in the reply):") {
		t.Fatalf("TurnResult.Receipt =\n%s\nwant the checklist section", res.Receipt)
	}
	if !strings.Contains(res.Receipt, "  - add the tests") {
		t.Errorf("TurnResult.Receipt =\n%s\nwant the unmentioned item \"add the tests\" named as missing", res.Receipt)
	}
	if strings.Contains(res.Receipt, "  - add the helper") {
		t.Errorf("TurnResult.Receipt =\n%s\nmust NOT name \"add the helper\" as missing (the reply accounts for it)", res.Receipt)
	}
}

// TestTurnWithPlanChecklistReceiptMeasuresWholeRun is the issue #220 step-3
// plan-then-execute e2e, the POSITIVE case: a plan run whose task carries a
// checklist measures the checklist ONCE per run, deterministically at the
// run's END (no model report turn — the run's reply is the deterministic
// per-step report, exactly the shape renderPlanReport renders). The reply
// the measurement checks is the rendered report itself: every step's line
// carries the STEP TEXT ("2. [done] add the tests — …"), so an item named in
// its own step's text is accounted for even when no step's model reply or
// note named it — "add the tests" (covered by step 2's text) is NOT reported.
// The other item — "update the docs" — appears in no step's text (the model
// planned no docs step at all), so it IS reported missing on
// PlanRunResult.Receipt. The receipt rides PlanRunResult.Receipt (the joined
// per-step blocks plus the run's own checklist block).
func TestTurnWithPlanChecklistReceiptMeasuresWholeRun(t *testing.T) {
	// The task's third item is the one the model never planned a step for —
	// it appears in no step's text, so no coverage path can reach it.
	task := "Add a helper and its tests, and update the docs.\n- [ ] add the helper\n- [ ] add the tests\n- [ ] update the docs\n"
	// Script: planning turn (no tools) → two-step plan (the model plans NO
	// docs step); each step turn (tools) → one write_file, then the step
	// reply. Neither reply nor note names "update the docs" — the step NOTE
	// is the project-check outcome ("check skipped: …"), not the model
	// reply, so step TEXT is what covers the items.
	script := []*AgentResponse{
		// planning turn: a two-step plan (no docs step).
		respWithAnswer("1. add the helper\n2. add the tests"),
		// step 1 turn (tools): one write_file, then the step reply.
		respWithCalls([]ToolCall{writeFileCall("w1", "helper.go", "package main\n\nfunc helper() {}\n")}),
		respWithAnswer("I added the helper (helper.go:1)"),
		// step 2 turn (tools): one write_file, then the step reply.
		respWithCalls([]ToolCall{writeFileCall("w2", "helper_test.go", "package main\n")}),
		respWithAnswer("tests added (helper_test.go:1)"),
	}
	cs := receiptChecklistSession(t, script)
	res, err := cs.TurnWithPlan(context.Background(), task)
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatalf("Planned = false, want true (the planning turn's reply parsed as a two-step plan)")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Status != stepDone {
			t.Errorf("step %d status = %v, want done", i+1, s.Status)
		}
	}
	// The run's reply is the DETERMINISTIC per-step report — no model report
	// turn ran (that would have replaced renderPlanReport's reply with model
	// text and added a tools-enabled turn after the per-step checks).
	if !strings.Contains(res.Reply, "Plan-then-execute") || !strings.Contains(res.Reply, "2/2 steps done") {
		t.Fatalf("Reply =\n%s\nwant the deterministic per-step report (renderPlanReport)", res.Reply)
	}
	if !strings.Contains(res.Reply, "2. [done] add the tests") {
		t.Fatalf("Reply =\n%s\nwant step 2's line to carry the step text (that text is what covers \"add the tests\")", res.Reply)
	}
	// THE positive point: the receipt carries the checklist section — it is
	// measured whether or not a turn's reply accounts for the items, and the
	// rendered report is the reply checked. The item no step's text names IS
	// reported missing...
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty — the checklist fact did not land")
	}
	if !strings.Contains(res.Receipt, "checklist (not accounted for in the reply):") {
		t.Fatalf("PlanRunResult.Receipt =\n%s\nwant the checklist section (the run's checklist account is measured at the run's end)", res.Receipt)
	}
	if !strings.Contains(res.Receipt, "  - update the docs") {
		t.Errorf("PlanRunResult.Receipt =\n%s\nwant the item no step names (\"update the docs\") reported missing", res.Receipt)
	}
	// ...and an item named in its own step's text is NOT: the report's step
	// line carries "add the tests", which covers the item even though no
	// model reply or note ever named it.
	if strings.Contains(res.Receipt, "  - add the tests") {
		t.Errorf("PlanRunResult.Receipt =\n%s\nmust NOT name \"add the tests\" as missing (step 2's line covers it)", res.Receipt)
	}
	if strings.Contains(res.Receipt, "  - add the helper") {
		t.Errorf("PlanRunResult.Receipt =\n%s\nmust NOT name \"add the helper\" as missing (step 1's line covers it)", res.Receipt)
	}
}

// readFileCall returns a ToolCall carrying one read_file call (id, path) —
// the forced-max-iter e2e's second tool round.
func readFileCall(id, path string) ToolCall {
	args, _ := json.Marshal(map[string]any{"path": path})
	return ToolCall{ID: id, Function: FunctionCall{Name: tools.FunctionReadFile, Arguments: string(args)}}
}
