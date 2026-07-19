package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestUserConfigPathRoutesThroughUserhome pins that userConfigPath() is
// derived from internal/userhome's resolver rather than duplicating the
// $CORTEX_HOME / os.UserHomeDir lookup inline: redirecting CORTEX_HOME to
// a temp dir must redirect userConfigPath() too.
func TestUserConfigPathRoutesThroughUserhome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	got := userConfigPath()
	want := filepath.Join(tmp, "config.json")
	if got != want {
		t.Errorf("userConfigPath() = %q, want %q", got, want)
	}
}

// TestModelSpecThinkingJSONCompat covers ModelSpec.Thinking's JSON
// compatibility (docs/thinking-models.md §1): a config file's "thinking" key
// accepts the legacy bool, a level string, or {"budget": N} — the whole
// point being that an existing config keeps parsing exactly as it did
// before this field's type changed from *bool to llm.Effort.
func TestModelSpecThinkingJSONCompat(t *testing.T) {
	tests := []struct {
		name string
		json string
		want llm.Effort
	}{
		{"absent key: unset", `{}`, llm.Effort{}},
		{"legacy false", `{"thinking": false}`, llm.Effort{Level: llm.EffortOff}},
		{"legacy true", `{"thinking": true}`, llm.Effort{Level: llm.EffortOn}},
		{"level string", `{"thinking": "high"}`, llm.Effort{Level: llm.EffortHigh}},
		{"budget object", `{"thinking": {"budget": 8192}}`, llm.Effort{Budget: 8192}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec ModelSpec
			if err := json.Unmarshal([]byte(tt.json), &spec); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.json, err)
			}
			if spec.Thinking != tt.want {
				t.Errorf("Thinking = %+v, want %+v", spec.Thinking, tt.want)
			}
		})
	}
}

// TestExistingThinkingFalseConfigByteForByte pins the P2 compatibility
// requirement end to end: a full config file with "thinking": false for the
// code role must produce EXACTLY today's {"enable_thinking": false} kwargs
// once resolved through resolveBinding — the whole config→binding→wire path,
// not just the Effort type in isolation (see
// pkg/llm.TestTranslateExistingConfigByteForByte for that narrower version).
func TestExistingThinkingFalseConfigByteForByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"models":{"code":{"model":"some-model","thinking":false}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := readConfigFile(path)
	if cfg == nil {
		t.Fatal("readConfigFile returned nil")
	}
	spec := cfg.resolveBinding(roleCode, nil)
	kw := spec.TemplateKwargs()
	b, err := json.Marshal(kw)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"enable_thinking":false}` {
		t.Errorf("kwargs = %s, want {\"enable_thinking\":false} (byte-for-byte with today's behavior)", b)
	}
}

// TestResolveBindingOpenRouterZeroConfigDefaultsToCuratedTopPick pins E2's
// "curated is primary" default end to end through the live resolution path:
// an OpenRouter backend with no models.<role> entry at all (just
// {"backend": {"type": "openrouter", ...}}, the true zero-config case) binds
// the curated table's top pick, not an empty model id OpenRouter would
// reject. This is also what makes the startup preflight reachable at all
// for a zero-config user — it only acts on a binding isCuratedModel
// recognizes.
func TestResolveBindingOpenRouterZeroConfigDefaultsToCuratedTopPick(t *testing.T) {
	cfg := &Config{Backend: Backend{Type: "openrouter", KeyEnv: "OPENROUTER_API_KEY"}}
	top := curatedTopPick()

	for _, role := range []string{roleCode, roleStudy} {
		t.Run(role, func(t *testing.T) {
			spec := cfg.resolveBinding(role, nil)
			if spec.Model != top.ID {
				t.Errorf("resolveBinding(%q).Model = %q, want curated top pick %q", role, spec.Model, top.ID)
			}
			if spec.Window != top.Window {
				t.Errorf("resolveBinding(%q).Window = %d, want %d", role, spec.Window, top.Window)
			}
			if !isCuratedModel(spec.Model) {
				t.Errorf("resolveBinding(%q).Model = %q is not recognized as curated; the startup preflight would never fire for it", role, spec.Model)
			}
		})
	}
}

// TestResolveBindingOpenRouterExplicitModelWins pins that the zero-config
// curated default only fires when nothing else set a model — an explicit
// models.code.model in config always wins, unchanged from before this
// track's work.
func TestResolveBindingOpenRouterExplicitModelWins(t *testing.T) {
	cfg := &Config{
		Backend: Backend{Type: "openrouter"},
		Models:  map[string]ModelSpec{roleCode: {Model: "anthropic/claude-haiku-4.5"}},
	}
	spec := cfg.resolveBinding(roleCode, nil)
	if spec.Model != "anthropic/claude-haiku-4.5" {
		t.Errorf("resolveBinding(code).Model = %q, want the explicit config pin unchanged", spec.Model)
	}
}

// TestResolveBindingNonOpenRouterNoCuratedDefault pins that the curated
// zero-config default is OpenRouter-specific: a LiteLLM-style backend with
// no fleet and no config model keeps resolving to an empty model id (its
// existing, pre-E2 behavior — fleet discovery is that backend's own
// resolution path).
func TestResolveBindingNonOpenRouterNoCuratedDefault(t *testing.T) {
	var cfg *Config // nil config == the true no-config-file case
	spec := cfg.resolveBinding(roleCode, nil)
	if spec.Model != "" {
		t.Errorf("resolveBinding(code).Model = %q, want empty for a nil (non-openrouter) config", spec.Model)
	}
}

// TestEffortEscalationEnabled covers P5c's opt-in gate
// (docs/thinking-models.md §5c): nil config, nil flag, and an explicit false
// all default to disabled; only an explicit true enables it.
func TestEffortEscalationEnabled(t *testing.T) {
	yes, no := true, false
	var nilCfg *Config
	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"nil config: disabled", nilCfg, false},
		{"empty config: disabled", &Config{}, false},
		{"explicit false: disabled", &Config{Tools: ToolConfig{EnableEffortEscalation: &no}}, false},
		{"explicit true: enabled", &Config{Tools: ToolConfig{EnableEffortEscalation: &yes}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.effortEscalationEnabled(); got != tt.want {
				t.Errorf("effortEscalationEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMergeToolsEffortEscalation covers the project-over-user merge for the
// new flag, matching the existing Enable* merge convention (remove_test.go's
// TestMergeTools-equivalent coverage).
func TestMergeToolsEffortEscalation(t *testing.T) {
	yes, no := true, false
	got := mergeTools(ToolConfig{EnableEffortEscalation: &yes}, ToolConfig{EnableEffortEscalation: &no})
	if got.EnableEffortEscalation == nil || *got.EnableEffortEscalation {
		t.Errorf("EnableEffortEscalation = %v, want the project-level override (false) to win", got.EnableEffortEscalation)
	}
	// An absent override leaves the base value intact.
	got2 := mergeTools(ToolConfig{EnableEffortEscalation: &yes}, ToolConfig{})
	if got2.EnableEffortEscalation == nil || !*got2.EnableEffortEscalation {
		t.Errorf("EnableEffortEscalation = %v, want the base value preserved", got2.EnableEffortEscalation)
	}
}
