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
	"os/exec"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/tools"
)

// planReceiptTestSession builds a scripted session whose workspace,
// transcript, and project commands are wired so a tools-ran turn produces a
// non-empty measurement receipt: the workspace is a fresh git repository, so
// a step's write_file (an untracked file) measures a files-changed fact
// through the receipt's own git read (receiptFilesChanged). The sender is
// scripted to: the planning turn gets a one-step plan ("run the check"), the
// step turn gets one tool call (write_file) then a final answer. The
// dispatcher runs write_file through the REAL tool path (the format hook,
// the touched-file record) and stubs every other call (no real file access).
func planReceiptTestSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	root := t.TempDir()
	git := exec.Command("git", "init", "-q")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitc := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	gitc("config", "user.email", "t@t")
	gitc("config", "user.name", "t")
	cs := newMemSession(t)
	// Explicit-root workspace: anchors the receipt's measurements to THIS
	// project (cs.Workdir()). A CWD-derived workspace returns "" and the
	// receipt would measure nothing. The empty Config (a non-nil one —
	// WorkspaceTrusted() is untrusted on nil) keeps the format hook on its
	// untrusted path, so this test exercises the receipt's carriage, not
	// the hook's measurement.
	cs.Config = &Config{}
	cs.workspace = &Workspace{Root: root, Explicit: true}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	t.Cleanup(func() { cs.Close() })
	// The project's own commands: the discovered test/build pair (what the
	// model-side bash recorder matches verification runs against) plus a
	// per-file format command, so a turn that edits a .go file records an
	// unformatted fact through the real format-hook path — the receipt the
	// plan carries is measured the way production measures it (no real
	// formatter binary needed: a per-file format command runs through the
	// internal tools' hookRunner seam, which production execs and tests stub).
	cs.projectCommands = projectcmd.Commands{
		Test:   projectcmd.Command{Cmd: "go test ./...", Source: "go.mod"},
		Build:  projectcmd.Command{Cmd: "go build ./...", Source: "go.mod"},
		Format: projectcmd.Command{Cmd: "gofmt -w {file}", PerFile: true, Extends: []string{".go"}, Source: "go.mod"},
	}
	cs.hookState = &tools.PostEditHookState{}
	tools.SetHookCeiling(tools.HookModeAll)
	t.Cleanup(func() { tools.SetHookCeiling(tools.HookModeAll) })
	cs.senderOverride = multiTurnScriptedSender(script)
	origDispatcher := cs.coderDispatcherOverride
	cs.coderDispatcherOverride = func() AgentDispatcher {
		return DispatchFunc(func(ctx context.Context, call ToolCall) string {
			// write_file/edit_file run through the REAL tool path (the format
			// hook — the receipt's unformatted fact — the touched-file
			// record), every other call is stubbed (no real file access).
			if call.Function.Name == tools.FunctionWriteFile || call.Function.Name == tools.FunctionEditFile {
				out, _, err := tools.Execute(ctx, call, cs)
				if err != nil {
					return "Error: " + err.Error()
				}
				return out
			}
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
// and --json surfaces cover --plan exactly like a single turn. Step 1's
// write_file runs through the REAL tool path (the format hook's untrusted
// one-time note — an unformatted fact the hook does NOT record), so the
// receipt the plan carries is the measurement the real path produced.
func TestTurnWithPlanCarriesTurnReceipt(t *testing.T) {
	script := []*AgentResponse{
		// planning turn (no tools): a two-step plan (planStepFloor is 2 —
		// a single numbered line is NOT a plan and would fall back to the
		// single-turn path, which the unparseable test covers instead).
		respWithAnswer("1. run the check\n2. report the result"),
		// step 1 turn (tools): one tool call, then the final answer.
		respWithCalls([]ToolCall{writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n")}),
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
	// Step 1 ran a tool (write_file), so its turn measured the file it
	// left behind: the workspace is a fresh git repository, so the
	// untracked main.go is a files-changed fact (receiptFilesChanged's
	// `git status --porcelain` read). The format hook stays on its
	// untrusted path (the one-time inactive note is NOT an unformatted
	// fact), so the receipt rides the plan result measuring exactly what
	// the real path produced — nothing more. Step 2 ran no tools, so its
	// turn measured nothing and addTurnReceipt adds nothing for it.
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty for a plan whose step 1 ran tools — addTurnReceipt did not carry the step's measurement")
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
		respWithCalls([]ToolCall{writeFileCall("w1", "main.go", "package main\n\nfunc main() {}\n")}),
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
	// The fallback turn ran a tool (write_file), so its measurement receipt
	// rides the plan result via the deferred stamp.
	if res.Receipt == "" {
		t.Fatal("PlanRunResult.Receipt empty for an unparseable fallback turn that ran tools — the deferred stamp must carry it")
	}
}
