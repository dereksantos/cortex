package commands

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
)

// TestLocalOnlyRouting confirms the local-only sweep keeps pins that
// resolve to a configured (local) endpoint and drops pins targeting a
// remote model whose prefix is no configured endpoint.
func TestLocalOnlyRouting(t *testing.T) {
	cfg := &config.Config{
		Endpoints: []config.EndpointDef{
			{Name: "chatterbox", BaseURL: "http://chatterbox:4000", Models: []string{"coder", "reasoner"}},
		},
		Routing: map[string]string{
			"decide.next":         "openai/gpt-5.4", // remote → drop
			"decide.tool_call":    "chatterbox/coder", // endpoint/model → keep
			"sense.estimate_scope": "reasoner",         // bare local model → keep
			"attend.compress":     "anthropic/claude", // remote → drop
		},
	}

	got := localOnlyRouting(cfg, cfg.Routing)

	if _, ok := got["decide.next"]; ok {
		t.Error("decide.next (openai/gpt-5.4) should be dropped — remote")
	}
	if _, ok := got["attend.compress"]; ok {
		t.Error("attend.compress (anthropic/claude) should be dropped — remote")
	}
	if v := got["decide.tool_call"]; v != "chatterbox/coder" {
		t.Errorf("decide.tool_call should be kept (chatterbox/coder), got %q", v)
	}
	if v := got["sense.estimate_scope"]; v != "reasoner" {
		t.Errorf("sense.estimate_scope should be kept (bare local), got %q", v)
	}
	if len(got) != 2 {
		t.Errorf("kept %d pins, want 2 (the two local ones)", len(got))
	}
}
