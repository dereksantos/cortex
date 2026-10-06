// Package llm provides LLM client implementations
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
	"github.com/dereksantos/cortex/pkg/events"
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	defaultMaxTokens    = 1024
)

// AnthropicClient handles communication with the Anthropic API
type AnthropicClient struct {
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
	vision     bool
}

// NewAnthropicClient creates a new Anthropic client
func NewAnthropicClient(cfg *config.Config) *AnthropicClient {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	model := cfg.AnthropicModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}

	return &AnthropicClient{
		apiKey:    apiKey,
		model:     model,
		maxTokens: defaultMaxTokens,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// anthropicRequest represents a request to the Anthropic Messages API
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

// anthropicMessage represents a message in the conversation. Content is
// either the plain string form (all text-only requests, unchanged) or
// Anthropic's structured content-block form, which is what an image must
// travel in (AnthropicImageContent in content.go builds the block).
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicResponse represents a response from the Anthropic API
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

// anthropicContentBlock represents a content block in the response
type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicUsage represents token usage
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicError represents an API error
type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// anthropicErrorResponse represents an error response
type anthropicErrorResponse struct {
	Type  string         `json:"type"`
	Error anthropicError `json:"error"`
}

// Name returns the provider identifier
func (c *AnthropicClient) Name() string {
	return "anthropic"
}

// IsAvailable checks if the provider is ready (API key is set)
func (c *AnthropicClient) IsAvailable() bool {
	return c.apiKey != ""
}

// Generate produces a response for the given prompt
func (c *AnthropicClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, _, err := c.generate(ctx, prompt, "")
	return result, err
}

// GenerateWithSystem includes system context (for context injection)
func (c *AnthropicClient) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	result, _, err := c.generate(ctx, prompt, system)
	return result, err
}

// GenerateWithStats produces a response and returns token usage statistics.
func (c *AnthropicClient) GenerateWithStats(ctx context.Context, prompt string) (string, GenerationStats, error) {
	return c.generate(ctx, prompt, "")
}

// Vision reports whether this client's model is permitted to receive image
// content blocks (issue #216's gate; cmd/cortex stamps it from the role
// binding's resolved verdict). Default false: images are refused with a
// clear error naming the model, never dropped.
func (c *AnthropicClient) Vision() bool { return c.vision }

// SetVision declares whether this client's model accepts image content.
func (c *AnthropicClient) SetVision(v bool) { c.vision = v }

// GenerateWithImages issues one Messages-API turn whose user content is
// the given content parts (text + image; AnthropicImageContent
// translates image parts into Anthropic's native image block). The gate
// refuses image parts for a non-vision model. Text-only callers should
// keep using Generate — the string-content shape is unchanged there.
func (c *AnthropicClient) GenerateWithImages(ctx context.Context, parts []ContentPart) (string, GenerationStats, error) {
	if err := GateImages(parts, c.model, c.vision); err != nil {
		return "", GenerationStats{}, err
	}
	blocks := make([]any, 0, len(parts))
	for i, p := range parts {
		if p.HasImage() {
			blk, ok := AnthropicImageContent(p)
			if !ok {
				// Never drop an image part quietly: GateImages above
				// validated the parts, so a translation failure here is a
				// bug, and a bug must surface rather than send the model a
				// prompt missing its image.
				return "", GenerationStats{}, fmt.Errorf("anthropic: image content part %d cannot be translated to an image block: %s", i, summarizeURL(p.ImageURL))
			}
			blocks = append(blocks, blk)
			continue
		}
		blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
	}
	return c.generateBlocks(ctx, blocks, "")
}

// generate calls the Anthropic Messages API
func (c *AnthropicClient) generate(ctx context.Context, prompt, system string) (string, GenerationStats, error) {
	return c.generateBlocks(ctx, prompt, system)
}

// generateBlocks is the shared Messages-API round trip: content is either
// a plain string (text-only, the historical shape) or a slice of content
// blocks (Anthropic's structured form).
func (c *AnthropicClient) generateBlocks(ctx context.Context, content any, system string) (string, GenerationStats, error) {
	if c.apiKey == "" {
		return "", GenerationStats{}, fmt.Errorf("anthropic API key not configured")
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages: []anthropicMessage{
			{Role: "user", Content: content},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", GenerationStats{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", GenerationStats{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", GenerationStats{}, fmt.Errorf("failed to call Anthropic API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", GenerationStats{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp anthropicErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil {
			return "", GenerationStats{}, fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", GenerationStats{}, fmt.Errorf("anthropic API returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", GenerationStats{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract text from content blocks
	var result string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			result += block.Text
		}
	}

	stats := GenerationStats{
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}

	return result, stats, nil
}

// SetMaxTokens allows configuring the max tokens for responses
func (c *AnthropicClient) SetMaxTokens(tokens int) {
	c.maxTokens = tokens
}

// Model returns the current model being used
func (c *AnthropicClient) Model() string {
	return c.model
}

// SetModel updates the model used for subsequent calls. Symmetric with
// OpenRouterClient.SetModel so NewLLMClient + WithModel can apply the
// caller's choice uniformly to either backend.
func (c *AnthropicClient) SetModel(m string) {
	c.model = m
}

// AnalyzeEvent analyzes an event and extracts insights
func (c *AnthropicClient) AnalyzeEvent(event *events.Event) (*Analysis, error) {
	filePath, _ := event.ToolInput["file_path"].(string)
	prompt := BuildAnalysisPrompt(event.ToolName, filePath, event.ToolResult)

	response, _, err := c.generate(context.Background(), prompt, AnalysisSystemPrompt)
	if err != nil {
		return nil, err
	}

	return ParseAnalysisWithFallback(response), nil
}
