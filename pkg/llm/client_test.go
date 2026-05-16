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
