package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/agent"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/internal/userhome"
	"github.com/dereksantos/cortex/pkg/llm"
)

// defaultEndpoint is a NEUTRAL local fallback — the conventional LiteLLM port.
const defaultEndpoint = "http://localhost:4000"

const defaultTemperature = 1.0

// Model roles. The harness routes each kind of work to a model binding.
// Two are agent roles the user configures — code (the agent) and study
// (the Study subagent + the summarizer + the shell-risk classifier, all of
// which build their sub-LLM client from cs.Study and then pin effort off
// at the call site — tool_deps.go's effortOffKwargs — rather than through a
// separate role). embed stays parsed-but-reserved for a future semantic
// memory_search (CortexSession.resolveEmbedder is the one live
// resolveBinding(roleEmbed, ...) call site outside this file).
//
// hard-code/reason/fast/rerank/tools were audited 2026-07-18
// (docs/completion-roadmap.md E1: role collapse) and found dead — no
// resolveBinding call site anywhere outside rolePolicies/tests — and were
// removed from the configurable surface. An old config.json with one of
// those keys under "models" still loads without error; warnUnknownRoles
// prints a one-line stderr warning naming it, and the key is otherwise
// inert (never visited by resolveBinding/selectModel, which only iterate
// or accept keys present in rolePolicies).
const (
	roleCode  = "code"
	roleStudy = "study"
	roleEmbed = "embed"
	// roleLearn is NOT a models.<role> binding — there is no models.learn
	// config surface, and it is deliberately absent from rolePolicies below.
	// The Learn subagent (internal/tools.Learn, docs/learning-loop.md)
	// always runs on the study role's binding, exactly like Study itself.
	// This constant exists only so subagentRequest can recognize Learn
	// alongside roleStudy and skip the coder-inherit override (a background
	// pass has no live coder request to inherit — see its call site's
	// comment), and so subagentBounds can resolve subagents.learn bounds
	// independently of subagents.study. Mirrors tools.Learn.Role's literal
	// "learn" string the same way roleStudy mirrors tools.FunctionStudy.
	roleLearn = "learn"
	// roleLearnUser is LearnUser's role constant (docs/cross-source-learning.md
	// piece 1's promotion half, cmd/cortex/learn_user.go) — the machine-wide
	// cross-project promotion pass, mirroring roleLearn exactly: not a
	// models.<role> binding (LearnUser also runs on the study role's
	// binding, no coder session to inherit from — a scheduled loop firing
	// again), just a tag subagentRequest/subagentBounds special-case so
	// subagents.learn_user resolves independently of subagents.learn.
	roleLearnUser = "learn_user"
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
	roleCode:  {tag: "coder", effort: llm.Effort{Level: llm.EffortOn}},
	roleStudy: {tag: "reasoner", preferSwapFree: true, effort: llm.Effort{Level: llm.EffortOn}},
	roleEmbed: {tag: "embedder"},
}

// warnUnknownRoles prints a single stderr warning naming any models.<role>
// keys in cfg that the live config surface no longer reads (E1's role
// collapse — see the roleCode/roleStudy/roleEmbed doc comment above).
// Back-compat: this never errors, and cfg itself is left untouched — an
// unknown role simply sits inert in cfg.Models, unreachable from
// resolveBinding/selectModel (both keyed off rolePolicies). source names
// the config file the warning came from, for an operator with both a user
// and a project config to know which one to edit.
func warnUnknownRoles(cfg *Config, source string) {
	if cfg == nil || len(cfg.Models) == 0 {
		return
	}
	var unknown []string
	for role := range cfg.Models {
		if !knownRole(role) {
			unknown = append(unknown, role)
		}
	}
	if len(unknown) == 0 {
		return
	}
	sort.Strings(unknown)
	fmt.Fprintf(os.Stderr, "cortex: %s: ignoring unknown config role(s) %s; live roles are code, study (embed reserved)\n",
		source, strings.Join(unknown, ", "))
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

	// RequestTimeoutSec / MaxSendAttempts / RetryBackoffMs are the P1 timeout-
	// unification config surface (docs/configuration.md): per-role overrides
	// for the transport knobs that used to be hardcoded (cmd/cortex's
	// requestTimeout/maxSendAttempts/retryBackoff, pkg/llm's
	// defaultCompatTimeoutSec). 0 means "not set" — same unset-sentinel
	// convention as MaxTokens/Window above, so an absent or explicit-0 key
	// both fall through to the next tier (env, then the historical default).
	// The coder's live request stamps these from the code role; every
	// subagent request stamps them from the study role, EXCEPT the agent
	// profile, which inherits the coder's live values (see
	// study.go's subagentRequest) — it runs on the coder's model, so it
	// should run on the coder's transport budget too.
	RequestTimeoutSec int `json:"request_timeout_sec,omitempty"`
	MaxSendAttempts   int `json:"max_send_attempts,omitempty"`
	RetryBackoffMs    int `json:"retry_backoff_ms,omitempty"`
}

// timeout resolves this spec's per-request HTTP timeout: an explicit
// request_timeout_sec wins, then CORTEX_COMPAT_TIMEOUT_SEC (the existing env
// knob pkg/llm's compat client already honored — P1 unifies cmd/cortex's
// transport path onto the same env override), then def (the caller's
// path-specific historical default). See resolveTimeout below — this method
// is the single place that precedence is encoded, called from every
// timeout-consuming call site so they can't drift apart.
func (s ModelSpec) timeout(def time.Duration) time.Duration {
	if s.RequestTimeoutSec > 0 {
		return time.Duration(s.RequestTimeoutSec) * time.Second
	}
	if v := strings.TrimSpace(os.Getenv("CORTEX_COMPAT_TIMEOUT_SEC")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}

func (s ModelSpec) maxAttempts(def int) int {
	if s.MaxSendAttempts > 0 {
		return s.MaxSendAttempts
	}
	return def
}

func (s ModelSpec) backoff(def time.Duration) time.Duration {
	if s.RetryBackoffMs > 0 {
		return time.Duration(s.RetryBackoffMs) * time.Millisecond
	}
	return def
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

// defaultFleetDiscoveryTimeout is the immutable historical default —
// referenced ONLY by Config.fleetDiscoveryTimeout()'s fallback, never
// reassigned, so that resolver keeps returning the true zero-config value
// even after fleetDiscoveryTimeout (below) has been overridden by an
// earlier session in the same process (tests construct many sessions/
// configs per binary; a resolver that fell back to the mutable var would
// leak one test's override into the next test's "zero config" expectation).
const defaultFleetDiscoveryTimeout = 4 * time.Second

// fleetDiscoveryTimeout is the LIVE value discoverFleet's internal
// context.WithTimeout uses. NewCortexSession and runModelCLI set it once
// from network.fleet_discovery_timeout_sec (via the resolver above); unset,
// it stays defaultFleetDiscoveryTimeout.
var fleetDiscoveryTimeout = defaultFleetDiscoveryTimeout

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

	// Subagents overrides the Study/Agent subagent profiles' bounds
	// (internal/tools.Study / .Agent). A top-level section rather than
	// models.study.subagent (the shape the original audit sketched) — see
	// the docs/configuration.md naming-honesty note: the Agent profile runs
	// on the CODER's live model, not the study role's, so nesting it under
	// "models.study" would misrepresent which binding it actually uses.
	Subagents SubagentsConfig `json:"subagents"`

	// Limits, Network, Serve, Repl, and Discord are the remaining slices of
	// the full-configurability surface (docs/configuration.md): every field
	// is optional and 0/nil preserves today's hardcoded value.
	Limits  LimitsConfig  `json:"limits"`
	Network NetworkConfig `json:"network"`
	Serve   ServeConfig   `json:"serve"`
	Repl    ReplConfig    `json:"repl"`
	Discord DiscordConfig `json:"discord"`

	// Prompt customizes the system prompt (docs/configuration.md's
	// `prompt.*` section): File replaces the built-in base prompt
	// (unreadable/empty degrades to the built-in with a stderr warning —
	// see prompt.go's resolvePrompt), Append rides after the base and
	// before any AGENTS.md section.
	Prompt PromptConfig `json:"prompt"`

	// Context configures the two-zone working-set fractions
	// (docs/context-architecture.md's budgets: tail high/low watermark and
	// outline cap, expressed as fractions of the window W). Derek's decision
	// (option 2, 2026-07-19): ONE validated config group — the 0.8
	// compact-trigger threshold (compactThreshold, main.go) stays hardcoded,
	// not part of this surface. See ContextConfig's doc comment for the
	// bit-identical-defaults subtlety and validateContextConfig for the
	// enforced invariants.
	Context ContextConfig `json:"context"`

	// Skills configures Agent Skills discovery (agentskills.io) — coder-only
	// playbooks discovered under .cortex/skills et al and indexed at turn
	// start alongside the memory index (docs/configuration.md's `skills.*`
	// section; see internal/skills and cmd/cortex/skills.go).
	Skills SkillsConfig `json:"skills"`
}

// SkillsConfig collects Agent Skills discovery tunables
// (docs/configuration.md's `skills.*` section).
type SkillsConfig struct {
	// Enabled gates discovery and index injection entirely. Nil means
	// enabled — an availability kill-switch (like tools.enable_web), not
	// consent.
	Enabled *bool `json:"enabled"`
	// IndexMax caps how many discovered skills the turn-start index lists;
	// 0/unset falls back to skillsIndexMaxDefault (20).
	IndexMax int `json:"index_max"`
	// Dirs overrides the four default discovery roots (./.cortex/skills,
	// ./.claude/skills, ./.agents/skills, ~/.cortex/skills) ENTIRELY when
	// non-empty — it does not merge with them. Earlier entries win on a name
	// collision (internal/skills.Discover's precedence rule).
	Dirs []string `json:"dirs"`
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

	// EnableEffortEscalation gates the stuck-guard's one-shot reasoning-effort
	// escalation (docs/thinking-models.md §5c): nil/false means disabled — an
	// opt-in engine behavior change (per the doc: "opt-in"), unlike the
	// nil-means-enabled Enable* fields above, which gate already-shipped
	// tools rather than a new default-off behavior.
	EnableEffortEscalation *bool `json:"enable_effort_escalation"`

	// CurationBudgetTokens / MaxToolOutput / OutlineDefaultBudget override
	// internal/tools' same-named constants (0 = today's value).
	CurationBudgetTokens int `json:"curation_budget_tokens"`
	MaxToolOutput        int `json:"max_tool_output"`
	OutlineDefaultBudget int `json:"outline_default_budget"`

	Read      ReadConfig      `json:"read"`
	Grep      GrepConfig      `json:"grep"`
	FetchURL  FetchURLConfig  `json:"fetch_url"`
	WebSearch WebSearchConfig `json:"web_search"`
}

// ReadConfig overrides read_file's window/ceiling constants
// (internal/tools.DefaultRangeLines/MaxRangeLines/maxReadBytes).
type ReadConfig struct {
	DefaultRangeLines int `json:"default_range_lines"`
	MaxRangeLines     int `json:"max_range_lines"`
	MaxReadBytes      int `json:"max_read_bytes"`
}

// GrepConfig overrides grep's caps (internal/tools grepMaxHits/grepLineCap/
// grepMaxOutputBytes).
type GrepConfig struct {
	MaxHits        int `json:"max_hits"`
	LineCap        int `json:"line_cap"`
	MaxOutputBytes int `json:"max_output_bytes"`
}

// FetchURLConfig overrides fetch_url's timeout/redirect/body caps
// (internal/tools fetchTimeout/fetchMaxRedirects/fetchMaxBodyBytes).
type FetchURLConfig struct {
	TimeoutSec   int `json:"timeout_sec"`
	MaxRedirects int `json:"max_redirects"`
	MaxBodyBytes int `json:"max_body_bytes"`
}

// WebSearchConfig overrides web_search's result-count defaults
// (internal/tools defaultSearchMax/maximumSearchMax).
type WebSearchConfig struct {
	DefaultMaxResults int `json:"default_max_results"`
	MaximumMaxResults int `json:"maximum_max_results"`
}

// SubagentProfileConfig overrides one subagent profile's agent.Bounds
// fields (internal/tools.Study.Bounds / .Agent.Bounds).
type SubagentProfileConfig struct {
	MaxTokens       int `json:"max_tokens"`
	MaxIter         int `json:"max_iter"`
	ReadBudgetBytes int `json:"read_budget_bytes"`
}

// SubagentsConfig is the top-level "subagents" section (see the Config.Subagents
// doc comment for the naming deviation from the audited models.study.subagent
// nesting).
type SubagentsConfig struct {
	// SeedBudgetTokens overrides internal/tools.StudySeedBudget — the outline
	// budget used to seed EITHER subagent profile before its loop starts
	// (runSubagent calls deps.Outline(path, StudySeedBudget) once, shared by
	// Study and Agent). It lives here, not duplicated under .Study/.Agent,
	// because the underlying constant is genuinely shared, not per-profile.
	SeedBudgetTokens int                   `json:"seed_budget_tokens"`
	Study            SubagentProfileConfig `json:"study"`
	Agent            SubagentProfileConfig `json:"agent"`
	// Learn overrides the background learning-loop subagent's bounds
	// (internal/tools.Learn.Bounds) — docs/learning-loop.md. A separate
	// section from .Study even though Learn runs on the study role's model
	// binding: the two profiles' bounds are independently tunable (Learn's
	// budget is about background cost, Study's about foreground latency).
	Learn SubagentProfileConfig `json:"learn"`
	// LearnUser overrides the cross-project promotion subagent's bounds
	// (internal/tools.LearnUser.Bounds) — docs/cross-source-learning.md
	// piece 1's promotion half. A separate section from .Learn even though
	// both run on the study role's binding: LearnUser's per-call seed is one
	// candidate group (small), not a whole capture window, so its bounds are
	// independently tunable from the project-scope pass.
	LearnUser SubagentProfileConfig `json:"learn_user"`
}

// LimitsConfig collects the assorted byte/count ceilings that don't belong to
// a specific tool or subsystem (docs/configuration.md's `limits.*` section).
type LimitsConfig struct {
	MaxToolIterations   int `json:"max_tool_iterations"`
	MaxInstructionBytes int `json:"max_instruction_bytes"`
	MemoryIndexCapChars int `json:"memory_index_cap_chars"`
	// UserMemoryIndexCapChars caps the user-tier memory index injected ahead
	// of the project-tier one (docs/cross-source-learning.md piece 1) —
	// independently of MemoryIndexCapChars. Default 1500, deliberately
	// smaller than the project cap: the user tier is meant to stay few
	// (promotion only fires on cross-project recurrence), so a small cap also
	// doubles as a soft precision signal — an index that keeps hitting it
	// means promotion has gotten too permissive.
	UserMemoryIndexCapChars int `json:"user_memory_index_cap_chars"`
	CaptureExcerptCapChars  int `json:"capture_excerpt_cap_chars"`
	MaxTaskContextChars     int `json:"max_task_context_chars"`
	RouteMaxOutputTokens    int `json:"route_max_output_tokens"`
	MaxServedModelsShown    int `json:"max_served_models_shown"`
}

// NetworkConfig collects bounded-probe timeouts (docs/configuration.md's
// `network.*` section).
type NetworkConfig struct {
	FleetDiscoveryTimeoutSec int `json:"fleet_discovery_timeout_sec"`
	PreflightTimeoutSec      int `json:"preflight_timeout_sec"`
	// SelfHeal gates model self-healing (docs/model-self-healing.md):
	// mid-session fallback onto the curated :free suite when a model call
	// fails with a healable class, plus the startup preflight's willingness
	// to substitute a PINNED model missing from the live catalog. Default
	// true; set false to restore the strict "never touch my pin" posture.
	SelfHeal *bool `json:"self_heal"`
	// OllamaProbeTimeoutSec is parsed, merged, and validated like every
	// other field here, but has NO resolver method: the probe it would
	// configure (bootstrap_wire.go's ollamaLocalProbe) only ever runs
	// during GuidedSetup on a true first run — no config file can exist yet
	// at that point by construction (docs/configuration.md's "Interactive
	// first-run setup") — so there is no reachable call site to read this
	// field from. Kept in the schema for documentation parity with the rest
	// of `network.*`; see bootstrap_wire.go's defaultOllamaProbeTimeout doc
	// comment for the full explanation.
	OllamaProbeTimeoutSec int `json:"ollama_probe_timeout_sec"`
	// CompatTimeoutSec is a config surface for the existing
	// CORTEX_COMPAT_TIMEOUT_SEC env var's fallback default (pkg/llm's
	// defaultCompatTimeoutSec): unlike every other timeout field in this
	// audit, ENV WINS OVER CONFIG here, matching the env knob's
	// subprocess-boundary rationale (docs/configuration.md) — this field only
	// adjusts the baseline compatTimeout() falls back to when the env var is
	// unset AND no per-call EndpointConfig.Timeout override applies.
	CompatTimeoutSec int `json:"compat_timeout_sec"`
}

// ServeConfig collects `cortex serve` tunables (docs/configuration.md's
// `serve.*` section).
type ServeConfig struct {
	Port                   int `json:"port"`
	SessionIdleTimeoutMin  int `json:"session_idle_timeout_min"`
	LoopCadenceFloorMin    int `json:"loop_cadence_floor_min"`
	LoopAutoDisableStrikes int `json:"loop_auto_disable_strikes"`
	ProjectSessionsLimit   int `json:"project_sessions_limit"`
}

// ReplConfig collects the interactive REPL's display tunables
// (docs/configuration.md's `repl.*` section).
type ReplConfig struct {
	TickerIntervalMs int `json:"ticker_interval_ms"`
	// Gauge selects the prompt-row/context-report gauge style
	// (contextbar.go's resolveGaugeStyle): "braille" (default, unset also
	// means this), "ascii", or "numeric" (the pre-bar "used/window" text).
	Gauge string `json:"gauge"`
}

// PromptConfig customizes the system prompt (docs/configuration.md's
// `prompt.*` section); consumed by prompt.go's resolvePrompt.
type PromptConfig struct {
	File   string `json:"file"`
	Append string `json:"append"`
}

// DiscordConfig collects the Discord adapter's tunables
// (docs/configuration.md's `discord.*` section). RouteConfidenceThreshold is
// a pointer (like Temperature) so an explicit 0 is distinguishable from
// unset — 0 would otherwise be a legitimate (if unusual) "always continue"
// setting, unlike the other fields here where 0 unambiguously means "unset".
type DiscordConfig struct {
	TypingRefreshSec         int      `json:"typing_refresh_sec"`
	RiskApprovalTimeoutSec   int      `json:"risk_approval_timeout_sec"`
	ProgressEditIntervalMs   int      `json:"progress_edit_interval_ms"`
	RouteConfidenceThreshold *float64 `json:"route_confidence_threshold"`
}

// ContextConfig configures the two-zone working set's three fractions of the
// window W (docs/context-architecture.md): the tail's demote (high) and
// drain (low) watermarks, and the outline's fold cap. Fields are pointers —
// like ModelSpec.Temperature / DiscordConfig.RouteConfidenceThreshold — so an
// explicit-but-invalid 0.0 is distinguishable from "not set" (a plain float64
// zero value could never be rejected as out-of-range by validateContextConfig,
// since it would be indistinguishable from an absent field).
//
// BIT-IDENTICAL-DEFAULTS SUBTLETY: the fractions this section's defaults
// document (0.5, 1/3, 0.125) are NOT what the resolvers below compute when a
// field is unset. today's hardcoded watermarks are the INTEGER-DIVISION
// expressions windowSize()/2, windowSize()/3, windowSize()/8 — and integer
// division is not the same function as float multiplication (e.g. W=10:
// W/3=3 but int(10 * (1.0/3.0))=3 too, yet W=8: W/3=2 while
// int(8*(1.0/3.0))=2.666→2 — these happen to agree often but are not
// guaranteed to for every W, and drift is exactly what "bit-identical" rules
// out). So an unset field must fall back to the ORIGINAL integer-division
// expression verbatim, not to `windowSize() * defaultFraction` — see
// tailHighWatermark/tailDrainWatermark/outlineBudget below, which do exactly
// that. Float math only ever runs when a field is explicitly configured.
type ContextConfig struct {
	TailHighFraction  *float64 `json:"tail_high_fraction"`
	TailDrainFraction *float64 `json:"tail_drain_fraction"`
	OutlineFraction   *float64 `json:"outline_fraction"`
}

// defaultTailHighFraction / defaultTailDrainFraction / defaultOutlineFraction
// document, as floats, what today's hardcoded integer-division watermarks
// (W/2, W/3, W/8) are EQUIVALENT to. Used ONLY by validateContextConfig to
// evaluate the hysteresis/dormancy inequalities against an unconfigured
// field's effective fraction — so setting only ONE of the three fields still
// gets checked against the other two's real (default) behavior, not skipped.
// The runtime resolvers do NOT use these consts; see ContextConfig's doc
// comment for why.
const (
	defaultTailHighFraction  = 0.5
	defaultTailDrainFraction = 1.0 / 3.0
	defaultOutlineFraction   = 0.125
)

// contextPrefixHeadroom is the conservative fixed-fraction allowance the
// dormancy inequality (validateContextConfig) reserves for zone A's other two
// pieces — the system prompt (+ AGENTS.md, capped by maxInstructionBytes) and
// the memory index (capped by memoryIndexCap) — neither of which is one of
// the three configurable fractions. Both caps are byte/char ceilings, not
// fractions of W, so expressing them as "a fraction of W" requires picking a
// reference window; erring conservative means picking the SMALLEST window
// these caps would plausibly still run against (a smaller window makes the
// same absolute overhead a LARGER fraction of it), which is fallbackWindow
// (32768 — the zero-fleet-info default, main.go's config.go const):
//
//	(maxInstructionBytes + memoryIndexCap) / CharsPerToken / fallbackWindow
//	= (16384 + 4000) / 4 / 32768 ≈ 0.1555, rounded UP to 0.16 for margin.
//
// docs/context-architecture.md's own reasoning is that today's defaults
// (W/2 + W/8 = 0.625) leave the 0.8 compact trigger unreachable given the
// remaining zones — this const makes that implicit slack an explicit,
// checkable number: 0.5 + 0.125 + 0.16 = 0.785 <= 0.8, a deliberately thin
// (0.015) but real margin.
const contextPrefixHeadroom = 0.16

// validateContextConfig rejects a nonsensical context.* fraction group.
// Called by validateConfig ONLY when at least one field is explicitly set
// (matching this file's existing only-validate-what-was-set convention) —
// an absent "context" section is untouched. Unset individual fields resolve
// to their documented default fraction for the two cross-field inequalities
// below, so a config that sets only tail_high_fraction still gets checked
// against the DEFAULT drain and outline fractions, not skipped.
func validateContextConfig(c ContextConfig) error {
	if c.TailHighFraction == nil && c.TailDrainFraction == nil && c.OutlineFraction == nil {
		return nil
	}
	high := defaultTailHighFraction
	if c.TailHighFraction != nil {
		high = *c.TailHighFraction
		if high <= 0 || high >= 1 {
			return fmt.Errorf("context.tail_high_fraction must be in (0, 1), got %v", high)
		}
	}
	drain := defaultTailDrainFraction
	if c.TailDrainFraction != nil {
		drain = *c.TailDrainFraction
		if drain <= 0 || drain >= 1 {
			return fmt.Errorf("context.tail_drain_fraction must be in (0, 1), got %v", drain)
		}
	}
	outline := defaultOutlineFraction
	if c.OutlineFraction != nil {
		outline = *c.OutlineFraction
		if outline <= 0 || outline >= 1 {
			return fmt.Errorf("context.outline_fraction must be in (0, 1), got %v", outline)
		}
	}
	if high <= drain {
		return fmt.Errorf("context: tail_high_fraction (%.4f) must be greater than tail_drain_fraction (%.4f) so demotion has hysteresis — the tail must be allowed to grow above the drain target before demoting fires, or every turn re-triggers demotion", high, drain)
	}
	if sum := high + outline + contextPrefixHeadroom; sum > compactThreshold {
		return fmt.Errorf("context: tail_high_fraction (%.4f) + outline_fraction (%.4f) + prefix_headroom (%.4f, system prompt + memory index slack) = %.4f exceeds the compact trigger (%.4f) — normal demotion could make the 80%% compact threshold reachable; lower tail_high_fraction and/or outline_fraction", high, outline, contextPrefixHeadroom, sum, compactThreshold)
	}
	return nil
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
	if spec.Model == "" && c.isOpenRouter() && (role == roleCode || role == roleStudy) {
		// E2: curated is the PRIMARY default — an OpenRouter backend with no
		// models.<role> entry at all (the true zero-config case: just
		// "backend": {"type": "openrouter", "key_env": "..."}) gets the
		// curated table's top pick rather than an empty model id, which
		// OpenRouter would otherwise reject outright. This is also what
		// makes the startup preflight (preflight.go) reachable for a
		// zero-config user: it only acts on a binding isCuratedModel
		// recognizes.
		pick := curatedTopPick()
		spec.Model = pick.ID
		if spec.Window == 0 {
			spec.Window = pick.Window
		}
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

// maxInstructionBytes is the historical AGENTS.md truncation default.
// instructionBytesCap is the LIVE value readInstructions actually uses — a
// package var (not this const directly) so NewCortexSession can set it once
// from limits.max_instruction_bytes before the first AGENTS.md read
// (CortexArgs.Request(), which runs before the rest of session construction
// — see NewCortexSession's ordering comment). Unset (every test, every
// caller that predates config.limits), it equals maxInstructionBytes —
// today's behavior exactly.
const maxInstructionBytes = 16384

var instructionBytesCap = maxInstructionBytes

func projectInstructions() string {
	path := findUp("AGENTS.md")
	if path == "" {
		return ""
	}
	return readInstructions(path)
}

// readInstructions reads and trims an AGENTS.md at an exact path (no
// upward search), truncating at instructionBytesCap — the shared body
// projectInstructions() (CWD-implicit, via findUp) and
// Workspace.Instructions() (explicit root, workspace.go) both use, so the
// two stay provably identical for the same resolved path.
func readInstructions(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > instructionBytesCap {
		s = s[:instructionBytesCap] + "\n...[AGENTS.md truncated]"
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
	warnUnknownRoles(&cfg, path)
	if err := validateConfig(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "cortex: %s: invalid config: %v\n", path, err)
		os.Exit(1)
	}
	return &cfg
}

// validateConfig rejects nonsensical values in the full-configurability
// surface (docs/configuration.md) — but ONLY fields the config file actually
// set: every field here uses 0/nil as its "not set" sentinel (matching the
// pre-existing MaxTokens/Window convention), so a config that never mentions
// a field can never trip this. A negative int is always nonsensical (every
// field in this surface is a byte count, token count, duration, or attempt
// count); route_confidence_threshold is checked against its own domain,
// (0, 1]. readConfigFile calls this and exits fatally with the returned
// error — kept a separate testable function (rather than folded into
// readConfigFile) so config_test.go can exercise the rejection paths without
// killing the test binary.
func validateConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if err := validateContextConfig(cfg.Context); err != nil {
		return err
	}
	for role, m := range cfg.Models {
		if err := nonNegative("models."+role+".request_timeout_sec", m.RequestTimeoutSec); err != nil {
			return err
		}
		if err := nonNegative("models."+role+".max_send_attempts", m.MaxSendAttempts); err != nil {
			return err
		}
		if err := nonNegative("models."+role+".retry_backoff_ms", m.RetryBackoffMs); err != nil {
			return err
		}
	}
	intFields := map[string]int{
		"tools.curation_budget_tokens":         cfg.Tools.CurationBudgetTokens,
		"tools.max_tool_output":                cfg.Tools.MaxToolOutput,
		"tools.outline_default_budget":         cfg.Tools.OutlineDefaultBudget,
		"tools.read.default_range_lines":       cfg.Tools.Read.DefaultRangeLines,
		"tools.read.max_range_lines":           cfg.Tools.Read.MaxRangeLines,
		"tools.read.max_read_bytes":            cfg.Tools.Read.MaxReadBytes,
		"tools.grep.max_hits":                  cfg.Tools.Grep.MaxHits,
		"tools.grep.line_cap":                  cfg.Tools.Grep.LineCap,
		"tools.grep.max_output_bytes":          cfg.Tools.Grep.MaxOutputBytes,
		"tools.fetch_url.timeout_sec":          cfg.Tools.FetchURL.TimeoutSec,
		"tools.fetch_url.max_redirects":        cfg.Tools.FetchURL.MaxRedirects,
		"tools.fetch_url.max_body_bytes":       cfg.Tools.FetchURL.MaxBodyBytes,
		"tools.web_search.default_max_results": cfg.Tools.WebSearch.DefaultMaxResults,
		"tools.web_search.maximum_max_results": cfg.Tools.WebSearch.MaximumMaxResults,

		"subagents.seed_budget_tokens":      cfg.Subagents.SeedBudgetTokens,
		"subagents.study.max_tokens":        cfg.Subagents.Study.MaxTokens,
		"subagents.study.max_iter":          cfg.Subagents.Study.MaxIter,
		"subagents.study.read_budget_bytes": cfg.Subagents.Study.ReadBudgetBytes,
		"subagents.agent.max_tokens":        cfg.Subagents.Agent.MaxTokens,
		"subagents.agent.max_iter":          cfg.Subagents.Agent.MaxIter,
		"subagents.agent.read_budget_bytes": cfg.Subagents.Agent.ReadBudgetBytes,
		"subagents.learn.max_tokens":        cfg.Subagents.Learn.MaxTokens,
		"subagents.learn.max_iter":          cfg.Subagents.Learn.MaxIter,
		"subagents.learn.read_budget_bytes": cfg.Subagents.Learn.ReadBudgetBytes,

		"limits.max_tool_iterations":         cfg.Limits.MaxToolIterations,
		"limits.max_instruction_bytes":       cfg.Limits.MaxInstructionBytes,
		"limits.memory_index_cap_chars":      cfg.Limits.MemoryIndexCapChars,
		"limits.user_memory_index_cap_chars": cfg.Limits.UserMemoryIndexCapChars,
		"limits.capture_excerpt_cap_chars":   cfg.Limits.CaptureExcerptCapChars,
		"limits.max_task_context_chars":      cfg.Limits.MaxTaskContextChars,
		"limits.route_max_output_tokens":     cfg.Limits.RouteMaxOutputTokens,
		"limits.max_served_models_shown":     cfg.Limits.MaxServedModelsShown,

		"network.fleet_discovery_timeout_sec": cfg.Network.FleetDiscoveryTimeoutSec,
		"network.preflight_timeout_sec":       cfg.Network.PreflightTimeoutSec,
		"network.ollama_probe_timeout_sec":    cfg.Network.OllamaProbeTimeoutSec,
		"network.compat_timeout_sec":          cfg.Network.CompatTimeoutSec,

		"serve.port":                      cfg.Serve.Port,
		"serve.session_idle_timeout_min":  cfg.Serve.SessionIdleTimeoutMin,
		"serve.loop_cadence_floor_min":    cfg.Serve.LoopCadenceFloorMin,
		"serve.loop_auto_disable_strikes": cfg.Serve.LoopAutoDisableStrikes,
		"serve.project_sessions_limit":    cfg.Serve.ProjectSessionsLimit,

		"repl.ticker_interval_ms": cfg.Repl.TickerIntervalMs,

		"discord.typing_refresh_sec":        cfg.Discord.TypingRefreshSec,
		"discord.risk_approval_timeout_sec": cfg.Discord.RiskApprovalTimeoutSec,
		"discord.progress_edit_interval_ms": cfg.Discord.ProgressEditIntervalMs,

		"skills.index_max": cfg.Skills.IndexMax,
	}
	// Deterministic order so a config with multiple bad fields always reports
	// the same one first (map iteration order is randomized in Go).
	keys := make([]string, 0, len(intFields))
	for k := range intFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := nonNegative(k, intFields[k]); err != nil {
			return err
		}
	}
	if t := cfg.Discord.RouteConfidenceThreshold; t != nil && (*t <= 0 || *t > 1) {
		return fmt.Errorf("discord.route_confidence_threshold must be in (0, 1], got %v", *t)
	}
	switch cfg.Repl.Gauge {
	case "", "braille", "ascii", "numeric":
	default:
		return fmt.Errorf("repl.gauge must be one of \"braille\", \"ascii\", \"numeric\", got %q", cfg.Repl.Gauge)
	}
	return nil
}

// nonNegative rejects an explicit negative value for a field whose only
// sentinel is 0 ("not set") — every field validateConfig checks is a count,
// byte size, or millisecond/second duration, none of which can be negative.
func nonNegative(field string, v int) error {
	if v < 0 {
		return fmt.Errorf("%s must be positive, got %d", field, v)
	}
	return nil
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
	out.Subagents = mergeSubagents(base.Subagents, over.Subagents)
	out.Limits = mergeLimits(base.Limits, over.Limits)
	out.Network = mergeNetwork(base.Network, over.Network)
	out.Serve = mergeServe(base.Serve, over.Serve)
	out.Repl = mergeRepl(base.Repl, over.Repl)
	out.Discord = mergeDiscord(base.Discord, over.Discord)
	out.Context = mergeContext(base.Context, over.Context)
	out.Prompt = mergePrompt(base.Prompt, over.Prompt)
	out.Skills = mergeSkills(base.Skills, over.Skills)
	return &out
}

// mergeIntField overrides base with over when over is non-zero — the shared
// field-by-field rule every new int config field in this audit follows (0 =
// "not set in this file", matching the pre-existing MaxTokens/Window
// convention on ModelSpec).
func mergeIntField(base, over int) int {
	if over != 0 {
		return over
	}
	return base
}

func mergeSubagentProfile(base, over SubagentProfileConfig) SubagentProfileConfig {
	return SubagentProfileConfig{
		MaxTokens:       mergeIntField(base.MaxTokens, over.MaxTokens),
		MaxIter:         mergeIntField(base.MaxIter, over.MaxIter),
		ReadBudgetBytes: mergeIntField(base.ReadBudgetBytes, over.ReadBudgetBytes),
	}
}

func mergeSubagents(base, over SubagentsConfig) SubagentsConfig {
	return SubagentsConfig{
		SeedBudgetTokens: mergeIntField(base.SeedBudgetTokens, over.SeedBudgetTokens),
		Study:            mergeSubagentProfile(base.Study, over.Study),
		Agent:            mergeSubagentProfile(base.Agent, over.Agent),
		Learn:            mergeSubagentProfile(base.Learn, over.Learn),
		LearnUser:        mergeSubagentProfile(base.LearnUser, over.LearnUser),
	}
}

func mergeLimits(base, over LimitsConfig) LimitsConfig {
	return LimitsConfig{
		MaxToolIterations:       mergeIntField(base.MaxToolIterations, over.MaxToolIterations),
		MaxInstructionBytes:     mergeIntField(base.MaxInstructionBytes, over.MaxInstructionBytes),
		MemoryIndexCapChars:     mergeIntField(base.MemoryIndexCapChars, over.MemoryIndexCapChars),
		UserMemoryIndexCapChars: mergeIntField(base.UserMemoryIndexCapChars, over.UserMemoryIndexCapChars),
		CaptureExcerptCapChars:  mergeIntField(base.CaptureExcerptCapChars, over.CaptureExcerptCapChars),
		MaxTaskContextChars:     mergeIntField(base.MaxTaskContextChars, over.MaxTaskContextChars),
		RouteMaxOutputTokens:    mergeIntField(base.RouteMaxOutputTokens, over.RouteMaxOutputTokens),
		MaxServedModelsShown:    mergeIntField(base.MaxServedModelsShown, over.MaxServedModelsShown),
	}
}

func mergeNetwork(base, over NetworkConfig) NetworkConfig {
	return NetworkConfig{
		FleetDiscoveryTimeoutSec: mergeIntField(base.FleetDiscoveryTimeoutSec, over.FleetDiscoveryTimeoutSec),
		PreflightTimeoutSec:      mergeIntField(base.PreflightTimeoutSec, over.PreflightTimeoutSec),
		OllamaProbeTimeoutSec:    mergeIntField(base.OllamaProbeTimeoutSec, over.OllamaProbeTimeoutSec),
		CompatTimeoutSec:         mergeIntField(base.CompatTimeoutSec, over.CompatTimeoutSec),
		SelfHeal:                 mergeBoolPtr(base.SelfHeal, over.SelfHeal),
	}
}

// mergeBoolPtr is field-by-field override for optional bools: an explicit
// project-level value (non-nil) wins, else the user-level value survives.
func mergeBoolPtr(base, over *bool) *bool {
	if over != nil {
		return over
	}
	return base
}

// selfHealEnabled reports whether model self-healing is on
// (docs/model-self-healing.md) — default true when the config or the field
// is absent, mirroring deleteEnabled's posture.
func (c *Config) selfHealEnabled() bool {
	if c == nil || c.Network.SelfHeal == nil {
		return true
	}
	return *c.Network.SelfHeal
}

func mergeServe(base, over ServeConfig) ServeConfig {
	return ServeConfig{
		Port:                   mergeIntField(base.Port, over.Port),
		SessionIdleTimeoutMin:  mergeIntField(base.SessionIdleTimeoutMin, over.SessionIdleTimeoutMin),
		LoopCadenceFloorMin:    mergeIntField(base.LoopCadenceFloorMin, over.LoopCadenceFloorMin),
		LoopAutoDisableStrikes: mergeIntField(base.LoopAutoDisableStrikes, over.LoopAutoDisableStrikes),
		ProjectSessionsLimit:   mergeIntField(base.ProjectSessionsLimit, over.ProjectSessionsLimit),
	}
}

func mergeRepl(base, over ReplConfig) ReplConfig {
	gauge := base.Gauge
	if over.Gauge != "" {
		gauge = over.Gauge
	}
	return ReplConfig{
		TickerIntervalMs: mergeIntField(base.TickerIntervalMs, over.TickerIntervalMs),
		Gauge:            gauge,
	}
}

func mergeDiscord(base, over DiscordConfig) DiscordConfig {
	out := DiscordConfig{
		TypingRefreshSec:         mergeIntField(base.TypingRefreshSec, over.TypingRefreshSec),
		RiskApprovalTimeoutSec:   mergeIntField(base.RiskApprovalTimeoutSec, over.RiskApprovalTimeoutSec),
		ProgressEditIntervalMs:   mergeIntField(base.ProgressEditIntervalMs, over.ProgressEditIntervalMs),
		RouteConfidenceThreshold: base.RouteConfidenceThreshold,
	}
	if over.RouteConfidenceThreshold != nil {
		out.RouteConfidenceThreshold = over.RouteConfidenceThreshold
	}
	return out
}

// mergeContext threads a project-level override over the user-level default,
// field-by-field like mergeDiscord's RouteConfidenceThreshold (a pointer
// override — a project config that sets one fraction doesn't clobber the
// other two from the user config).
func mergeContext(base, over ContextConfig) ContextConfig {
	out := base
	if over.TailHighFraction != nil {
		out.TailHighFraction = over.TailHighFraction
	}
	if over.TailDrainFraction != nil {
		out.TailDrainFraction = over.TailDrainFraction
	}
	if over.OutlineFraction != nil {
		out.OutlineFraction = over.OutlineFraction
	}
	return out
}

// mergeSkills overrides field-by-field like every other section, EXCEPT
// Dirs: an explicit project-level `skills.dirs` replaces the user-level list
// wholesale rather than appending to it (matching SkillsConfig.Dirs's own
// doc comment — "entirely", not merged).
func mergeSkills(base, over SkillsConfig) SkillsConfig {
	dirs := base.Dirs
	if len(over.Dirs) > 0 {
		dirs = over.Dirs
	}
	return SkillsConfig{
		Enabled:  mergeBoolPtr(base.Enabled, over.Enabled),
		IndexMax: mergeIntField(base.IndexMax, over.IndexMax),
		Dirs:     dirs,
	}
}

func mergeRead(base, over ReadConfig) ReadConfig {
	return ReadConfig{
		DefaultRangeLines: mergeIntField(base.DefaultRangeLines, over.DefaultRangeLines),
		MaxRangeLines:     mergeIntField(base.MaxRangeLines, over.MaxRangeLines),
		MaxReadBytes:      mergeIntField(base.MaxReadBytes, over.MaxReadBytes),
	}
}

func mergeGrep(base, over GrepConfig) GrepConfig {
	return GrepConfig{
		MaxHits:        mergeIntField(base.MaxHits, over.MaxHits),
		LineCap:        mergeIntField(base.LineCap, over.LineCap),
		MaxOutputBytes: mergeIntField(base.MaxOutputBytes, over.MaxOutputBytes),
	}
}

func mergeFetchURL(base, over FetchURLConfig) FetchURLConfig {
	return FetchURLConfig{
		TimeoutSec:   mergeIntField(base.TimeoutSec, over.TimeoutSec),
		MaxRedirects: mergeIntField(base.MaxRedirects, over.MaxRedirects),
		MaxBodyBytes: mergeIntField(base.MaxBodyBytes, over.MaxBodyBytes),
	}
}

func mergeWebSearch(base, over WebSearchConfig) WebSearchConfig {
	return WebSearchConfig{
		DefaultMaxResults: mergeIntField(base.DefaultMaxResults, over.DefaultMaxResults),
		MaximumMaxResults: mergeIntField(base.MaximumMaxResults, over.MaximumMaxResults),
	}
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
	base.RequestTimeoutSec = mergeIntField(base.RequestTimeoutSec, over.RequestTimeoutSec)
	base.MaxSendAttempts = mergeIntField(base.MaxSendAttempts, over.MaxSendAttempts)
	base.RetryBackoffMs = mergeIntField(base.RetryBackoffMs, over.RetryBackoffMs)
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
	if over.EnableEffortEscalation != nil {
		base.EnableEffortEscalation = over.EnableEffortEscalation
	}
	base.CurationBudgetTokens = mergeIntField(base.CurationBudgetTokens, over.CurationBudgetTokens)
	base.MaxToolOutput = mergeIntField(base.MaxToolOutput, over.MaxToolOutput)
	base.OutlineDefaultBudget = mergeIntField(base.OutlineDefaultBudget, over.OutlineDefaultBudget)
	base.Read = mergeRead(base.Read, over.Read)
	base.Grep = mergeGrep(base.Grep, over.Grep)
	base.FetchURL = mergeFetchURL(base.FetchURL, over.FetchURL)
	base.WebSearch = mergeWebSearch(base.WebSearch, over.WebSearch)
	return base
}

// effortEscalationEnabled reports whether the stuck-guard's one-shot effort
// escalation is enabled (docs/thinking-models.md §5c) — opt-in, defaulting
// off when the config or the flag itself is absent.
func (c *Config) effortEscalationEnabled() bool {
	return c != nil && c.Tools.EnableEffortEscalation != nil && *c.Tools.EnableEffortEscalation
}

// --- resolver methods -----------------------------------------------------
//
// Every method below returns the config-set value when explicit (non-zero),
// else the parameter EXACTLY matching today's hardcoded constant — so a nil
// *Config or a config that never mentions the field reproduces the old
// behavior byte-for-byte. Grouped here rather than beside each call site so
// the "what does 0/nil resolve to" answer lives in one place per field.

func resolveInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func (c *Config) maxToolIterations() int {
	if c == nil {
		return maxToolIterations
	}
	return resolveInt(c.Limits.MaxToolIterations, maxToolIterations)
}

func (c *Config) instructionBytesCap() int {
	if c == nil {
		return maxInstructionBytes
	}
	return resolveInt(c.Limits.MaxInstructionBytes, maxInstructionBytes)
}

func (c *Config) memoryIndexCapChars() int {
	if c == nil {
		return memoryIndexCap
	}
	return resolveInt(c.Limits.MemoryIndexCapChars, memoryIndexCap)
}

// userMemoryIndexCapChars resolves the user-tier memory index's cap,
// independently of the project-tier one — docs/cross-source-learning.md
// piece 1, config field limits.user_memory_index_cap_chars.
func (c *Config) userMemoryIndexCapChars() int {
	if c == nil {
		return userMemoryIndexCap
	}
	return resolveInt(c.Limits.UserMemoryIndexCapChars, userMemoryIndexCap)
}

func (c *Config) captureExcerptCapChars() int {
	if c == nil {
		return captureExcerptCap
	}
	return resolveInt(c.Limits.CaptureExcerptCapChars, captureExcerptCap)
}

func (c *Config) maxTaskContextChars() int {
	if c == nil {
		return shellrisk.DefaultMaxTaskContextChars
	}
	return resolveInt(c.Limits.MaxTaskContextChars, shellrisk.DefaultMaxTaskContextChars)
}

func (c *Config) routeMaxOutputTokens() int {
	if c == nil {
		return routeMaxOutputTokens
	}
	return resolveInt(c.Limits.RouteMaxOutputTokens, routeMaxOutputTokens)
}

func (c *Config) maxServedModelsShown() int {
	if c == nil {
		return maxServedModelsShown
	}
	return resolveInt(c.Limits.MaxServedModelsShown, maxServedModelsShown)
}

func (c *Config) fleetDiscoveryTimeout() time.Duration {
	if c == nil || c.Network.FleetDiscoveryTimeoutSec <= 0 {
		return defaultFleetDiscoveryTimeout
	}
	return time.Duration(c.Network.FleetDiscoveryTimeoutSec) * time.Second
}

func (c *Config) preflightTimeout() time.Duration {
	if c == nil || c.Network.PreflightTimeoutSec <= 0 {
		return defaultOpenRouterPreflightTimeout
	}
	return time.Duration(c.Network.PreflightTimeoutSec) * time.Second
}

func (c *Config) servePort(def int) int {
	if c == nil {
		return def
	}
	return resolveInt(c.Serve.Port, def)
}

func (c *Config) sessionIdleTimeout() time.Duration {
	if c == nil || c.Serve.SessionIdleTimeoutMin <= 0 {
		return defaultSessionIdleTimeout
	}
	return time.Duration(c.Serve.SessionIdleTimeoutMin) * time.Minute
}

func (c *Config) loopCadenceFloorMin() int {
	if c == nil {
		return loops.DefaultCadenceFloorMinutes
	}
	return resolveInt(c.Serve.LoopCadenceFloorMin, loops.DefaultCadenceFloorMinutes)
}

func (c *Config) loopAutoDisableStrikes() int {
	if c == nil {
		return defaultLoopAutoDisableStrikes
	}
	return resolveInt(c.Serve.LoopAutoDisableStrikes, defaultLoopAutoDisableStrikes)
}

func (c *Config) projectSessionsLimit() int {
	if c == nil {
		return defaultProjectSessionsLimit
	}
	return resolveInt(c.Serve.ProjectSessionsLimit, defaultProjectSessionsLimit)
}

func (c *Config) tickerInterval() time.Duration {
	if c == nil || c.Repl.TickerIntervalMs <= 0 {
		return time.Second
	}
	return time.Duration(c.Repl.TickerIntervalMs) * time.Millisecond
}

func (c *Config) discordTypingRefresh() time.Duration {
	if c == nil || c.Discord.TypingRefreshSec <= 0 {
		return defaultTypingRefresh
	}
	return time.Duration(c.Discord.TypingRefreshSec) * time.Second
}

func (c *Config) discordRiskApprovalTimeout() time.Duration {
	if c == nil || c.Discord.RiskApprovalTimeoutSec <= 0 {
		return defaultRiskApprovalTimeout
	}
	return time.Duration(c.Discord.RiskApprovalTimeoutSec) * time.Second
}

func (c *Config) discordProgressEditInterval() time.Duration {
	if c == nil || c.Discord.ProgressEditIntervalMs <= 0 {
		return defaultProgressEditInterval
	}
	return time.Duration(c.Discord.ProgressEditIntervalMs) * time.Millisecond
}

func (c *Config) discordRouteConfidenceThreshold() float64 {
	if c == nil || c.Discord.RouteConfidenceThreshold == nil {
		return routeConfidenceThreshold
	}
	return *c.Discord.RouteConfidenceThreshold
}

// curationBudgetTokensCfg / maxToolOutputCfg / outlineDefaultBudgetCfg /
// readCfg / grepCfg / fetchURLCfg / webSearchCfg resolve the internal/tools
// caps (curation_budget_tokens, max_tool_output, outline_default_budget,
// read.*, grep.*, fetch_url.*, web_search.*). internal/tools exposes these
// as package vars (tools.CurationBudgetTokens etc. become tools.Limits, see
// tools/limits.go) defaulting to the historical constants; NewCortexSession
// calls tools.Configure(...) once at session construction with the resolved
// values so every tool call site reads the same override without a second
// config-plumbing path into internal/tools.
func (c *Config) toolLimits() tools.Limits {
	def := tools.DefaultLimits()
	if c == nil {
		return def
	}
	return tools.Limits{
		CurationBudgetTokens: resolveInt(c.Tools.CurationBudgetTokens, def.CurationBudgetTokens),
		MaxToolOutput:        resolveInt(c.Tools.MaxToolOutput, def.MaxToolOutput),
		OutlineDefaultBudget: resolveInt(c.Tools.OutlineDefaultBudget, def.OutlineDefaultBudget),
		DefaultRangeLines:    resolveInt(c.Tools.Read.DefaultRangeLines, def.DefaultRangeLines),
		MaxRangeLines:        resolveInt(c.Tools.Read.MaxRangeLines, def.MaxRangeLines),
		MaxReadBytes:         resolveInt(c.Tools.Read.MaxReadBytes, def.MaxReadBytes),
		GrepMaxHits:          resolveInt(c.Tools.Grep.MaxHits, def.GrepMaxHits),
		GrepLineCap:          resolveInt(c.Tools.Grep.LineCap, def.GrepLineCap),
		GrepMaxOutputBytes:   resolveInt(c.Tools.Grep.MaxOutputBytes, def.GrepMaxOutputBytes),
		FetchTimeoutSec:      resolveInt(c.Tools.FetchURL.TimeoutSec, def.FetchTimeoutSec),
		FetchMaxRedirects:    resolveInt(c.Tools.FetchURL.MaxRedirects, def.FetchMaxRedirects),
		FetchMaxBodyBytes:    resolveInt(c.Tools.FetchURL.MaxBodyBytes, def.FetchMaxBodyBytes),
		DefaultSearchMax:     resolveInt(c.Tools.WebSearch.DefaultMaxResults, def.DefaultSearchMax),
		MaximumSearchMax:     resolveInt(c.Tools.WebSearch.MaximumMaxResults, def.MaximumSearchMax),
	}
}

// subagentBounds resolves one profile's agent.Bounds overrides
// (subagents.study / subagents.agent), starting from the profile's own
// registered defaults so an unset field falls back to the profile's shipped
// value, not a generic zero.
func (c *Config) subagentBounds(role string, def agent.Bounds) agent.Bounds {
	if c == nil {
		return def
	}
	var pc SubagentProfileConfig
	switch role {
	case roleStudy:
		pc = c.Subagents.Study
	case roleLearn:
		pc = c.Subagents.Learn
	case roleLearnUser:
		pc = c.Subagents.LearnUser
	default:
		pc = c.Subagents.Agent
	}
	def.MaxTokens = resolveInt(pc.MaxTokens, def.MaxTokens)
	def.MaxIter = resolveInt(pc.MaxIter, def.MaxIter)
	def.ReadBudgetBytes = resolveInt(pc.ReadBudgetBytes, def.ReadBudgetBytes)
	return def
}

// tailHighWatermark / tailDrainWatermark / outlineBudget resolve the two-zone
// working set's three fractions (docs/context-architecture.md, docs/configuration.md's
// `context.*` section) against a resolved window w. Per ContextConfig's doc
// comment: an unset field falls back to the ORIGINAL integer-division
// expression (w/2, w/3, w/8) verbatim — NOT `int(float64(w) * defaultFraction)`
// — so omitted config reproduces today's watermarks bit-for-bit; only an
// EXPLICIT fraction switches to float math.
func (c *Config) tailHighWatermark(w int) int {
	if c == nil || c.Context.TailHighFraction == nil {
		return w / 2
	}
	return int(float64(w) * *c.Context.TailHighFraction)
}

func (c *Config) tailDrainWatermark(w int) int {
	if c == nil || c.Context.TailDrainFraction == nil {
		return w / 3
	}
	return int(float64(w) * *c.Context.TailDrainFraction)
}

func (c *Config) outlineBudget(w int) int {
	if c == nil || c.Context.OutlineFraction == nil {
		return w / 8
	}
	return int(float64(w) * *c.Context.OutlineFraction)
}

func (c *Config) seedBudget() int {
	if c == nil {
		return tools.StudySeedBudget
	}
	return resolveInt(c.Subagents.SeedBudgetTokens, tools.StudySeedBudget)
}

// mergePrompt overlays prompt.* field-by-field: a set field in the project
// config wins over the user config, empty preserves.
func mergePrompt(base, over PromptConfig) PromptConfig {
	out := base
	if over.File != "" {
		out.File = over.File
	}
	if over.Append != "" {
		out.Append = over.Append
	}
	return out
}

// skillsIndexMaxDefault is how many discovered skills the turn-start index
// lists before falling back to an "N more omitted" line — config-overridable
// via skills.index_max (cmd/cortex/skills.go).
const skillsIndexMaxDefault = 20

// skillsEnabled reports whether Agent Skills discovery+injection is on.
// Nil/absent means enabled — an availability kill-switch, not consent,
// matching scanEnabled/deleteEnabled's posture.
func (c *Config) skillsEnabled() bool {
	if c == nil || c.Skills.Enabled == nil {
		return true
	}
	return *c.Skills.Enabled
}

// skillsIndexMax resolves skills.index_max, falling back to
// skillsIndexMaxDefault when unset.
func (c *Config) skillsIndexMax() int {
	if c == nil {
		return skillsIndexMaxDefault
	}
	return resolveInt(c.Skills.IndexMax, skillsIndexMaxDefault)
}

// skillsDirsOverride returns skills.dirs verbatim; nil c or an empty list
// means "use the four defaults" (cmd/cortex/skills.go's skillsDirs).
func (c *Config) skillsDirsOverride() []string {
	if c == nil {
		return nil
	}
	return c.Skills.Dirs
}
