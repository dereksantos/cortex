// Package llm — Ollama-backend Probe.
//
// Wraps a minimal /api/tags fetch and normalizes each entry into a
// registry-facing ModelInfo. Capabilities and SizeBillion are inferred
// from the model id (Ollama doesn't expose label metadata via /api/tags).
// EffectiveContextWindow is left at 0 (unknown) — Ollama's per-model
// context is configurable via /api/show, which adds N round-trips per
// list; downstream code treats 0 as "unknown, be conservative" and
// falls back to InferContextClass for class-based defaults.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OllamaProbeConfig wires an OllamaProbe.
type OllamaProbeConfig struct {
	// BaseURL is the Ollama root, e.g. "http://localhost:11434".
	// Empty defaults to DefaultOllamaURL stripped of the chat suffix.
	BaseURL string

	// HTTPClient lets tests inject a transport. nil uses a 5s-timeout
	// default — Ollama is local, /api/tags is cheap.
	HTTPClient *http.Client
}

// NewOllamaProbe builds a Probe for an Ollama backend.
func NewOllamaProbe(cfg OllamaProbeConfig) Probe {
	base := cfg.BaseURL
	if base == "" {
		// DefaultOllamaURL is the /v1/chat/completions endpoint; the
		// /api/tags call wants the root. Strip if present.
		base = trimChatSuffix(DefaultOllamaURL)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &ollamaProbe{baseURL: base, client: client}
}

type ollamaProbe struct {
	baseURL string
	client  *http.Client
}

func (p *ollamaProbe) Name() string { return "ollama" }

func (p *ollamaProbe) Probe(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama probe: build request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama probe: status %d", resp.StatusCode)
	}
	var tags tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("ollama probe: decode: %w", err)
	}
	out := make([]ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, ModelInfo{
			ID:                     m.Name,
			Endpoint:               "ollama",
			BaseURL:                p.baseURL,
			IsLocal:                true,
			EffectiveContextWindow: 0, // unknown from /api/tags alone
			SizeBillion:            float64(parseParamCount(m.Name)),
			Capabilities:           InferCapabilities(m.Name),
		})
	}
	return out, nil
}

// trimChatSuffix strips "/v1/chat/completions" from an OpenAI-compat URL
// to recover the Ollama root (which serves /api/tags). Returns the
// input unchanged when the suffix isn't present.
func trimChatSuffix(u string) string {
	const suffix = "/v1/chat/completions"
	if n := len(u) - len(suffix); n >= 0 && u[n:] == suffix {
		return u[:n]
	}
	return u
}
