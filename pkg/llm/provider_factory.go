// Package llm — ProviderFactory.
//
// The factory resolves a model ID (e.g. "qwen2.5-coder:7b" or
// "anthropic/claude-haiku-4.5") to a configured Provider. Used by
// per-node model dispatch: when a DAG NodeSpec carries attrs.model,
// the handler asks the factory for a Provider bound to that model
// rather than using the registry-time-injected default. This is the
// composable-small-models mechanic — different ops can run on
// different models within a single REPL turn.
//
// Convention follows the rest of the codebase: model IDs containing
// "/" route through OpenRouter; bare model names route through
// Ollama. An empty model ID returns the configured default.
package llm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dereksantos/cortex/pkg/config"
)

// ProviderFactory resolves model IDs to Providers. Implementations may
// cache constructed Providers per-ID for the lifetime of the factory.
type ProviderFactory interface {
	// Get returns a Provider configured for the given model ID. An
	// empty modelID returns the factory's default Provider. Returns
	// (nil, err) when no Provider can be configured for the ID (e.g.,
	// missing API key for an OpenRouter route).
	Get(modelID string) (Provider, error)

	// Default returns the Provider used when no model ID is specified.
	// May be nil when no default is configured — callers checking for
	// availability should test the returned Provider, not the factory.
	Default() Provider
}

// defaultProviderFactory backs the factory with the Ollama / OpenRouter
// resolution logic the REPL already uses. Configurable via:
//
//   - cfg:           pkg/config used by OpenRouterClient + LLMClient
//   - openRouterKey: API key for OpenRouter; empty disables cloud routes
//   - ollamaAPIURL:  chat-completions URL for local Ollama; empty falls
//     back to the OpenRouter cloud default
//   - defaultModel:  model ID used by Default() and when Get("") is called
//
// Constructed Providers are cached per-modelID so repeated Get calls
// in a single REPL turn don't reconstruct the underlying HTTP client.
type defaultProviderFactory struct {
	cfg            *config.Config
	openRouterKey  string
	ollamaAPIURL   string
	defaultModel   string
	defaultAPIURL  string
	defaultRouting Provider

	mu    sync.Mutex
	cache map[string]Provider
}

// FactoryConfig parameterizes NewProviderFactory.
type FactoryConfig struct {
	// Cfg is forwarded to underlying Provider constructors.
	Cfg *config.Config
	// OpenRouterKey enables cloud routes. Empty disables them — calls
	// to Get for a slash-prefixed model ID will return an error.
	OpenRouterKey string
	// OllamaAPIURL is the chat-completions endpoint for local Ollama.
	// Empty means "no local route configured" — bare model names will
	// fall back to OpenRouter's default endpoint.
	OllamaAPIURL string
	// DefaultModel is returned by Default() and used when Get("") is
	// called. May be empty if no default routing is desired.
	DefaultModel string
	// DefaultAPIURL pairs with DefaultModel — pointing the default
	// Provider at the right endpoint for its type.
	DefaultAPIURL string
}

// NewProviderFactory constructs a factory. The default Provider is
// resolved eagerly from FactoryConfig.DefaultModel; an unresolvable
// default is left as nil (Default() returns nil), but per-call Get
// still works for IDs the factory can resolve.
func NewProviderFactory(cfg FactoryConfig) ProviderFactory {
	f := &defaultProviderFactory{
		cfg:           cfg.Cfg,
		openRouterKey: cfg.OpenRouterKey,
		ollamaAPIURL:  cfg.OllamaAPIURL,
		defaultModel:  cfg.DefaultModel,
		defaultAPIURL: cfg.DefaultAPIURL,
		cache:         make(map[string]Provider),
	}
	if cfg.DefaultModel != "" {
		if p, err := f.build(cfg.DefaultModel, cfg.DefaultAPIURL); err == nil {
			f.defaultRouting = p
		}
	}
	return f
}

// Default returns the eagerly-resolved default Provider, or nil when
// none was configurable.
func (f *defaultProviderFactory) Default() Provider {
	return f.defaultRouting
}

// Get returns a Provider for the given model ID. Caches per ID so
// callers can re-use the factory across a multi-op turn without
// reconstructing clients.
func (f *defaultProviderFactory) Get(modelID string) (Provider, error) {
	if modelID == "" {
		if f.defaultRouting == nil {
			return nil, fmt.Errorf("provider factory: no default model configured")
		}
		return f.defaultRouting, nil
	}

	f.mu.Lock()
	if p, ok := f.cache[modelID]; ok {
		f.mu.Unlock()
		return p, nil
	}
	f.mu.Unlock()

	// Pick the API URL appropriate for this model's route.
	apiURL := f.ollamaAPIURL
	if strings.Contains(modelID, "/") {
		apiURL = "" // OpenRouter client picks its own default
	}
	p, err := f.build(modelID, apiURL)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.cache[modelID] = p
	f.mu.Unlock()
	return p, nil
}

// build constructs the right Provider type for a given model ID.
// Resolution order:
//
//  1. cfg.ResolveModelRoute(modelID) — Phase 4 endpoint registry.
//     Hits for both "endpoint/model" explicit form and bare names
//     declared in an endpoint's Models list (Case 3). Builds an
//     OpenAI-compat client bound to the matched endpoint.
//  2. Slash-prefixed modelID with no Phase 4 match → OpenRouter.
//  3. Bare modelID → Ollama via the unified LLMClient.
//
// Mirrors internal/llm.BuildProvider's logic but kept inline so
// pkg/llm doesn't depend on internal/. The two paths produce
// indistinguishable Provider clients for the same modelID.
func (f *defaultProviderFactory) build(modelID, apiURL string) (Provider, error) {
	// 1. Phase 4 endpoint registry — matches "endpoint/model" and
	// bare names declared in an endpoint's Models list.
	if ep, routedModel, ok := f.cfg.ResolveModelRoute(modelID); ok {
		client := NewOpenAICompatClient(EndpointConfig{
			Name:    ep.Name,
			BaseURL: ep.BaseURL,
			APIKey:  ep.ResolveAPIKey(),
		})
		client.SetModel(routedModel)
		return client, nil
	}

	if strings.Contains(modelID, "/") {
		// 2. Slash-prefixed with no Phase 4 hit → OpenRouter.
		if f.openRouterKey == "" {
			return nil, fmt.Errorf("provider factory: cannot route %q (OpenRouter key not configured)", modelID)
		}
		client := NewOpenRouterClientWithKey(f.cfg, f.openRouterKey)
		client.SetModel(modelID)
		if apiURL != "" {
			client.SetAPIURL(apiURL)
		}
		return client, nil
	}
	// 3. Bare model name with no Phase 4 hit → Ollama.
	c, _, err := NewLLMClient(f.cfg,
		WithBackend(BackendOllama),
		WithModel(modelID),
	)
	if err != nil {
		return nil, fmt.Errorf("provider factory: build ollama client for %q: %w", modelID, err)
	}
	return c, nil
}
