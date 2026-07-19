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

	// Multi-endpoint LLM registry (Phase 4 model-registry).
	// Endpoints lists OpenAI-compatible servers Cortex should probe and
	// route through; Models pins per-role model assignments. Both
	// optional — when empty, the legacy slash-routes-to-OpenRouter +
	// bare-name-routes-to-Ollama behavior is preserved.
	Endpoints []EndpointDef `json:"endpoints,omitempty"`
	Models    *ModelsMap    `json:"models,omitempty"`

	// DefaultModel pins the REPL's per-session default model when the
	// user hasn't set CORTEX_REPL_MODEL or passed --model. Resolved
	// through the same Endpoints / Ollama / OpenRouter routing that
	// any --model value goes through — `local-gw/Qwen3-Coder-30B-...`
	// here works the same as passing it on the command line. Empty
	// preserves the compile-time fallback (currently
	// qwen2.5-coder:1.5b via Ollama).
	DefaultModel string `json:"default_model,omitempty"`
}

// EndpointDef is one user-configured OpenAI-compatible endpoint.
// Name is a short stable identifier ("local-gw", "lm-studio") used
// both in telemetry and as the routing prefix in model IDs
// (e.g. "local-gw/Qwen3-Coder-30B-A3B-Instruct-GGUF"). BaseURL is
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

	// MaxContextOverride pins the effective context window for every
	// model on this endpoint, in tokens. Set this when the endpoint's
	// /v1/models response advertises the model's *theoretical* max
	// (e.g. lemonade returns 262144 for Qwen3-Coder-30B because that's
	// the model's max, even when llama-server was booted with
	// --ctx-size 65536). Cortex's size-vs-window math (`act.read_file`
	// chunking, salience cap) must respect the runtime deployment, not
	// the model card. Zero (default) trusts /v1/models's value.
	MaxContextOverride int `json:"max_context_override,omitempty"`

	// ModelContextOverrides pins the effective context window per model
	// id, in tokens, for fleets where models are served at different
	// --ctx-size values. A model's entry here wins over
	// MaxContextOverride; models absent from the map fall back to
	// MaxContextOverride, then to the /v1/models-advertised value.
	ModelContextOverrides map[string]int `json:"model_context_overrides,omitempty"`

	// Models lists the bare model ids this endpoint serves. Used by
	// ResolveModelRoute to resolve bare names (e.g. "coder",
	// "xlam-1b-fc-r") without forcing every name into the role-map's
	// five fixed slots. The OpenAI-compat client built for such a
	// bare-name route binds to this endpoint's BaseURL with the
	// declared model id passed through unchanged. Empty (default) =
	// no bare-name routing for this endpoint; bare names fall through
	// to Ollama (legacy behavior preserved).
	Models []string `json:"models,omitempty"`

	// ModelCapabilities lets the operator declare per-model capability
	// tags that the id-pattern auto-detection in
	// `pkg/llm/capabilities.go` can't recognize. The map key is the
	// bare model id served by this endpoint (e.g. "reasoner"); values
	// are Cap* constants from pkg/llm (e.g. ["reasoning",
	// "reasoning:specialist", "tool-calling"]).
	//
	// When the endpoint's /v1/models response already advertises
	// labels (Lemonade), those win. When the response is label-less,
	// this map is preferred over id-pattern inference. The map is
	// purely additive: omitting a model leaves the existing inference
	// path unchanged.
	//
	// Use case: local-gw's "reasoner" alias serves gpt-oss-20b, but
	// the id-pattern detector only recognizes "o1/o3/o4" as reasoning
	// specialists. Adding `"reasoner": ["reasoning",
	// "reasoning:specialist"]` here lets the per-node router pick it
	// for nodes that declare `Requires: [CapReasoningSpecialist]`
	// without modifying source code each time an operator renames a
	// fleet alias.
	ModelCapabilities map[string][]string `json:"model_capabilities,omitempty"`

	// ModelChatTemplateKwargs declares per-model chat-template variables
	// sent verbatim as `chat_template_kwargs` on every request to that
	// model. llama.cpp-backed endpoints (and LiteLLM proxies in front of
	// them) pass these to the model's Jinja template; unknown variables
	// are ignored. The map key is the bare model id served by this
	// endpoint (e.g. "coder").
	//
	// Use case: local-gw's "coder" alias serves a hybrid thinking
	// model. `"coder": {"enable_thinking": false}` suppresses built-in
	// reasoning so coding turns spend tokens on answers, not
	// deliberation. Omitting a model sends a standard request.
	ModelChatTemplateKwargs map[string]map[string]any `json:"model_chat_template_kwargs,omitempty"`
}

// ChatTemplateKwargsFor returns the chat-template variables declared for
// the given bare model id, or nil when none are configured.
func (e EndpointDef) ChatTemplateKwargsFor(model string) map[string]any {
	return e.ModelChatTemplateKwargs[model]
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
// pair using the configured endpoint registry. Four resolution paths:
//
//  1. "endpoint-name/model-id" → looks up endpoint-name in
//     Config.Endpoints; if found, returns (endpoint, "model-id", true).
//     This is the explicit form — what /model command uses.
//  2. Bare model id that matches a role-map entry → returns the
//     pinned (endpoint, model) for that role. Lets the REPL pass
//     bare names through without ambiguity.
//  3. Bare model id that appears in any endpoint's declared Models
//     list → returns that endpoint. Scales beyond the role-map's
//     five slots; first endpoint in declaration order wins on
//     collision.
//  4. Anything else → returns (nil, "", false). Caller falls back to
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
	// Case 3: bare name matches any endpoint's declared Models list.
	// Scales beyond the role-map's five slots — operators can list as
	// many models as their endpoint serves and bare names route there
	// automatically. First endpoint wins on collision (declaration
	// order in cfg.Endpoints is intentional).
	for i := range c.Endpoints {
		for _, m := range c.Endpoints[i].Models {
			if m == model {
				return &c.Endpoints[i], model, true
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
