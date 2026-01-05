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
	baseURL        string
	model          string
	embeddingModel string
	httpClient     *http.Client
}

// NewOllamaClient creates a new Ollama client
func NewOllamaClient(cfg *config.Config) *OllamaClient {
	embeddingModel := cfg.OllamaEmbeddingModel
	if embeddingModel == "" {
		embeddingModel = "nomic-embed-text"
	}
	return &OllamaClient{
		baseURL:        cfg.OllamaURL,
		model:          cfg.OllamaModel,
		embeddingModel: embeddingModel,
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

	// Check if our model is in the list (match exactly or with :latest suffix)
	for _, model := range modelsResp.Models {
		if model.Name == c.model || model.Name == c.model+":latest" {
			return true
		}
	}

	return false
}

// AnalyzeEvent analyzes an event and extracts insights
func (c *OllamaClient) AnalyzeEvent(event *events.Event) (*Analysis, error) {
	filePath, _ := event.ToolInput["file_path"].(string)
	prompt := BuildAnalysisPrompt(event.ToolName, filePath, event.ToolResult)

	response, err := c.generateInternal(prompt, AnalysisSystemPrompt)
	if err != nil {
		return nil, err
	}

	return ParseAnalysisWithFallback(response), nil
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

// EmbedRequest represents a request to Ollama's embedding endpoint
type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// EmbedResponse represents a response from Ollama's embedding endpoint
type EmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed generates embeddings for text using Ollama
func (c *OllamaClient) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody := EmbedRequest{
		Model: c.embeddingModel,
		Input: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embed", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call Ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embed returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("failed to decode embed response: %w", err)
	}

	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return embedResp.Embeddings[0], nil
}

// IsEmbeddingModelAvailable checks if the embedding model is available
func (c *OllamaClient) IsEmbeddingModelAvailable() bool {
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

	for _, model := range modelsResp.Models {
		// Match exactly or with :latest suffix (e.g., "nomic-embed-text" matches "nomic-embed-text:latest")
		if model.Name == c.embeddingModel || model.Name == c.embeddingModel+":latest" {
			return true
		}
	}

	return false
}

// IsEmbeddingAvailable satisfies the llm.Embedder interface.
func (c *OllamaClient) IsEmbeddingAvailable() bool {
	return c.IsAvailable() && c.IsEmbeddingModelAvailable()
}

