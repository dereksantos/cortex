package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// fakeSubagentTool builds a minimal dispatchable Tool declaration for a test
// fixture profile — same path/goal shape as StudyTool, but under a distinct
// name so the fixture is a first-class registered subagent tool a scripted
// model can call (unlike Study, whose own Tools never offer "study" itself).
func fakeSubagentTool(name string) Tool {
	return Tool{Type: "function", Function: tools.ToolFunction{
		Name:        name,
		Description: "test fixture subagent",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"goal": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}}
}

// TestSubagentDepthPolicy covers GOAL.md §3 slice 2: the blanket "subagents
// can't recurse" rule (previously true only because Study's own Tools never
// offered a subagent tool) is now an explicit, enforced Subagent.DepthCap
// threaded through runSubagentStats — the shared path every subagent
// invocation funnels through, regardless of a profile's toolset.
func TestSubagentDepthPolicy(t *testing.T) {
	t.Run("cap 0: a nested subagent call is refused with a clear error", func(t *testing.T) {
		// A fixture mirroring Study's cap (0) but, unlike Study, offering
		// itself in its own toolset — so we can exercise the depth check
		// directly rather than the (separate) allowlist check that already
		// keeps Study from seeing a subagent tool at all.
		role := "test-depth-cap0"
		fake := tools.Subagent{Name: "cap0-agent", Role: role, System: "sys", DepthCap: 0}
		fake.Declaration = fakeSubagentTool(role)
		fake.Tools = []Tool{fake.Declaration}
		tools.Register(fake)

		cs := &CortexSession{}
		args, _ := json.Marshal(map[string]any{"path": "."})
		call := ToolCall{ID: "c1", Type: "function", Function: FunctionCall{Name: role, Arguments: string(args)}}

		// Simulate the call arriving from INSIDE a running instance of this
		// profile's own loop — exactly the ctx runSubagentStats hands to
		// runLoop (and on to every tool it dispatches) once it has entered
		// the profile at depth 1.
		ctx := withSubagentDepth(context.Background(), 1)
		obs := cs.dispatcherFor(fake).Dispatch(ctx, call)

		if !strings.HasPrefix(obs, "Error:") {
			t.Errorf("observation = %q, want an Error: prefix (a clear error)", obs)
		}
		if !strings.Contains(obs, "depth cap") {
			t.Errorf("observation = %q, want it to name the depth cap", obs)
		}
	})

	t.Run("cap 1: one nested subagent is allowed; its own subagent call is refused at depth 2", func(t *testing.T) {
		role := "test-depth-cap1"
		fake := tools.Subagent{
			Name: "cap1-agent", Role: role, System: "sys", DepthCap: 1,
			Bounds: Bounds{MaxTokens: 200, MaxIter: 8},
			Seed:   func(goal, path, ol string) string { return "seed" },
		}
		fake.Declaration = fakeSubagentTool(role)
		fake.Tools = []Tool{fake.Declaration}
		tools.Register(fake)

		// The fake backend scripts a strictly sequential recursion attempt:
		// request 1 (root, depth 0->1) asks for a nested call; request 2
		// (child, depth 1->2) asks for ANOTHER nested call — that attempt is
		// refused in-process (no request 3 in this shape), so the child's
		// next request finalizes, and finally the root's next request
		// finalizes. Four round-trips total.
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&calls, 1)
			var resp AgentResponse
			resp.Usage = Usage{PromptTokens: 1, CompletionTokens: 1}
			if n <= 2 {
				args, _ := json.Marshal(map[string]any{"path": "."})
				resp.Choices = []Choice{{FinishReason: "tool_calls", Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID: fmt.Sprintf("call-%d", n), Type: "function",
						Function: FunctionCall{Name: role, Arguments: string(args)},
					}},
				}}}
			} else {
				resp.Choices = []Choice{{FinishReason: "stop", Message: Message{
					Role: "assistant", Content: fmt.Sprintf("final-%d", n),
				}}}
			}
			json.NewEncoder(w).Encode(&resp)
		}))
		defer srv.Close()

		cs := &CortexSession{quiet: true, Study: ModelSpec{Model: "m", Endpoint: srv.URL}}

		digest, err := cs.RunSubagent(context.Background(), fake, "seed")
		if err != nil {
			t.Fatalf("RunSubagent: %v", err)
		}
		if got := atomic.LoadInt32(&calls); got != 4 {
			t.Errorf("model round-trips = %d, want 4 (root ask, child ask, child finalize, root finalize)", got)
		}
		if !strings.Contains(digest, "final-4") {
			t.Errorf("digest = %q, want it to carry the root's own finalize (final-4)", digest)
		}
	})

	t.Run("study behavior unchanged: cap 0, no subagent tool offered", func(t *testing.T) {
		sa, ok := tools.Lookup(tools.FunctionStudy)
		if !ok {
			t.Fatal("expected \"study\" to be registered")
		}
		if sa.DepthCap != 0 {
			t.Errorf("Study.DepthCap = %d, want 0", sa.DepthCap)
		}
		if tools.AllowOf(sa.Tools)[tools.FunctionStudy] {
			t.Error("Study's own toolset should not offer \"study\" (no recursion)")
		}
	})
}
