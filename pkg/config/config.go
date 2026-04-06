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
	OllamaURL            string `json:"ollama_url"`
	OllamaModel          string `json:"ollama_model"`
	OllamaEmbeddingModel string `json:"ollama_embedding_model"`

	// LLM settings - Anthropic (API key read from ANTHROPIC_API_KEY env var)
	AnthropicModel string `json:"anthropic_model,omitempty"`

	// Storage
	DatabaseURL string `json:"database_url,omitempty"` // SQLite path override (default: .cortex/db/events.db)

	// Web dashboard
	WebPort int `json:"web_port,omitempty"`

	// Cognitive mode tuning
	Modes *ModeConfig `json:"modes,omitempty"`

	// Feature flags
	EnableGraph  bool `json:"enable_graph"`
	EnableVector bool `json:"enable_vector"`
}

// ModeConfig provides per-mode tuning knobs.
// All fields are optional — nil means use defaults.
type ModeConfig struct {
	Think   *ThinkModeConfig   `json:"think,omitempty"`
	Dream   *DreamModeConfig   `json:"dream,omitempty"`
	Digest  *DigestModeConfig  `json:"digest,omitempty"`
	Capture *CaptureModeConfig `json:"capture,omitempty"`
	Search  *SearchModeConfig  `json:"search,omitempty"`
}

// ThinkModeConfig controls Think mode behavior.
type ThinkModeConfig struct {
	Enabled            *bool  `json:"enabled,omitempty"`
	MaxBudget          *int   `json:"max_budget,omitempty"`
	MinBudget          *int   `json:"min_budget,omitempty"`
	Mode               string `json:"mode,omitempty"`                // "fast" or "full"
	OperationTimeoutMs *int   `json:"operation_timeout_ms,omitempty"`
}

// DreamModeConfig controls Dream mode behavior.
type DreamModeConfig struct {
	Enabled         *bool `json:"enabled,omitempty"`
	MaxBudget       *int  `json:"max_budget,omitempty"`
	MinBudget       *int  `json:"min_budget,omitempty"`
	IdleThresholdS  *int  `json:"idle_threshold_s,omitempty"`
	GrowthDurationM *int  `json:"growth_duration_m,omitempty"`
}

// DigestModeConfig controls Digest mode behavior.
type DigestModeConfig struct {
	Enabled             *bool    `json:"enabled,omitempty"`
	MaxMerges           *int     `json:"max_merges,omitempty"`
	SimilarityThreshold *float64 `json:"similarity_threshold,omitempty"`
}

// CaptureModeConfig controls event capture behavior.
type CaptureModeConfig struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Queue   string `json:"queue,omitempty"` // "file" or "direct"
}

// SearchModeConfig controls search/retrieval behavior.
type SearchModeConfig struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Mode    string `json:"mode,omitempty"` // "fast" or "full"
}

// boolPtr is a helper for creating *bool values in config.
func boolPtr(b bool) *bool { return &b }

// intPtr is a helper for creating *int values in config.
func intPtr(i int) *int { return &i }

// Default returns a default configuration
func Default() *Config {
	projectRoot, _ := os.Getwd()

	return &Config{
		ContextDir:  filepath.Join(projectRoot, ".cortex"),
		ProjectRoot: projectRoot,
		SkipPatterns: []string{
			".git",
			"node_modules",
			"venv",
			".cortex",
			"__pycache__",
		},
		OllamaURL:            "http://localhost:11434",
		OllamaModel:          "qwen2.5-coder:1.5b",
		OllamaEmbeddingModel: "nomic-embed-text",
		AnthropicModel:       "claude-haiku-4-5-20251001",
		WebPort:              9090,
		EnableGraph:  true,
		EnableVector: true,
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
		filepath.Join(c.ContextDir, "evals"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}

// KnowledgePath returns the path to a knowledge category directory.
func (c *Config) KnowledgePath(category string) string {
	return filepath.Join(c.ContextDir, "knowledge", category)
}
