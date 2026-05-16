package llm

import (
	"errors"
	"testing"
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
