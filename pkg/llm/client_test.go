package llm

import (
	"errors"
	"os"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
)

// stubKey returns a fixed (key, source, err) — used to drive
// resolveLLMClient's keychain branch in tests without touching the
// host machine's actual keychain.
func stubKey(key string, err error) openRouterKeyResolver {
	return func() (string, string, error) {
		return key, "keychain", err
	}
}

// TestResolveLLMClient_Order pins the resolution order from
// docs/prompts/eval-principles.md: OpenRouter first (keychain then
// env), Anthropic only as a last resort. Each subtest pre-stages env
// + keychain so the assertion is unambiguous about which path won.
func TestResolveLLMClient_Order(t *testing.T) {
	cases := []struct {
		name           string
		keychain       openRouterKeyResolver
		envOR          string // OPEN_ROUTER_API_KEY
		envAnthropic   string // ANTHROPIC_API_KEY
		wantSource     LLMClientSource
		wantErrSubstr  string // non-empty -> expect this in error, no client
		wantClientName string // non-empty -> assert returned client's Name()
	}{
		{
			name:           "keychain wins over env",
			keychain:       stubKey("kc-key", nil),
			envOR:          "env-key",
			envAnthropic:   "ant-key",
			wantSource:     SourceOpenRouterKeychain,
			wantClientName: "openrouter",
		},
		{
			name:           "env wins when keychain empty",
			keychain:       stubKey("", errors.New("not in keychain")),
			envOR:          "env-key",
			envAnthropic:   "ant-key",
			wantSource:     SourceOpenRouterEnv,
			wantClientName: "openrouter",
		},
		{
			name:           "anthropic fallback when no openrouter",
			keychain:       stubKey("", errors.New("not in keychain")),
			envOR:          "",
			envAnthropic:   "ant-key",
			wantSource:     SourceAnthropicEnv,
			wantClientName: "anthropic",
		},
		{
			name:          "error when no credentials anywhere",
			keychain:      stubKey("", errors.New("not in keychain")),
			envOR:         "",
			envAnthropic:  "",
			wantErrSubstr: "no LLM client available",
		},
		{
			name:           "nil keychain resolver still falls through to env",
			keychain:       nil,
			envOR:          "env-key",
			envAnthropic:   "ant-key",
			wantSource:     SourceOpenRouterEnv,
			wantClientName: "openrouter",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OPEN_ROUTER_API_KEY", c.envOR)
			t.Setenv("ANTHROPIC_API_KEY", c.envAnthropic)

			got, src, err := resolveLLMClient(nil, c.keychain)
			if c.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got client=%v src=%v", c.wantErrSubstr, got, src)
				}
				if !contains(err.Error(), c.wantErrSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), c.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if src != c.wantSource {
				t.Errorf("source = %q, want %q", src, c.wantSource)
			}
			if c.wantClientName != "" && got.Name() != c.wantClientName {
				t.Errorf("Name() = %q, want %q", got.Name(), c.wantClientName)
			}
		})
	}
}

// TestNewLLMClient_WithModel pins that WithModel applies uniformly to
// whichever backend resolves first. Both clients expose Model() so we
// can assert without touching SetModel internals.
func TestNewLLMClient_WithModel(t *testing.T) {
	cases := []struct {
		name  string
		envOR string
		envAn string
		model string
		want  string
	}{
		{"openrouter receives model", "or-key", "ant-key", "anthropic/claude-haiku-4.5", "anthropic/claude-haiku-4.5"},
		{"anthropic receives model", "", "ant-key", "claude-3-haiku-20240307", "claude-3-haiku-20240307"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OPEN_ROUTER_API_KEY", c.envOR)
			t.Setenv("ANTHROPIC_API_KEY", c.envAn)
			// Force keychain miss so env wins.
			got, _, err := resolveLLMClient(nil, stubKey("", errors.New("no keychain")))
			if err != nil {
				t.Fatalf("resolveLLMClient: %v", err)
			}
			// Apply WithModel as the public path does.
			(&llmOpts{model: c.model}).applyTo(got)

			gotModel := modelOf(t, got)
			if gotModel != c.want {
				t.Errorf("Model() = %q, want %q", gotModel, c.want)
			}
		})
	}
}

// TestNewLLMClient_WithModelPublicAPI exercises the public option path
// (NewLLMClient(...) with opts...) end-to-end, asserting via a
// post-resolution Model() readout.
func TestNewLLMClient_WithModelPublicAPI(t *testing.T) {
	t.Setenv("OPEN_ROUTER_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	got, _, err := NewLLMClient(nil, WithModel("anthropic/claude-haiku-4.5"))
	if err != nil {
		t.Skipf("no LLM client (this run has keychain or env empty): %v", err)
	}
	if m := modelOf(t, got); m != "anthropic/claude-haiku-4.5" {
		t.Errorf("Model() = %q, want anthropic/claude-haiku-4.5", m)
	}
}

// applyTo lets the WithModel-application test loop drive options
// without re-implementing the dispatch logic inside NewLLMClient.
func (o *llmOpts) applyTo(p Provider) {
	if o.model != "" {
		if ms, ok := p.(modelSetter); ok {
			ms.SetModel(o.model)
		}
	}
}

// modelOf reads Model() off whichever concrete type p is. Used by the
// WithModel tests since Provider doesn't include Model() in its public
// interface.
func modelOf(t *testing.T, p Provider) string {
	t.Helper()
	switch c := p.(type) {
	case *OpenRouterClient:
		return c.Model()
	case *AnthropicClient:
		return c.Model()
	default:
		t.Fatalf("unexpected Provider concrete type %T", p)
		return ""
	}
}

// TestNewLLMClient_LiveResolver smoke-tests the public API by going
// through the real secret.OpenRouterKey resolver. It doesn't assert a
// specific source (that depends on the machine) — only that the
// function returns a usable client when ANY credential is present, or
// a clean error when none are.
func TestNewLLMClient_LiveResolver(t *testing.T) {
	got, src, err := NewLLMClient(nil)
	if err != nil {
		// Acceptable when running on a CI box with no creds; just
		// assert the error wording is the documented one.
		if !contains(err.Error(), "no LLM client available") {
			t.Fatalf("unexpected error wording: %v", err)
		}
		return
	}
	if got == nil {
		t.Fatal("nil client with nil error")
	}
	if src == "" {
		t.Error("source is empty on success")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestNewLLMClient_BackendOllama exercises the WithBackend(BackendOllama)
// path: no credential required, default URL applied, source tagged
// correctly. This is the path that replaces the stub-key + SetAPIURL
// dance previously duplicated across calibrate_test.go,
// cmd/cortex/commands/repl.go, cmd/cortex/commands/code.go.
func TestNewLLMClient_BackendOllama(t *testing.T) {
	got, src, err := NewLLMClient(nil,
		WithBackend(BackendOllama),
		WithModel("qwen2.5-coder:1.5b"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("nil client")
	}
	if src != SourceOllamaLocal {
		t.Errorf("expected source=%s, got %s", SourceOllamaLocal, src)
	}
	// IsAvailable must be true so callers don't have to special-case
	// (the stub-key dance existed precisely because IsAvailable would
	// otherwise return false).
	if !got.IsAvailable() {
		t.Error("ollama-backed client must report IsAvailable=true (stub key handled internally)")
	}
	if ModelOf(got) != "qwen2.5-coder:1.5b" {
		t.Errorf("expected model qwen2.5-coder:1.5b, got %s", ModelOf(got))
	}
}

func TestNewLLMClient_BackendOllama_WithAPIURL(t *testing.T) {
	// Custom URL should override the default.
	custom := "http://my-ollama:11434/v1/chat/completions"
	got, _, err := NewLLMClient(nil,
		WithBackend(BackendOllama),
		WithAPIURL(custom),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, ok := got.(*OpenRouterClient)
	if !ok {
		t.Fatalf("expected *OpenRouterClient, got %T", got)
	}
	if c.apiURL != custom {
		t.Errorf("expected apiURL=%s, got %s", custom, c.apiURL)
	}
}

func TestConstructOpenRouterClient_ExplicitWithoutKey(t *testing.T) {
	// When the caller explicitly requests OpenRouter but no key is
	// reachable (keychain empty + env unset), the constructor must
	// error — auto-fallback to Anthropic is suppressed.
	saved := os.Getenv("OPEN_ROUTER_API_KEY")
	_ = os.Unsetenv("OPEN_ROUTER_API_KEY")
	defer func() {
		if saved != "" {
			_ = os.Setenv("OPEN_ROUTER_API_KEY", saved)
		}
	}()
	emptyResolver := func() (string, string, error) { return "", "", nil }
	_, _, err := constructOpenRouterClient(config.Default(), "", emptyResolver)
	if err == nil {
		t.Fatal("expected error when BackendOpenRouter requested without key")
	}
	if !contains(err.Error(), "BackendOpenRouter") {
		t.Errorf("error should name BackendOpenRouter; got: %v", err)
	}
}

func TestNewLLMClient_UnknownBackendErrors(t *testing.T) {
	_, _, err := NewLLMClient(nil, WithBackend(Backend("nonsense")))
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
	if !contains(err.Error(), "unknown backend") {
		t.Errorf("error should mention unknown backend; got: %v", err)
	}
}
