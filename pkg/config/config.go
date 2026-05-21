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
	GlobalDir   string `json:"global_dir,omitempty"` // ~/.cortex/ — central storage and daemon home
	ProjectID   string `json:"project_id,omitempty"` // Slug identifying this project in the global registry

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

	// Multi-endpoint LLM registry (Phase 4 model-registry).
	// Endpoints lists OpenAI-compatible servers Cortex should probe and
	// route through; Models pins per-role model assignments. Both
	// optional — when empty, the legacy slash-routes-to-OpenRouter +
	// bare-name-routes-to-Ollama behavior is preserved.
	Endpoints []EndpointDef `json:"endpoints,omitempty"`
	Models    *ModelsMap    `json:"models,omitempty"`

	// Feature flags
	EnableGraph  bool `json:"enable_graph"`
	EnableVector bool `json:"enable_vector"`
}

// EndpointDef is one user-configured OpenAI-compatible endpoint.
// Name is a short stable identifier ("chatterbox", "lm-studio") used
// both in telemetry and as the routing prefix in model IDs
// (e.g. "chatterbox/Qwen3-Coder-30B-A3B-Instruct-GGUF"). BaseURL is
// the endpoint's OpenAI root (e.g. "http://localhost:13305/v1").
//
// APIKey can be set inline (insecure but convenient for dev), via
// APIKeyEnv (preferred — names an env var to read at runtime), or
// omitted entirely (many local endpoints accept any value or no auth).
type EndpointDef struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// ResolveAPIKey returns the API key for this endpoint. APIKeyEnv wins
// over APIKey when both are set (lets users keep the value out of
// committed config). Empty string is a valid return — endpoints
// without auth still work.
func (e EndpointDef) ResolveAPIKey() string {
	if e.APIKeyEnv != "" {
		if v := os.Getenv(e.APIKeyEnv); v != "" {
			return v
		}
	}
	return e.APIKey
}

// ModelsMap pins per-role model assignments. Each role names a
// (endpoint, model) pair the REPL / harness uses for that purpose.
// Endpoint name must match an EndpointDef.Name in Config.Endpoints.
// All fields optional — nil role falls back to the slash-routing
// heuristic.
type ModelsMap struct {
	Code   *RoleAssignment `json:"code,omitempty"`
	Reason *RoleAssignment `json:"reason,omitempty"`
	Fast   *RoleAssignment `json:"fast,omitempty"`
	Embed  *RoleAssignment `json:"embed,omitempty"`
	Rerank *RoleAssignment `json:"rerank,omitempty"`
}

// RoleAssignment binds a role to a specific endpoint+model pair.
type RoleAssignment struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// FindEndpoint returns the EndpointDef matching name, or nil if no
// such endpoint is configured. O(N) but the list is short by design
// (most users have 2-5 endpoints).
func (c *Config) FindEndpoint(name string) *EndpointDef {
	for i := range c.Endpoints {
		if c.Endpoints[i].Name == name {
			return &c.Endpoints[i]
		}
	}
	return nil
}

// ResolveModelRoute parses a model string into an endpoint + model
// pair using the configured endpoint registry. Three resolution paths:
//
//  1. "endpoint-name/model-id" → looks up endpoint-name in
//     Config.Endpoints; if found, returns (endpoint, "model-id", true).
//     This is the explicit form — what /model command uses.
//  2. Bare model id that matches a role-map entry → returns the
//     pinned (endpoint, model) for that role. Lets the REPL pass
//     bare names through without ambiguity.
//  3. Anything else → returns (nil, "", false). Caller falls back to
//     the legacy slash-routing (OpenRouter for slashed, Ollama for
//     bare).
//
// The function is intentionally permissive: it never errors on
// unknown prefixes (those just fall to case 3), because the same
// slash convention is also used by OpenRouter for its own provider
// prefixes ("anthropic/...", "openai/..."). Custom endpoints take
// priority via FindEndpoint; everything else falls through to
// existing behavior.
func (c *Config) ResolveModelRoute(model string) (*EndpointDef, string, bool) {
	if c == nil {
		return nil, "", false
	}
	// Case 1: explicit endpoint/model form.
	if idx := indexSlash(model); idx > 0 {
		prefix := model[:idx]
		rest := model[idx+1:]
		if ep := c.FindEndpoint(prefix); ep != nil {
			return ep, rest, true
		}
	}
	// Case 2: bare name matches a role-map model — only when the
	// role-map's model is bare too (no slash collision with case 1).
	if c.Models != nil {
		for _, a := range []*RoleAssignment{c.Models.Code, c.Models.Reason, c.Models.Fast, c.Models.Embed, c.Models.Rerank} {
			if a == nil || a.Model != model {
				continue
			}
			if ep := c.FindEndpoint(a.Endpoint); ep != nil {
				return ep, a.Model, true
			}
		}
	}
	return nil, "", false
}

// DefaultGenerationModel returns the model id callers should treat as
// the implicit default when none is supplied.
//
// Used by internal/llm.BuildProvider when invoked with an empty
// modelID — the helper centralizes the "what should we route to?"
// answer instead of scattering `cfg.OllamaModel` references across
// every command. Order:
//
//  1. OllamaModel (local default — covers the common Cortex setup).
//  2. AnthropicModel (typically a slash-prefixed OpenRouter form when
//     the user wants hosted-by-default; bare anthropic-direct ids stay
//     supported but route to Ollama unless the user updates them).
//  3. "" — caller falls back to mechanical / fail-soft.
//
// nil-receiver returns "" so callers don't have to defend against it.
func (c *Config) DefaultGenerationModel() string {
	if c == nil {
		return ""
	}
	if c.OllamaModel != "" {
		return c.OllamaModel
	}
	return c.AnthropicModel
}

// indexSlash returns the position of the first '/' in s, or -1.
// Local helper to avoid importing strings in this hot-path resolver.
func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
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
	Mode               string `json:"mode,omitempty"` // "fast" or "full"
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
	homeDir, _ := os.UserHomeDir()

	globalDir := ""
	if homeDir != "" {
		globalDir = filepath.Join(homeDir, ".cortex")
	}

	return &Config{
		ContextDir:  filepath.Join(projectRoot, ".cortex"),
		ProjectRoot: projectRoot,
		GlobalDir:   globalDir,
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
		EnableGraph:          true,
		EnableVector:         true,
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
		filepath.Join(c.ContextDir, "journal", "capture"),
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

// LoadGlobal loads global config from ~/.cortex/config.json, then overlays
// per-project config on top. Global settings provide defaults; project
// settings override them.
func LoadGlobal(projectConfigPath string) (*Config, error) {
	// Start with defaults
	cfg := Default()

	// Load global config if it exists
	if cfg.GlobalDir != "" {
		globalPath := filepath.Join(cfg.GlobalDir, "config.json")
		if data, err := os.ReadFile(globalPath); err == nil {
			var global Config
			if err := json.Unmarshal(data, &global); err == nil {
				mergeConfig(cfg, &global)
			}
		}
	}

	// Overlay project config
	if projectConfigPath != "" {
		if data, err := os.ReadFile(projectConfigPath); err == nil {
			var project Config
			if err := json.Unmarshal(data, &project); err == nil {
				mergeConfig(cfg, &project)
			}
		}
	}

	return cfg, nil
}

// mergeConfig overlays non-zero values from src onto dst.
func mergeConfig(dst, src *Config) {
	if src.ContextDir != "" {
		dst.ContextDir = src.ContextDir
	}
	if src.ProjectRoot != "" {
		dst.ProjectRoot = src.ProjectRoot
	}
	if src.GlobalDir != "" {
		dst.GlobalDir = src.GlobalDir
	}
	if src.ProjectID != "" {
		dst.ProjectID = src.ProjectID
	}
	if src.OllamaURL != "" {
		dst.OllamaURL = src.OllamaURL
	}
	if src.OllamaModel != "" {
		dst.OllamaModel = src.OllamaModel
	}
	if src.OllamaEmbeddingModel != "" {
		dst.OllamaEmbeddingModel = src.OllamaEmbeddingModel
	}
	if src.AnthropicModel != "" {
		dst.AnthropicModel = src.AnthropicModel
	}
	if src.DatabaseURL != "" {
		dst.DatabaseURL = src.DatabaseURL
	}
	if src.WebPort != 0 {
		dst.WebPort = src.WebPort
	}
	if src.Modes != nil {
		dst.Modes = src.Modes
	}
	if src.EnableGraph {
		dst.EnableGraph = true
	}
	if src.EnableVector {
		dst.EnableVector = true
	}
	if len(src.SkipPatterns) > 0 {
		dst.SkipPatterns = src.SkipPatterns
	}
}

// GlobalDataDir returns the path to the global data directory.
// This is where the central JSONL files live.
func (c *Config) GlobalDataDir() string {
	if c.GlobalDir != "" {
		return filepath.Join(c.GlobalDir, "data")
	}
	return filepath.Join(c.ContextDir, "data")
}

// KnowledgePath returns the path to a knowledge category directory.
func (c *Config) KnowledgePath(category string) string {
	return filepath.Join(c.ContextDir, "knowledge", category)
}
