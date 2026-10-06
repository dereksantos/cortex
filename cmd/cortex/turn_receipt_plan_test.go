package main

// Step 3 of issue #219: the turn receipt surfaces at the human-facing
// boundaries. This file holds the two plan-mode receipt tests that drive
// the REAL TurnWithPlan path with a scripted model (the senderOverride +
// coderDispatcherOverride seams, no network) and assert the per-turn
// measurement receipts land on PlanRunResult.Receipt (addTurnReceipt),
// which cli.go's runTurnCLI maps to TurnResult.Receipt — so the non-JSON
// stderr and --json surfaces cover --plan exactly like a single turn.

import (
	"context"
	"strings"
	"testing"
)

// planReceiptTestSession builds a scripted session whose workspace,
// transcript, and project commands are wired so a tools-ran turn produces
// a non-empty measurement receipt (the harness's own final verification
// runs of the project's test/build commands). The sender is scripted to:
// the planning turn gets a one-step plan ("run the check"), the step turn
// gets one tool call (read_file) then a final answer. The dispatcher
// stubs the tool call so no real file access happens.
func planReceiptTestSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	cs := newMemSession(t)
	// Explicit-root workspace: anchors the receipt's final verification
	// (cs.Workdir()). A CWD-derived workspace returns "" and the final
	// run is skipped — the receipt would measure nothing.
	cs.workspace = &Workspace{Root: root, Explicit: true}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	cs.projectCommands = receiptTestCmds()
	cs.senderOverride = multiTurnScriptedSender(script)
	origDispatcher := cs.coderDispatcherOverride
	cs.coderDispatcherOverride = func() AgentDispatcher {
		return DispatchFunc(func(_ context.Context, _ ToolCall) string {
			return "ok"
		})
	}
	t.Cleanup(func() { cs.coderDispatcherOverride = origDispatcher })
	return cs
}

// TestTurnWithPlanCarriesTurnReceipt is the step 3 acceptance test for
// issue #219: a plan run whose steps run tools carries their measurement
// receipts on PlanRunResult.Receipt (addTurnReceipt joins distinct blocks
// with "\n\n", one per step that measured something). This is the field
// cli.go's runTurnCLI maps to TurnResult.Receipt, so the non-JSON stderr
// and --json surfaces cover --plan exactly like a single turn.
func TestTurnWithPlanCarriesTurnReceipt(t *testing.T) {
	script := []*AgentResponse{
		// planning turn (no tools): a two-step plan (planStepFloor is 2 —
		// a single numbered line is NOT a plan and would fall back to the
		// single-turn path, which the unparseable test covers instead).
		respWithAnswer("1. run the check\n2. report the result"),
		// step 1 turn (tools): one tool call, then the final answer.
		respWithCalls([]ToolCall{readCall("r1", "go.mod")}),
		respWithAnswer("checked the build"),
		// step 2 turn (tools): plain answer, no tool call.
		respWithAnswer("reported the result"),
	}
	cs := planReceiptTestSession(t, script)

	res, err := cs.TurnWithPlan(context.Background(), "check the build")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if !res.Planned {
		t.Fatalf("Planned = false, want true (the planning turn's reply parsed as a two-step plan)")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(res.Steps))
	}
	if res.Steps[0].Status != stepDone {
		t.Fatalf("Steps[0].Status = %q, want %q", res.Steps[0].Status, stepDone)
	}
	// Step 1 ran a tool (read_file), so its turn measured the harness's
	// own final verification runs — the receipt must ride the plan result.
	// Step 2 ran no tools, so its turn measured nothing and addTurnReceipt
	// adds nothing for it — the plan's Receipt carries exactly step 1's
	// block.
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty for a plan whose step 1 ran tools — addTurnReceipt did not carry the step's measurement")
	}
	if !strings.Contains(res.Receipt, "verification:") {
		t.Errorf("PlanRunResult.Receipt = %q, want the verification section (the harness's own final runs)", res.Receipt)
	}
}

// TestTurnWithPlanUnparseableFallbackCarriesTurnReceipt covers the
// UNPARSEABLE fallback path: when the planning turn's reply is not a
// numbered list, the whole task runs as a single fallback turn (the
// "unparseable" case, #150) — and that turn's measurement receipt must
// ride the plan result too (the deferred stamp puts it on whichever result
// the run returns, so no return path can drop one).
func TestTurnWithPlanUnparseableFallbackCarriesTurnReceipt(t *testing.T) {
	script := []*AgentResponse{
		// planning turn: a PROSE reply, not a numbered list — unparseable.
		respWithAnswer("I'll just do it"),
		// fallback turn (tools): one tool call, then the final answer.
		respWithCalls([]ToolCall{readCall("r1", "go.mod")}),
		respWithAnswer("done the whole thing"),
	}
	cs := planReceiptTestSession(t, script)

	res, err := cs.TurnWithPlan(context.Background(), "check the build")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if res.Planned {
		t.Fatal("Planned = true, want false (the planning turn's prose reply is unparseable — the fallback ran)")
	}
	// The fallback turn ran a tool, so its measurement receipt rides the
	// plan result via the deferred stamp.
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty for an unparseable fallback turn that ran tools — the deferred stamp must carry it")
	}
	if !strings.Contains(res.Receipt, "verification:") {
		t.Errorf("PlanRunResult.Receipt = %q, want the verification section", res.Receipt)
	}
}
