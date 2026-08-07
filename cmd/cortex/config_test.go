package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadMergedConfig(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("project overrides user, inherits the rest", func(t *testing.T) {
		dir := t.TempDir()
		userPath := write(t, dir, "user.json", `{
			"temperature": 0.6,
			"backend": {"type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY"},
			"models": {
				"code":  {"model": "qwen/qwen3-coder:free"},
				"study": {"model": "openai/gpt-oss-20b:free"}
			}
		}`)
		projPath := write(t, dir, "proj.json", `{
			"models": {"code": {"model": "anthropic/claude-sonnet"}}
		}`)

		cfg := loadMergedConfig(userPath, projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		// Project overrode only code's model.
		if cfg.Models["code"].Model != "anthropic/claude-sonnet" {
			t.Errorf("code model = %q, want the project override", cfg.Models["code"].Model)
		}
		// Backend and the study role inherited from the user layer.
		if cfg.Backend.Type != "openrouter" || cfg.Backend.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("backend not inherited: %+v", cfg.Backend)
		}
		if cfg.Models["study"].Model != "openai/gpt-oss-20b:free" {
			t.Errorf("study model = %q, want inherited free model", cfg.Models["study"].Model)
		}
		if cfg.Temperature == nil || *cfg.Temperature != 0.6 {
			t.Errorf("temperature = %v, want inherited 0.6", cfg.Temperature)
		}
	})

	t.Run("field-level merge within a shared role", func(t *testing.T) {
		dir := t.TempDir()
		userTemp := 0.8
		projectTemp := 0.3
		userPath := write(t, dir, "user.json", `{
			"temperature": 0.8,
			"models": {"code": {"model": "qwen/qwen3-coder:free", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY", "temperature": 0.8}}
		}`)
		projPath := write(t, dir, "proj.json", `{
			"temperature": 0.3,
			"models": {"code": {"model": "openai/gpt-oss-120b:free"}}
		}`)
		cfg := loadMergedConfig(userPath, projPath)
		code := cfg.Models["code"]
		if code.Model != "openai/gpt-oss-120b:free" {
			t.Errorf("model = %q, want project override", code.Model)
		}
		if code.Endpoint != "https://openrouter.ai/api/v1" || code.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("endpoint/key_env should inherit from user: %+v", code)
		}
		if cfg.Temperature == nil || *cfg.Temperature != projectTemp {
			t.Errorf("top-level temperature = %v, want project override %v", cfg.Temperature, projectTemp)
		}
		if code.Temperature == nil || *code.Temperature != userTemp {
			t.Errorf("role temperature = %v, want inherited role value %v", code.Temperature, userTemp)
		}
	})

	t.Run("only one layer present", func(t *testing.T) {
		dir := t.TempDir()
		userPath := write(t, dir, "user.json", `{"backend": {"type": "openrouter"}}`)
		if cfg := loadMergedConfig(userPath, filepath.Join(dir, "missing.json")); cfg == nil || cfg.Backend.Type != "openrouter" {
			t.Errorf("user-only load failed: %+v", cfg)
		}
		projPath := write(t, dir, "proj.json", `{"backend": {"type": "litellm"}}`)
		if cfg := loadMergedConfig(filepath.Join(dir, "missing.json"), projPath); cfg == nil || cfg.Backend.Type != "litellm" {
			t.Errorf("project-only load failed: %+v", cfg)
		}
	})

	t.Run("neither present returns nil", func(t *testing.T) {
		if cfg := loadMergedConfig("", ""); cfg != nil {
			t.Errorf("want nil when no layer exists, got %+v", cfg)
		}
	})

	t.Run("malformed layer degrades to absent", func(t *testing.T) {
		dir := t.TempDir()
		bad := write(t, dir, "bad.json", `{not json`)
		good := write(t, dir, "good.json", `{"backend": {"type": "openrouter"}}`)
		// Bad user layer, good project layer → project alone survives.
		if cfg := loadMergedConfig(bad, good); cfg == nil || cfg.Backend.Type != "openrouter" {
			t.Errorf("malformed user layer should be ignored: %+v", cfg)
		}
	})
}

// TestReadConfigFileWarnsOnMalformedJSON pins the distinction between a config
// that is absent and one that is broken. Both fall back to the lower layer, but
// only the broken one says so: a real user config sat inert for weeks with a
// single missing brace while cortex quietly served the zero-config fallback,
// and nothing on any surface mentioned it.
func TestReadConfigFileWarnsOnMalformedJSON(t *testing.T) {
	// The malformed fixture is the shape the real breakage took: an inner
	// object left open, so the key after it nests instead of closing the file.
	const malformed = `{
  "backend": { "type": "litellm", "endpoint": "http://chatterbox:4000" },
  "models": {
    "code": { "model": "some/model", "window": 500000
  },
  "scan": { "roots": ["/tmp"] }
}`

	tests := []struct {
		name     string
		write    bool
		body     string
		wantNil  bool
		wantWarn bool
	}{
		{name: "malformed json warns and falls back", write: true, body: malformed, wantNil: true, wantWarn: true},
		{name: "valid json loads silently", write: true, body: `{"backend":{"type":"ollama"}}`, wantNil: false, wantWarn: false},
		{name: "absent file is silent", write: false, wantNil: true, wantWarn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}

			var cfg *Config
			out := captureStderr(t, func() { cfg = readConfigFile(path) })

			if got := cfg == nil; got != tc.wantNil {
				t.Errorf("readConfigFile returned nil=%v, want nil=%v", got, tc.wantNil)
			}
			warned := strings.Contains(out, "malformed config")
			if warned != tc.wantWarn {
				t.Errorf("warned=%v, want %v; stderr=%q", warned, tc.wantWarn, out)
			}
			if tc.wantWarn && !strings.Contains(out, path) {
				t.Errorf("warning omits the offending path; stderr=%q", out)
			}
		})
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it, so a test can assert on warnUnknownRoles' (or
// any other) stderr message without letting it leak into `go test`'s own
// output.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	return buf.String()
}

// E1 back-compat (docs/completion-roadmap.md): an old config.json with a
// role key removed from the configurable surface (hard-code/reason/fast/
// rerank/tools were audited dead and dropped) must still load without
// error — the key sits inert in cfg.Models, unreachable from
// resolveBinding/selectModel (both keyed off rolePolicies) — and produces
// exactly one stderr warning naming it.
func TestLoadMergedConfigUnknownRoleIgnoredWithWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"backend": {"type": "openrouter"},
		"models": {
			"code": {"model": "qwen/qwen3-coder:free"},
			"hard-code": {"model": "old-hard-code-model"},
			"rerank": {"model": "old-rerank-model"}
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var cfg *Config
	stderr := captureStderr(t, func() {
		cfg = loadMergedConfig(path, "")
	})

	if cfg == nil {
		t.Fatal("expected a non-nil config — unknown role keys must not error")
	}
	if cfg.Models["code"].Model != "qwen/qwen3-coder:free" {
		t.Errorf("code model = %q, want the known role to still resolve", cfg.Models["code"].Model)
	}
	if !strings.Contains(stderr, "hard-code") || !strings.Contains(stderr, "rerank") {
		t.Errorf("stderr warning = %q, want it to name both unknown roles", stderr)
	}
	if n := strings.Count(strings.TrimRight(stderr, "\n"), "\n"); n != 0 {
		t.Errorf("stderr = %q, want exactly one warning line", stderr)
	}
	// The unknown role is never visited by resolveBinding/selectModel:
	// nothing in rolePolicies matches it, so fleet auto-selection can't
	// accidentally pick it.
	if got := selectModel(testFleet, "hard-code"); got != "" {
		t.Errorf("selectModel(hard-code) = %q, want empty — the role is unknown", got)
	}
}

// A config with only known roles produces no warning at all — the warning
// is strictly for the back-compat path, not a default nag.
func TestLoadMergedConfigKnownRolesOnlyProducesNoWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"models": {"code": {"model": "x"}, "study": {"model": "y"}, "embed": {"model": "z"}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		loadMergedConfig(path, "")
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want no warning for an all-known-role config", stderr)
	}
}

// TemplateKwargs: off and on are both said affirmatively; levels degrade to
// the affirmative on (this dialect can't represent them); only unset defers
// to the model's template default (docs/thinking-models.md §2).
func TestTemplateKwargs(t *testing.T) {
	enabled := func(b bool) *bool { return &b }
	tests := []struct {
		name     string
		thinking llm.Effort
		want     *bool // nil: no kwargs; else the expected enable_thinking
	}{
		{"unset defers to template default", llm.Effort{}, nil},
		{"on emits enable_thinking=true", llm.Effort{Level: llm.EffortOn}, enabled(true)},
		{"off emits enable_thinking=false", llm.Effort{Level: llm.EffortOff}, enabled(false)},
		{"high degrades to affirmative on", llm.Effort{Level: llm.EffortHigh}, enabled(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := ModelSpec{Thinking: tt.thinking}.TemplateKwargs()
			if tt.want == nil {
				if kw != nil {
					t.Errorf("TemplateKwargs() = %v, want nil", kw)
				}
				return
			}
			if v, ok := kw["enable_thinking"].(bool); !ok || v != *tt.want {
				t.Errorf("TemplateKwargs() = %v, want enable_thinking=%v", kw, *tt.want)
			}
		})
	}
}

// TestModelSpecReasoning covers the OpenRouter dialect counterpart to
// TemplateKwargs.
func TestModelSpecReasoning(t *testing.T) {
	tests := []struct {
		name     string
		thinking llm.Effort
		want     *llm.Reasoning
	}{
		{"unset: nil", llm.Effort{}, nil},
		{"on: enabled true (affirmative)", llm.Effort{Level: llm.EffortOn}, &llm.Reasoning{}},
		{"off: enabled false", llm.Effort{Level: llm.EffortOff}, &llm.Reasoning{}},
		{"high: effort high", llm.Effort{Level: llm.EffortHigh}, &llm.Reasoning{Effort: "high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModelSpec{Thinking: tt.thinking}.Reasoning()
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("Reasoning() = %+v, want %+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Effort != tt.want.Effort {
				t.Errorf("Reasoning().Effort = %q, want %q", got.Effort, tt.want.Effort)
			}
		})
	}
}

// thinkingLabel is the eval-telemetry attribution derived from a built
// kwargs map (ModelSpec.TemplateKwargs' output) — "off" only when
// enable_thinking is explicitly false, "on" for every other shape (nil, an
// unrelated kwargs map, or enable_thinking=true).
func TestThinkingLabel(t *testing.T) {
	tests := []struct {
		name   string
		kwargs map[string]any
		want   string
	}{
		{"nil kwargs: on", nil, "on"},
		{"enable_thinking=false: off", map[string]any{"enable_thinking": false}, "off"},
		{"enable_thinking=true: on", map[string]any{"enable_thinking": true}, "on"},
		{"unrelated kwargs: on", map[string]any{"some_other_key": "x"}, "on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := thinkingLabel(tt.kwargs); got != tt.want {
				t.Errorf("thinkingLabel(%v) = %q, want %q", tt.kwargs, got, tt.want)
			}
		})
	}
}

// The wire body must omit chat_template_kwargs when unset (universal
// compatibility) and carry it when the code role disables thinking.
func TestRequestMarshalsTemplateKwargs(t *testing.T) {
	bare, err := json.Marshal(&AgentRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "chat_template_kwargs") {
		t.Errorf("unset kwargs should be omitted from the body: %s", bare)
	}

	req := &AgentRequest{Model: "m", ChatTemplateKwargs: map[string]any{"enable_thinking": false}}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"chat_template_kwargs":{"enable_thinking":false}`) {
		t.Errorf("kwargs missing from body: %s", b)
	}
}
