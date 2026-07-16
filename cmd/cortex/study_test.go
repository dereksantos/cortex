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
	"github.com/dereksantos/cortex/pkg/llm"
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

// TestSubagentModelBinding covers subagentRequest's binding rules: study
// stays on the study role's spec; any other profile (agent) inherits the
// coder's live request — so it follows /model switches and defaults to the
// model that spawned it; a per-call Subagent.Model pins just the model name
// on that binding; and with no live coder request the study spec is the
// fallback (headless drivers, test fixtures).
func TestSubagentModelBinding(t *testing.T) {
	study := ModelSpec{Model: "study-m", Endpoint: "http://study.example"}
	coder := &AgentRequest{Model: "coder-live", BaseURL: "http://coder.example", APIKey: "sk-live", Temperature: 0.3}
	tests := []struct {
		name      string
		cs        *CortexSession
		sa        tools.Subagent
		wantModel string
		wantBase  string
		wantKey   string
	}{
		{"study role draws the study binding", &CortexSession{Study: study, Request: coder}, tools.Subagent{Role: roleStudy}, "study-m", "http://study.example", ""},
		{"agent role inherits the coder's live request", &CortexSession{Study: study, Request: coder}, tools.Subagent{Role: "agent"}, "coder-live", "http://coder.example", "sk-live"},
		{"per-call model override pins the model only", &CortexSession{Study: study, Request: coder}, tools.Subagent{Role: "agent", Model: "pinned-m"}, "pinned-m", "http://coder.example", "sk-live"},
		{"no live request falls back to the study binding", &CortexSession{Study: study}, tools.Subagent{Role: "agent"}, "study-m", "http://study.example", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.cs.subagentRequest(tt.sa, "seed")
			if req.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", req.Model, tt.wantModel)
			}
			if req.BaseURL != tt.wantBase {
				t.Errorf("BaseURL = %q, want %q", req.BaseURL, tt.wantBase)
			}
			if req.APIKey != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", req.APIKey, tt.wantKey)
			}
		})
	}
}

// TestSubagentModelBindingClearsEffortOnDivergentPin covers P3's seam fix
// (docs/thinking-models.md known seam bug #2): a per-call Subagent.Model
// that pins a DIFFERENT model than the one it inherited from the coder must
// not silently carry over that model's effort wire fields — they belong to
// a different binding. The switch matrix: an effort-bearing inherited
// request pinned to a plain (fleet-unknown) model clears to neutral; a plain
// inherited request pinned to a fleet-known hybrid model gets "on"; and the
// same-model case (no divergence) is untouched either way.
func TestSubagentModelBindingClearsEffortOnDivergentPin(t *testing.T) {
	fleet := Fleet{
		"hybrid-model": {Thinking: true},  // hybrid
		"plain-model":  {Thinking: false}, // none
	}
	baseCoder := func(model string, effort llm.Effort) *AgentRequest {
		req := &AgentRequest{Model: model, BaseURL: "http://coder.example"}
		applyEffort(req, llm.DialectTemplateKwargs, effort)
		return req
	}
	tests := []struct {
		name       string
		coder      *AgentRequest
		fleet      Fleet
		pinnedTo   string
		wantEffort llm.Effort
	}{
		{
			name:       "effort-model pinned to a plain model: cleared to neutral",
			coder:      baseCoder("coder-live", llm.Effort{Level: llm.EffortOff}),
			fleet:      fleet,
			pinnedTo:   "plain-model",
			wantEffort: llm.Effort{},
		},
		{
			name:       "plain inherited request pinned to a hybrid model: re-derived to on",
			coder:      baseCoder("coder-live", llm.Effort{}),
			fleet:      fleet,
			pinnedTo:   "hybrid-model",
			wantEffort: llm.Effort{Level: llm.EffortOn},
		},
		{
			name:       "fleet unknown: pin still clears to neutral",
			coder:      baseCoder("coder-live", llm.Effort{Level: llm.EffortOn}),
			fleet:      nil,
			pinnedTo:   "some-model",
			wantEffort: llm.Effort{},
		},
		{
			name:       "same model as inherited (no divergence): effort untouched",
			coder:      baseCoder("coder-live", llm.Effort{Level: llm.EffortOff}),
			fleet:      fleet,
			pinnedTo:   "coder-live",
			wantEffort: llm.Effort{Level: llm.EffortOff},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &CortexSession{Request: tt.coder, Fleet: tt.fleet}
			req := cs.subagentRequest(tools.Subagent{Role: "agent", Model: tt.pinnedTo}, "seed")
			if req.Effort != tt.wantEffort {
				t.Errorf("Effort = %+v, want %+v", req.Effort, tt.wantEffort)
			}
			if req.Model != tt.pinnedTo {
				t.Errorf("Model = %q, want the pinned %q", req.Model, tt.pinnedTo)
			}
		})
	}
}
