// client.go — NewLLMClient resolves the single provider the rest of the
// codebase should reach for. Both OpenRouter and Anthropic implement
// Provider, but having every caller embed the "try OpenRouter, fall
// back to Anthropic" logic is how the project ended up with ~10 direct
// NewAnthropicClient sites and ~5 direct NewOpenRouterClient sites
// drifting between modes.
//
// Per eval-principles spirit: tests of cognition primitives should not
// hardcode a provider — they should ask for "the LLM" and let resolution
// pick the credential that's actually present. Production paths benefit
// from the same indirection because operators routinely move from
// Anthropic-direct (cheap) to OpenRouter (lets them swap models per
// call without re-keying).

package llm

import (
	"errors"
	"fmt"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/secret"
)

// LLMClientSource records which credential resolution path produced
// the returned client. Exposed so callers can log attribution and so
// tests can assert resolution order without inspecting the concrete
// type.
type LLMClientSource string

const (
	// SourceOpenRouterKeychain — OpenRouter key read from macOS Keychain
	// entry "cortex-openrouter".
	SourceOpenRouterKeychain LLMClientSource = "openrouter-keychain"
	// SourceOpenRouterEnv — OpenRouter key read from $OPEN_ROUTER_API_KEY.
	SourceOpenRouterEnv LLMClientSource = "openrouter-env"
	// SourceAnthropicEnv — Anthropic key read from $ANTHROPIC_API_KEY.
	SourceAnthropicEnv LLMClientSource = "anthropic-env"
)

// openRouterKeyResolver is the signature of secret.OpenRouterKey. The
// indirection lets tests exercise resolution order (e.g. "keychain
// empty → fall through to env") without machine-level state.
type openRouterKeyResolver func() (key string, source string, err error)

// LLMOption tunes NewLLMClient construction. Options are applied after
// the underlying client is resolved so they work uniformly across both
// OpenRouter and Anthropic backends.
type LLMOption func(*llmOpts)

// llmOpts is the internal config accumulator. Kept unexported so the
// shape of the option set can grow without breaking the API.
type llmOpts struct {
	model string
}

// WithModel sets the model the returned client will use for subsequent
// calls. Pass model IDs in their provider-native form:
//
//   - OpenRouter: "anthropic/claude-haiku-4.5", "qwen/qwen3-coder",
//     "openai/gpt-oss-20b:free"
//   - Anthropic-direct: "claude-3-haiku-20240307", "claude-opus-4-7"
//
// Crossing model IDs across backends doesn't error here — the API
// call itself will fail with a clear provider-side error. Use the
// LLMClientSource returned from NewLLMClient to pick the right form
// when you genuinely need different model IDs per backend.
func WithModel(m string) LLMOption {
	return func(o *llmOpts) { o.model = m }
}

// modelSetter is the narrow interface both OpenRouterClient and
// AnthropicClient satisfy. Defined here (not in provider.go) so the
// Provider interface stays minimal — SetModel is a constructor knob,
// not part of every backend's contract.
type modelSetter interface {
	SetModel(string)
}

// ModelOf returns the current model name from p if p is a concrete LLM
// client that tracks one (OpenRouterClient, AnthropicClient, etc.).
// Returns "" otherwise so callers can fall back to a config default.
//
// Use when telemetry or display needs the model attribution without
// the caller knowing which concrete type the unified surface returned.
func ModelOf(p Provider) string {
	if mg, ok := p.(interface{ Model() string }); ok {
		return mg.Model()
	}
	return ""
}

// NewLLMClient returns the first available provider, trying OpenRouter
// before Anthropic. Resolution order:
//
//  1. OpenRouter via keychain ("cortex-openrouter" on macOS)
//  2. OpenRouter via $OPEN_ROUTER_API_KEY
//  3. Anthropic via $ANTHROPIC_API_KEY
//
// Returns the constructed Provider plus the source that produced it
// (useful for telemetry and for tests asserting fallback behavior).
//
// Options modify the returned client after construction. The common
// case is WithModel:
//
//	client, src, err := llm.NewLLMClient(cfg, llm.WithModel("anthropic/claude-haiku-4.5"))
//
// Callers that need to insist on a specific provider should construct
// it directly via NewOpenRouterClient / NewAnthropicClient — this
// function is for the common case where "any LLM" will do.
//
// cfg may be nil; downstream constructors call config.Default() as
// needed.
func NewLLMClient(cfg *config.Config, opts ...LLMOption) (Provider, LLMClientSource, error) {
	options := llmOpts{}
	for _, opt := range opts {
		opt(&options)
	}
	client, source, err := resolveLLMClient(cfg, secret.OpenRouterKey)
	if err != nil {
		return nil, "", err
	}
	if options.model != "" {
		if ms, ok := client.(modelSetter); ok {
			ms.SetModel(options.model)
		}
	}
	return client, source, nil
}

// resolveLLMClient is the testable core of NewLLMClient. orKey is the
// keychain-resolution function (in production: secret.OpenRouterKey;
// in tests: a stub that returns whatever the test needs).
//
// cfg is materialized via config.Default() when nil so the downstream
// constructors don't have to defend against nil pointers individually.
func resolveLLMClient(cfg *config.Config, orKey openRouterKeyResolver) (Provider, LLMClientSource, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	// 1. OpenRouter via keychain.
	if orKey != nil {
		if key, _, err := orKey(); err == nil && key != "" {
			c := NewOpenRouterClientWithKey(cfg, key)
			if c.IsAvailable() {
				return c, SourceOpenRouterKeychain, nil
			}
		}
	}
	// 2. OpenRouter via env.
	if c := NewOpenRouterClient(cfg); c.IsAvailable() {
		return c, SourceOpenRouterEnv, nil
	}
	// 3. Anthropic via env.
	if c := NewAnthropicClient(cfg); c.IsAvailable() {
		return c, SourceAnthropicEnv, nil
	}
	return nil, "", errors.New("no LLM client available: set OPEN_ROUTER_API_KEY (or keychain cortex-openrouter) or ANTHROPIC_API_KEY")
}

// MustLLMClient is NewLLMClient that wraps the error so callers that
// genuinely cannot proceed without a provider can collapse two lines
// into one. Use sparingly — most CLI commands prefer to render a clean
// error message rather than panic.
func MustLLMClient(cfg *config.Config, opts ...LLMOption) (Provider, LLMClientSource) {
	c, src, err := NewLLMClient(cfg, opts...)
	if err != nil {
		panic(fmt.Sprintf("llm.MustLLMClient: %v", err))
	}
	return c, src
}
