package main

// Step 3 of issue #220: the checklist fact (checklistMissingItems, prompt.go)
// rides the measurement-only turn receipt's existing surfaces (TurnResult.
// Receipt and the kindNote transcript entry) — computed at the turn's END
// (after computeReceipt) from the turn's input (the task) and its reply (the
// model's final answer), and aggregated per step in plan-then-execute
// (PlanRunResult.Receipt joins distinct blocks, one per step that measured
// something, first step owning an item wins). The receipt stays
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
	// accounts for "add the helper" verbatim but NOT "add the tests".
	script := []*AgentResponse{
		respWithCalls([]ToolCall{writeFileCall("w1", "helper.go", "package main\n\nfunc helper() {}\n")}),
		respWithAnswer("add the helper (helper.go:1) is done; the tests are deferred."),
	}
	cs := receiptChecklistSession(t, script)
	res, err := cs.Turn(context.Background(), checklistTask)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The reply is untouched: measurement-only means the receipt never
	// rewrites the model's answer — it is the verbatim scripted reply.
	if res.Reply != "add the helper (helper.go:1) is done; the tests are deferred." {
		t.Fatalf("Reply = %q, want the verbatim scripted answer (measurement-only: the receipt must not touch the reply)", res.Reply)
	}
	// The receipt rides TurnResult.Receipt (the existing #219 surface) and
	// carries the checklist section naming the MISSING item — "add the tests"
	// (the reply did not name it verbatim). The item the reply DID name
	// verbatim ("add the helper") must NOT appear as missing.
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
		t.Errorf("TurnResult.Receipt =\n%s\nmust NOT name \"add the helper\" as missing (the reply accounts for it verbatim)", res.Receipt)
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
// issues tool rounds until the cap trips (MaxIter=2), the forced-finalize
// answer is the turn's reply, and the reply does NOT name either checklist
// item verbatim — so BOTH items are missing from the receipt.
func TestTurnReceiptChecklistLandsOnForcedMaxIterTurn(t *testing.T) {
	// Script: round 0 = write_file (tool round 1), round 1 = read_file
	// (tool round 2), round 2 = the forced-finalize answer (the cap trips
	// here — MaxIter=2 means the loop runs i=0..1, then stop=max-iter and
	// finalizeLoop sends the forced-finalize prompt). The forced-finalize
	// answer is the turn's reply (content): it names NEITHER checklist item
	// verbatim, so both are missing.
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
	// The receipt rides TurnResult.Receipt and carries the checklist section
	// with BOTH items missing (the forced-finalize reply named neither).
	if res.Receipt == "" {
		t.Fatal("TurnResult.Receipt empty for a forced-max-iter tools-ran turn with a checklist — the checklist fact must land even when the turn is cut off")
	}
	if !strings.Contains(res.Receipt, "checklist (not accounted for in the reply):") {
		t.Fatalf("TurnResult.Receipt =\n%s\nwant the checklist section", res.Receipt)
	}
	for _, item := range []string{"add the helper", "add the tests"} {
		if !strings.Contains(res.Receipt, "  - "+item) {
			t.Errorf("TurnResult.Receipt =\n%s\nwant %q named as missing (the forced-finalize reply did not account for it)", res.Receipt, item)
		}
	}
}

// TestTurnWithPlanChecklistReceiptJoinsStepBlocks is the issue #220 step-3
// plan-then-execute e2e: a plan run whose task carries a checklist aggregates
// the per-step measurement receipts — PlanRunResult.Receipt joins the
// distinct blocks (addTurnReceipt, one per step that measured something), and
// the FIRST step that owns a checklist item (its step prompt includes the
// task, so its step turn's input carries the checklist) produces the
// checklist fact. Step 1's reply accounts for "add the helper" verbatim;
// "add the tests" is missing from it, so the joined receipt carries a
// "checklist (not accounted for in the reply):" block naming "add the tests".
func TestTurnWithPlanChecklistReceiptJoinsStepBlocks(t *testing.T) {
	// Script: planning turn (no tools) → two-step plan; step 1 turn (tools)
	// → write_file then the step-1 reply (names "add the helper" verbatim, not
	// "add the tests"); step 2 turn (no tools) → a plain reply.
	script := []*AgentResponse{
		// planning turn: a two-step plan.
		respWithAnswer("1. add the helper\n2. add the tests"),
		// step 1 turn (tools): one write_file, then the step reply.
		respWithCalls([]ToolCall{writeFileCall("w1", "helper.go", "package main\n\nfunc helper() {}\n")}),
		respWithAnswer("add the helper (helper.go:1) is done"),
		// step 2 turn (no tools): plain reply.
		respWithAnswer("reported"),
	}
	cs := receiptChecklistSession(t, script)
	res, err := cs.TurnWithPlan(context.Background(), checklistTask)
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatalf("Planned = false, want true (the planning turn's reply parsed as a two-step plan)")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(res.Steps))
	}
	// The plan's Receipt joins the step blocks: step 1 ran tools and its input
	// (planStepPrompt(task, …)) carried the checklist, so step 1's turn
	// measured the checklist fact — "add the tests" is missing from step 1's
	// reply. Step 2 ran no tools (no receipt block for it). The joined
	// receipt must carry the checklist section naming the missing item.
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty for a plan whose step 1 ran tools with a checklist task — the checklist fact did not land")
	}
	if !strings.Contains(res.Receipt, "checklist (not accounted for in the reply):") {
		t.Fatalf("PlanRunResult.Receipt =\n%s\nwant the checklist section", res.Receipt)
	}
	if !strings.Contains(res.Receipt, "  - add the tests") {
		t.Errorf("PlanRunResult.Receipt =\n%s\nwant the missing item \"add the tests\" named (step 1's reply did not account for it)", res.Receipt)
	}
	if strings.Contains(res.Receipt, "  - add the helper") {
		t.Errorf("PlanRunResult.Receipt =\n%s\nmust NOT name \"add the helper\" as missing (step 1's reply accounts for it verbatim)", res.Receipt)
	}
}

// readFileCall returns a ToolCall carrying one read_file call (id, path) —
// the forced-max-iter e2e's second tool round.
func readFileCall(id, path string) ToolCall {
	args, _ := json.Marshal(map[string]any{"path": path})
	return ToolCall{ID: id, Function: FunctionCall{Name: tools.FunctionReadFile, Arguments: string(args)}}
}
