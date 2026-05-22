// build.go — unified provider/embedder construction for the cortex CLI.
//
// Before this helper, `cmd/cortex/commands/` had three different
// opinions about how to construct an LLM provider:
//
//  1. REPL did 3-step routing (Phase 4 model_routes → Ollama-shaped
//     apiURL → slash-prefix OpenRouter).
//  2. Bootstrap had a parallel `--provider | --endpoint | --model`
//     resolver that bypassed Phase 4 entirely.
//  3. Every other command (`query`, `embed`, `ingest`, `daemon`,
//     `dream_debug`, etc.) hardcoded `llm.NewOllamaClient(cfg)` and
//     ignored the user's configured default model.
//
// BuildProvider/BuildEmbedder collapse those three opinions into one
// with **model id as the routing key** — the same convention the REPL
// already used correctly. Bootstrap's --endpoint survives as a
// transient override; study's --provider does not (the routing
// key carries that information).
//
// See docs/provider-resolution-refactor.md for the migration plan.
package llm

import (
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
	"github.com/dereksantos/cortex/pkg/secret"
)

// BuildProvider returns an llm.Provider whose backend is selected by
// model id, with optional transient overrides.
//
// Resolution order:
//
//  1. WithEndpointOverride (--endpoint) — wins when set, forces
//     OpenAI-compat to the given URL.
//  2. cfg.ResolveModelRoute(modelID) — Phase 4 model_routes
//     (endpoint-prefixed or role-mapped model id) → OpenAI-compat to
//     the matched endpoint.
//  3. WithAPIURL — legacy REPL apiURL behavior: Ollama-shaped URL
//     routes to Ollama; everything else routes to OpenRouter with
//     the apiURL applied. New code should prefer WithEndpointOverride.
//  4. Slash-prefixed modelID (e.g. "anthropic/claude-haiku-4.5")
//     with no Phase 4 route → OpenRouter via keychain key.
//  5. Bare modelID → Ollama default URL.
//
// Returns nil only when every path fails to produce a working client
// (e.g. slash-prefix model id but no OpenRouter key reachable, and no
// other override). Callers tolerate nil providers by degrading to
// mechanical paths.
func BuildProvider(cfg *config.Config, modelID string, opts ...Option) llm.Provider {
	o := buildOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.modelOverride != "" {
		modelID = o.modelOverride
	}
	if modelID == "" {
		modelID = cfg.DefaultGenerationModel()
	}

	// 1. Explicit endpoint override wins over everything.
	if o.endpointOverride != "" {
		client := llm.NewOpenAICompatClient(llm.EndpointConfig{
			Name:    "cortex-override",
			BaseURL: o.endpointOverride,
		})
		if modelID != "" {
			client.SetModel(modelID)
		}
		return client
	}

	// 2. Phase 4 model registry — endpoint-prefixed or role-mapped
	// model ids route to a configured OpenAI-compat endpoint.
	if ep, routedModel, ok := cfg.ResolveModelRoute(modelID); ok {
		client := llm.NewOpenAICompatClient(llm.EndpointConfig{
			Name:    ep.Name,
			BaseURL: ep.BaseURL,
			APIKey:  ep.ResolveAPIKey(),
		})
		client.SetModel(routedModel)
		return client
	}

	// 3. Legacy REPL apiURL path — preserved for the existing REPL
	// callers. A non-Ollama apiURL is treated as an OpenRouter
	// endpoint override; an Ollama-shaped apiURL routes to Ollama.
	if o.apiURL != "" {
		if o.apiURL == llm.DefaultOllamaURL {
			c, _, err := llm.NewLLMClient(cfg,
				llm.WithBackend(llm.BackendOllama),
				llm.WithModel(modelID),
			)
			if err != nil {
				return nil
			}
			return c
		}
		key, _, kerr := secret.MustOpenRouterKey()
		if kerr != nil {
			return nil
		}
		client := llm.NewOpenRouterClientWithKey(cfg, key)
		if modelID != "" {
			client.SetModel(modelID)
		}
		client.SetAPIURL(o.apiURL)
		return client
	}

	// 4. Slash-prefixed model id with no Phase 4 match → OpenRouter.
	if hasSlash(modelID) {
		key, _, kerr := secret.MustOpenRouterKey()
		if kerr != nil {
			return nil
		}
		client := llm.NewOpenRouterClientWithKey(cfg, key)
		client.SetModel(modelID)
		return client
	}

	// 5. Bare model id (or empty when no cfg.OllamaModel was set) →
	// Ollama via NewLLMClient. NewLLMClient handles nil cfg by
	// materializing config.Default(), so nil-safe.
	c, _, err := llm.NewLLMClient(cfg,
		llm.WithBackend(llm.BackendOllama),
		llm.WithModel(modelID),
	)
	if err != nil {
		return nil
	}
	return c
}

// BuildEmbedder mirrors BuildProvider for embedding-only callers
// (vector_search, capture pipeline, ingest, query). Today every
// embedding caller uses Ollama nomic-embed-text by default; a future
// Phase 4 entry can route to a hosted embedder by inspecting
// cfg.Models.Embed without changing the call sites.
//
// Returns nil when cfg is nil — callers that need a non-nil embedder
// should construct config.Default() themselves first.
func BuildEmbedder(cfg *config.Config, _ ...Option) llm.Embedder {
	if cfg == nil {
		cfg = config.Default()
	}
	return llm.NewOllamaClient(cfg)
}

// Option tunes BuildProvider/BuildEmbedder construction.
type Option func(*buildOpts)

// buildOpts is the internal accumulator. Kept unexported so the
// option set can grow without breaking the API.
type buildOpts struct {
	endpointOverride string
	apiURL           string
	modelOverride    string
}

// WithEndpointOverride forces OpenAI-compat against the given URL,
// bypassing model_routes and the slash-prefix heuristic. Used by
// `cortex study --endpoint` for one-off ad-hoc invocations.
// The persistent answer is `model_routes` in `.cortex/config.json`;
// this flag is the transient escape hatch.
func WithEndpointOverride(url string) Option {
	return func(o *buildOpts) { o.endpointOverride = url }
}

// WithAPIURL preserves the REPL's legacy apiURL routing where a
// non-default URL signals OpenRouter and an Ollama-default URL
// signals Ollama. New code should prefer WithEndpointOverride —
// it's clearer about intent.
func WithAPIURL(url string) Option {
	return func(o *buildOpts) { o.apiURL = url }
}

// WithModelOverride lets callers force a model id different from
// the one passed as modelID. Edge case; most callers won't need it.
// Useful for tests that need to assert routing without rebuilding
// the cfg+model pair.
func WithModelOverride(id string) Option {
	return func(o *buildOpts) { o.modelOverride = id }
}

// hasSlash reports whether s contains a '/' (without dragging in
// the strings package for a one-byte search).
func hasSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}
