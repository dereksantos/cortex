// Package llm — OpenAI-compat-backend Probe.
//
// Wraps OpenAICompatClient.ListModels to harvest one endpoint's
// /v1/models catalog. Lemonade exposes labels[] + max_context_window
// natively, so the resulting ModelInfo carries both real capabilities
// and the *deployment's* effective context window (not the model's
// theoretical max). For label-less endpoints we fall back to
// EffectiveLabels' id-pattern inference.

package llm

import (
	"context"
	"fmt"
)

// OpenAICompatProbeConfig wires one OpenAICompatProbe to one endpoint.
type OpenAICompatProbeConfig struct {
	// Endpoint identifies the backend (name, base URL, optional key).
	Endpoint EndpointConfig

	// IsLocal flags whether this endpoint runs on the local machine /
	// LAN. Propagates into ModelInfo.IsLocal so consumers can prefer
	// local routing without re-inferring from BaseURL.
	IsLocal bool
}

// NewOpenAICompatProbe builds a Probe over one configured OpenAI-compat
// endpoint. Each endpoint gets its own probe — the composite registry
// fans them out concurrently.
func NewOpenAICompatProbe(cfg OpenAICompatProbeConfig) Probe {
	return &openAICompatProbe{
		cfg:    cfg,
		client: NewOpenAICompatClient(cfg.Endpoint),
	}
}

type openAICompatProbe struct {
	cfg    OpenAICompatProbeConfig
	client *OpenAICompatClient
}

func (p *openAICompatProbe) Name() string {
	return "compat:" + p.cfg.Endpoint.Name
}

func (p *openAICompatProbe) Probe(ctx context.Context) ([]ModelInfo, error) {
	models, err := p.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s probe: %w", p.Name(), err)
	}
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		// Prefix the model id with the endpoint name so it matches the
		// routing convention consumers use (e.g.
		// "chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF"). Without the
		// prefix, registry.Get() misses on user-pinned ids that include
		// the endpoint and downstream size/cap math falls back to the
		// "unknown" path.
		fullID := m.ID
		if p.cfg.Endpoint.Name != "" {
			fullID = p.cfg.Endpoint.Name + "/" + m.ID
		}
		out = append(out, ModelInfo{
			ID:                     fullID,
			Endpoint:               p.cfg.Endpoint.Name,
			BaseURL:                p.cfg.Endpoint.BaseURL,
			IsLocal:                p.cfg.IsLocal,
			EffectiveContextWindow: m.ContextLength,
			SizeBillion:            float64(parseParamCount(m.ID)),
			Capabilities:           EffectiveLabels(m),
		})
	}
	return out, nil
}
