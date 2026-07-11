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
	thinkingOff        bool
}

var rolePolicies = map[string]rolePolicy{
	roleCode:     {tag: "coder", thinkingOff: true},
	roleHardCode: {tag: "coder", preferExperimental: true},
	roleReason:   {tag: "reasoner", preferSwapFree: true},
	roleFast:     {tag: "fast", thinkingOff: true},
	roleStudy:    {tag: "reasoner", preferSwapFree: true},
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

// thinkingOff exists so role bindings can take a *bool address.
var thinkingOff = false

// ModelSpec is one role's binding: where to send, which model, and context size.
type ModelSpec struct {
	Endpoint    string   `json:"endpoint"`
	Model       string   `json:"model"`
	Window      int      `json:"window"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
	KeyEnv      string   `json:"key_env"`
	KeyService  string   `json:"key_service"`
	Thinking    *bool    `json:"thinking"`
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

func (m ModelSpec) TemplateKwargs() map[string]any {
	if m.Thinking != nil && !*m.Thinking {
		return map[string]any{"enable_thinking": false}
	}
	return nil
}

type ModelInfo struct {
	MaxInput     int
	Role         string
	Silicon      string
	Thinking     bool
	SwapGroup    string
	AlwaysWarm   bool
	Experimental bool
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
				SwapGroup      string `json:"swap_group"`
				AlwaysWarm     bool   `json:"always_warm"`
				Experimental   bool   `json:"experimental"`
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
			SwapGroup:    m.ModelInfo.SwapGroup,
			AlwaysWarm:   m.ModelInfo.AlwaysWarm,
			Experimental: m.ModelInfo.Experimental,
		}
	}
	return f
}

func applyFleet(spec ModelSpec, fleet Fleet) ModelSpec {
	info, ok := fleet[spec.Model]
	if !ok {
		return spec
	}
	if info.MaxInput > 0 && spec.Window == 0 {
		spec.Window = info.MaxInput
	}
	if !info.Thinking {
		spec.Thinking = nil
	}
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
	EnableScan  *bool  `json:"enable_scan"`

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
	spec := ModelSpec{Endpoint: c.backendEndpoint()}
	if c != nil && c.Temperature != nil {
		spec.Temperature = c.Temperature
	}
	if pol.thinkingOff {
		spec.Thinking = &thinkingOff
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
			if m.Thinking != nil {
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
		if m, ok := c.Models[role]; ok && m.Thinking != nil {
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
	if over.Thinking != nil {
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
