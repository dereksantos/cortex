package config

import (
	"os"
	"testing"
)

func TestResolveModelRouteExplicitEndpointPrefix(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{Name: "chatterbox", BaseURL: "http://localhost:13305/v1"},
		},
	}
	ep, model, ok := cfg.ResolveModelRoute("chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF")
	if !ok {
		t.Fatal("expected resolution to succeed")
	}
	if ep == nil || ep.Name != "chatterbox" {
		t.Errorf("endpoint: got %v want chatterbox", ep)
	}
	if model != "Qwen3-Coder-30B-A3B-Instruct-GGUF" {
		t.Errorf("model: got %q", model)
	}
}

func TestResolveModelRouteUnknownPrefixFallsThrough(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{Name: "chatterbox", BaseURL: "http://localhost:13305/v1"},
		},
	}
	// "anthropic/foo" is a valid OpenRouter route; should fall through
	// to case 3 so the legacy slash heuristic still routes it.
	ep, _, ok := cfg.ResolveModelRoute("anthropic/claude-haiku-4.5")
	if ok || ep != nil {
		t.Errorf("expected unknown prefix to fall through, got %+v", ep)
	}
}

func TestResolveModelRouteRoleMapBareName(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{Name: "chatterbox", BaseURL: "http://localhost:13305/v1"},
		},
		Models: &ModelsMap{
			Code: &RoleAssignment{Endpoint: "chatterbox", Model: "Qwen3-Coder-30B-A3B-Instruct-GGUF"},
			Fast: &RoleAssignment{Endpoint: "chatterbox", Model: "qwen3-8b-FLM"},
		},
	}
	ep, model, ok := cfg.ResolveModelRoute("Qwen3-Coder-30B-A3B-Instruct-GGUF")
	if !ok || ep == nil || ep.Name != "chatterbox" {
		t.Errorf("bare-name role-map lookup failed: ep=%v ok=%v", ep, ok)
	}
	if model != "Qwen3-Coder-30B-A3B-Instruct-GGUF" {
		t.Errorf("model: got %q", model)
	}

	// Bare name not in any role assignment should fall through.
	if _, _, ok := cfg.ResolveModelRoute("some-other-model"); ok {
		t.Errorf("unknown bare name should not resolve")
	}
}

// TestResolveModelRouteEndpointModelsList — case 3 of
// ResolveModelRoute. A bare name that appears in an endpoint's
// declared Models list routes to that endpoint; first endpoint wins
// on collision.
func TestResolveModelRouteEndpointModelsList(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{
				Name:    "chatterbox",
				BaseURL: "http://localhost:4000",
				Models:  []string{"coder", "reasoner", "xlam-1b-fc-r"},
			},
		},
	}
	for _, want := range []string{"coder", "reasoner", "xlam-1b-fc-r"} {
		ep, model, ok := cfg.ResolveModelRoute(want)
		if !ok || ep == nil || ep.Name != "chatterbox" || model != want {
			t.Errorf("%s: ep=%v model=%q ok=%v", want, ep, model, ok)
		}
	}
	// Unlisted bare name falls through.
	if _, _, ok := cfg.ResolveModelRoute("unlisted-model"); ok {
		t.Errorf("unlisted bare name should not resolve via case 3")
	}
}

// TestResolveModelRouteFirstEndpointWinsOnCollision pins the
// declaration-order tie-break for case 3.
func TestResolveModelRouteFirstEndpointWinsOnCollision(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{Name: "primary", BaseURL: "http://primary:4000", Models: []string{"shared"}},
			{Name: "secondary", BaseURL: "http://secondary:4000", Models: []string{"shared"}},
		},
	}
	ep, _, ok := cfg.ResolveModelRoute("shared")
	if !ok || ep == nil || ep.Name != "primary" {
		t.Errorf("expected primary endpoint to win on collision, got %v", ep)
	}
}

func TestResolveModelRouteNilConfigSafe(t *testing.T) {
	var cfg *Config
	if ep, _, ok := cfg.ResolveModelRoute("anything"); ok || ep != nil {
		t.Errorf("nil config should return false, got %v", ep)
	}
}

func TestResolveAPIKeyEnvWinsOverInline(t *testing.T) {
	os.Setenv("CORTEX_TEST_KEY_VAR", "from-env")
	defer os.Unsetenv("CORTEX_TEST_KEY_VAR")

	ep := EndpointDef{APIKey: "inline-fallback", APIKeyEnv: "CORTEX_TEST_KEY_VAR"}
	if got := ep.ResolveAPIKey(); got != "from-env" {
		t.Errorf("APIKeyEnv should win, got %q", got)
	}

	// Env unset → falls back to inline.
	os.Unsetenv("CORTEX_TEST_KEY_VAR")
	if got := ep.ResolveAPIKey(); got != "inline-fallback" {
		t.Errorf("APIKey fallback failed, got %q", got)
	}

	// Both empty → returns empty (no-auth endpoint).
	empty := EndpointDef{}
	if got := empty.ResolveAPIKey(); got != "" {
		t.Errorf("empty endpoint should return empty key, got %q", got)
	}
}

func TestFindEndpoint(t *testing.T) {
	cfg := &Config{
		Endpoints: []EndpointDef{
			{Name: "a", BaseURL: "http://a"},
			{Name: "b", BaseURL: "http://b"},
		},
	}
	if ep := cfg.FindEndpoint("a"); ep == nil || ep.BaseURL != "http://a" {
		t.Errorf("FindEndpoint(a) = %v", ep)
	}
	if ep := cfg.FindEndpoint("missing"); ep != nil {
		t.Errorf("FindEndpoint(missing) should be nil, got %v", ep)
	}
}

func TestDefaultGenerationModelOllamaWins(t *testing.T) {
	cfg := &Config{OllamaModel: "qwen2.5-coder:1.5b", AnthropicModel: "anthropic/claude-haiku-4.5"}
	if got := cfg.DefaultGenerationModel(); got != "qwen2.5-coder:1.5b" {
		t.Errorf("DefaultGenerationModel = %q, want qwen2.5-coder:1.5b", got)
	}
}

func TestDefaultGenerationModelFallsBackToAnthropic(t *testing.T) {
	cfg := &Config{AnthropicModel: "anthropic/claude-haiku-4.5"}
	if got := cfg.DefaultGenerationModel(); got != "anthropic/claude-haiku-4.5" {
		t.Errorf("DefaultGenerationModel = %q, want anthropic/claude-haiku-4.5", got)
	}
}

func TestDefaultGenerationModelEmpty(t *testing.T) {
	cfg := &Config{}
	if got := cfg.DefaultGenerationModel(); got != "" {
		t.Errorf("DefaultGenerationModel = %q, want empty", got)
	}
}

func TestDefaultGenerationModelNilSafe(t *testing.T) {
	var cfg *Config
	if got := cfg.DefaultGenerationModel(); got != "" {
		t.Errorf("nil cfg DefaultGenerationModel = %q, want empty", got)
	}
}
