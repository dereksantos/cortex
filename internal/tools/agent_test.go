package tools

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

// TestAgentProfileRegistered covers GOAL.md §3 slice 3b: the `agent` profile
// (decisions settled in docs/agent-tool.md) is registered on the shared
// subagent registry via the same Register/Lookup path Study uses, with a
// depth cap of 1 (decision 3) and the mandatory MaxTokens runaway backstop
// (see Study.Bounds).
func TestAgentProfileRegistered(t *testing.T) {
	sa, ok := Lookup(FunctionAgent)
	if !ok {
		t.Fatal("expected \"agent\" to be registered")
	}
	if sa.Name != "agent" || sa.Role != "agent" {
		t.Errorf("got Name=%q Role=%q, want both \"agent\"", sa.Name, sa.Role)
	}
	if sa.AsTool().Function.Name != FunctionAgent {
		t.Errorf("Agent.AsTool() name = %q, want %q", sa.AsTool().Function.Name, FunctionAgent)
	}
	if sa.DepthCap != 1 {
		t.Errorf("Agent.DepthCap = %d, want 1 (docs/agent-tool.md decision 3)", sa.DepthCap)
	}
	if sa.Bounds.MaxTokens == 0 {
		t.Error("Agent.Bounds.MaxTokens is unset — mandatory runaway backstop, see Study")
	}
	if sa.Seed == nil {
		t.Error("Agent.Seed is nil — should be set explicitly (or fall back to StudySeed at call time)")
	}
}

// TestAgentToolsetMatchesDesignDoc covers docs/agent-tool.md decision 1
// (toolset scope: read/search plus write/edit/bash) and decision 3 (excluded
// coder-only tools): the allowlist Execute would use for a running Agent
// instance is exactly outline/grep/read_file/write_file/edit_file/bash — no
// more, no less — so none of the session-scoped coder-only tools (recall,
// memory_*, context_*) or the subagent tools themselves (study, agent) are
// reachable through it.
func TestAgentToolsetMatchesDesignDoc(t *testing.T) {
	allow := AllowOf(Agent.Tools)

	want := []string{FunctionOutline, FunctionGrep, FunctionReadFile, FunctionWriteFile, FunctionEditFile, FunctionBash}
	if len(allow) != len(want) {
		t.Errorf("Agent toolset has %d entries, want exactly %d: %v", len(allow), len(want), want)
	}
	for _, name := range want {
		if !allow[name] {
			t.Errorf("Agent toolset missing %q", name)
		}
	}

	excluded := []string{
		FunctionRecall, FunctionMemoryWrite, FunctionMemoryRead, FunctionMemorySearch, FunctionMemoryForget,
		FunctionContextEvict, FunctionContextMerge, FunctionContextAdjustWatermarks,
		FunctionStudy, FunctionAgent, FunctionRemove,
	}
	for _, name := range excluded {
		if allow[name] {
			t.Errorf("Agent toolset should exclude %q (docs/agent-tool.md decision 3)", name)
		}
	}
}

// TestAgentInAllToolset covers that the coder actually sees the agent tool:
// tools.All (the coder's declared tool set, cmd/cortex/tool_deps.go's
// toolSet) includes Agent's declaration, same as Study's.
func TestAgentInAllToolset(t *testing.T) {
	found := false
	for _, tl := range All {
		if tl.Function.Name == FunctionAgent {
			found = true
			break
		}
	}
	if !found {
		t.Error("tools.All is missing the agent tool declaration")
	}
}

// disablingDeps wraps fakeSubagentDeps and reports one named tool disabled —
// exercising Execute's generic IsToolEnabled gate (the same seam
// cmd/cortex.CortexSession.IsToolEnabled implements for EnableWeb/EnableAgent/
// EnableContext*) without needing the concrete *CortexSession/Config types,
// which live in cmd/cortex and would import-cycle back to this package.
type disablingDeps struct {
	*fakeSubagentDeps
	disable string
}

func (d *disablingDeps) IsToolEnabled(name string) bool { return name != d.disable }

// TestExecuteRefusesDisabledAgent covers that Execute's generic enable/disable
// check (IsToolEnabled) — the same seam EnableWeb/EnableContext* use — gates
// the agent tool by name: a deps stub that reports "agent" disabled short-
// circuits before RunSubagent is ever called.
func TestExecuteRefusesDisabledAgent(t *testing.T) {
	deps := &fakeSubagentDeps{outline: "OUTLINE", digest: "DIGEST"}
	off := &disablingDeps{fakeSubagentDeps: deps, disable: FunctionAgent}

	tc := agent.ToolCall{Function: agent.FunctionCall{
		Name:      FunctionAgent,
		Arguments: `{"path":".","goal":"do the thing"}`,
	}}
	out, err := Execute(context.Background(), tc, off)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if deps.gotRole != "" {
		t.Errorf("RunSubagent should not have been called; got role %q", deps.gotRole)
	}
	if out == "DIGEST" {
		t.Error("disabled agent tool should not return the subagent's digest")
	}
}

// TestAgentModelArg covers the per-call model override: the agent tool's
// optional "model" argument is stamped onto the profile copy handed to
// RunSubagent (where the runner pins it over its default binding — the
// coder's own live model); omitting it leaves the default in force.
func TestAgentModelArg(t *testing.T) {
	if props, ok := AgentTool.Function.Parameters["properties"].(map[string]any); !ok || props["model"] == nil {
		t.Error(`AgentTool schema should declare the optional "model" argument`)
	}

	tests := []struct {
		name string
		args string
		want string
	}{
		{"model arg stamps the profile copy", `{"path":".","goal":"g","model":"qwen3-coder"}`, "qwen3-coder"},
		{"omitted model leaves the runner default", `{"path":".","goal":"g"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &fakeSubagentDeps{outline: "OUTLINE", digest: "DIGEST"}
			tc := agent.ToolCall{Function: agent.FunctionCall{
				Name:      FunctionAgent,
				Arguments: tt.args,
			}}
			if _, err := Execute(context.Background(), tc, deps); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if deps.gotModel != tt.want {
				t.Errorf("RunSubagent got Model %q, want %q", deps.gotModel, tt.want)
			}
		})
	}
}
