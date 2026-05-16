// Package llm — OpenRouter provider, OpenAI-compatible chat-completions
// gateway used both by cortex's internal cognitive modes and as the
// canonical model gateway for the eval-harness grid runner.
//
// Cost extraction relies on the inline `usage.cost` field, which is
// populated when the request body includes `{"usage":{"include":true}}`.
// The /api/v1/generation lookup is too eventually-consistent to depend on
// (404'd within 5s of a successful completion in our 2026-05-10 probe);
// inline usage is authoritative.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
)

const (
	openrouterAPIURL       = "https://openrouter.ai/api/v1/chat/completions"
	openrouterReferer      = "https://github.com/dereksantos/cortex"
	openrouterTitle        = "cortex"
	openrouterDefaultModel = "openai/gpt-oss-20b:free" // verified-working free model on the OpenInference provider
	openrouterTimeoutSec   = 60
)

// OpenRouterClient is a Provider for the OpenRouter unified gateway.
//
// Per docs/openrouter-tiers.md, the env-var convention in this project is
// OPEN_ROUTER_API_KEY (the user's actual export name). The Aider harness
// re-exports to OPENROUTER_API_KEY for litellm compatibility — that's
// not this client's concern.
type OpenRouterClient struct {
	apiKey     string
	model      string
	maxTokens  int
	apiURL     string // overridable for tests via SetAPIURL
	httpClient *http.Client

	lastCostUSD  float64 // surface this via LastCostUSD()
	lastProvider string  // upstream provider that served the most recent call
}

// NewOpenRouterClient constructs a client. The model defaults to a known
// working :free model (suitable for development); callers wiring a grid
// run should override via SetModel before each cell.
//
// Reading order for the API key: OPEN_ROUTER_API_KEY env var. There is
// no config-file fallback by design — secrets stay in the environment.
func NewOpenRouterClient(cfg *config.Config) *OpenRouterClient {
	apiKey := os.Getenv("OPEN_ROUTER_API_KEY")

	model := os.Getenv("OPEN_ROUTER_MODEL")
	if model == "" {
		model = openrouterDefaultModel
	}
	_ = cfg // reserved for future cfg-driven knobs (max_tokens, timeout)

	return &OpenRouterClient{
		apiKey:    apiKey,
		model:     model,
		maxTokens: defaultMaxTokens,
		apiURL:    openrouterAPIURL,
		httpClient: &http.Client{
			Timeout: openrouterTimeoutSec * time.Second,
		},
	}
}

// Name returns the provider identifier.
func (c *OpenRouterClient) Name() string { return "openrouter" }

// IsAvailable reports whether the API key is set. Does not probe the
// network — callers should treat this as a precondition check, not a
// liveness check.
func (c *OpenRouterClient) IsAvailable() bool { return c.apiKey != "" }

// SetModel changes the model used for subsequent calls. Pass any model
// ID OpenRouter accepts verbatim (e.g. "anthropic/claude-haiku-4.5",
// "openai/gpt-oss-20b:free", "qwen/qwen3-coder").
func (c *OpenRouterClient) SetModel(m string) { c.model = m }

// Model returns the currently selected model.
func (c *OpenRouterClient) Model() string { return c.model }

// SetMaxTokens overrides the default response token cap.
func (c *OpenRouterClient) SetMaxTokens(n int) { c.maxTokens = n }

// SetAPIURL replaces the OpenRouter endpoint. Test-only.
func (c *OpenRouterClient) SetAPIURL(u string) { c.apiURL = u }

// LastCostUSD returns the per-call USD cost reported by OpenRouter for
// the most recent successful call. Zero on free models, zero before the
// first call, zero on calls where usage.cost was absent.
func (c *OpenRouterClient) LastCostUSD() float64 { return c.lastCostUSD }

// LastProvider returns the upstream provider name (e.g. "OpenInference",
// "Venice") that served the most recent successful call. Useful for
// debugging which pool routed a 429 versus a 200.
func (c *OpenRouterClient) LastProvider() string { return c.lastProvider }

// Generate produces a response for the given prompt.
func (c *OpenRouterClient) Generate(ctx context.Context, prompt string) (string, error) {
	out, _, err := c.generate(ctx, prompt, "")
	return out, err
}

// GenerateWithSystem prepends a system-role message before the user prompt.
func (c *OpenRouterClient) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	out, _, err := c.generate(ctx, prompt, system)
	return out, err
}

// GenerateWithStats returns the response plus prompt/completion token counts.
// Per-call USD is exposed separately via LastCostUSD() so the generic
// GenerationStats type stays untouched.
func (c *OpenRouterClient) GenerateWithStats(ctx context.Context, prompt string) (string, GenerationStats, error) {
	return c.generate(ctx, prompt, "")
}

// orRequest is the OpenAI-compatible chat-completions body. The `usage`
// sub-object is OpenRouter's request-side opt-in for surfacing per-call
// cost in the response.
type orRequest struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	Messages  []orMessage `json:"messages"`
	Usage     orUsageReq  `json:"usage"`
}

type orMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type orUsageReq struct {
	Include bool `json:"include"`
}

type orResponse struct {
	ID       string      `json:"id"`
	Model    string      `json:"model"`
	Provider string      `json:"provider"`
	Choices  []orChoice  `json:"choices"`
	Usage    orUsageResp `json:"usage"`
	Error    *orErr      `json:"error,omitempty"`
}

type orChoice struct {
	Message      orMessage `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type orUsageResp struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

type orErr struct {
	Code     int            `json:"code"`
	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (c *OpenRouterClient) generate(ctx context.Context, prompt, system string) (string, GenerationStats, error) {
	msgs := make([]orMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, orMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, orMessage{Role: "user", Content: prompt})

	body := orRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		Messages:  msgs,
		Usage:     orUsageReq{Include: true},
	}

	bb, err := c.doRaw(ctx, body)
	if err != nil {
		return "", GenerationStats{}, err
	}

	var apiResp orResponse
	if err := json.Unmarshal(bb, &apiResp); err != nil {
		return "", GenerationStats{}, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", GenerationStats{}, fmt.Errorf("openrouter: response had no choices")
	}

	c.lastCostUSD = apiResp.Usage.Cost
	c.lastProvider = apiResp.Provider

	stats := GenerationStats{
		InputTokens:  apiResp.Usage.PromptTokens,
		OutputTokens: apiResp.Usage.CompletionTokens,
	}
	return apiResp.Choices[0].Message.Content, stats, nil
}

// doRaw sends body to the chat-completions endpoint and returns the
// raw response bytes. Extracted so the plaintext generate() path and
// the tool-call GenerateWithTools() path (openrouter_tools.go) share
// authentication, header setup, and error decoding without duplicating
// the HTTP plumbing.
//
// body may be any JSON-serializable value. Non-200 responses are
// decoded as orResponse so the structured error message comes through
// when OpenRouter returns one.
func (c *OpenRouterClient) doRaw(ctx context.Context, body any) ([]byte, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("openrouter: OPEN_ROUTER_API_KEY not set")
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("openrouter: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", openrouterReferer)
	req.Header.Set("X-Title", openrouterTitle)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: request failed: %w", err)
	}
	defer resp.Body.Close()

	bb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var er orResponse
		if json.Unmarshal(bb, &er) == nil && er.Error != nil {
			return nil, fmt.Errorf("openrouter (%d): %s", resp.StatusCode, er.Error.Message)
		}
		return nil, fmt.Errorf("openrouter status %d: %s", resp.StatusCode, string(bb))
	}
	return bb, nil
}
