package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestFilterEnabledTools is the wire-side half of the config gates
// (docs/eval-context-pivot.md; Track B item B1): config-disabling a tool used
// to refuse the call at dispatch time (tools.go's Execute → IsToolEnabled)
// while the declaration stayed on the wire regardless, so a model could see
// and call a tool that would only ever be refused. NewCortexSession now runs
// every base declaration through filterEnabledTools(toolSet, cs.IsToolEnabled)
// — the same predicate dispatch consults, not a second parallel list — so a
// disabled tool's declaration is absent from Request.Tools too, and dispatch
// keeps its own check as defense-in-depth against a hallucinated tool name.
//
// Covers every tool name IsToolEnabled's switch (session_core.go) knows
// about, plus remove_path — gated through the separate DeleteGate/allowDelete
// path (not IsToolEnabled), stripped via toolsExcept at construction — to
// confirm that older, already-symmetric mechanism still holds.
func TestFilterEnabledTools(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		off      *CortexSession // a session whose gate is OFF for toolName
		args     string         // minimal JSON args for a dispatch probe
	}{
		{
			name:     "context_merge (enable_context_merge)",
			toolName: tools.FunctionContextMerge,
			off:      &CortexSession{Config: &Config{Tools: ToolConfig{EnableContextMerge: boolPtr(false)}}},
			args:     `{"start_citation":"@session/x#m1-1","end_citation":"@session/x#m1-1"}`,
		},
		{
			name:     "web_search (enable_web)",
			toolName: tools.FunctionWebSearch,
			off:      &CortexSession{Config: &Config{Tools: ToolConfig{EnableWeb: boolPtr(false)}}},
			args:     `{"query":"x"}`,
		},
		{
			name:     "agent (enable_agent)",
			toolName: tools.FunctionAgent,
			off:      &CortexSession{Config: &Config{Tools: ToolConfig{EnableAgent: boolPtr(false)}}},
			args:     `{"path":".","goal":"x"}`,
		},
		{
			name:     "scan_landscape (enable_scan)",
			toolName: tools.FunctionScanLandscape,
			off:      &CortexSession{Config: &Config{Tools: ToolConfig{EnableScan: boolPtr(false)}}},
			args:     `{}`,
		},
		{
			name:     "remove_path (allow_delete)",
			toolName: FunctionRemove,
			off:      &CortexSession{allowDelete: false},
			args:     `{"path":"x"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Gate off: declaration must be absent from the assembled request.
			filtered := assembleTools(c.off)
			for _, tl := range filtered {
				if tl.Function.Name == c.toolName {
					t.Errorf("%s: declaration should be stripped when its gate is off", c.toolName)
				}
			}

			// Gate off: dispatch must still refuse the call (defense-in-depth).
			// Most gates (IsToolEnabled) refuse via a plain observation string;
			// remove_path's separate DeleteGate refuses via an error — both are
			// "the call did not go through", so accept either shape.
			obs, err := tools.Execute(context.Background(), tc(c.toolName, c.args), c.off)
			refusal := obs
			if err != nil {
				refusal = err.Error()
			}
			if !strings.Contains(strings.ToLower(refusal), "disabled") {
				t.Errorf("%s: dispatch should refuse a disabled tool, got observation: %q, err: %v", c.toolName, obs, err)
			}

			// Gate on (no config at all — every gate defaults enabled; allowDelete
			// mirrors cfg.deleteEnabled()'s nil-means-true default): declaration present.
			on := &CortexSession{allowDelete: true}
			enabled := assembleTools(on)
			found := false
			for _, tl := range enabled {
				if tl.Function.Name == c.toolName {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: declaration should be present on the wire when its gate is on (default)", c.toolName)
			}
		})
	}
}

// assembleTools mirrors NewCortexSession's declaration-filtering pipeline
// (allow_delete's toolsExcept, then the generic filterEnabledTools pass) over
// the base coder tool set, without paying for NewCortexSession's config-load
// and fleet-discovery side effects.
func assembleTools(cs *CortexSession) []Tool {
	ts := toolSet
	if !cs.allowDelete {
		ts = toolsExcept(ts, FunctionRemove)
	}
	return filterEnabledTools(ts, cs.IsToolEnabled)
}

func boolPtr(b bool) *bool { return &b }

// TestPrintStartupWarning pins the fix for headless `cortex turn --json`
// leaking startup preflight diagnostics (missing model discovery, shared
// swap_group) onto stdout ahead of the JSON object, which broke machine
// consumers. NewCortexSession now routes both warnings through this single
// helper called with os.Stderr; this test pins the helper's own contract —
// it writes to whatever io.Writer it is given (and only that writer) — so a
// future call site can't regress back to fmt.Println(stdout) without this
// test catching the signature change.
func TestPrintStartupWarning(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{
			name: "swap_group warning",
			msg:  `warning: code (qwen3-coder-q3) and study (study) share swap_group "cuda-8090" — they evict each other every turn; route one to different silicon`,
		},
		{
			name: "model discovery unavailable note",
			msg:  "note: model discovery unavailable at http://localhost:11434 — set backend in .cortex/config.json or pin models",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			printStartupWarning(&buf, c.msg)

			got := buf.String()
			if !strings.Contains(got, c.msg) {
				t.Errorf("printStartupWarning(%q): output %q does not contain the message", c.msg, got)
			}
			if !strings.HasSuffix(got, "\n") {
				t.Errorf("printStartupWarning(%q): output %q should end with a newline", c.msg, got)
			}
		})
	}
}
