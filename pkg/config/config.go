// Package config handles Cortex configuration
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds Cortex configuration
type Config struct {
	// Paths
	ContextDir  string `json:"context_dir"`
	ProjectRoot string `json:"project_root"`

	// Capture settings
	SkipPatterns []string `json:"skip_patterns"`

	// LLM settings - Ollama
	OllamaURL   string `json:"ollama_url"`
	OllamaModel string `json:"ollama_model"`

	// LLM settings - Anthropic
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string `json:"anthropic_model,omitempty"`

	// Feature flags
	EnableGraph  bool `json:"enable_graph"`
	EnableVector bool `json:"enable_vector"`
}

// Default returns a default configuration
func Default() *Config {
	projectRoot, _ := os.Getwd()

	return &Config{
		ContextDir:  filepath.Join(projectRoot, ".context"),
		ProjectRoot: projectRoot,
		SkipPatterns: []string{
			".git",
			"node_modules",
			"venv",
			".context",
			"__pycache__",
		},
		OllamaURL:      "http://localhost:11434",
		OllamaModel:    "mistral:7b",
		AnthropicModel: "claude-3-5-haiku-20241022",
		EnableGraph:    true,
		EnableVector: false,
	}
}

// Load loads configuration from file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default if file doesn't exist
			return Default(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// EnsureDirectories creates required directory structure
func (c *Config) EnsureDirectories() error {
	dirs := []string{
		c.ContextDir,
		filepath.Join(c.ContextDir, "queue", "pending"),
		filepath.Join(c.ContextDir, "queue", "processing"),
		filepath.Join(c.ContextDir, "queue", "processed"),
		filepath.Join(c.ContextDir, "knowledge", "decisions"),
		filepath.Join(c.ContextDir, "knowledge", "patterns"),
		filepath.Join(c.ContextDir, "knowledge", "insights"),
		filepath.Join(c.ContextDir, "knowledge", "strategies"),
		filepath.Join(c.ContextDir, "logs"),
		filepath.Join(c.ContextDir, "db"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}
