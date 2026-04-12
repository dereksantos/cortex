// Package llm provides internal LLM detection utilities
package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// LLMStatus represents the detected LLM configuration
type LLMStatus struct {
	Available        bool
	Provider         string // "ollama" | "anthropic" | ""
	Model            string // e.g., "qwen2.5:3b"
	OllamaInstalled  bool
	OllamaPath       string
	OllamaModels     []string // all installed models
	AnthropicKeySet  bool
	RecommendedModel string // suggestion if no good model
}

// RecommendedModels lists models in priority order for Cortex
var RecommendedModels = []string{
	"qwen2.5:3b",   // Best balance
	"qwen2.5:7b",   // Higher quality
	"qwen2.5:0.5b", // Lightweight
	"llama3.2:3b",  // Alternative
	"gemma2:2b",    // Alternative
}

// DetectLLM checks for available LLM providers and models
func DetectLLM() LLMStatus {
	status := LLMStatus{}

	// 1. Check Anthropic API key
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		status.AnthropicKeySet = true
		status.Available = true
		status.Provider = "anthropic"
		// Don't return yet - still check Ollama for local option
	}

	// 2. Check Ollama installed
	ollamaPath, err := exec.LookPath("ollama")
	if err != nil {
		if !status.Available {
			status.RecommendedModel = "qwen2.5:3b"
		}
		return status
	}
	status.OllamaInstalled = true
	status.OllamaPath = ollamaPath

	// 3. Check if Ollama is running
	if !isOllamaRunning() {
		if !status.Available {
			status.RecommendedModel = "qwen2.5:3b"
		}
		return status
	}

	// 4. List Ollama models
	models, err := listOllamaModels()
	if err != nil {
		if !status.Available {
			status.RecommendedModel = "qwen2.5:3b"
		}
		return status
	}
	status.OllamaModels = models

	// 5. Check for recommended model
	for _, rec := range RecommendedModels {
		for _, installed := range status.OllamaModels {
			if strings.HasPrefix(installed, rec) {
				status.Available = true
				status.Provider = "ollama"
				status.Model = installed
				return status
			}
		}
	}

	// 6. Use any available model
	if len(status.OllamaModels) > 0 {
		status.Available = true
		status.Provider = "ollama"
		status.Model = status.OllamaModels[0]
		return status
	}

	// 7. Ollama installed and running but no models
	status.RecommendedModel = "qwen2.5:3b"
	return status
}

// isOllamaRunning checks if Ollama service is running
func isOllamaRunning() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// listOllamaModels returns a list of installed Ollama models
func listOllamaModels() ([]string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, m.Name)
	}

	return models, nil
}
