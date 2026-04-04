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
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
}

// anthropicMessage represents a message in the conversation
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse represents a response from the Anthropic API
type anthropicResponse struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	Role         string                   `json:"role"`
	Content      []anthropicContentBlock  `json:"content"`
	Model        string                   `json:"model"`
	StopReason   string                   `json:"stop_reason"`
	Usage        anthropicUsage           `json:"usage"`
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

// generate calls the Anthropic Messages API
func (c *AnthropicClient) generate(ctx context.Context, prompt, system string) (string, GenerationStats, error) {
	if c.apiKey == "" {
		return "", GenerationStats{}, fmt.Errorf("anthropic API key not configured")
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages: []anthropicMessage{
			{Role: "user", Content: prompt},
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
