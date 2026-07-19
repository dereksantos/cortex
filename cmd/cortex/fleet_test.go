package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// testFleet mirrors the live fleet for resolution tests. qwen3-4b carries a
// "fast" Role tag as fleet-discovery data only — no rolePolicy matches that
// tag since E1's role collapse (docs/completion-roadmap.md), but the model
// itself is still a useful non-thinking fixture for tests below that pin it
// explicitly.
var testFleet = Fleet{
	"coder":        {Role: "coder", MaxInput: 131072, Thinking: true, SwapGroup: "igpu-8080"},
	"reasoner":     {Role: "reasoner", MaxInput: 32768, Thinking: true, SwapGroup: "igpu-8080"},
	"reasoner-npu": {Role: "reasoner", MaxInput: 32768, Thinking: true},
	"qwen3-4b":     {Role: "fast", MaxInput: 131072, Thinking: false, SwapGroup: "igpu-8080"},
	"embedder":     {Role: "embedder", MaxInput: 32768},
}

// selectModel picks a role's model from discovery by capability, with no
// model names baked in source. study prefers swap-free silicon.
func TestSelectModel(t *testing.T) {
	cases := []struct{ role, want string }{
		{roleCode, "coder"},
		{roleStudy, "reasoner-npu"},
		{roleEmbed, "embedder"},
	}
	for _, c := range cases {
		if got := selectModel(testFleet, c.role); got != c.want {
			t.Errorf("selectModel(%s) = %q, want %q", c.role, got, c.want)
		}
	}
	t.Run("study auto-falls-back to reasoner when the NPU model is gone", func(t *testing.T) {
		f := Fleet{"reasoner": {Role: "reasoner", MaxInput: 32768, SwapGroup: "igpu-8080"}}
		if got := selectModel(f, roleStudy); got != "reasoner" {
			t.Errorf("got %q, want reasoner", got)
		}
	})
	t.Run("nil fleet selects nothing", func(t *testing.T) {
		if got := selectModel(nil, roleCode); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestResolveBinding(t *testing.T) {
	t.Run("nil config selects from discovery by capability", func(t *testing.T) {
		var c *Config
		code := c.resolveBinding(roleCode, testFleet)
		if code.Model != "coder" || code.Endpoint == "" || code.Window != 131072 {
			t.Errorf("code = %+v", code)
		}
		if study := c.resolveBinding(roleStudy, testFleet); study.Model != "reasoner-npu" || study.Window != 32768 {
			t.Errorf("study = %+v", study)
		}
	})

	t.Run("config pins the model; window from discovery, endpoint from backend", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{roleStudy: {Model: "coder"}}}
		s := c.resolveBinding(roleStudy, testFleet)
		if s.Model != "coder" {
			t.Errorf("model = %q, want pinned coder", s.Model)
		}
		if s.Window != 131072 || s.Endpoint != c.backendEndpoint() {
			t.Errorf("window from discovery + endpoint from backend, got %+v", s)
		}
	})

	t.Run("config-pinned window wins over discovery", func(t *testing.T) {
		c := &Config{Models: map[string]ModelSpec{roleCode: {Window: 8000}}}
		if s := c.resolveBinding(roleCode, testFleet); s.Window != 8000 {
			t.Errorf("window = %d, want pinned 8000", s.Window)
		}
	})

	t.Run("temperature defaults globally and per-role can override", func(t *testing.T) {
		global := 0.7
		codeTemp := 0.2
		c := &Config{
			Temperature: &global,
			Models:      map[string]ModelSpec{roleCode: {Temperature: &codeTemp}},
		}
		if got := c.resolveBinding(roleCode, testFleet).temperature(defaultTemperature); got != codeTemp {
			t.Errorf("code temperature = %v, want per-role %v", got, codeTemp)
		}
		if got := c.resolveBinding(roleStudy, testFleet).temperature(defaultTemperature); got != global {
			t.Errorf("study temperature = %v, want global %v", got, global)
		}
		var nilCfg *Config
		if got := nilCfg.resolveBinding(roleCode, testFleet).temperature(defaultTemperature); got != defaultTemperature {
			t.Errorf("nil config temperature = %v, want default %v", got, defaultTemperature)
		}
	})

	t.Run("thinking on for code by default; fleet degrades non-thinkers; config can disable", func(t *testing.T) {
		var nilCfg *Config
		// Pure role default (nil fleet): code deliberates by default —
		// Derek's 2026-07-17 call; effort-off is opt-in via config.
		if code := nilCfg.resolveBinding(roleCode, nil); code.Thinking.Level != llm.EffortOn {
			t.Errorf("code Thinking = %+v, want on by default", code.Thinking)
		}
		// testFleet's coder model IS thinking-capable (hybrid), so the "on"
		// default survives fleet resolution. (The degrade-to-unset path for a
		// non-thinking fleet model is covered by the webui models golden.)
		if code := nilCfg.resolveBinding(roleCode, testFleet); code.Thinking.Level != llm.EffortOn {
			t.Errorf("code Thinking = %+v, want on (hybrid fleet model keeps it)", code.Thinking)
		}
		// study draws from the reasoner tag and deliberates: an explicit "on"
		// default now, rather than the old implicit nil.
		if study := nilCfg.resolveBinding(roleStudy, testFleet); study.Thinking.Level != llm.EffortOn {
			t.Errorf("study Thinking = %+v, want on (reasoner thinks by default)", study.Thinking)
		}
		c := &Config{Models: map[string]ModelSpec{roleCode: {Thinking: llm.Effort{Level: llm.EffortOff}}}}
		if got := c.resolveBinding(roleCode, testFleet); got.Thinking.Level != llm.EffortOff {
			t.Errorf("config thinking=off should win, got %+v", got.Thinking)
		}
	})

	// Role-default "off" degrading to unset for a "none" (non-thinking)
	// fleet model is covered generically by TestApplyFleet's "drops
	// enable_thinking for a non-thinking model" — no live role defaults to
	// "off" since E1's role collapse removed the fast role.

	t.Run("explicit config thinking survives a backend-non-thinking model", func(t *testing.T) {
		// qwen3-4b is reported thinking:false by the backend, but it thinks by
		// default and needs enable_thinking=false. A config override must NOT be
		// stripped by applyFleet (the regression that made study run it slow).
		c := &Config{Models: map[string]ModelSpec{roleStudy: {Model: "qwen3-4b", Thinking: llm.Effort{Level: llm.EffortOff}}}}
		got := c.resolveBinding(roleStudy, testFleet)
		if got.Model != "qwen3-4b" {
			t.Fatalf("model = %q, want qwen3-4b", got.Model)
		}
		if got.Thinking.Level != llm.EffortOff {
			t.Errorf("config thinking=off must survive applyFleet, got %+v", got.Thinking)
		}
		if kw := got.TemplateKwargs(); kw["enable_thinking"] != false {
			t.Errorf("TemplateKwargs should send enable_thinking=false, got %v", kw)
		}
	})

	t.Run("key_service: per-role override, else backend default", func(t *testing.T) {
		c := &Config{
			Backend: Backend{KeyService: "backend-key"},
			Models:  map[string]ModelSpec{roleCode: {KeyService: "cortex-openrouter"}},
		}
		if got := c.resolveBinding(roleCode, testFleet); got.KeyService != "cortex-openrouter" {
			t.Errorf("per-role key = %q, want cortex-openrouter", got.KeyService)
		}
		if got := c.resolveBinding(roleStudy, testFleet); got.KeyService != "backend-key" {
			t.Errorf("study should inherit backend key, got %q", got.KeyService)
		}
	})

	t.Run("key_env: per-role override, else backend default", func(t *testing.T) {
		c := &Config{
			Backend: Backend{KeyEnv: "BACKEND_KEY"},
			Models:  map[string]ModelSpec{roleCode: {KeyEnv: "OPENROUTER_API_KEY"}},
		}
		if got := c.resolveBinding(roleCode, testFleet); got.KeyEnv != "OPENROUTER_API_KEY" {
			t.Errorf("per-role key_env = %q, want OPENROUTER_API_KEY", got.KeyEnv)
		}
		if got := c.resolveBinding(roleStudy, testFleet); got.KeyEnv != "BACKEND_KEY" {
			t.Errorf("study should inherit backend key_env, got %q", got.KeyEnv)
		}
	})
}

func TestResolveKey(t *testing.T) {
	t.Run("key_env wins when the var is set", func(t *testing.T) {
		t.Setenv("CORTEX_TEST_KEY", "sk-from-env")
		if got := resolveKey(ModelSpec{KeyEnv: "CORTEX_TEST_KEY", KeyService: "ignored"}); got != "sk-from-env" {
			t.Errorf("resolveKey = %q, want sk-from-env", got)
		}
	})

	t.Run("empty when neither source is set", func(t *testing.T) {
		if got := resolveKey(ModelSpec{}); got != "" {
			t.Errorf("resolveKey = %q, want empty", got)
		}
	})

	t.Run("blank env value falls through to keychain", func(t *testing.T) {
		t.Setenv("CORTEX_TEST_KEY", "   ")
		// KeyService is empty, so keychainKey returns "" without shelling out —
		// proves the env path doesn't return a blank value as if it were a key.
		if got := resolveKey(ModelSpec{KeyEnv: "CORTEX_TEST_KEY"}); got != "" {
			t.Errorf("resolveKey = %q, want empty (blank env is not a key)", got)
		}
	})
}

// A realistic /model/info payload (trimmed to the fields we read, plus extra
// keys to prove we ignore them) for discovery tests.
const fleetInfoJSON = `{"data":[
  {"model_name":"coder","litellm_params":{"model":"openai/coder"},"model_info":{"max_input_tokens":131072,"role":"coder","silicon":"igpu","thinking":true,"swap_group":"igpu-8080","always_warm":false,"experimental":false,"input_cost_per_token":0}},
  {"model_name":"reasoner-npu","model_info":{"max_input_tokens":32768,"role":"reasoner","silicon":"npu","thinking":true,"swap_group":null,"always_warm":true}},
  {"model_name":"reranker","model_info":{"max_input_tokens":8192,"role":"reranker","silicon":"cpu","thinking":null}},
  {"model_name":"or-levels","model_info":{"max_input_tokens":8192,"role":"reasoner","thinking_mode":"levels"}}
]}`

func fleetServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" {
			t.Errorf("discovery hit %q, want /model/info", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverFleet(t *testing.T) {
	t.Run("parses model_info, ignores extra keys", func(t *testing.T) {
		srv := fleetServer(t, 200, fleetInfoJSON)
		f := discoverFleet(context.Background(), srv.URL)
		if f == nil {
			t.Fatal("expected a fleet, got nil")
		}
		coder, ok := f["coder"]
		if !ok {
			t.Fatal("coder missing from fleet")
		}
		if coder.MaxInput != 131072 || coder.Role != "coder" || coder.Silicon != "igpu" || !coder.Thinking || coder.SwapGroup != "igpu-8080" {
			t.Errorf("coder = %+v", coder)
		}
		if npu := f["reasoner-npu"]; npu.MaxInput != 32768 || npu.SwapGroup != "" || !npu.AlwaysWarm {
			t.Errorf("reasoner-npu = %+v", npu)
		}
		if rr := f["reranker"]; rr.MaxInput != 8192 || rr.Thinking {
			t.Errorf("reranker = %+v", rr)
		}
		if lv := f["or-levels"]; lv.ThinkingMode != "levels" {
			t.Errorf("or-levels ThinkingMode = %q, want levels", lv.ThinkingMode)
		}
	})

	t.Run("best-effort: nil on non-200, bad JSON, empty", func(t *testing.T) {
		for _, c := range []struct {
			name, body string
			status     int
		}{
			{"500", "{}", 500},
			{"bad json", "not json", 200},
			{"empty data", `{"data":[]}`, 200},
		} {
			t.Run(c.name, func(t *testing.T) {
				srv := fleetServer(t, c.status, c.body)
				if f := discoverFleet(context.Background(), srv.URL); f != nil {
					t.Errorf("want nil fleet, got %+v", f)
				}
			})
		}
	})

	t.Run("nil on unreachable backend", func(t *testing.T) {
		if f := discoverFleet(context.Background(), "http://127.0.0.1:1"); f != nil {
			t.Errorf("want nil for unreachable, got %+v", f)
		}
	})
}

// No backend address lives in source: the endpoint resolves config > env >
// neutral localhost, and every role inherits it unless pinned.
func TestBackendEndpoint(t *testing.T) {
	t.Run("neutral localhost fallback, no env", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "")
		var c *Config
		if got := c.backendEndpoint(); got != defaultEndpoint {
			t.Errorf("nil config = %q, want %q", got, defaultEndpoint)
		}
		// Source carries no address: a binding resolved with no config/env/fleet
		// falls back to the neutral localhost only.
		if b := (&Config{}).resolveBinding(roleCode, nil); b.Endpoint != defaultEndpoint {
			t.Errorf("resolved endpoint = %q, want neutral %q", b.Endpoint, defaultEndpoint)
		}
	})
	t.Run("env overrides the fallback", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "http://env-host:4000")
		var c *Config
		if got := c.backendEndpoint(); got != "http://env-host:4000" {
			t.Errorf("env = %q, want http://env-host:4000", got)
		}
	})
	t.Run("config wins over env, and every role inherits it", func(t *testing.T) {
		t.Setenv("CORTEX_BACKEND", "http://env-host:4000")
		c := &Config{Backend: Backend{Endpoint: "http://cfg-host:4000", KeyService: "cortex-openrouter"}}
		if got := c.backendEndpoint(); got != "http://cfg-host:4000" {
			t.Errorf("config = %q, want http://cfg-host:4000", got)
		}
		for _, role := range []string{roleCode, roleStudy, roleEmbed} {
			s := c.resolveBinding(role, testFleet)
			if s.Endpoint != "http://cfg-host:4000" {
				t.Errorf("%s endpoint = %q, want backend address", role, s.Endpoint)
			}
			if s.KeyService != "cortex-openrouter" {
				t.Errorf("%s should inherit backend key_service, got %q", role, s.KeyService)
			}
		}
	})
	t.Run("a role may pin its own endpoint", func(t *testing.T) {
		c := &Config{
			Backend: Backend{Endpoint: "http://cfg-host:4000"},
			Models:  map[string]ModelSpec{roleEmbed: {Endpoint: "http://embed-host:8081"}},
		}
		if s := c.resolveBinding(roleEmbed, testFleet); s.Endpoint != "http://embed-host:8081" {
			t.Errorf("pinned endpoint = %q, want http://embed-host:8081", s.Endpoint)
		}
	})
}

func TestApplyFleet(t *testing.T) {
	fleet := Fleet{
		"coder":     {MaxInput: 131072, Thinking: true},       // hybrid
		"qwen3-4b":  {MaxInput: 131072, Thinking: false},      // none
		"or-model":  {MaxInput: 8192, ThinkingMode: "levels"}, // explicit thinking_mode
		"always-on": {MaxInput: 8192, ThinkingMode: "always"}, // can't stop reasoning
	}
	t.Run("fills an unset window from discovery", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder"}, fleet)
		if got.Window != 131072 {
			t.Errorf("window = %d, want 131072", got.Window)
		}
	})
	t.Run("leaves a config-pinned window intact", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Window: 8000}, fleet)
		if got.Window != 8000 {
			t.Errorf("window = %d, want pinned 8000", got.Window)
		}
	})
	t.Run("keeps enable_thinking for a hybrid model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if got.Thinking.Level != llm.EffortOff {
			t.Errorf("thinking spec should survive for a hybrid model, got %+v", got.Thinking)
		}
	})
	t.Run("drops enable_thinking for a non-thinking model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "qwen3-4b", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if !got.Thinking.IsZero() {
			t.Errorf("non-thinking model should not carry the kwarg, got %+v", got.Thinking)
		}
	})
	t.Run("a level degrades to on for a hybrid model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "coder", Thinking: llm.Effort{Level: llm.EffortHigh}}, fleet)
		if got.Thinking.Level != llm.EffortOn {
			t.Errorf("thinking = %+v, want degraded to on (hybrid has no real levels)", got.Thinking)
		}
	})
	t.Run("a level survives for a levels-capable model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "or-model", Thinking: llm.Effort{Level: llm.EffortHigh}}, fleet)
		if got.Thinking.Level != llm.EffortHigh {
			t.Errorf("thinking = %+v, want high (unchanged)", got.Thinking)
		}
	})
	t.Run("off degrades to on for an always-reasoning model", func(t *testing.T) {
		got := applyFleet(ModelSpec{Model: "always-on", Thinking: llm.Effort{Level: llm.EffortOff}}, fleet)
		if got.Thinking.Level != llm.EffortOn {
			t.Errorf("thinking = %+v, want degraded to on (can't stop reasoning)", got.Thinking)
		}
	})
	t.Run("unknown model and nil fleet pass through untouched", func(t *testing.T) {
		in := ModelSpec{Model: "mystery", Window: 4096, Thinking: llm.Effort{Level: llm.EffortOff}}
		if got := applyFleet(in, fleet); got != in {
			t.Errorf("unknown model mutated: %+v", got)
		}
		if got := applyFleet(in, nil); got != in {
			t.Errorf("nil fleet mutated: %+v", got)
		}
	})
}

func TestSharedSwapGroup(t *testing.T) {
	fleet := Fleet{
		"coder":        {SwapGroup: "igpu-8080"},
		"reasoner":     {SwapGroup: "igpu-8080"},
		"reasoner-npu": {SwapGroup: ""},
	}
	spec := func(m string) ModelSpec { return ModelSpec{Model: m} }
	t.Run("flags two different models in the same group", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("reasoner")); g != "igpu-8080" {
			t.Errorf("want igpu-8080, got %q", g)
		}
	})
	t.Run("no conflict across silicon (swap-free study)", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("reasoner-npu")); g != "" {
			t.Errorf("want no conflict, got %q", g)
		}
	})
	t.Run("same model is not a conflict, nil fleet is safe", func(t *testing.T) {
		if g := sharedSwapGroup(fleet, spec("coder"), spec("coder")); g != "" {
			t.Errorf("same model should not conflict, got %q", g)
		}
		if g := sharedSwapGroup(nil, spec("coder"), spec("reasoner")); g != "" {
			t.Errorf("nil fleet should be safe, got %q", g)
		}
	})
}

func TestSetModel(t *testing.T) {
	s := &CortexSession{Request: &AgentRequest{Model: "coder", BaseURL: "http://backend.example:4000"}}
	s.SetModel("reasoner")
	if s.Request.Model != "reasoner" {
		t.Errorf("model = %q, want reasoner", s.Request.Model)
	}
	if s.Request.BaseURL != "http://backend.example:4000" {
		t.Errorf("endpoint should be unchanged on a model swap, got %q", s.Request.BaseURL)
	}
}

// TestSetModelReResolvesEffortAndWindow covers P3's seam fix
// (docs/thinking-models.md known seam bug #1): SetModel used to swap only
// the model name, leaving effort wire fields and the window stale from the
// OLD binding. It must now re-derive both for the NEW model via the
// discovered Fleet, and clear to neutral when the fleet doesn't know it.
func TestSetModelReResolvesEffortAndWindow(t *testing.T) {
	fleet := Fleet{
		"hybrid-model": {MaxInput: 65536, Thinking: true},  // hybrid
		"plain-model":  {MaxInput: 16384, Thinking: false}, // none
	}
	t.Run("switching to a fleet-known hybrid model re-derives the window", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		s.SetModel("hybrid-model")
		if s.Window != 65536 {
			t.Errorf("Window = %d, want 65536 (from the fleet)", s.Window)
		}
	})
	t.Run("prior explicit off degrades to unset for a non-thinking model", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		s.SetModel("plain-model")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (plain-model can't honor any ask)", s.Request.Effort)
		}
		if s.Request.ChatTemplateKwargs != nil {
			t.Errorf("ChatTemplateKwargs = %v, want nil", s.Request.ChatTemplateKwargs)
		}
	})
	t.Run("prior effort survives and stays on for a hybrid model", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		s.SetModel("hybrid-model")
		if s.Request.Effort.Level != llm.EffortOn {
			t.Errorf("Effort = %+v, want on", s.Request.Effort)
		}
	})
	t.Run("fleet nil clears effort to neutral and window to fallback", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})
		s.SetModel("anything")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (fleet unknown)", s.Request.Effort)
		}
		if s.Request.ChatTemplateKwargs != nil {
			t.Errorf("ChatTemplateKwargs = %v, want nil", s.Request.ChatTemplateKwargs)
		}
		if s.Window != 0 {
			t.Errorf("Window = %d, want 0 (falls back via windowSize())", s.Window)
		}
	})
	t.Run("fleet known but model absent clears effort to neutral", func(t *testing.T) {
		s := &CortexSession{Request: &AgentRequest{Model: "coder"}, Fleet: fleet}
		applyEffort(s.Request, llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOn})
		s.SetModel("mystery-model")
		if !s.Request.Effort.IsZero() {
			t.Errorf("Effort = %+v, want unset (model not in fleet)", s.Request.Effort)
		}
	})
}
