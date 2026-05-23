// Package llm — OpenRouter-backend Probe.
//
// Wraps OpenRouterClient.ListModels to surface the full hosted
// catalogue. ContextLength on each OpenRouterModel becomes the
// EffectiveContextWindow (OpenRouter advertises a single window per
// model; there's no separate deployment-time setting like local
// llama-server has). Capabilities are inferred from id pattern —
// OpenRouter doesn't expose label metadata per model.
//
// The probe is a no-op when the OpenRouter key is empty (returns nil
// models, nil error) so the registry composes cleanly in
// no-OpenRouter environments. Auth failures surface through ListModels.

package llm

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/pkg/config"
)

// OpenRouterProbeConfig wires the OpenRouter probe.
type OpenRouterProbeConfig struct {
	// APIKey is the OPEN_ROUTER_API_KEY value. Empty = skip probing
	// (the probe returns an empty list, no error).
	APIKey string

	// Cfg is the project config. Only needed for the OpenRouterClient
	// constructor — its actual fields aren't consulted by the probe.
	Cfg *config.Config
}

// NewOpenRouterProbe builds a Probe over OpenRouter. Pass an empty
// APIKey when OpenRouter isn't configured; the probe will return no
// models without erroring (the rest of the registry continues to work).
func NewOpenRouterProbe(cfg OpenRouterProbeConfig) Probe {
	return &openRouterProbe{cfg: cfg}
}

type openRouterProbe struct {
	cfg OpenRouterProbeConfig
}

func (p *openRouterProbe) Name() string { return "openrouter" }

func (p *openRouterProbe) Probe(ctx context.Context) ([]ModelInfo, error) {
	if p.cfg.APIKey == "" {
		return nil, nil
	}
	client := NewOpenRouterClientWithKey(p.cfg.Cfg, p.cfg.APIKey)
	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("openrouter probe: %w", err)
	}
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, ModelInfo{
			ID:                     m.ID,
			Endpoint:               "openrouter",
			BaseURL:                "", // OpenRouterClient uses its own internal URL
			IsLocal:                false,
			EffectiveContextWindow: m.ContextLength,
			SizeBillion:            float64(parseParamCount(m.ID)),
			Capabilities:           InferCapabilities(m.ID),
		})
	}
	return out, nil
}
