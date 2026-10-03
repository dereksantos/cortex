// forced_finalize_turn_test.go — issue #161, steps 4 & 5: the session-side
// wiring in cs.turn (turn.go). When a bound (max-iter here) drags the run to
// its forced finalize, the turn never took the clean-finalize path, so
// Toolset.FinalizeHook never ran and the testwatch / turn-end-lint receipts
// would vanish. This file locks the SESSION-LEVEL contract:
//
//   - cs.turn sets ts.OnForcedFinalize to the forced-finishing framing that
//     reuses testwatchFinalizeNote + turnLintAtFinalize;
//   - when the turn was cut off with a scratch file left behind (issue #154),
//     the forced-finalize note names the leftover and asks the model to
//     account for it — the wire carries the "cut off at the tool-call limit"
//     framing;
//   - when there is nothing to report (no leftover debug, no lint findings),
//     the note is empty and the forced answer is untouched (no extra round);
//   - the hook never fires on a clean finalize (the turn's normal exit path).
//
// Step 4 pins the wiring via TurnWithBudget (the loop-firing path); step 5
// pins it end-to-end via cs.Turn with the limits.max_tool_iterations config
// override (the interactive REPL / headless `cortex turn` path) — the same
// turn_test.go pattern: hand-built session + senderOverride + scripted model.
//
// Driven through the REAL turn path (cs.turn via the senderOverride test-only
// seam) with a scripted model, zero network. The real coderDispatcher runs
// (no override), so the turn-end scans see the same file state a production
// turn would — the write_file call actually creates the file on disk.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

// forcedFinalizeScriptedSession builds a scripted session whose coder's
// round-trip is driven by script (the fake Sender) — zero network — while
// the REAL coderDispatcher runs (the write_file call creates the file on
// disk, the turn-end scans see it). The session is rooted at a temp dir
// (t.Chdir) so relative tool paths resolve there.
func forcedFinalizeScriptedSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    20000,
		SessionID: "forced-finalize-test",
		Request:   &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })

	var i int
	cs.senderOverride = SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := script[i]
		if i < len(script)-1 {
			i++
		}
		return r, false, nil
	})
	return cs
}

// lintNoteShapeSession is a forcedFinalizeScriptedSession with the turn-end
// lint pass armed and TRUSTED — the shape the note's lint half reads
// (cs.runTurnLint): a {dir} lint over .go files that reports one finding on
// every run it is asked to make, so the raw receipt the note folds in
// carries the finding ("lint: … — …finding…"). The stub hookRunner
// (tools.SetHookRunner) is restored by the test's cleanup; the ceiling is
// pinned to "all" the way turn_lint_test.go's turnLintSession does.
func lintNoteShapeSession(t *testing.T, script []*AgentResponse) *CortexSession {
	t.Helper()
	cs := forcedFinalizeScriptedSession(t, script)
	// The trust gate reads cs.Config.WorkspaceTrusted: a nil config is
	// untrusted (tool_deps.go), so the session needs a config. The
	// user-level trust list must name the session's OWN dir — redirect
	// CORTEX_HOME to a private temp home and write the list there, the way
	// turn_lint_test.go's corpusTrustUserConfig does.
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"project": {"trusted": ["`+cs.Workdir()+`"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cs.Config = &Config{}
	cs.hookState = &tools.PostEditHookState{}
	cs.projectCommands = goLintProjectCmds()
	tools.SetHookCeiling(tools.HookModeAll)
	t.Cleanup(func() { tools.SetHookCeiling(tools.HookModeAll) })
	prev := tools.SetHookRunner(func(_ context.Context, _ []string, _ string) (time.Duration, string, error) {
		return 10 * time.Millisecond, "LINT-FINDING: unused variable in pkg", nil
	})
	t.Cleanup(func() { tools.SetHookRunner(prev) })
	return cs
}

// writeFileCallResp returns an AgentResponse carrying one write_file tool
// call (id, path, content).
func writeFileCallResp(id, path, content string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path, "content": content})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       id,
					Function: FunctionCall{Name: "write_file", Arguments: string(args)},
				}},
			},
		}},
	}
}

// readCallResp returns an AgentResponse carrying one read_file tool call.
func readCallResp(id, path string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       id,
					Function: FunctionCall{Name: "read_file", Arguments: string(args)},
				}},
			},
		}},
	}
}

// answerResp returns an AgentResponse carrying a plain final answer.
func answerResp(content string) *AgentResponse {
	return &AgentResponse{
		Choices: []Choice{{Message: Message{Role: "assistant", Content: content}}},
	}
}

// TestForcedFinalizeWiringScratchFile is the issue #161 step-4 session-side
// contract: when a turn is cut off at the tool-call cap (max-iter) with a
// scratch file left behind, the forced-finalize note names the leftover and
// asks the model to account for it — the wire carries the forced-finishing
// framing, and the answer has the note round's reply appended.
func TestForcedFinalizeWiringScratchFile(t *testing.T) {
	// Script: round 0 = write_file (creates the scratch file), round 1 =
	// read_file (another tool round to push past the cap), round 2 = the
	// forced finalize answer, round 3 = the note round's reply.
	script := []*AgentResponse{
		writeFileCallResp("c1", "zz_debug_tmp.go", "package main\nfunc main() {}\n"),
		readCallResp("c2", "existing.go"),
		answerResp("partial work"),
		answerResp("I left zz_debug_tmp.go behind; it should be removed."),
	}

	cs := forcedFinalizeScriptedSession(t, script)
	// Drive the REAL turn path with a 2-round cap: the model issues 2 tool
	// calls (rounds 0 and 1), the cap trips at round 2 (max-iter), and the
	// forced finalize runs. OnForcedFinalize should fire with the
	// leftover-debug framing.
	res, err := cs.TurnWithBudget(context.Background(), "create a file", 2, 0)
	if err != nil {
		t.Fatalf("TurnWithBudget: %v", err)
	}
	if res.StopReason != "max-iter" {
		t.Fatalf("stop = %q, want max-iter", res.StopReason)
	}

	// The forced-finalize note must have fired: the wire for the note round
	// carried the forced-finishing framing. Find the last user message that
	// carries the "cut off" framing (it is the OnForcedFinalize note, which
	// comes AFTER the forced finalize prompt).
	var foundFraming bool
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser && strings.Contains(m.Content, "cut this turn off at the tool-call limit") {
			foundFraming = true
			if !strings.Contains(m.Content, "leftover debug") {
				t.Errorf("forced-finalize note = %q, want the leftover-debug receipt", m.Content)
			}
			if !strings.Contains(m.Content, "zz_debug_tmp.go") {
				t.Errorf("forced-finalize note = %q, want the scratch file named", m.Content)
			}
		}
	}
	if !foundFraming {
		t.Fatal("no message on the wire carried the forced-finalize framing — the model was never told the turn was cut off with leftover work")
	}

	// The answer has the note round's reply appended.
	want := "partial work\n\nI left zz_debug_tmp.go behind; it should be removed."
	if res.Reply != want {
		t.Errorf("Reply = %q, want the forced answer with the note round's reply appended: %q", res.Reply, want)
	}

	// The DebugReceipt rides the TurnResult (the human-facing surface).
	if !strings.Contains(res.DebugReceipt, "zz_debug_tmp.go") {
		t.Errorf("DebugReceipt = %q, want the scratch file named", res.DebugReceipt)
	}
}

// TestForcedFinalizeWiringClean is the no-op case: when the turn is cut off
// at the cap but there is nothing to report (no leftover debug, no lint
// findings), the forced-finalize note is empty and the forced answer is
// untouched — no extra round.
func TestForcedFinalizeWiringClean(t *testing.T) {
	// Script: 2 tool rounds (the cap), then the forced finalize answer.
	// No scratch files, no test file removals, no lint findings → the
	// OnForcedFinalize note is empty → no note round.
	script := []*AgentResponse{
		readCallResp("c1", "pkg_a.go"),
		readCallResp("c2", "pkg_b.go"),
		answerResp("clean forced answer"),
	}

	cs := forcedFinalizeScriptedSession(t, script)
	// Create a non-scratch file so the read_file calls have something to
	// read (the real dispatcher will read it).
	if err := os.WriteFile("pkg_a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pkg_b.go", []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := cs.TurnWithBudget(context.Background(), "read files", 2, 0)
	if err != nil {
		t.Fatalf("TurnWithBudget: %v", err)
	}
	if res.StopReason != "max-iter" {
		t.Fatalf("stop = %q, want max-iter", res.StopReason)
	}
	// No note round: the answer is the forced answer, untouched.
	if res.Reply != "clean forced answer" {
		t.Errorf("Reply = %q, want the forced answer untouched (no leftover to report)", res.Reply)
	}
	// No message on the wire carries the forced-finalize framing.
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser && strings.Contains(m.Content, "cut this turn off at the tool-call limit") {
			t.Fatalf("unexpected forced-finalize framing on the wire: %q (nothing to report)", m.Content)
		}
	}
}

// TestForcedFinalizeWiringCleanFinalize is the never-fire case: when the
// turn ends with a clean finalize (the model answers with no tool calls
// before the cap), OnForcedFinalize is never consulted.
func TestForcedFinalizeWiringCleanFinalize(t *testing.T) {
	// Script: 1 tool round, then a clean answer (no tool calls) →
	// clean-finalize, not max-iter. The cap is 100, so the model finishes
	// well before it.
	script := []*AgentResponse{
		readCallResp("c1", "a.go"),
		answerResp("clean answer"),
	}

	cs := forcedFinalizeScriptedSession(t, script)
	if err := os.WriteFile("a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := cs.TurnWithBudget(context.Background(), "read a file", 100, 0)
	if err != nil {
		t.Fatalf("TurnWithBudget: %v", err)
	}
	if res.StopReason != "clean-finalize" {
		t.Fatalf("stop = %q, want clean-finalize", res.StopReason)
	}
	if res.Reply != "clean answer" {
		t.Errorf("Reply = %q, want the clean answer untouched", res.Reply)
	}
}

// forcedFinalizeTurnSession builds a scripted session for the cs.Turn path
// (step 5's end-to-end pattern): a hand-built session with a Config that
// sets Limits.MaxToolIterations to the given cap, driven by the
// senderOverride seam (zero network). The REAL coderDispatcher runs, so
// the write_file call creates the file on disk and the turn-end scans see
// it. This is the turn_test.go pattern — the interactive REPL / headless
// `cortex turn` path — as opposed to step 4's TurnWithBudget (the loop-
// firing path).
func forcedFinalizeTurnSession(t *testing.T, script []*AgentResponse, maxIter int) *CortexSession {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    20000,
		SessionID: "forced-finalize-turn-test",
		Config:    &Config{Limits: LimitsConfig{MaxToolIterations: maxIter}},
		Request:   &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })

	var i int
	cs.senderOverride = SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := script[i]
		if i < len(script)-1 {
			i++
		}
		return r, false, nil
	})
	return cs
}

// TestForcedFinalizeE2EScratchFile is the issue #161 step-5 end-to-end
// contract, driven through the REAL cs.Turn path (the interactive REPL /
// headless `cortex turn` path) with the maxIter cap set via the
// limits.max_tool_iterations config override:
//
//   - a turn that exceeds the cap stops max-iter (not an error);
//   - the forced-finalize receipt round is on the wire: the OnForcedFinalize
//     note carries the "cut off at the tool-call limit" framing and names
//     the leftover scratch file (the debug receipt);
//   - the receipts ride the TurnResult: DebugReceipt names the scratch file;
//   - the model's forced-finalize reply is included in TurnResult.Reply
//     (the forced answer with the note round's reply appended);
//   - the stop reason on the TurnResult is "max-iter".
func TestForcedFinalizeE2EScratchFile(t *testing.T) {
	// Script: round 0 = write_file (creates the scratch file), round 1 =
	// read_file (pushes past the 2-round cap), round 2 = the forced finalize
	// answer, round 3 = the note round's reply.
	script := []*AgentResponse{
		writeFileCallResp("c1", "zz_debug_tmp.go", "package main\nfunc main() {}\n"),
		readCallResp("c2", "existing.go"),
		answerResp("partial work done"),
		answerResp("I left zz_debug_tmp.go behind; it should be removed."),
	}

	cs := forcedFinalizeTurnSession(t, script, 2)
	// Drive the REAL cs.Turn path (the interactive REPL / headless `cortex
	// turn` path): the cap comes from the Config, not a per-run override.
	res, err := cs.Turn(context.Background(), "create a file")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.StopReason != "max-iter" {
		t.Fatalf("stop = %q, want max-iter (the turn exceeded the limits.max_tool_iterations cap)", res.StopReason)
	}

	// (1) The forced-finalize receipt round is on the wire: the note carries
	// the forced-finishing framing and names the leftover scratch file.
	var foundFraming bool
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser && strings.Contains(m.Content, "cut this turn off at the tool-call limit") {
			foundFraming = true
			if !strings.Contains(m.Content, "leftover debug") {
				t.Errorf("forced-finalize note = %q, want the leftover-debug receipt on the wire", m.Content)
			}
			if !strings.Contains(m.Content, "zz_debug_tmp.go") {
				t.Errorf("forced-finalize note = %q, want the scratch file named on the wire", m.Content)
			}
		}
	}
	if !foundFraming {
		t.Fatal("no message on the wire carried the forced-finalize framing — the model was never told the turn was cut off with leftover work")
	}

	// (2) The receipts ride the TurnResult (the human-facing surface).
	if !strings.Contains(res.DebugReceipt, "zz_debug_tmp.go") {
		t.Errorf("TurnResult.DebugReceipt = %q, want the scratch file named (the human-facing receipt)", res.DebugReceipt)
	}

	// (3) The model's forced-finalize reply is included in TurnResult.Reply:
	// the forced answer with the note round's reply appended.
	want := "partial work done\n\nI left zz_debug_tmp.go behind; it should be removed."
	if res.Reply != want {
		t.Errorf("TurnResult.Reply = %q, want the forced answer with the note round's reply appended: %q", res.Reply, want)
	}
}

// TestForcedFinalizeE2EClean is the step-5 no-op case, driven through the
// REAL cs.Turn path: when the turn is cut off at the cap but there is
// nothing to report (no leftover debug, no lint findings), the forced-
// finalize note is empty and the forced answer is untouched — no extra
// round, no receipt on the TurnResult.
func TestForcedFinalizeE2EClean(t *testing.T) {
	// Script: 2 tool rounds (the cap), then the forced finalize answer.
	// No scratch files, no test file removals, no lint findings → the
	// OnForcedFinalize note is empty → no note round.
	script := []*AgentResponse{
		readCallResp("c1", "pkg_a.go"),
		readCallResp("c2", "pkg_b.go"),
		answerResp("clean forced answer"),
	}

	cs := forcedFinalizeTurnSession(t, script, 2)
	// Create non-scratch files so the read_file calls have something to read.
	if err := os.WriteFile("pkg_a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pkg_b.go", []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := cs.Turn(context.Background(), "read files")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.StopReason != "max-iter" {
		t.Fatalf("stop = %q, want max-iter", res.StopReason)
	}
	// No note round: the answer is the forced answer, untouched.
	if res.Reply != "clean forced answer" {
		t.Errorf("TurnResult.Reply = %q, want the forced answer untouched (no leftover to report)", res.Reply)
	}
	// No receipt on the TurnResult.
	if res.DebugReceipt != "" {
		t.Errorf("TurnResult.DebugReceipt = %q, want empty (nothing to report)", res.DebugReceipt)
	}
	if res.TestReceipt != "" {
		t.Errorf("TurnResult.TestReceipt = %q, want empty (nothing to report)", res.TestReceipt)
	}
	// No message on the wire carries the forced-finalize framing.
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser && strings.Contains(m.Content, "cut this turn off at the tool-call limit") {
			t.Fatalf("unexpected forced-finalize framing on the wire: %q (nothing to report)", m.Content)
		}
	}
}

// TestForcedFinalizeE2ECleanFinalize is the step-5 never-fire case, driven
// through the REAL cs.Turn path: when the turn ends with a clean finalize
// (the model answers with no tool calls before the cap), OnForcedFinalize
// is never consulted — no extra round, no forced-finalize framing on the
// wire.
func TestForcedFinalizeE2ECleanFinalize(t *testing.T) {
	// Script: 1 tool round, then a clean answer (no tool calls) →
	// clean-finalize, not max-iter. The cap is 100, so the model finishes
	// well before it.
	script := []*AgentResponse{
		readCallResp("c1", "a.go"),
		answerResp("clean answer"),
	}

	cs := forcedFinalizeTurnSession(t, script, 100)
	if err := os.WriteFile("a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := cs.Turn(context.Background(), "read a file")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.StopReason != "clean-finalize" {
		t.Fatalf("stop = %q, want clean-finalize", res.StopReason)
	}
	if res.Reply != "clean answer" {
		t.Errorf("TurnResult.Reply = %q, want the clean answer untouched", res.Reply)
	}
	// No forced-finalize framing on the wire.
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser && strings.Contains(m.Content, "cut this turn off at the tool-call limit") {
			t.Fatalf("unexpected forced-finalize framing on the wire: %q (clean finalize)", m.Content)
		}
	}
}

// TestForcedFinalizeNoteShape is the issue #161 review fix for the
// forced-finalize note's shape: the note must carry ONE framing and ONE
// restatement ask (wrapping an already-framed receipt in a second "before
// you …" framing doubled both for the model), and its lead-in must name the
// bound that actually forced the finish — a token-budget stop must not claim
// "the tool-call limit" (the hook receives the run's stats, so the stop
// reason is known).
func TestForcedFinalizeNoteShape(t *testing.T) {
	// One scripted session per subtest: the helper t.Chdir's into its own
	// temp dir, so the scratch file (and every relative write in these
	// subtests) must be created AFTER the helper returns, or it lands in the
	// parent test's dir and the scans (which read from the session's workdir)
	// see nothing.
	newScratchSession := func(t *testing.T) *CortexSession {
		t.Helper()
		cs := forcedFinalizeScriptedSession(t, []*AgentResponse{answerResp("partial work")})
		if err := os.WriteFile("zz_debug_tmp.go", []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rel := "zz_debug_tmp.go"
		cs.testwatch = map[string]*testwatchSnapshot{rel: {abs: filepath.Join(cs.Workdir(), rel), display: rel, before: "", created: true}}
		return cs
	}

	t.Run("max-iter: single framing, single restate ask, names the tool-call limit", func(t *testing.T) {
		// Scratch file left behind → the note is non-empty (leftover-debug
		// receipt).
		cs := newScratchSession(t)
		note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: "max-iter"})
		if note == "" {
			t.Fatal("forced-finalize note is empty, want the leftover-debug receipt")
		}
		// Exactly ONE framing and exactly ONE restatement ask: the note is
		// built from the raw receipts, not wrapped in a second framing around
		// an already-framed note.
		for _, phrase := range []string{
			"Before you answer: the harness detected that this turn ",
			"Restate your complete final answer",
		} {
			if n := strings.Count(note, phrase); n != 1 {
				t.Errorf("note contains %q %d times, want exactly 1:\n%s", phrase, n, note)
			}
		}
		// The lead-in names the bound that actually forced the finish: a
		// max-iter stop names the tool-call limit, and no "Before you finish"
		// framing (the clean-finalize receipt's lead-in) rides the note.
		if !strings.Contains(note, "cut this turn off at the tool-call limit") {
			t.Errorf("note = %q, want the tool-call-limit lead-in (max-iter stop)", note)
		}
		if strings.Contains(note, "Before you finish") {
			t.Errorf("note = %q, must not carry a second \"Before you finish\" framing", note)
		}
	})

	t.Run("token-budget: names the token budget, not the tool-call limit", func(t *testing.T) {
		cs := newScratchSession(t)
		note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: "token-budget"})
		if note == "" {
			t.Fatal("forced-finalize note is empty, want the leftover-debug receipt")
		}
		if !strings.Contains(note, "cut this turn off at the token budget") {
			t.Errorf("note = %q, want the token-budget lead-in (token-budget stop)", note)
		}
		if strings.Contains(note, "tool-call limit") {
			t.Errorf("note = %q, must not name the tool-call limit for a token-budget stop", note)
		}
		if n := strings.Count(note, "Restate your complete final answer"); n != 1 {
			t.Errorf("note contains the restatement ask %d times, want exactly 1:\n%s", n, note)
		}
	})

	t.Run("other bounds: generic lead-in, never a false cause", func(t *testing.T) {
		cs := newScratchSession(t)
		for _, stop := range []string{"read-budget", "no-progress", "stuck", "deadline", "error-recovered"} {
			note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: stop})
			if note == "" {
				t.Fatalf("stop %s: forced-finalize note is empty", stop)
			}
			if strings.Contains(note, "tool-call limit") || strings.Contains(note, "token budget") {
				t.Errorf("stop %s: note names a specific bound that did not fire: %q", stop, note)
			}
			if n := strings.Count(note, "Restate your complete final answer"); n != 1 {
				t.Errorf("stop %s: restatement ask appears %d times, want 1", stop, n)
			}
		}
	})

	t.Run("nothing to report: empty note, answer untouched", func(t *testing.T) {
		cs := forcedFinalizeScriptedSession(t, []*AgentResponse{answerResp("clean")})
		if note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: "max-iter"}); note != "" {
			t.Fatalf("note = %q, want empty (nothing to report)", note)
		}
	})

	t.Run("lint: the receipt folds in raw — one framing, one restate ask", func(t *testing.T) {
		// The turn touched a .go file the {dir} lint covers: the note's lint
		// half is the RAW receipt (cs.runTurnLint's "lint: …" line), NOT
		// turnLintAtFinalize's already-framed "Before you finish …" note —
		// wrapping the framed note in the forced note's own lead-in gave the
		// model two framings and two restatement asks (the double framing
		// this test's other subtests pin for the testwatch half).
		cs := lintNoteShapeSession(t, []*AgentResponse{answerResp("partial work")})
		if err := os.MkdirAll("pkg", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("pkg", "a.go"), []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs.lintTouchedPath("pkg/a.go")
		note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: "max-iter"})
		if note == "" {
			t.Fatal("forced-finalize note is empty, want the lint receipt")
		}
		// The finding itself reaches the model (the receipt is raw, but not
		// dropped).
		if !strings.Contains(note, "lint:") || !strings.Contains(note, "LINT-FINDING") {
			t.Errorf("note = %q, want the raw lint receipt with the finding", note)
		}
		// Case-insensitive count: the lint copy says "restate" (lowercase),
		// the forced note's own ask says "Restate" — exactly ONE of the ask
		// total, and the clean-finalize framing ("Before you finish") never
		// rides the note.
		lower := strings.ToLower(note)
		if n := strings.Count(lower, "restate your complete final answer"); n != 1 {
			t.Errorf("note contains the restatement ask %d times (case-insensitive), want exactly 1:\n%s", n, note)
		}
		if n := strings.Count(note, "Before you finish"); n != 0 {
			t.Errorf("note contains the clean-finalize framing %q %d times, want 0:\n%s", "Before you finish", n, note)
		}
		// Exactly one of the forced note's own framings.
		if n := strings.Count(note, "Before you answer: the harness detected that this turn "); n != 1 {
			t.Errorf("note contains the forced lead-in %d times, want exactly 1:\n%s", n, note)
		}
		// The receipt joins the note without a doubled sentence break: a
		// receipt that already ends in punctuation must not pick up a second
		// one.
		if strings.Contains(note, ")..") || strings.Contains(note, "..") {
			t.Errorf("note = %q, want a single sentence break around the receipt", note)
		}
		// runTurnLint stores the receipt: the TurnResult surface stays
		// populated on the forced path.
		if cs.lintReceipt == "" || !strings.Contains(cs.lintReceipt, "LINT-FINDING") {
			t.Errorf("cs.lintReceipt = %q, want the pass's receipt stored", cs.lintReceipt)
		}
	})

	t.Run("lint clean: no touched .go file leaves the note empty", func(t *testing.T) {
		// The pass is armed and trusted, but the turn touched no .go file:
		// no receipt, no note — the common case is byte-for-byte silent.
		cs := lintNoteShapeSession(t, []*AgentResponse{answerResp("clean")})
		if note := cs.forcedFinalizeNote(context.Background(), loopStats{StopReason: "max-iter"}); note != "" {
			t.Fatalf("note = %q, want empty (the lint pass found nothing applicable)", note)
		}
	})
}
