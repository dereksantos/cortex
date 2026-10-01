// testwatch_test.go — issue #141's turn-level receipt: the harness snapshots
// the before-content of the files a turn's write_file/edit_file/remove_path
// calls touch, runs internal/testguard over the diffs at turn end, and the
// resulting "tests changed: …" line rides the capture summary so the turn's
// journal record — and the model's final answer — must account for any tests
// the turn removed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
)

// fixtureGoTest is a small realistic Go test file: three test functions plus a
// helper. The fixture's line count stays above testguard's shrink floor so the
// "shrank" arm only fires when it should.
const fixtureGoTest = "package tools\n\nimport \"testing\"\n\nfunc TestAlpha(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n\nfunc TestBeta(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n\nfunc TestGamma(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n\nfunc helper() string { return \"ok\" }\n"

// testwatchHarness drives one model-free turn through the same seams a real
// turn uses (coderDispatcher for the tool calls, captureTurn for the receipt).
type testwatchHarness struct {
	cs *CortexSession
}

// newTestwatchSession builds a session rooted at root (t.TempDir), with the
// transcript and capture wired exactly as the REPL does, and chdirs the test
// into root so relative tool paths and capture both resolve there.
func newTestwatchSession(t *testing.T, root string) *CortexSession {
	t.Helper()
	t.Chdir(root)
	cs := newMemSession(t)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed")
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// writeCall builds the assistant message carrying one write_file call.
func writeCall(id, path, content string) Message {
	args, _ := json.Marshal(map[string]any{"path": path, "content": content})
	return Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID:       id,
		Function: FunctionCall{Name: FunctionWriteFile, Arguments: string(args)},
	}}}
}

// runTurn drives one turn through the real dispatcher: each assistant message's
// tool calls go through cs.coderDispatcher (the same hook that snapshots
// files), then captureTurn runs over the turn's messages — the full receipt
// path, model-free. The snapshot is dropped at turn START, mirroring turn.go's
// unconditional testwatchDrop, so a stale before-side from an earlier turn
// that never reached captureTurn (error/interrupt) can't leak in.
func (h *testwatchHarness) runTurn(t *testing.T, user string, calls ...Message) {
	t.Helper()
	start := len(h.cs.Request.Messages)
	h.cs.testwatchDrop() // turn-start: no stale before-side carried in
	h.cs.Append(Message{Role: RoleUser, Content: user})
	for _, m := range calls {
		for _, call := range m.ToolCalls {
			out := h.cs.coderDispatcher().Dispatch(context.Background(), call)
			h.cs.Append(Message{Role: RoleTool, ToolCallID: call.ID, Content: out})
		}
	}
	// A final assistant message is the turn's answer.
	h.cs.Append(Message{Role: "assistant", Content: "done"})
	h.cs.captureTurn(user, h.cs.Request.Messages[start:])
}

// lastCaptureResult returns the most recent capture event's ToolResult — the
// same reader Learn uses (scanCaptureWindow), so this proves the receipt is on
// the journal path, not just in memory.
func lastCaptureResult(t *testing.T, cs *CortexSession) string {
	t.Helper()
	entries, _, err := scanCaptureWindow(cs)
	if err != nil {
		t.Fatalf("scanCaptureWindow: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no capture entries on disk")
	}
	return entries[len(entries)-1].Result
}

func TestTestwatchTurnReceiptRemovedTests(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "alpha_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newTestwatchSession(t, root)
	h := &testwatchHarness{cs: cs}

	// The turn rewrites the test file, dropping TestAlpha and TestBeta — the
	// shape of the #127 incident where a failing test was edited out.
	after := "package tools\n\nimport \"testing\"\n\nfunc TestGamma(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n\nfunc helper() string { return \"ok\" }\n"
	h.runTurn(t, "fix the flaky tests",
		writeCall("c1", "pkg/alpha_test.go", after))

	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, "tests changed:") {
		t.Fatalf("capture summary = %q, want a tests changed: line", got)
	}
	if !strings.Contains(got, "pkg/alpha_test.go") {
		t.Errorf("capture summary = %q, want the file path", got)
	}
	if !strings.Contains(got, "2 test definitions removed") {
		t.Errorf("capture summary = %q, want the two removed test definitions counted", got)
	}
	// The receipt renders the sample of the removed definitions so the
	// model sees exactly what disappeared.
	if !strings.Contains(got, "func TestAlpha(t *testing.T) {") {
		t.Errorf("capture summary = %q, want a sample of the removed definitions", got)
	}
	// The snapshot is dropped at turn end: a second turn that only touches a
	// non-test file adds no receipt.
	h.runTurn(t, "follow up",
		writeCall("c2", "README.md", "# x\n"))
	got2 := lastCaptureResult(t, cs)
	if strings.Contains(got2, "tests changed:") {
		t.Errorf("second capture summary = %q, want no tests changed: line (snapshot dropped)", got2)
	}
}

func TestTestwatchTurnReceiptWholeFileDeleted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "beta_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newTestwatchSession(t, root)
	h := &testwatchHarness{cs: cs}

	// The turn deletes the whole test file via write_file (empty content) —
	// the shape of the #127 incident where a test file was sed'd away.
	h.runTurn(t, "remove the unused tests",
		writeCall("c1", "pkg/beta_test.go", ""))

	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, "tests changed:") {
		t.Fatalf("capture summary = %q, want a tests changed: line for a deleted test file", got)
	}
	if !strings.Contains(got, "pkg/beta_test.go") {
		t.Errorf("capture summary = %q, want the file path", got)
	}
	// All three test definitions are gone and the file shrank to nothing.
	if !strings.Contains(got, "3 test definitions removed") {
		t.Errorf("capture summary = %q, want all three test definitions counted", got)
	}
	if !strings.Contains(got, "shrank") {
		t.Errorf("capture summary = %q, want the shrink flag for a file emptied to zero lines", got)
	}
}

func TestTestwatchTurnReceiptPythonFixture(t *testing.T) {
	root := t.TempDir()
	before := "import pytest\n\n\ndef test_one():\n    assert 1 == 1\n\n\ndef test_two():\n    assert 2 == 2\n"
	after := "import pytest\n\n\ndef test_two():\n    assert 2 == 2\n"
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "test_parser.py"), []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newTestwatchSession(t, root)
	h := &testwatchHarness{cs: cs}

	h.runTurn(t, "trim the parser tests",
		writeCall("c1", "app/test_parser.py", after))

	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, "tests changed:") {
		t.Fatalf("capture summary = %q, want a tests changed: line (python fixture)", got)
	}
	if !strings.Contains(got, "app/test_parser.py") {
		t.Errorf("capture summary = %q, want the file path", got)
	}
	if !strings.Contains(got, "1 test definition removed") {
		t.Errorf("capture summary = %q, want the removed def test_one counted", got)
	}
}

// TestTestwatchTurnReceiptBashArmsWorkspace is the issue #141 fix's bash
// contract: a turn that deletes a test through a bash call — the #127
// incident's `sed -i` shape — must produce a receipt naming the file, even
// though bash names no file in its tool call. The turn's bash call arms the
// workspace's test-named files (armTestwatch) before the command runs, so
// the turn-end scan has the before-side to diff against.
func TestTestwatchTurnReceiptBashArmsWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The test file the incident shape deletes: two test functions. The
	// `printf` bash command below rewrites it to drop TestBeta, leaving
	// TestAlpha — a real test-removal via a non-named command.
	if err := os.WriteFile(filepath.Join(root, "pkg", "sed_target_test.go"), []byte(
		"package tools\n\nimport \"testing\"\n\nfunc TestAlpha(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n\nfunc TestBeta(t *testing.T) {\n\tif false {\n\t\tt.Error(\"no\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newTestwatchSession(t, root)
	cs.workspace = mustWorkspace(t, root)
	// Stub the shell-risk classifier as Safe so the `printf >` command clears
	// the gate and actually runs (a gray-zone command with no classifier
	// fails closed to Risky and is blocked in a headless session — the
	// command would never execute and the snapshot would never see a
	// removal). This mirrors a real turn where the classifier judged the
	// rewrite safe.
	cs.classifyShell = func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test stub", nil
	}
	h := &testwatchHarness{cs: cs}

	// The bash command is the #127 shape: rewrite the test file via shell,
	// dropping TestBeta. The command's text doesn't name the file in a way
	// the touch hook can parse — the arm (armTestwatch) must have snapshotted
	// it BEFORE the command ran for the receipt to see the removal.
	bc := bashCall("c1", `printf 'package tools\n\nimport "testing"\n\nfunc TestAlpha(t *testing.T) {\n\tif false {\n\t\tt.Error("no")\n\t}\n}\n' > pkg/sed_target_test.go`)
	h.runTurn(t, "remove the failing test",
		Message{Role: "assistant", ToolCalls: []ToolCall{bc}})

	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, "tests changed:") {
		t.Fatalf("capture summary = %q, want a tests changed: line (bash-armed snapshot)", got)
	}
	if !strings.Contains(got, "pkg/sed_target_test.go") {
		t.Errorf("capture summary = %q, want the file path", got)
	}
	if !strings.Contains(got, "1 test definition removed") {
		t.Errorf("capture summary = %q, want TestBeta counted as removed", got)
	}
}

// TestTestwatchNoDoubleReportAcrossArms is the issue #141 review's
// double-recording contract: the named-tool arm (touchFile) keys its
// snapshot on the path the model named, the bash arm (armTestwatch) keys
// its baselines on the CLEAN relative path it builds from the directory
// walk. A model that spells a path differently ("./pkg/dup_test.go") must
// NOT land the same file under both keys in both stores — the receipt
// would report it twice. The dedupe check (armTestwatch skipping files
// already in cs.testwatch) only catches the spelling that matches the
// walk; the fix (touchFile normalizing via filepath.Clean) makes BOTH
// directions safe. This test pins the reverse direction the unit tests
// don't: a bash call FIRST (arm builds "pkg/dup_test.go"), then a write_file
// with a "./"-spelled path (touchFile builds "pkg/dup_test.go" after the
// fix, "./pkg/dup_test.go" before it).
func TestTestwatchNoDoubleReportAcrossArms(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "dup_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newTestwatchSession(t, root)
	cs.workspace = mustWorkspace(t, root)
	cs.classifyShell = func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Safe, "test stub", nil
	}
	h := &testwatchHarness{cs: cs}

	// The turn: a bash call FIRST (arms the workspace baseline — the file
	// lands in cs.testwatchBash under the walk's clean key), THEN a
	// write_file with a "./"-spelled path that removes TestGamma.
	//
	// Under the OLD touchFile (no normalization), the write_file arm
	// would store the snapshot under "./pkg/dup_test.go" (a NEW key, not
	// seen in cs.testwatchBash), so BOTH stores would carry the file:
	// the receipt would report it once per store — "1 test definition
	// removed" would still appear (the count is per-file, not per-
	// record), but the file PATH would appear TWICE in the receipt.
	// Under the FIX, touchFile normalizes to "pkg/dup_test.go", which
	// IS seen in cs.testwatchBash, so the named-tool arm snapshots it
	// (the full content is more precise than the baseline) and the
	// receipt reports it exactly once.
	//
	// The `true` bash command is a no-op — it just arms the baseline.
	// The write_file is the mutation: it empties the file (all three
	// test definitions gone), so the receipt is non-empty.
	bc := bashCall("c1", "true")
	wc := writeCall("c2", "./pkg/dup_test.go", "")
	h.runTurn(t, "remove the unused tests",
		Message{Role: "assistant", ToolCalls: []ToolCall{bc}},
		wc)

	got := lastCaptureResult(t, cs)
	if !strings.Contains(got, "tests changed:") {
		t.Fatalf("capture summary = %q, want a tests changed: line", got)
	}
	// The file path appears EXACTLY ONCE in the receipt: once per file,
	// not once per snapshot store. A double-recorded file would appear
	// twice — once from the named-tool arm (cs.testwatch), once from the
	// bash arm (cs.testwatchBash) — and the receipt's "tests changed: …"
	// line would list it twice separated by "; ".
	count := strings.Count(got, "pkg/dup_test.go")
	if count != 1 {
		t.Errorf("capture summary = %q: the file path appears %d times, want 1 (the file is recorded once, not once per snapshot store)", got, count)
	}
	if !strings.Contains(got, "3 test definitions removed") {
		t.Errorf("capture summary = %q, want all three test definitions counted", got)
	}
	// The named-tool snapshot has the file under the CLEAN key (the fix's
	// normalization), and the bash baseline has it too — but the receipt
	// de-dupes at render time via the map key, so only one FileReport
	// lands.
	if len(cs.testwatch) != 1 {
		t.Errorf("named-tool snapshot = %d entries, want 1 (./pkg/dup_test.go normalized to pkg/dup_test.go)", len(cs.testwatch))
	}
}

// scriptedModelServer returns an httptest server that serves one scripted
// chat-completions response per request (turnTestBackend's pattern, but
// request-scripted rather than fixed): the i-th request gets script[i], or
// the last entry once the script is exhausted. It records each request's
// wire body in order so a test can inspect what the model saw — e.g. that
// the finalize round's wire carried the harness's "tests changed" note.
func scriptedModelServer(t *testing.T, script []string) (*httptest.Server, *struct {
	n     int
	wires []string
}) {
	t.Helper()
	st := &struct {
		n     int
		wires []string
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		st.n++
		st.wires = append(st.wires, string(body))
		resp := script[len(script)-1] // last entry once the script is exhausted
		if st.n-1 < len(script) {
			resp = script[st.n-1]
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

// testwatchTurnSession builds a quiet, CWD-anchored session rooted at root —
// the headless `cortex turn` shape — ready for a REAL cs.Turn against a
// scripted model server (served at srvURL). The session is hand-built exactly
// as turnTestSessionFactory (serve_turn_test.go) builds its headless sessions
// — bare *CortexSession plus the BaseURL wiring — so the test exercises
// turn.go's actual Toolset construction (coderDispatcher + FinalizeHook) and
// its TurnResult assembly, not a test-side copy of them.
func testwatchTurnSession(t *testing.T, root, srvURL string) *CortexSession {
	t.Helper()
	t.Chdir(root)
	cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	cs.Request.BaseURL = srvURL
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestTestwatchReceiptReachesTurnResult is the issue #141 fix's visible-surface
// contract, driven end to end through cs.Turn — the exact path a headless
// `cortex turn` driver uses — against a scripted model:
//
//  1. The harness hands the model the "tests changed" receipt at finalize, so
//     the model's FINAL answer accounts for the loss (the note carried the
//     scan's facts on the wire).
//  2. The receipt rides TurnResult.TestReceipt — the field turn.go fills and
//     the REPL / cortex turn / self-dev driver print — and the returned
//     REPLY still contains the turn's real work summary (the pre-note answer),
//     not just a reply to the test-loss note. The finalize round APPENDS the
//     model's answer to the original answer rather than replacing it: a small
//     model answers the note narrowly (just the test loss), and a replaced
//     TurnResult.Reply would lose the actual work summary — the headless
//     driver's only output.
//  3. The snapshot is dropped after the turn, so a follow-up turn that
//     touches no test file stays silent (no stale before-side).
func TestTestwatchReceiptReachesTurnResult(t *testing.T) {
	const summary = "Removed the unused tests."
	const ack = "I removed 3 test definitions from pkg/gone_test.go; they were redundant."
	srv, st := scriptedModelServer(t, []string{
		// round 0: the model empties the test file via write_file (all three
		// test definitions gone) — the scripted tool call.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"pkg/gone_test.go\",\"content\":\"\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		// round 1: the model's real final answer (the work summary).
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"` + summary + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		// round 2: the model's reply to the harness's finalize note — it
		// answers the note narrowly (a small model's shape), which the engine
		// APPENDS to the summary rather than replacing it with.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"` + ack + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`,
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "gone_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := testwatchTurnSession(t, root, srv.URL)
	cs.workspace = mustWorkspace(t, root)

	// The REAL turn: turn.go builds the Toolset (coderDispatcher + the
	// testwatch FinalizeHook) and assembles the TurnResult. The scripted
	// model answers through the ordinary HTTP transport, so the whole path —
	// snapshot arm, finalize note, answer assembly, receipt — is the one a
	// self-dev driver would run.
	res, err := cs.Turn(context.Background(), "remove the unused tests")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.StopReason != "clean-finalize" {
		t.Fatalf("stop reason = %q, want clean-finalize", res.StopReason)
	}
	if st.n != 3 {
		t.Fatalf("model round-trips = %d, want 3 (write_file round, summary round, finalize-note round)", st.n)
	}

	// (1) The harness handed the model the receipt at finalize: the wire for
	// the last round carried the note with the scan's facts.
	if len(st.wires) != 3 {
		t.Fatalf("wires recorded = %d, want 3", len(st.wires))
	}
	wire := st.wires[2]
	if !strings.Contains(wire, "Before you finish") {
		t.Fatalf("the finalize round's wire did not carry the harness's note — the model was never told about the removal")
	}
	if !strings.Contains(wire, "tests changed:") || !strings.Contains(wire, "pkg/gone_test.go") {
		t.Errorf("finalize note on the wire = %q, want the 'tests changed' receipt with the file path", wire)
	}

	// (2) The receipt rides TurnResult — the durable, always-present surface
	// a human-facing caller (REPL, cortex turn, a self-dev driver) prints.
	if !strings.Contains(res.TestReceipt, "tests changed:") || !strings.Contains(res.TestReceipt, "pkg/gone_test.go") {
		t.Errorf("TurnResult.TestReceipt = %q, want a tests changed: line with the file path", res.TestReceipt)
	}
	if !strings.Contains(res.TestReceipt, "3 test definitions removed") {
		t.Errorf("TurnResult.TestReceipt = %q, want all three test definitions counted", res.TestReceipt)
	}

	// (3) The RETURNED REPLY still contains the turn's real work summary —
	// the pre-note answer — and the model's note answer rides after it. A
	// replaced answer would have dropped summary; that is the regression this
	// test pins (the headless driver's output is exactly this Reply).
	if !strings.Contains(res.Reply, summary) {
		t.Errorf("TurnResult.Reply = %q, want it to still contain the pre-note work summary %q", res.Reply, summary)
	}
	if !strings.Contains(res.Reply, ack) {
		t.Errorf("TurnResult.Reply = %q, want it to include the model's reply to the finalize note", res.Reply)
	}
	if i1, i2 := strings.Index(res.Reply, summary), strings.Index(res.Reply, ack); i1 >= i2 {
		t.Errorf("TurnResult.Reply = %q, want the summary BEFORE the note's answer", res.Reply)
	}

	// (4) The snapshot is dropped at the next turn's start, so a follow-up
	// turn that touches no test file stays silent (no stale before-side).
	if got := cs.testwatchReceipt(); got != "" {
		// turn.go computes the receipt at turn end, BEFORE the next
		// turn's testwatchDrop; the value is still live in-session until
		// the next turn clears it.
		t.Logf("in-session receipt (expected until the next turn's drop): %q", got)
	}
	cs.testwatchDrop() // the next turn's start would do this
	if got := cs.testwatchReceipt(); got != "" {
		t.Errorf("post-drop receipt = %q, want empty", got)
	}
}

// TestTestwatchNoStaleSnapshotAfterError is the issue #141 snapshot-lifecycle
// contract: a turn that arms a test-file snapshot but fails BEFORE captureTurn
// (the runLoop error/interrupt path returns early) must NOT leak that
// before-side into the next turn. The turn-start testwatchDrop is what clears
// it — without it, turn 2 would diff against a stale before-side and the
// 32-file cap could fill up across turns.
func TestTestwatchNoStaleSnapshotAfterError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "keep_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	// No model server is needed: the test exercises the snapshot lifecycle
	// (arm, drop, silent receipt) directly — no model round-trips happen.
	cs := testwatchTurnSession(t, root, "")

	// Turn 1: the dispatcher arms the snapshot (the model call will error, so
	// captureTurn never runs and the snapshot would survive).
	cs.testwatchDrop() // turn 1 start
	cs.touchFile("pkg/keep_test.go")
	if len(cs.testwatch) != 1 {
		t.Fatalf("turn 1 snapshot armed = %d entries, want 1", len(cs.testwatch))
	}
	// Turn 2 START: the unconditional drop clears the stale before-side.
	cs.testwatchDrop()
	if len(cs.testwatch) != 0 {
		t.Fatalf("after turn 2 start drop, snapshot = %d entries, want 0 (no stale before-side carried in)", len(cs.testwatch))
	}
	// Turn 2 now touches a non-test file only. With no stale before-side, the
	// receipt must be silent — had turn 1's before-side leaked, the scan would
	// have a (now-stale) pkg/keep_test.go entry to diff.
	cs.touchFile("README.md")
	if got := cs.testwatchReceipt(); got != "" {
		t.Errorf("turn 2 receipt = %q, want empty (no stale before-side from the failed turn 1)", got)
	}
}

// TestTestwatchSilentWhenNoTestsLost proves the receipt degrades to silence
// for every non-test-relevant turn: non-test files, a test file that gains a
// test, a test file with only body lines changed, and a turn with no mutating
// calls at all.
func TestTestwatchSilentWhenNoTestsLost(t *testing.T) {
	tests := []struct {
		name   string
		before map[string]string
		after  map[string]string
	}{
		{
			name:   "non-test file rewritten to empty",
			before: map[string]string{"src/main.go": "package main\n\nfunc main() {}\n"},
			after:  map[string]string{"src/main.go": ""},
		},
		{
			name:   "test file gains a test",
			before: map[string]string{"pkg/a_test.go": "package p\n\nfunc TestKeep(t *testing.T) {}\n"},
			after:  map[string]string{"pkg/a_test.go": "package p\n\nfunc TestKeep(t *testing.T) {}\nfunc TestNew(t *testing.T) {}\n"},
		},
		{
			name:   "test file body edit only",
			before: map[string]string{"pkg/b_test.go": fixtureGoTest},
			after:  map[string]string{"pkg/b_test.go": strings.ReplaceAll(fixtureGoTest, "t.Error(\"no\")", "t.Error(\"changed\")")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for p, c := range tc.before {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cs := newTestwatchSession(t, root)
			h := &testwatchHarness{cs: cs}
			var calls []Message
			i := 0
			for _, p := range sortedPaths(tc.after) {
				i++
				calls = append(calls, writeCall(fmt.Sprintf("c%d", i), p, tc.after[p]))
			}
			h.runTurn(t, "do the thing", calls...)
			if got := lastCaptureResult(t, cs); strings.Contains(got, "tests changed:") {
				t.Errorf("capture summary = %q, want no tests changed: line", got)
			}
		})
	}

	t.Run("no mutating calls", func(t *testing.T) {
		cs := newTestwatchSession(t, t.TempDir())
		h := &testwatchHarness{cs: cs}
		h.runTurn(t, "just talk")
		if got := lastCaptureResult(t, cs); strings.Contains(got, "tests changed:") {
			t.Errorf("capture summary = %q, want no tests changed: line", got)
		}
	})
}

// TestTestwatchTouchFileBounds pins the snapshot's bounded, best-effort
// contract: over-cap files, directories, missing paths, and absolute paths
// all degrade to "no snapshot" without failing the turn.
func TestTestwatchTouchFileBounds(t *testing.T) {
	t.Run("over byte cap is not snapshotted", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		// A single file larger than the cap cannot be snapshotted.
		big := make([]byte, testwatchMaxBytes+1)
		if err := os.WriteFile(filepath.Join(root, "big_test.go"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		cs.touchFile("big_test.go")
		if len(cs.testwatch) != 0 {
			t.Errorf("touchFile snapshotted an over-cap file: %d entries", len(cs.testwatch))
		}
	})

	t.Run("directory is not snapshotted", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		cs.touchFile("pkg")
		if len(cs.testwatch) != 0 {
			t.Errorf("touchFile snapshotted a directory: %d entries", len(cs.testwatch))
		}
	})

	t.Run("missing path is not snapshotted", func(t *testing.T) {
		cs := newTestwatchSession(t, t.TempDir())
		cs.touchFile("nope/never_there_test.go")
		if len(cs.testwatch) != 0 {
			t.Errorf("touchFile snapshotted a missing file: %d entries", len(cs.testwatch))
		}
	})

	t.Run("absolute path outside the workdir is not snapshotted", func(t *testing.T) {
		cs := newTestwatchSession(t, t.TempDir())
		cs.touchFile("/etc/passwd")
		if len(cs.testwatch) != 0 {
			t.Errorf("touchFile snapshotted an out-of-workdir absolute path: %d entries", len(cs.testwatch))
		}
	})

	t.Run("absolute path inside the workdir IS snapshotted", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		// newTestwatchSession leaves a CWD-derived workspace (no anchor); set
		// an explicit one so touchFile's workdir-relative normalization kicks
		// in — the issue #141 case where a model edits /root/foo_test.go by
		// absolute path.
		cs.workspace = mustWorkspace(t, root)
		if err := os.WriteFile(filepath.Join(root, "pkg_abs_test.go"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs.touchFile(filepath.Join(root, "pkg_abs_test.go"))
		if len(cs.testwatch) != 1 {
			t.Fatalf("touchFile should snapshot an in-workdir absolute path, got %d entries", len(cs.testwatch))
		}
		// The snapshot keys and displays the WORKDIR-RELATIVE form, not the
		// raw absolute path, so the receipt reads like the relative case.
		if _, ok := cs.testwatch["pkg_abs_test.go"]; !ok {
			got := make([]string, 0, len(cs.testwatch))
			for k := range cs.testwatch {
				got = append(got, k)
			}
			t.Errorf("expected snapshot keyed by workdir-relative path, got keys: %v", got)
		}
	})

	t.Run("file count cap is enforced", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < testwatchMaxFiles+5; i++ {
			p := fmt.Sprintf("pkg/f%d_test.go", i)
			if err := os.WriteFile(filepath.Join(root, p), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cs.touchFile(p)
		}
		if len(cs.testwatch) > testwatchMaxFiles {
			t.Errorf("touchFile snapshotted %d files, want at most %d", len(cs.testwatch), testwatchMaxFiles)
		}
	})
}

// TestArmTestwatch pins the bash-arm contract: a single best-effort walk of
// the workdir (the first bash call per turn) that baselines test-named files
// (skipping .git/.cortex/vendor/node_modules) into cs.testwatchBash — a
// SEPARATE budget from touchFile's full-content snapshot (cs.testwatch), so
// it can't starve the named-tool arm. It does not re-baseline a file
// already snapshotted by a named-tool call in the same turn (the receipt
// would double-report it).
func TestArmTestwatch(t *testing.T) {
	t.Run("no workdir is a no-op", func(t *testing.T) {
		cs := newTestwatchSession(t, t.TempDir())
		// No explicit workspace set: Workdir() returns "".
		cs.armTestwatch()
		if len(cs.testwatchBash) != 0 {
			t.Errorf("armTestwatch baselined %d files with no workdir, want 0", len(cs.testwatchBash))
		}
	})

	t.Run("baselines test-named files in the workdir", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		// Test files: each name carries a test prefix/suffix/extension
		// convention testguard.IsTestPath recognizes.
		testFiles := []string{
			"alpha_test.go",      // _test suffix (Go)
			"pkg/beta_test.go",   // _test suffix (Go)
			"app/test_parser.py", // test_ prefix (Python)
			"app/parser.spec.js", // .spec extension (JS/TS)
		}
		for _, p := range testFiles {
			abs := filepath.Join(root, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// Non-test files: none of these carries a test convention, so the
		// arm must skip them all.
		nonTestFiles := []string{"README.md", "src/main.go", "pkg/notatest.go"}
		for _, p := range nonTestFiles {
			abs := filepath.Join(root, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cs.armTestwatch()
		// Assert on the exact set of baselined keys, not just the count:
		// the old fixture list had a test file IsTestPath does NOT recognize
		// (app/parser.js.spec — extension .spec is not .spec.js) and a
		// non-test file it DOES (pkg/not_a_test.go — _test suffix), which
		// canceled out to the same count by coincidence. The corrected
		// fixtures make the labels true, and an exact-set assertion would
		// have caught that.
		want := map[string]bool{}
		for _, p := range testFiles {
			want[p] = true
		}
		got := map[string]bool{}
		for k := range cs.testwatchBash {
			got[k] = true
		}
		if len(got) != len(want) {
			t.Fatalf("armTestwatch baselined %d files, want %d (test-named only); got %v", len(got), len(want), sortedKeys(got))
		}
		for k := range want {
			if !got[k] {
				t.Errorf("armTestwatch did not baseline the test file %q; keys: %v", k, sortedKeys(got))
			}
		}
		for k := range got {
			if !want[k] {
				t.Errorf("armTestwatch baselined a non-test file %q; keys: %v", k, sortedKeys(got))
			}
		}
	})

	t.Run("skips .git, .cortex, vendor, node_modules", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		for _, d := range []string{".git", ".cortex", "vendor", "node_modules"} {
			abs := filepath.Join(root, d)
			if err := os.MkdirAll(abs, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(abs, "hidden_test.go"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "visible_test.go"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs.armTestwatch()
		if len(cs.testwatchBash) != 1 {
			t.Errorf("armTestwatch baselined %d files, want 1 (visible_test.go only); got %v", len(cs.testwatchBash), sortedKeys(cs.testwatchBash))
		}
		if _, ok := cs.testwatchBash["visible_test.go"]; !ok {
			t.Errorf("armTestwatch did not baseline the visible test file; keys: %v", sortedKeys(cs.testwatchBash))
		}
	})

	t.Run("does not re-baseline files already snapshotted by touchFile", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		if err := os.WriteFile(filepath.Join(root, "already_test.go"), []byte("original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "other_test.go"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// touchFile snapshots already_test.go first (the write_file case).
		cs.touchFile("already_test.go")
		// armTestwatch must NOT re-baseline it (the receipt would
		// double-report it), but SHOULD baseline other_test.go.
		cs.armTestwatch()
		if len(cs.testwatch) != 1 {
			t.Errorf("touchFile + armTestwatch: named-tool snapshot has %d entries, want 1 (already_test.go)", len(cs.testwatch))
		}
		if len(cs.testwatchBash) != 1 {
			t.Errorf("touchFile + armTestwatch: bash baseline has %d entries, want 1 (other_test.go only); got %v", len(cs.testwatchBash), sortedKeys(cs.testwatchBash))
		}
		if _, ok := cs.testwatchBash["already_test.go"]; ok {
			t.Errorf("armTestwatch re-baselined a file already snapshotted by touchFile: %q", "already_test.go")
		}
		if got := cs.testwatch["already_test.go"]; got == nil || got.before != "original\n" {
			t.Errorf("already_test.go before-side = %q, want the ORIGINAL content (not re-snapshotted)", got.before)
		}
	})

	t.Run("respects the bash-baseline cap", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		// testwatchMaxBashBaselines+10 test files: the walk must stop at
		// the cap, not snapshot full content into the named-tool budget.
		for i := 0; i < testwatchMaxBashBaselines+10; i++ {
			p := fmt.Sprintf("f%d_test.go", i)
			if err := os.WriteFile(filepath.Join(root, p), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cs.armTestwatch()
		if len(cs.testwatchBash) > testwatchMaxBashBaselines {
			t.Errorf("armTestwatch baselined %d files, want at most %d", len(cs.testwatchBash), testwatchMaxBashBaselines)
		}
		// The named-tool budget must be untouched: armTestwatch stores
		// baselines, not content, so it never eats the 32-file / 1 MiB
		// touchFile budget.
		if len(cs.testwatch) != 0 {
			t.Errorf("armTestwatch ate the named-tool snapshot budget: %d entries, want 0", len(cs.testwatch))
		}
	})

	t.Run("arms once per turn", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		if err := os.WriteFile(filepath.Join(root, "a_test.go"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs.armTestwatch()
		firstArmed := cs.testwatchBashArmed
		if !firstArmed {
			t.Fatal("first armTestwatch did not set testwatchBashArmed")
		}
		// A second bash call in the same turn must be a no-op: the walk
		// already ran, and re-walking would just re-read the same files.
		cs.armTestwatch()
		if len(cs.testwatchBash) != 1 {
			t.Errorf("second armTestwatch re-walked: %d baselined files, want 1", len(cs.testwatchBash))
		}
		// testwatchDrop (turn start) re-arms for the next turn.
		cs.testwatchDrop()
		if cs.testwatchBashArmed {
			t.Error("testwatchDrop did not reset testwatchBashArmed")
		}
		if len(cs.testwatchBash) != 0 {
			t.Errorf("testwatchDrop did not clear testwatchBash: %d entries, want 0", len(cs.testwatchBash))
		}
		cs.armTestwatch()
		if len(cs.testwatchBash) != 1 {
			t.Errorf("armTestwatch after drop re-baselined %d files, want 1", len(cs.testwatchBash))
		}
	})
}

// sortedPaths returns a map's keys in sorted order — for deterministic
// CALL ORDER, not just deterministic failure messages. A turn's capture
// summary is the harness's own text (file order inside it is sorted), so
// the order in which the turn's tool calls fire is not observable in it;
// but the snapshot BUDGETS (32 files / 1 MiB) are — a map-ordered call
// sequence over a budget-tight fixture could hit a cap in one order and
// not in another, making the assertion flaky.
func sortedPaths[V any](m map[string]V) []string {
	return sortedKeys(m)
}

// sortedKeys returns a map's keys in sorted order — for deterministic
// failure messages.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestArmTestwatchCoversLargeWorkspace is the issue #141 review's headline
// fix: a bash-arm that stores full content under a 32-file / 1 MiB cap
// fills with whichever test files sort first, leaving the receipt blind to
// most test files in a real repo (this one has 213, 122 in cmd/cortex). A
// bash `sed -i` on internal/tools/tools_test.go (the #127 shape) was never
// snapshotted, so it got no receipt. The fix stores a compact per-file
// baseline for EVERY test-named file (up to testwatchMaxBashBaselines) in a
// SEPARATE budget from the named-tool snapshots, so the bash arm can't
// starve touchFile and vice versa. These two cases pin that: (a) a bash
// call removes a test from a file that sorts LAST in a >32-file workspace;
// (b) a bash call (which would previously have filled the content budget)
// is followed by an edit_file removing a test from a late-sorting file, and
// the named-tool snapshot still lands.
func TestArmTestwatchCoversLargeWorkspace(t *testing.T) {
	// testFileContent is a small Go test file with two test functions. The
	// removed test's definition is recognizable by testguard's Go pattern.
	const testFileContent = "package p\n\nimport \"testing\"\n\nfunc TestKeep(t *testing.T) {}\n\nfunc TestGone(t *testing.T) {}\n"

	t.Run("bash removes a test from a late-sorting file in a large workspace", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		cs.classifyShell = func(_ context.Context, _ string) (shellrisk.Level, string, error) {
			return shellrisk.Safe, "test stub", nil
		}
		// A workspace with MORE than testwatchMaxFiles (the old 32-file
		// content cap) test files: the alphabetically EARLIEST ones
		// (f000..f040) fill that budget, so zz_last_test.go — which sorts
		// AFTER all of them — would NOT be snapshotted under the old
		// full-content approach. The new compact-baseline budget
		// (testwatchMaxBashBaselines = 512) comfortably covers all 42
		// files, including the late-sorting one.
		for i := 0; i < 41; i++ {
			p := fmt.Sprintf("f%03d_test.go", i)
			if err := os.WriteFile(filepath.Join(root, p), []byte("package p\n\nfunc TestA(t *testing.T) {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The file a real #127-shaped bash call would touch: sorts last.
		if err := os.WriteFile(filepath.Join(root, "zz_last_test.go"), []byte(testFileContent), 0o644); err != nil {
			t.Fatal(err)
		}
		h := &testwatchHarness{cs: cs}
		// A bash command that removes TestGone from zz_last_test.go. The
		// command names the file in a way touchFile's hook cannot parse —
		// only the arm's baseline covers it. (Using a grep -v pipe to a
		// temp file + mv avoids the quoting pitfalls of `sed -i '...'`
		// through the JSON-encoded tool-call argument.)
		bc := bashCall("c1", "grep -v 'func TestGone' zz_last_test.go > zz.tmp && mv zz.tmp zz_last_test.go")
		h.runTurn(t, "remove the flaky test",
			Message{Role: "assistant", ToolCalls: []ToolCall{bc}})
		got := lastCaptureResult(t, cs)
		if !strings.Contains(got, "tests changed:") {
			t.Fatalf("capture summary = %q, want a tests changed: line (bash-armed baseline for a late-sorting file)", got)
		}
		if !strings.Contains(got, "zz_last_test.go") {
			t.Errorf("capture summary = %q, want the late-sorting file path", got)
		}
		if !strings.Contains(got, "1 test definition removed") {
			t.Errorf("capture summary = %q, want TestGone counted as removed", got)
		}
	})

	t.Run("bash call does not starve a later edit_file snapshot", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		cs.classifyShell = func(_ context.Context, _ string) (shellrisk.Level, string, error) {
			return shellrisk.Safe, "test stub", nil
		}
		// A large workspace: the FIRST bash call in the turn arms the
		// baseline budget. Under the OLD code, that arm would have filled
		// the shared 32-file / 1 MiB content budget, starving the later
		// edit_file's named-tool snapshot — no receipt for the edit.
		for i := 0; i < testwatchMaxBashBaselines; i++ {
			p := fmt.Sprintf("f%03d_test.go", i)
			if err := os.WriteFile(filepath.Join(root, p), []byte("package p\n\nfunc TestA(t *testing.T) {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The file the later edit_file will touch: sorts last, so it is the
		// FIRST candidate the old arm would skip once the budget filled.
		if err := os.WriteFile(filepath.Join(root, "zz_last_test.go"), []byte(testFileContent), 0o644); err != nil {
			t.Fatal(err)
		}
		h := &testwatchHarness{cs: cs}
		// Turn: a bash call (arms the baseline budget) followed by an
		// edit_file that removes TestGone from zz_last_test.go. The
		// edit_file's snapshot MUST land in its own named-tool budget,
		// independent of the bash baseline.
		bc := bashCall("c1", "true")
		ec := editCall("c2", "zz_last_test.go", "func TestGone(t *testing.T) {}\n", "")
		h.runTurn(t, "remove the flaky test",
			Message{Role: "assistant", ToolCalls: []ToolCall{bc}},
			ec)
		got := lastCaptureResult(t, cs)
		if !strings.Contains(got, "tests changed:") {
			t.Fatalf("capture summary = %q, want a tests changed: line (edit_file snapshot not starved by a prior bash arm)", got)
		}
		if !strings.Contains(got, "zz_last_test.go") {
			t.Errorf("capture summary = %q, want the file path", got)
		}
		if !strings.Contains(got, "1 test definition removed") {
			t.Errorf("capture summary = %q, want TestGone counted as removed", got)
		}
	})
}

// editCall builds the assistant message carrying one edit_file call.
func editCall(id, path, oldS, newS string) Message {
	args, _ := json.Marshal(map[string]any{"path": path, "old_string": oldS, "new_string": newS})
	return Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID:       id,
		Function: FunctionCall{Name: FunctionEditFile, Arguments: string(args)},
	}}}
}

// TestTestwatchTouchFileNames pins which tool names arm the snapshot: the two
// writers and the deleter, and nothing else.
func TestTestwatchTouchFileNames(t *testing.T) {
	tests := []struct {
		tool string
		want bool
	}{
		{tools.FunctionWriteFile, true},
		{tools.FunctionEditFile, true},
		{tools.FunctionRemove, true},
		{tools.FunctionReadFile, false},
		{tools.FunctionBash, false},
		{tools.FunctionOutline, false},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			root := t.TempDir()
			cs := newTestwatchSession(t, root)
			if err := os.WriteFile(filepath.Join(root, "workspace_test.go"), []byte("y\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			call := ToolCall{ID: "c1", Function: FunctionCall{Name: tc.tool, Arguments: `{"path":"workspace_test.go"}`}}
			p := testwatchTouchedPath(call)
			cs.touchFile(p)
			if got := len(cs.testwatch) > 0; got != tc.want {
				t.Errorf("touchFile(testwatchTouchedPath(%s)) armed snapshot = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}

	// The issue #141 review's double-recording case: a model that names a
	// path with a "./" prefix (or other non-clean spelling) must get the
	// SAME snapshot key as the armTestwatch walk's clean relative form —
	// otherwise the same file lands under two keys and the receipt
	// double-reports it.
	t.Run("'./'-prefixed path normalizes to the clean key", func(t *testing.T) {
		root := t.TempDir()
		cs := newTestwatchSession(t, root)
		cs.workspace = mustWorkspace(t, root)
		if err := os.WriteFile(filepath.Join(root, "dot_prefixed_test.go"), []byte("y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs.touchFile("./dot_prefixed_test.go")
		if len(cs.testwatch) != 1 {
			t.Fatalf("touchFile snapshotted %d entries, want 1", len(cs.testwatch))
		}
		if _, ok := cs.testwatch["dot_prefixed_test.go"]; !ok {
			got := make([]string, 0, len(cs.testwatch))
			for k := range cs.testwatch {
				got = append(got, k)
			}
			t.Errorf("snapshot key = %v, want the CLEAN relative form (matching the armTestwatch walk's key), not the raw './' spelling", got)
		}
		// The display form is the same clean key, so the receipt reads
		// like the relative case.
		if s := cs.testwatch["dot_prefixed_test.go"]; s != nil && s.display != "dot_prefixed_test.go" {
			t.Errorf("snapshot display = %q, want %q", s.display, "dot_prefixed_test.go")
		}
		// The arm must NOT re-baseline it: one file, one record.
		cs.armTestwatch()
		if _, rebaselined := cs.testwatchBash["dot_prefixed_test.go"]; rebaselined {
			t.Errorf("armTestwatch re-baselined a file touchFile snapshotted under a './' spelling")
		}
	})
}

// TestTestwatchReceiptReachesPlanRun: plan mode (#150, TurnWithPlan) runs its
// work as ordinary turns, so each of them produces issue #141's "tests
// changed" receipt — and the plan run must carry it out to the REPL's /plan
// and `cortex turn --plan` exactly as a single turn's TurnResult does, or a
// test removed inside a plan would reach the journal and the model but never
// the human. Driven through the real TurnWithPlan on its single-turn fallback
// path (an unparseable planning reply), which needs no project check.
func TestTestwatchReceiptReachesPlanRun(t *testing.T) {
	const summary = "Removed the unused tests."
	const ack = "I removed 3 test definitions from pkg/gone_test.go; they were redundant."
	srv, st := scriptedModelServer(t, []string{
		// planning turn: prose, not a step list → single-turn fallback.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"I will just do it."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		// fallback turn, round 0: empty the test file via write_file.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"pkg/gone_test.go\",\"content\":\"\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		// fallback turn, round 1: the work summary.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"` + summary + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		// fallback turn, round 2: the reply to the finalize note.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"` + ack + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":6}}`,
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "gone_test.go"), []byte(fixtureGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := testwatchTurnSession(t, root, srv.URL)
	cs.workspace = mustWorkspace(t, root)

	plan, err := cs.TurnWithPlan(context.Background(), "remove the unused tests")
	if err != nil {
		t.Fatalf("TurnWithPlan: %v", err)
	}
	if plan.Planned {
		t.Fatal("Planned = true, want the single-turn fallback")
	}
	if st.n != 4 {
		t.Fatalf("model round-trips = %d, want 4 (planning, write_file, summary, finalize note)", st.n)
	}
	if !strings.Contains(plan.TestReceipt, "tests changed:") || !strings.Contains(plan.TestReceipt, "pkg/gone_test.go") {
		t.Errorf("PlanRunResult.TestReceipt = %q, want the fallback turn's tests changed: line with the file path", plan.TestReceipt)
	}
	if !strings.Contains(plan.Reply, summary) || !strings.Contains(plan.Reply, ack) {
		t.Errorf("PlanRunResult.Reply = %q, want the summary and the note's answer", plan.Reply)
	}
}

// TestPlanRunResultAddReceipt pins how a plan run accumulates its turns'
// receipts: one per line, in run order, empty receipts skipped.
func TestPlanRunResultAddReceipt(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{name: "none", in: nil, want: ""},
		{name: "all empty", in: []string{"", ""}, want: ""},
		{name: "one", in: []string{"", "tests changed: a_test.go"}, want: "tests changed: a_test.go"},
		{name: "two in order", in: []string{"tests changed: a_test.go", "", "tests changed: b_test.go"}, want: "tests changed: a_test.go\ntests changed: b_test.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r PlanRunResult
			for _, s := range tt.in {
				r.addReceipt(s)
			}
			if r.TestReceipt != tt.want {
				t.Errorf("TestReceipt = %q, want %q", r.TestReceipt, tt.want)
			}
		})
	}
}
