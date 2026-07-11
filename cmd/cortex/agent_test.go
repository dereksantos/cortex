package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
)

// TestAgentToolEndToEnd is GOAL.md §3 slice 3b's last acceptance bullet: a
// scripted-Sender loop test driving a full coder Turn that calls the "agent"
// tool, whose own loop dispatches at least one tool before finalizing, with
// the digest landing back in the coder's own turn.
//
// The dispatched tool is a Risky bash command, exercised along the REAL
// agent-tool path (coder Turn -> runSubagentStats -> runLoop -> dispatcherFor
// -> bash -> gateShell), not via a hand-built ctx as
// cmd/cortex/main_test.go's TestBashShellSyntax subagent-depth subtest does —
// so a regression in Agent's toolset (e.g. bash dropped, M4.1) fails this
// test too: the dispatcher's "not available here" refusal doesn't match the
// blocked-safety-gate assertions below (verified by hand: temporarily
// dropping Bash from tools.Agent.Tools fails this test with exactly that
// mismatch). confirmRisky is wired to t.Fatal if invoked at all, as a
// defense-in-depth check — this session is quiet, which alone keeps Risky
// treated as Blocked (the pure depth-vs-quiet distinction for M4.2 is
// TestBashShellSyntax's job in main_test.go).
func TestAgentToolEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "greeting.go"),
		[]byte("package greeting\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Four scripted round-trips, strictly sequential (mirrors
	// TestSubagentDepthPolicy's cap-1 case in study_test.go): coder asks for
	// the agent tool; the agent subagent asks for a risky bash command;
	// that call is blocked in-process (no request 3 in this shape), so the
	// subagent's next request finalizes with a digest; the coder's next
	// request finalizes with that digest folded into its own reply.
	var calls int32
	var wires [][]Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		wires = append(wires, req.Messages)
		n := atomic.AddInt32(&calls, 1)

		var resp AgentResponse
		resp.Usage = Usage{PromptTokens: 5, CompletionTokens: 5}
		switch n {
		case 1: // coder round 1: delegate to the agent subagent
			args, _ := json.Marshal(map[string]any{"path": ".", "goal": "add a doc comment to Hello"})
			resp.Choices = []Choice{{FinishReason: "tool_calls", Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID: "call-agent", Type: "function",
					Function: FunctionCall{Name: tools.FunctionAgent, Arguments: string(args)},
				}},
			}}}
		case 2: // agent subagent round 1: attempts a risky bash command
			args, _ := json.Marshal(map[string]string{"command": "rm -rf build"})
			resp.Choices = []Choice{{FinishReason: "tool_calls", Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID: "call-bash", Type: "function",
					Function: FunctionCall{Name: tools.FunctionBash, Arguments: string(args)},
				}},
			}}}
		case 3: // agent subagent round 2: finalizes after the bash call is blocked
			resp.Choices = []Choice{{FinishReason: "stop", Message: Message{
				Role:    "assistant",
				Content: "the bash call was blocked by the safety gate; no change was made",
			}}}
		default: // coder round 2: finalizes after the agent tool's digest lands
			resp.Choices = []Choice{{FinishReason: "stop", Message: Message{
				Role:    "assistant",
				Content: "Delegated to agent; it reported the bash call was blocked.",
			}}}
		}
		json.NewEncoder(w).Encode(&resp)
	}))
	defer srv.Close()

	stubRisky := func(_ context.Context, _ string) (shellrisk.Level, string, error) {
		return shellrisk.Risky, "test-fixture: always risky", nil
	}
	cs := &CortexSession{
		quiet:         true,
		classifyShell: stubRisky,
		confirmRisky: func(string) bool {
			t.Fatal("confirmRisky must not be invoked for a call at subagent depth")
			return true
		},
		Request: &AgentRequest{
			Model:    "coder-m",
			BaseURL:  srv.URL,
			Tools:    tools.All,
			Messages: []Message{{Role: RoleSystem, Content: "you are cortex"}},
		},
		Study: ModelSpec{Model: "study-m", Endpoint: srv.URL},
	}

	res, err := cs.Turn(context.Background(), "please delegate a small fix to the agent tool")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("model round-trips = %d, want 4 (coder ask, subagent ask, subagent finalize, coder finalize)", got)
	}

	if !strings.Contains(res.Reply, "Delegated to agent") {
		t.Errorf("TurnResult.Reply = %q, want the coder's own finalize text", res.Reply)
	}

	// The subagent's own bash call must have been blocked headlessly (M4.2),
	// visible on the wire the subagent's own finalize round-trip (call 3) saw.
	if len(wires) < 4 {
		t.Fatalf("only captured %d wires, want 4", len(wires))
	}
	subagentFinalWire := wires[2]
	var sawBlocked bool
	for _, m := range subagentFinalWire {
		if m.Role == RoleTool && strings.Contains(strings.ToLower(m.Content), "no interactive approval") {
			sawBlocked = true
		}
	}
	if !sawBlocked {
		t.Errorf("subagent's own wire did not carry the headless-blocked bash result: %+v", subagentFinalWire)
	}

	// The subagent's digest must land back on the coder's OWN turn as a tool
	// result — the wire the coder's finalize round-trip (call 4) actually
	// saw — proving the digest crosses back out of the subagent loop.
	coderFinalWire := wires[3]
	var sawDigest bool
	for _, m := range coderFinalWire {
		if m.Role == RoleTool && strings.Contains(m.Content, "blocked by the safety gate") {
			sawDigest = true
		}
	}
	if !sawDigest {
		t.Errorf("coder's final wire did not carry the agent subagent's digest as a tool result: %+v", coderFinalWire)
	}
}
