// Package llm — generic OpenAI-compatible embeddings provider.
//
// Mirrors openai_compat.go (chat) for the /embeddings endpoint. Used to
// reach a hosted embedding model behind an OpenAI-shaped proxy — in
// particular the fleet's dedicated CPU `embedder` served by LiteLLM at
// chatterbox:4000. Kept separate from HugotEmbedder (in-process, pure Go)
// so callers can pick network-vs-local without dragging in the ONNX stack.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// embedRequest is the OpenAI /embeddings request body. Input is `any` so a
// single call can send one string or a []string batch — both are valid per
// the OpenAI shape and LiteLLM forwards them unchanged.
type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

// embedResponse is the OpenAI /embeddings response. Data is index-ordered,
// but we sort defensively by Index since the spec only guarantees each item
// carries its own index, not array order.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// OpenAICompatEmbedder is an Embedder backed by an OpenAI-compatible
// /embeddings endpoint. It reuses the chat client's transport (doRaw, base
// URL normalization, auth, attribution, timeout) by composing one — only
// the request/response shapes differ.
type OpenAICompatEmbedder struct {
	client *OpenAICompatClient
	model  string
}

// NewOpenAICompatEmbedder builds an embedder for one endpoint+model. The
// model is the backend role/id to embed with (e.g. "embedder"). BaseURL is
// the OpenAI root; a bare host:port gets "/v1" appended like the chat client.
func NewOpenAICompatEmbedder(ep EndpointConfig, model string) *OpenAICompatEmbedder {
	if ep.Name == "" {
		ep.Name = "embedder"
	}
	c := NewOpenAICompatClient(ep)
	c.SetModel(model)
	return &OpenAICompatEmbedder{client: c, model: model}
}

// Embed converts one text to a vector via a single /embeddings call.
func (e *OpenAICompatEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("%s: no embedding returned", e.model)
	}
	return vecs[0], nil
}

// EmbedBatch converts multiple texts in one round-trip — cheaper than N
// calls when indexing a backlog. Order of the returned slice matches texts.
func (e *OpenAICompatEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return e.embed(ctx, texts)
}

func (e *OpenAICompatEmbedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	bb, err := e.client.doRaw(ctx, "/embeddings", embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}
	var resp embedResponse
	if err := json.Unmarshal(bb, &resp); err != nil {
		return nil, fmt.Errorf("%s: decode embeddings response: %w", e.model, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %s", e.model, resp.Error.Message)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("%s: expected %d embeddings, got %d", e.model, len(texts), len(resp.Data))
	}
	// Place each vector at its declared index so callers can rely on order.
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("%s: embedding index %d out of range", e.model, d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// IsEmbeddingAvailable reports whether the embedder is configured. It is a
// cheap, non-network check by design: embedders may be called on a hot path
// that bounds reachability itself via a context timeout + circuit breaker, so
// a blocking HTTP probe here would defeat that budget.
func (e *OpenAICompatEmbedder) IsEmbeddingAvailable() bool {
	return e.client != nil && e.client.baseURL != "" && e.model != ""
}

// ModelName returns the backend model id used for embeddings.
func (e *OpenAICompatEmbedder) ModelName() string { return e.model }
