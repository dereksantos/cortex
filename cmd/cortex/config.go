package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/userhome"
	"github.com/dereksantos/cortex/pkg/llm"
)

// defaultEndpoint is a NEUTRAL local fallback — the conventional LiteLLM port.
const defaultEndpoint = "http://localhost:4000"

const defaultTemperature = 1.0

// Model roles. The harness routes each kind of work to a model binding.
const (
	roleCode     = "code"
	roleHardCode = "hard-code"
	roleReason   = "reason"
	roleFast     = "fast"
	roleStudy    = "study"
	roleEmbed    = "embed"
	roleRerank   = "rerank"
	roleTools    = "tools"
)

type rolePolicy struct {
	tag                string
	preferExperimental bool
	preferSwapFree     bool
	// effort is the role's default reasoning-effort intent
	// (docs/thinking-models.md §1); the zero value (llm.EffortUnset) means
	// "no policy opinion" — resolveBinding leaves the binding at whatever a
	// config override or applyFleet degradation settles on.
	effort llm.Effort
}

var rolePolicies = map[string]rolePolicy{
	roleCode:     {tag: "coder", effort: llm.Effort{Level: llm.EffortOff}},
	roleHardCode: {tag: "coder", preferExperimental: true, effort: llm.Effort{Level: llm.EffortHigh}},
	roleReason:   {tag: "reasoner", preferSwapFree: true, effort: llm.Effort{Level: llm.EffortOn}},
	roleFast:     {tag: "fast", effort: llm.Effort{Level: llm.EffortOff}},
	roleStudy:    {tag: "reasoner", preferSwapFree: true, effort: llm.Effort{Level: llm.EffortOn}},
	roleEmbed:    {tag: "embedder"},
	roleRerank:   {tag: "reranker"},
	roleTools:    {tag: "tool"},
}

func selectModel(fleet Fleet, role string) string {
	pol, ok := rolePolicies[role]
	if !ok {
		return ""
	}
	best, bestScore := "", -1
	for name, info := range fleet {
		if info.Role != pol.tag {
			continue
		}
		score := 0
		if info.Experimental == pol.preferExperimental {
			score += 2
		}
		if pol.preferSwapFree && info.SwapGroup == "" {
			score++
		}
		if score > bestScore || (score == bestScore && (best == "" || name < best)) {
			best, bestScore = name, score
		}
	}
	return best
}

const fallbackWindow = 32768

// ModelSpec is one role's binding: where to send, which model, and context size.
type ModelSpec struct {
	Endpoint    string   `json:"endpoint"`
	Model       string   `json:"model"`
	Window      int      `json:"window"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
	KeyEnv      string   `json:"key_env"`
	KeyService  string   `json:"key_service"`
	// Thinking is the resolved reasoning-effort intent for this binding
	// (docs/thinking-models.md §1). JSON-compatible with the legacy bool
	// (false→off, true→on), a level string ("off"/"on"/"low"/"medium"/
	// "high"), or {"budget": N} — see llm.Effort's UnmarshalJSON. The zero
	// value means "unset" (JSON key absent).
	Thinking llm.Effort `json:"thinking"`
}

func (s ModelSpec) maxOut(def int) int {
	if s.MaxTokens > 0 {
		return s.MaxTokens
	}
	return def
}

func (s ModelSpec) temperature(def float64) float64 {
	if s.Temperature != nil {
		return *s.Temperature
	}
	return def
}

// TemplateKwargs translates m.Thinking to the chat_template_kwargs dialect
// (llama.cpp/LiteLLM — docs/thinking-models.md §2). Routed through
// llm.Translate so this and the OpenRouter dialect (Reasoning, below) share
// one translation function.
func (m ModelSpec) TemplateKwargs() map[string]any {
	kwargs, _ := llm.Translate(llm.DialectTemplateKwargs, m.Thinking)
	return kwargs
}

// Reasoning translates m.Thinking to OpenRouter's request-body `reasoning`
// dialect (docs/thinking-models.md §2). nil when there's nothing to say
// (unset or "on" — model default).
func (m ModelSpec) Reasoning() *llm.Reasoning {
	_, reasoning := llm.Translate(llm.DialectOpenRouter, m.Thinking)
	return reasoning
}

// thinkingLabel resolves a request's chat_template_kwargs into the
// eval-telemetry "thinking" attribution: "off" when enable_thinking is
// explicitly suppressed (ModelSpec.Thinking == false, via TemplateKwargs
// above), "on" otherwise — either the model always reasons, or thinking is
// left at its default. Takes the built kwargs map (not the spec) so it works
// uniformly for both a live ModelSpec and a request inherited/overridden
// elsewhere (subagentRequest).
func thinkingLabel(kwargs map[string]any) string {
	if v, ok := kwargs["enable_thinking"]; ok {
		if b, ok2 := v.(bool); ok2 && !b {
			return "off"
		}
	}
	return "on"
}

type ModelInfo struct {
	MaxInput int
	Role     string
	Silicon  string
	Thinking bool
	// ThinkingMode is the fleet's optional "none"|"hybrid"|"levels"|"always"
	// descriptor (docs/thinking-models.md §2) — omitempty so the JSON shape
	// is unchanged for every fleet response that doesn't send it (kept
	// empty; thinkingMode() derives it from the legacy Thinking bool).
	ThinkingMode string `json:",omitempty"`
	SwapGroup    string
	AlwaysWarm   bool
	Experimental bool
}

// thinkingMode resolves the model's effective thinking_mode: the fleet's
// explicit value when it sent one, else derived from the legacy Thinking
// bool (true→hybrid, false→none) — so a /model/info response that only ever
// sends `"thinking": true/false` (today's shape) still degrades correctly.
func (info ModelInfo) thinkingMode() string {
	if info.ThinkingMode != "" {
		return info.ThinkingMode
	}
	if info.Thinking {
		return "hybrid"
	}
	return "none"
}

type Fleet map[string]ModelInfo

const fleetDiscoveryTimeout = 4 * time.Second

func discoverFleet(ctx context.Context, endpoint string) Fleet {
	ctx, cancel := context.WithTimeout(ctx, fleetDiscoveryTimeout)
	defer cancel()
	url := strings.TrimRight(endpoint, "/") + "/model/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				MaxInputTokens int    `json:"max_input_tokens"`
				Role           string `json:"role"`
				Silicon        string `json:"silicon"`
				Thinking       bool   `json:"thinking"`
				// ThinkingMode is the optional "none"|"hybrid"|"levels"|
				// "always" descriptor (docs/thinking-models.md §2); a fleet
				// that only sends the legacy bool leaves this empty and
				// ModelInfo.thinkingMode() derives it (true→hybrid).
				ThinkingMode string `json:"thinking_mode"`
				SwapGroup    string `json:"swap_group"`
				AlwaysWarm   bool   `json:"always_warm"`
				Experimental bool   `json:"experimental"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || len(body.Data) == 0 {
		return nil
	}
	f := make(Fleet, len(body.Data))
	for _, m := range body.Data {
		f[m.ModelName] = ModelInfo{
			MaxInput:     m.ModelInfo.MaxInputTokens,
			Role:         m.ModelInfo.Role,
			Silicon:      m.ModelInfo.Silicon,
			Thinking:     m.ModelInfo.Thinking,
			ThinkingMode: m.ModelInfo.ThinkingMode,
			SwapGroup:    m.ModelInfo.SwapGroup,
			AlwaysWarm:   m.ModelInfo.AlwaysWarm,
			Experimental: m.ModelInfo.Experimental,
		}
	}
	return f
}

// degradeForThinkingMode refuses an effort ask a model's thinking_mode can't
// satisfy (docs/thinking-models.md §2): "none" can't reason at all, so ANY
// ask — including an explicit "off", which would just be a no-op kwarg — is
// dropped to unset; "hybrid" (an on/off toggle, no real levels) degrades a
// level or budget ask to plain "on"; "always" (can never stop reasoning)
// degrades an explicit "off" ask to "on"; "levels" (and any unrecognized
// mode) needs no degradation.
func degradeForThinkingMode(e llm.Effort, mode string) llm.Effort {
	switch mode {
	case "none":
		return llm.Effort{}
	case "hybrid":
		if e.Level == llm.EffortLow || e.Level == llm.EffortMedium || e.Level == llm.EffortHigh ||
			(e.Level == llm.EffortUnset && e.Budget > 0) {
			return llm.Effort{Level: llm.EffortOn}
		}
		return e
	case "always":
		if e.Level == llm.EffortOff {
			return llm.Effort{Level: llm.EffortOn}
		}
		return e
	default: // "levels", or an unrecognized mode: no degradation
		return e
	}
}

func applyFleet(spec ModelSpec, fleet Fleet) ModelSpec {
	info, ok := fleet[spec.Model]
	if !ok {
		return spec
	}
	if info.MaxInput > 0 && spec.Window == 0 {
		spec.Window = info.MaxInput
	}
	spec.Thinking = degradeForThinkingMode(spec.Thinking, info.thinkingMode())
	return spec
}

func sharedSwapGroup(fleet Fleet, a, b ModelSpec) string {
	if fleet == nil || a.Model == b.Model {
		return ""
	}
	if g := fleet[a.Model].SwapGroup; g != "" && g == fleet[b.Model].SwapGroup {
		return g
	}
	return ""
}

func keychainKey(service string) string {
	if service == "" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func resolveKey(m ModelSpec) string {
	if m.KeyEnv != "" {
		if v := strings.TrimSpace(os.Getenv(m.KeyEnv)); v != "" {
			return v
		}
	}
	return keychainKey(m.KeyService)
}

type Backend struct {
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	KeyEnv     string `json:"key_env"`
	KeyService string `json:"key_service"`
}

type Config struct {
	Backend     Backend              `json:"backend"`
	Models      map[string]ModelSpec `json:"models"`
	Tools       ToolConfig           `json:"tools"`
	Temperature *float64             `json:"temperature"`
}

type ToolConfig struct {
	AllowDelete *bool  `json:"allow_delete"`
	DeleteRoot  string `json:"delete_root"`
	EnableWeb   *bool  `json:"enable_web"`
	// EnableAgent gates the general `agent` implementation subagent (GOAL.md §3
	// slice 3b, docs/agent-tool.md). Nil means enabled — a missing config key
	// must not disable a shipped tool, matching the EnableContext* precedent.
	EnableAgent *bool `json:"enable_agent"`
	// EnableScan gates the scan_landscape coder tool (cortex-web M2.6). Nil
	// means enabled: it is an availability kill-switch, not consent — consent
	// stays the user's in-conversation reply.
	EnableScan *bool `json:"enable_scan"`

	// Context window modification tools
	EnableContextEvict            *bool `json:"enable_context_evict"`
	EnableContextMerge            *bool `json:"enable_context_merge"`
	EnableContextAdjustWatermarks *bool `json:"enable_context_adjust_watermarks"`
}

func (c *Config) isOpenRouter() bool {
	return c != nil && strings.EqualFold(c.Backend.Type, "openrouter")
}

func (c *Config) deleteEnabled() bool {
	if c == nil || c.Tools.AllowDelete == nil {
		return true
	}
	return *c.Tools.AllowDelete
}

// scanEnabled reports whether the scan_landscape coder tool is enabled
// (GOAL.md M2.6: tools.enable_scan is an availability kill-switch, default
// enabled — absent or nil means the tool stays registered).
func (c *Config) scanEnabled() bool {
	if c == nil || c.Tools.EnableScan == nil {
		return true
	}
	return *c.Tools.EnableScan
}

func (c *Config) backendEndpoint() string {
	if c != nil && c.Backend.Endpoint != "" {
		return c.Backend.Endpoint
	}
	if v := os.Getenv("CORTEX_BACKEND"); v != "" {
		return v
	}
	return defaultEndpoint
}

func (c *Config) resolveBinding(role string, fleet Fleet) ModelSpec {
	pol := rolePolicies[role]
	spec := ModelSpec{Endpoint: c.backendEndpoint(), Thinking: pol.effort}
	if c != nil && c.Temperature != nil {
		spec.Temperature = c.Temperature
	}
	if c != nil {
		if m, ok := c.Models[role]; ok {
			spec.Model = m.Model
			if m.Endpoint != "" {
				spec.Endpoint = m.Endpoint
			}
			if m.Window > 0 {
				spec.Window = m.Window
			}
			if m.Temperature != nil {
				spec.Temperature = m.Temperature
			}
			if m.KeyEnv != "" {
				spec.KeyEnv = m.KeyEnv
			}
			if m.KeyService != "" {
				spec.KeyService = m.KeyService
			}
			if !m.Thinking.IsZero() {
				spec.Thinking = m.Thinking
			}
		}
		if spec.KeyEnv == "" {
			spec.KeyEnv = c.Backend.KeyEnv
		}
		if spec.KeyService == "" {
			spec.KeyService = c.Backend.KeyService
		}
	}
	if spec.Model == "" {
		spec.Model = selectModel(fleet, role)
	}
	spec = applyFleet(spec, fleet)
	if c != nil {
		// Re-stamp an EXPLICIT config override after applyFleet's
		// thinking_mode degradation: a user who pinned "thinking" for this
		// role always wins, even over a fleet that reports the model can't
		// honor it (docs/thinking-models.md known regression: a config
		// override must not be silently stripped).
		if m, ok := c.Models[role]; ok && !m.Thinking.IsZero() {
			spec.Thinking = m.Thinking
		}
	}
	return spec
}

func findUp(rel string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func findConfigPath() string { return findUp(filepath.Join(".cortex", "config.json")) }

const maxInstructionBytes = 16384

func projectInstructions() string {
	path := findUp("AGENTS.md")
	if path == "" {
		return ""
	}
	return readInstructions(path)
}

// readInstructions reads and trims an AGENTS.md at an exact path (no
// upward search), truncating at maxInstructionBytes — the shared body
// projectInstructions() (CWD-implicit, via findUp) and
// Workspace.Instructions() (explicit root, workspace.go) both use, so the
// two stay provably identical for the same resolved path.
func readInstructions(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > maxInstructionBytes {
		s = s[:maxInstructionBytes] + "\n...[AGENTS.md truncated]"
	}
	return s
}

func LoadConfig() *Config {
	return loadMergedConfig(userConfigPath(), findConfigPath())
}

func loadMergedConfig(userPath, projectPath string) *Config {
	user := readConfigFile(userPath)
	project := readConfigFile(projectPath)
	switch {
	case user == nil:
		return project
	case project == nil:
		return user
	default:
		return mergeConfig(user, project)
	}
}

func readConfigFile(path string) *Config {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func userConfigPath() string {
	p, err := userhome.Path("config.json")
	if err != nil {
		return ""
	}
	return p
}

func mergeConfig(base, over *Config) *Config {
	if base == nil {
		return over
	}
	if over == nil {
		return base
	}
	out := *base
	out.Backend = mergeBackend(base.Backend, over.Backend)
	out.Models = mergeModels(base.Models, over.Models)
	out.Tools = mergeTools(base.Tools, over.Tools)
	if over.Temperature != nil {
		out.Temperature = over.Temperature
	}
	return &out
}

func mergeBackend(base, over Backend) Backend {
	if over.Type != "" {
		base.Type = over.Type
	}
	if over.Endpoint != "" {
		base.Endpoint = over.Endpoint
	}
	if over.KeyEnv != "" {
		base.KeyEnv = over.KeyEnv
	}
	if over.KeyService != "" {
		base.KeyService = over.KeyService
	}
	return base
}

func mergeModels(base, over map[string]ModelSpec) map[string]ModelSpec {
	if len(base) == 0 {
		return over
	}
	out := make(map[string]ModelSpec, len(base)+len(over))
	for role, spec := range base {
		out[role] = spec
	}
	for role, o := range over {
		if b, ok := out[role]; ok {
			out[role] = mergeSpec(b, o)
		} else {
			out[role] = o
		}
	}
	return out
}

func mergeSpec(base, over ModelSpec) ModelSpec {
	if over.Endpoint != "" {
		base.Endpoint = over.Endpoint
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	if over.Window > 0 {
		base.Window = over.Window
	}
	if over.Temperature != nil {
		base.Temperature = over.Temperature
	}
	if over.KeyEnv != "" {
		base.KeyEnv = over.KeyEnv
	}
	if over.KeyService != "" {
		base.KeyService = over.KeyService
	}
	if !over.Thinking.IsZero() {
		base.Thinking = over.Thinking
	}
	return base
}

func mergeTools(base, over ToolConfig) ToolConfig {
	if over.AllowDelete != nil {
		base.AllowDelete = over.AllowDelete
	}
	if over.DeleteRoot != "" {
		base.DeleteRoot = over.DeleteRoot
	}
	if over.EnableWeb != nil {
		base.EnableWeb = over.EnableWeb
	}
	if over.EnableAgent != nil {
		base.EnableAgent = over.EnableAgent
	}
	if over.EnableScan != nil {
		base.EnableScan = over.EnableScan
	}
	if over.EnableContextEvict != nil {
		base.EnableContextEvict = over.EnableContextEvict
	}
	if over.EnableContextMerge != nil {
		base.EnableContextMerge = over.EnableContextMerge
	}
	if over.EnableContextAdjustWatermarks != nil {
		base.EnableContextAdjustWatermarks = over.EnableContextAdjustWatermarks
	}
	return base
}
