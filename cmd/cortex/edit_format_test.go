package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

func TestConfigEditFormat(t *testing.T) {
	tests := []struct {
		name   string
		format string
		model  string
		want   string
	}{
		{"auto: gpt-oss alias", "", "gpt-oss-20b", editFormatPatch},
		{"auto: provider-prefixed GPT-5", "auto", "openai/gpt-5-mini", editFormatPatch},
		{"auto: codex", "", "openai/codex-mini", editFormatPatch},
		{"auto: case-insensitive", "", "GPT-OSS-120B", editFormatPatch},
		{"auto: another family", "", "qwen3.8-27b", editFormatReplace},
		{"explicit replace beats the family", "replace", "gpt-oss-20b", editFormatReplace},
		{"explicit patch for any model", "patch", "qwen3.8-27b", editFormatPatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Tools: ToolConfig{EditFormat: tc.format}}
			if got := c.editFormat(tc.model); got != tc.want {
				t.Errorf("editFormat(%q) with %q = %q, want %q", tc.model, tc.format, got, tc.want)
			}
		})
	}
	if got := (*Config)(nil).editFormat("gpt-oss-20b"); got != editFormatPatch {
		t.Errorf("nil config: editFormat = %q, want %q", got, editFormatPatch)
	}
}

// Exactly one edit tool is declared, for the coder and the agent subagent.
func TestEditFormatDeclaresExactlyOneEditTool(t *testing.T) {
	for _, format := range []string{editFormatReplace, editFormatPatch, ""} {
		t.Run("format="+format, func(t *testing.T) {
			cs := &CortexSession{Config: &Config{}, editFormat: format}
			for _, list := range map[string][]Tool{"coder": toolSet, "agent": tools.Agent.Tools} {
				var edit, patch bool
				for _, tl := range filterEnabledTools(list, cs.IsToolEnabled) {
					edit = edit || tl.Function.Name == tools.FunctionEditFile
					patch = patch || tl.Function.Name == tools.FunctionApplyPatch
				}
				if wantPatch := format == editFormatPatch; edit == wantPatch || patch != wantPatch {
					t.Errorf("edit_file declared=%v apply_patch declared=%v, want exactly %s", edit, patch, map[bool]string{true: "apply_patch", false: "edit_file"}[wantPatch])
				}
			}
		})
	}
}

func TestValidateConfigEditFormat(t *testing.T) {
	for _, v := range []string{"", "auto", "replace", "patch"} {
		if err := validateConfig(&Config{Tools: ToolConfig{EditFormat: v}}); err != nil {
			t.Errorf("edit_format %q rejected: %v", v, err)
		}
	}
	err := validateConfig(&Config{Tools: ToolConfig{EditFormat: "diff"}})
	if err == nil || !strings.Contains(err.Error(), "tools.edit_format") {
		t.Errorf("edit_format \"diff\": err = %v, want a tools.edit_format error", err)
	}
}

func TestConfigMergeEditFormat(t *testing.T) {
	got := mergeTools(ToolConfig{EditFormat: "patch"}, ToolConfig{EditFormat: "replace"})
	if got.EditFormat != "replace" {
		t.Errorf("project layer edit_format not applied: %q", got.EditFormat)
	}
	if got := mergeTools(ToolConfig{EditFormat: "patch"}, ToolConfig{}); got.EditFormat != "patch" {
		t.Errorf("absent project edit_format dropped the user's: %q", got.EditFormat)
	}
}
