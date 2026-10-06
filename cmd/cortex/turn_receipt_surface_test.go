package main

// Step 2 of issue #219: the turn receipt lands on its two required
// surfaces — the turn's visible reply (the distinct TurnResult.Receipt
// field, which callers print; the receipt is harness output and is never
// appended to the model's reply) and the session transcript (persisted in
// turn.go as a transcript-only kindNote entry that loadSession skips, so a
// resumed session never replays it into the model's context) — and the
// journal's capture.event carries it (folded into captureTurn's summary).
// A tools-ran turn's receipt must appear in both the raw transcript file
// and the journal capture entry.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/projectcmd"
)

// receiptTestCmds is the project command set a receipt test drives the
// final verification through (the goLintProjectCmds shape, test and build
// roles).
func receiptTestCmds() projectcmd.Commands {
	return projectcmd.Commands{
		Test:  projectcmd.Command{Cmd: "go test ./..."},
		Build: projectcmd.Command{Cmd: "go build ./..."},
	}
}

// receiptTranscriptText re-reads the session's raw transcript file and
// returns its full content (every entry, every line) — the on-disk record
// this test exists to prove the receipt actually persists to, not just
// holds in memory.
func receiptTranscriptText(t *testing.T, cs *CortexSession) string {
	t.Helper()
	path := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading transcript: %v", err)
	}
	return string(b)
}

// TestTurnReceiptSurfaces is the step 2 acceptance test for issue #219: a
// turn that runs tools carries its measurement receipt on ALL THREE
// surfaces — (1) the turn's visible reply, as the distinct TurnResult.Receipt
// field (callers print it; the model's reply is never rewritten, the receipt
// is harness output, not model content), (2) the raw session transcript file,
// and (3) the journal's capture.event (the capture summary). The receipt
// never pollutes the model's own stored messages — the stored assistant
// message carries the model's verbatim reply, and the transcript's copy
// rides a transcript-only kindNote entry that loadSession skips, so resume
// never replays the receipt into the model's context.
func TestTurnReceiptSurfaces(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cs := newMemSession(t)
	// An explicit-root workspace anchors the receipt's final verification
	// (cs.Workdir()): the harness's own run of the project's test/build
	// commands has a working directory. A CWD-derived (non-explicit)
	// workspace returns "" and the final run is skipped — the receipt would
	// measure nothing.
	cs.workspace = &Workspace{Root: root, Explicit: true}
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	defer cs.transcript.Close()
	cs.projectCommands = receiptTestCmds()
	// Script the model: one tool round (a read_file call), then a final
	// answer. The bash recorder records nothing (no bash call) — the
	// receipt's content is the harness's OWN final verification runs of the
	// project's test/build commands (the go test / go build exit codes),
	// which is exactly the "harness made the run itself, independent of the
	// model" measurement.
	script := []*AgentResponse{
		respWithCalls([]ToolCall{readCall("r1", "go.mod")}),
		respWithAnswer("checked the build"),
	}
	cs.senderOverride = multiTurnScriptedSender(script)

	res, err := cs.Turn(context.Background(), "check the build")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// --- Surface 1: the visible reply carries the receipt. --------------
	// The receipt is the distinct TurnResult.Receipt field — harness output
	// a caller chooses to print, NOT content appended to the model's reply.
	if res.Receipt == "" {
		t.Fatal("TurnResult.Receipt empty for a tools-ran turn — the measurement produced nothing")
	}
	if strings.Contains(res.Reply, res.Receipt) {
		t.Errorf("TurnResult.Reply = %q, must not have the harness receipt %q appended — the receipt is harness output, not model content (it rides the distinct Receipt field)", res.Reply, res.Receipt)
	}

	// --- Surface 2: the raw transcript file holds the receipt. ---------
	// The receipt persists as a transcript-only kindNote entry (loadSession
	// skips it, so resume never replays it into the model's context) — the
	// on-disk record the receipt rides on. The receipt is JSON-escaped in
	// the file (newlines become \n), so anchor on a single-line fact: the
	// test role's verification line.
	transcript := receiptTranscriptText(t, cs)
	wantLine := "test: go test ./... (exit 0, "
	if !strings.Contains(transcript, wantLine) {
		t.Errorf("transcript file does not contain the receipt's test verification line %q", wantLine)
	}

	// --- Surface 3: the journal capture event carries it. ---------------
	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, res.Receipt) {
		t.Errorf("capture summary = %q, want the turn receipt %q folded in", got, res.Receipt)
	}

	// --- The model's own messages are not polluted. --------------------
	// The receipt is harness output: NO assistant message in the session
	// carries it — every stored assistant message holds the model's verbatim
	// output, the final one verbatim equal to res.Reply.
	assistantMsgs := 0
	for _, m := range cs.Request.Messages {
		if m.Role == "assistant" && m.Content != "" {
			assistantMsgs++
			if strings.Contains(m.Content, res.Receipt) {
				t.Errorf("an assistant message carries the receipt — the receipt must not pollute the model's own output\nmsg: %q", m.Content)
			}
		}
	}
	if assistantMsgs == 0 {
		t.Error("no assistant messages in the session — the turn did not run")
	}
}
