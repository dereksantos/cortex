// Package llm provides LLM client implementations
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// OllamaClient handles communication with Ollama
type OllamaClient struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOllamaClient creates a new Ollama client
func NewOllamaClient(cfg *config.Config) *OllamaClient {
	return &OllamaClient{
		baseURL: cfg.OllamaURL,
		model:   cfg.OllamaModel,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// OllamaRequest represents a request to Ollama
type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
}

// OllamaResponse represents a response from Ollama
type OllamaResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Response  string    `json:"response"`
	Done      bool      `json:"done"`
}

// IsAvailable checks if Ollama is running
func (c *OllamaClient) IsAvailable() bool {
	resp, err := c.httpClient.Get(c.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// Name returns the provider identifier
func (c *OllamaClient) Name() string {
	return "ollama"
}

// Generate produces a response for the given prompt (implements Provider)
func (c *OllamaClient) Generate(ctx context.Context, prompt string) (string, error) {
	return c.generate(prompt)
}

// GenerateWithSystem generates with system context (implements Provider)
func (c *OllamaClient) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	fullPrompt := prompt
	if system != "" {
		fullPrompt = fmt.Sprintf("Context:\n%s\n\n---\n\nQuestion: %s", system, prompt)
	}
	return c.generate(fullPrompt)
}

// ModelsResponse represents the response from /api/tags
type ModelsResponse struct {
	Models []ModelInfo `json:"models"`
}

// ModelInfo represents information about a model
type ModelInfo struct {
	Name string `json:"name"`
}

// IsModelAvailable checks if the configured model is available
func (c *OllamaClient) IsModelAvailable() bool {
	resp, err := c.httpClient.Get(c.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return false
	}

	// Check if our model is in the list
	for _, model := range modelsResp.Models {
		if model.Name == c.model {
			return true
		}
	}

	return false
}

// AnalyzeEvent analyzes an event and extracts insights
func (c *OllamaClient) AnalyzeEvent(event *events.Event) (*Analysis, error) {
	prompt := c.buildAnalysisPrompt(event)

	// Call Ollama with analysis system prompt
	response, err := c.generateInternal(prompt, AnalysisSystemPrompt)
	if err != nil {
		return nil, err
	}

	// Parse the response into structured analysis
	analysis, err := parseAnalysisJSON(response)
	if err != nil || analysis == nil {
		// Return raw response as summary if parsing fails
		return &Analysis{
			Summary:    response,
			Category:   "insight",
			Importance: 5,
			Tags:       []string{},
			Reasoning:  "Could not parse structured response",
		}, nil
	}

	return analysis, nil
}

// buildAnalysisPrompt creates a prompt for event analysis
func (c *OllamaClient) buildAnalysisPrompt(event *events.Event) string {
	var filePath string
	if fp, ok := event.ToolInput["file_path"].(string); ok {
		filePath = fp
	}

	prompt := fmt.Sprintf(`Analyze this development event and provide insights:

Tool: %s
File: %s
Result: %s

Respond in JSON format:
{
  "summary": "Brief summary (1 sentence)",
  "category": "decision|pattern|insight|strategy",
  "importance": 1-10,
  "tags": ["tag1", "tag2"],
  "reasoning": "Why this is important"
}

Focus on:
- Architectural decisions
- Code patterns
- Problem-solving approaches
- Strategic choices

JSON:`, event.ToolName, filePath, event.ToolResult)

	return prompt
}

// generate calls Ollama to generate text (no system prompt)
func (c *OllamaClient) generate(prompt string) (string, error) {
	return c.generateInternal(prompt, "")
}

// generateInternal calls Ollama with optional system prompt
func (c *OllamaClient) generateInternal(prompt, system string) (string, error) {
	reqBody := OllamaRequest{
		Model:  c.model,
		Prompt: prompt,
		System: system,
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/generate",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", fmt.Errorf("failed to call Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	var ollamaResp OllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return ollamaResp.Response, nil
}

