// config_full_configurability_test.go covers the "full configurability"
// slices 1-3 audit: every new config field defaults to today's hardcoded
// value (zero-config byte-identical), project overrides user field-by-field
// exactly like the pre-existing fields, and validateConfig rejects
// nonsensical explicit values with a clear error. See docs/configuration.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/agent"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/tools"
)

// TestZeroConfigResolversMatchHistoricalDefaults pins that every resolver
// method added by this audit reproduces the exact hardcoded value it
// replaced, for BOTH a nil *Config and an explicit empty &Config{} — the two
// shapes every existing call site sees today when a section is never
// mentioned.
func TestZeroConfigResolversMatchHistoricalDefaults(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		label := "nil"
		if cfg != nil {
			label = "empty"
		}
		t.Run(label, func(t *testing.T) {
			if got := cfg.maxToolIterations(); got != maxToolIterations {
				t.Errorf("maxToolIterations() = %d, want %d", got, maxToolIterations)
			}
			if got := cfg.instructionBytesCap(); got != maxInstructionBytes {
				t.Errorf("instructionBytesCap() = %d, want %d", got, maxInstructionBytes)
			}
			if got := cfg.memoryIndexCapChars(); got != memoryIndexCap {
				t.Errorf("memoryIndexCapChars() = %d, want %d", got, memoryIndexCap)
			}
			if got := cfg.userMemoryIndexCapChars(); got != userMemoryIndexCap {
				t.Errorf("userMemoryIndexCapChars() = %d, want %d", got, userMemoryIndexCap)
			}
			if got := cfg.captureExcerptCapChars(); got != captureExcerptCap {
				t.Errorf("captureExcerptCapChars() = %d, want %d", got, captureExcerptCap)
			}
			if got := cfg.maxTaskContextChars(); got != 800 {
				t.Errorf("maxTaskContextChars() = %d, want 800 (shellrisk.DefaultMaxTaskContextChars)", got)
			}
			if got := cfg.routeMaxOutputTokens(); got != routeMaxOutputTokens {
				t.Errorf("routeMaxOutputTokens() = %d, want %d", got, routeMaxOutputTokens)
			}
			if got := cfg.maxServedModelsShown(); got != maxServedModelsShown {
				t.Errorf("maxServedModelsShown() = %d, want %d", got, maxServedModelsShown)
			}
			if got := cfg.fleetDiscoveryTimeout(); got != defaultFleetDiscoveryTimeout {
				t.Errorf("fleetDiscoveryTimeout() = %v, want %v", got, defaultFleetDiscoveryTimeout)
			}
			if got := cfg.preflightTimeout(); got != defaultOpenRouterPreflightTimeout {
				t.Errorf("preflightTimeout() = %v, want %v", got, defaultOpenRouterPreflightTimeout)
			}
			if got := cfg.servePort(defaultServePort); got != defaultServePort {
				t.Errorf("servePort(default) = %d, want %d", got, defaultServePort)
			}
			if got := cfg.sessionIdleTimeout(); got != defaultSessionIdleTimeout {
				t.Errorf("sessionIdleTimeout() = %v, want %v", got, defaultSessionIdleTimeout)
			}
			if got := cfg.loopCadenceFloorMin(); got != loops.DefaultCadenceFloorMinutes {
				t.Errorf("loopCadenceFloorMin() = %d, want %d", got, loops.DefaultCadenceFloorMinutes)
			}
			if got := cfg.loopAutoDisableStrikes(); got != defaultLoopAutoDisableStrikes {
				t.Errorf("loopAutoDisableStrikes() = %d, want %d", got, defaultLoopAutoDisableStrikes)
			}
			if got := cfg.projectSessionsLimit(); got != defaultProjectSessionsLimit {
				t.Errorf("projectSessionsLimit() = %d, want %d", got, defaultProjectSessionsLimit)
			}
			if got := cfg.tickerInterval(); got != time.Second {
				t.Errorf("tickerInterval() = %v, want 1s", got)
			}
			if got := cfg.discordTypingRefresh(); got != defaultTypingRefresh {
				t.Errorf("discordTypingRefresh() = %v, want %v", got, defaultTypingRefresh)
			}
			if got := cfg.discordRiskApprovalTimeout(); got != defaultRiskApprovalTimeout {
				t.Errorf("discordRiskApprovalTimeout() = %v, want %v", got, defaultRiskApprovalTimeout)
			}
			if got := cfg.discordProgressEditInterval(); got != defaultProgressEditInterval {
				t.Errorf("discordProgressEditInterval() = %v, want %v", got, defaultProgressEditInterval)
			}
			if got := cfg.discordRouteConfidenceThreshold(); got != routeConfidenceThreshold {
				t.Errorf("discordRouteConfidenceThreshold() = %v, want %v", got, routeConfidenceThreshold)
			}
			if got := cfg.seedBudget(); got != tools.StudySeedBudget {
				t.Errorf("seedBudget() = %d, want %d", got, tools.StudySeedBudget)
			}

			def := tools.DefaultLimits()
			if got := cfg.toolLimits(); got != def {
				t.Errorf("toolLimits() = %+v, want DefaultLimits() %+v", got, def)
			}

			studyDef := agent.Bounds{MaxTokens: 111, MaxIter: 12, ReadBudgetBytes: 96_000}
			if got := cfg.subagentBounds(roleStudy, studyDef); got != studyDef {
				t.Errorf("subagentBounds(study, def) = %+v, want unchanged %+v", got, studyDef)
			}
			agentDef := agent.Bounds{MaxTokens: 222, MaxIter: 20, ReadBudgetBytes: 128_000}
			if got := cfg.subagentBounds("agent", agentDef); got != agentDef {
				t.Errorf(`subagentBounds("agent", def) = %+v, want unchanged %+v`, got, agentDef)
			}
			learnDef := agent.Bounds{MaxTokens: 333, MaxIter: 12, ReadBudgetBytes: 96_000}
			if got := cfg.subagentBounds(roleLearn, learnDef); got != learnDef {
				t.Errorf(`subagentBounds(learn, def) = %+v, want unchanged %+v`, got, learnDef)
			}

			// context.* resolvers: unset must reproduce the ORIGINAL integer-division
			// expressions verbatim (ContextConfig's bit-identical-defaults subtlety) —
			// pinned at an arbitrary non-round window (8000) so a future edit that
			// swapped the unset branch for windowSize()*defaultFraction would still be
			// caught by ANY of these three (int division and float-then-truncate can
			// disagree by a token at some window sizes, even where they happen to
			// agree at 8000).
			const w = 8000
			if got := cfg.tailHighWatermark(w); got != w/2 {
				t.Errorf("tailHighWatermark(%d) = %d, want %d (w/2)", w, got, w/2)
			}
			if got := cfg.tailDrainWatermark(w); got != w/3 {
				t.Errorf("tailDrainWatermark(%d) = %d, want %d (w/3)", w, got, w/3)
			}
			if got := cfg.outlineBudget(w); got != w/8 {
				t.Errorf("outlineBudget(%d) = %d, want %d (w/8)", w, got, w/8)
			}

			if got := cfg.skillsEnabled(); !got {
				t.Errorf("skillsEnabled() = %v, want true (nil/absent means enabled)", got)
			}
			if got := cfg.skillsIndexMax(); got != skillsIndexMaxDefault {
				t.Errorf("skillsIndexMax() = %d, want %d", got, skillsIndexMaxDefault)
			}
			if got := cfg.skillsDirsOverride(); got != nil {
				t.Errorf("skillsDirsOverride() = %v, want nil", got)
			}
		})
	}
}

// TestConfigMergeNewSections covers project-overrides-user, field-by-field,
// for every new section — the same merge contract the pre-existing
// tools/models sections already have (TestLoadMergedConfig).
func TestConfigMergeNewSections(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dir := t.TempDir()
	userPath := write(t, dir, "user.json", `{
		"models": {
			"code": {"request_timeout_sec": 600, "max_send_attempts": 5, "retry_backoff_ms": 250},
			"study": {"request_timeout_sec": 900}
		},
		"subagents": {
			"seed_budget_tokens": 7000,
			"study": {"max_tokens": 9000, "max_iter": 15, "read_budget_bytes": 100000},
			"agent": {"max_tokens": 9500, "max_iter": 25, "read_budget_bytes": 130000}
		},
		"tools": {
			"curation_budget_tokens": 20000,
			"max_tool_output": 12000,
			"outline_default_budget": 5000,
			"read": {"default_range_lines": 250, "max_range_lines": 900, "max_read_bytes": 30000},
			"grep": {"max_hits": 150, "line_cap": 1500, "max_output_bytes": 7000},
			"fetch_url": {"timeout_sec": 25, "max_redirects": 6, "max_body_bytes": 2000000},
			"web_search": {"default_max_results": 6, "maximum_max_results": 12}
		},
		"limits": {
			"max_tool_iterations": 120,
			"max_instruction_bytes": 20000,
			"memory_index_cap_chars": 5000,
			"user_memory_index_cap_chars": 2000,
			"capture_excerpt_cap_chars": 300,
			"max_task_context_chars": 900,
			"route_max_output_tokens": 90,
			"max_served_models_shown": 50
		},
		"network": {
			"fleet_discovery_timeout_sec": 5,
			"preflight_timeout_sec": 6,
			"ollama_probe_timeout_sec": 3,
			"compat_timeout_sec": 400
		},
		"serve": {
			"port": 8000,
			"session_idle_timeout_min": 45,
			"loop_cadence_floor_min": 10,
			"loop_auto_disable_strikes": 4,
			"project_sessions_limit": 60
		},
		"repl": {"ticker_interval_ms": 2000, "gauge": "ascii"},
		"discord": {
			"typing_refresh_sec": 10,
			"risk_approval_timeout_sec": 150,
			"progress_edit_interval_ms": 2000,
			"route_confidence_threshold": 0.9
		},
		"context": {"tail_high_fraction": 0.45, "tail_drain_fraction": 0.2, "outline_fraction": 0.1},
		"skills": {"enabled": true, "index_max": 15, "dirs": ["/tmp/user-skills"]}
	}`)
	projPath := write(t, dir, "proj.json", `{
		"models": {"code": {"max_send_attempts": 2}},
		"subagents": {"study": {"max_tokens": 8000}},
		"tools": {"grep": {"max_hits": 40}},
		"limits": {"route_max_output_tokens": 60},
		"network": {"preflight_timeout_sec": 9},
		"serve": {"port": 9001},
		"repl": {"ticker_interval_ms": 500},
		"skills": {"index_max": 5},
		"discord": {"route_confidence_threshold": 0.5},
		"context": {"outline_fraction": 0.08}
	}`)

	cfg := loadMergedConfig(userPath, projPath)
	if cfg == nil {
		t.Fatal("merged config is nil")
	}

	// Project overrides win where set...
	if cfg.Models["code"].MaxSendAttempts != 2 {
		t.Errorf("code.max_send_attempts = %d, want project override 2", cfg.Models["code"].MaxSendAttempts)
	}
	if cfg.Subagents.Study.MaxTokens != 8000 {
		t.Errorf("subagents.study.max_tokens = %d, want project override 8000", cfg.Subagents.Study.MaxTokens)
	}
	if cfg.Tools.Grep.MaxHits != 40 {
		t.Errorf("tools.grep.max_hits = %d, want project override 40", cfg.Tools.Grep.MaxHits)
	}
	if cfg.Limits.RouteMaxOutputTokens != 60 {
		t.Errorf("limits.route_max_output_tokens = %d, want project override 60", cfg.Limits.RouteMaxOutputTokens)
	}
	if cfg.Network.PreflightTimeoutSec != 9 {
		t.Errorf("network.preflight_timeout_sec = %d, want project override 9", cfg.Network.PreflightTimeoutSec)
	}
	if cfg.Serve.Port != 9001 {
		t.Errorf("serve.port = %d, want project override 9001", cfg.Serve.Port)
	}
	if cfg.Repl.TickerIntervalMs != 500 {
		t.Errorf("repl.ticker_interval_ms = %d, want project override 500", cfg.Repl.TickerIntervalMs)
	}
	if cfg.Repl.Gauge != "ascii" {
		t.Errorf("repl.gauge = %q, want inherited user value \"ascii\" (project layer doesn't set it)", cfg.Repl.Gauge)
	}
	if cfg.Discord.RouteConfidenceThreshold == nil || *cfg.Discord.RouteConfidenceThreshold != 0.5 {
		t.Errorf("discord.route_confidence_threshold = %v, want project override 0.5", cfg.Discord.RouteConfidenceThreshold)
	}
	if cfg.Context.OutlineFraction == nil || *cfg.Context.OutlineFraction != 0.08 {
		t.Errorf("context.outline_fraction = %v, want project override 0.08", cfg.Context.OutlineFraction)
	}
	if cfg.Skills.IndexMax != 5 {
		t.Errorf("skills.index_max = %d, want project override 5", cfg.Skills.IndexMax)
	}

	// ...everything else inherits from the user layer untouched.
	if cfg.Models["code"].RequestTimeoutSec != 600 {
		t.Errorf("code.request_timeout_sec = %d, want inherited 600", cfg.Models["code"].RequestTimeoutSec)
	}
	if cfg.Models["code"].RetryBackoffMs != 250 {
		t.Errorf("code.retry_backoff_ms = %d, want inherited 250", cfg.Models["code"].RetryBackoffMs)
	}
	if cfg.Models["study"].RequestTimeoutSec != 900 {
		t.Errorf("study.request_timeout_sec = %d, want inherited 900", cfg.Models["study"].RequestTimeoutSec)
	}
	if cfg.Subagents.SeedBudgetTokens != 7000 {
		t.Errorf("subagents.seed_budget_tokens = %d, want inherited 7000", cfg.Subagents.SeedBudgetTokens)
	}
	if cfg.Subagents.Study.MaxIter != 15 || cfg.Subagents.Study.ReadBudgetBytes != 100000 {
		t.Errorf("subagents.study max_iter/read_budget_bytes not inherited: %+v", cfg.Subagents.Study)
	}
	if cfg.Subagents.Agent != (SubagentProfileConfig{MaxTokens: 9500, MaxIter: 25, ReadBudgetBytes: 130000}) {
		t.Errorf("subagents.agent not inherited: %+v", cfg.Subagents.Agent)
	}
	if cfg.Tools.CurationBudgetTokens != 20000 || cfg.Tools.MaxToolOutput != 12000 || cfg.Tools.OutlineDefaultBudget != 5000 {
		t.Errorf("tools top-level caps not inherited: %+v", cfg.Tools)
	}
	if cfg.Tools.Read != (ReadConfig{DefaultRangeLines: 250, MaxRangeLines: 900, MaxReadBytes: 30000}) {
		t.Errorf("tools.read not inherited: %+v", cfg.Tools.Read)
	}
	if cfg.Tools.Grep.LineCap != 1500 || cfg.Tools.Grep.MaxOutputBytes != 7000 {
		t.Errorf("tools.grep line_cap/max_output_bytes not inherited: %+v", cfg.Tools.Grep)
	}
	if cfg.Tools.FetchURL != (FetchURLConfig{TimeoutSec: 25, MaxRedirects: 6, MaxBodyBytes: 2000000}) {
		t.Errorf("tools.fetch_url not inherited: %+v", cfg.Tools.FetchURL)
	}
	if cfg.Tools.WebSearch != (WebSearchConfig{DefaultMaxResults: 6, MaximumMaxResults: 12}) {
		t.Errorf("tools.web_search not inherited: %+v", cfg.Tools.WebSearch)
	}
	if cfg.Limits.MaxToolIterations != 120 || cfg.Limits.MaxInstructionBytes != 20000 ||
		cfg.Limits.MemoryIndexCapChars != 5000 || cfg.Limits.UserMemoryIndexCapChars != 2000 ||
		cfg.Limits.CaptureExcerptCapChars != 300 ||
		cfg.Limits.MaxTaskContextChars != 900 || cfg.Limits.MaxServedModelsShown != 50 {
		t.Errorf("limits.* not inherited: %+v", cfg.Limits)
	}
	if cfg.Network.FleetDiscoveryTimeoutSec != 5 || cfg.Network.OllamaProbeTimeoutSec != 3 || cfg.Network.CompatTimeoutSec != 400 {
		t.Errorf("network.* not inherited: %+v", cfg.Network)
	}
	if cfg.Serve.SessionIdleTimeoutMin != 45 || cfg.Serve.LoopCadenceFloorMin != 10 ||
		cfg.Serve.LoopAutoDisableStrikes != 4 || cfg.Serve.ProjectSessionsLimit != 60 {
		t.Errorf("serve.* not inherited: %+v", cfg.Serve)
	}
	if cfg.Discord.TypingRefreshSec != 10 || cfg.Discord.RiskApprovalTimeoutSec != 150 || cfg.Discord.ProgressEditIntervalMs != 2000 {
		t.Errorf("discord.* not inherited: %+v", cfg.Discord)
	}
	if cfg.Context.TailHighFraction == nil || *cfg.Context.TailHighFraction != 0.45 ||
		cfg.Context.TailDrainFraction == nil || *cfg.Context.TailDrainFraction != 0.2 {
		t.Errorf("context.tail_high_fraction/tail_drain_fraction not inherited: %+v", cfg.Context)
	}
	if cfg.Skills.Enabled == nil || !*cfg.Skills.Enabled {
		t.Errorf("skills.enabled not inherited: %+v", cfg.Skills)
	}
	if len(cfg.Skills.Dirs) != 1 || cfg.Skills.Dirs[0] != "/tmp/user-skills" {
		t.Errorf("skills.dirs not inherited (project layer doesn't set it): %+v", cfg.Skills.Dirs)
	}
}

// TestConfigMergeUnknownKeysDoNotError covers the "unknown keys inside the
// new sections must not error" requirement — an operator's typo or a
// forward-compat field from a newer cortex must load silently.
func TestConfigMergeUnknownKeysDoNotError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	body := `{
		"tools": {"grep": {"max_hits": 5, "future_field": 99}},
		"limits": {"max_tool_iterations": 10, "another_future_field": "x"},
		"serve": {"port": 8001, "yet_another": true},
		"totally_unknown_top_level_section": {"a": 1}
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := readConfigFile(p)
	if cfg == nil {
		t.Fatal("readConfigFile returned nil for a config with only unknown extra keys")
	}
	if cfg.Tools.Grep.MaxHits != 5 {
		t.Errorf("known field tools.grep.max_hits = %d, want 5 (unknown sibling key must not break parsing)", cfg.Tools.Grep.MaxHits)
	}
	if cfg.Limits.MaxToolIterations != 10 {
		t.Errorf("known field limits.max_tool_iterations = %d, want 10", cfg.Limits.MaxToolIterations)
	}
	if cfg.Serve.Port != 8001 {
		t.Errorf("known field serve.port = %d, want 8001", cfg.Serve.Port)
	}
}

// TestValidateConfigRejectsBadValues is the validation table: every explicit
// nonsensical value the audit calls out (non-positive timeouts/caps,
// route_confidence_threshold outside (0,1]) must produce a clear error;
// absent (zero-value) fields must never trip it, since 0 is this schema's
// universal "not set" sentinel.
func TestValidateConfigRejectsBadValues(t *testing.T) {
	neg := -1
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"zero-value config: valid", Config{}, false},
		{"negative model timeout: invalid", Config{Models: map[string]ModelSpec{"code": {RequestTimeoutSec: neg}}}, true},
		{"negative max_send_attempts: invalid", Config{Models: map[string]ModelSpec{"code": {MaxSendAttempts: neg}}}, true},
		{"negative retry_backoff_ms: invalid", Config{Models: map[string]ModelSpec{"code": {RetryBackoffMs: neg}}}, true},
		{"positive model timeout: valid", Config{Models: map[string]ModelSpec{"code": {RequestTimeoutSec: 60}}}, false},
		{"negative curation_budget_tokens: invalid", Config{Tools: ToolConfig{CurationBudgetTokens: neg}}, true},
		{"negative grep.max_hits: invalid", Config{Tools: ToolConfig{Grep: GrepConfig{MaxHits: neg}}}, true},
		{"negative read.max_read_bytes: invalid", Config{Tools: ToolConfig{Read: ReadConfig{MaxReadBytes: neg}}}, true},
		{"negative fetch_url.timeout_sec: invalid", Config{Tools: ToolConfig{FetchURL: FetchURLConfig{TimeoutSec: neg}}}, true},
		{"negative web_search.maximum_max_results: invalid", Config{Tools: ToolConfig{WebSearch: WebSearchConfig{MaximumMaxResults: neg}}}, true},
		{"negative subagents.study.max_tokens: invalid", Config{Subagents: SubagentsConfig{Study: SubagentProfileConfig{MaxTokens: neg}}}, true},
		{"negative subagents.seed_budget_tokens: invalid", Config{Subagents: SubagentsConfig{SeedBudgetTokens: neg}}, true},
		{"negative limits.max_tool_iterations: invalid", Config{Limits: LimitsConfig{MaxToolIterations: neg}}, true},
		{"negative limits.user_memory_index_cap_chars: invalid", Config{Limits: LimitsConfig{UserMemoryIndexCapChars: neg}}, true},
		{"negative network.fleet_discovery_timeout_sec: invalid", Config{Network: NetworkConfig{FleetDiscoveryTimeoutSec: neg}}, true},
		{"negative serve.port: invalid", Config{Serve: ServeConfig{Port: neg}}, true},
		{"negative repl.ticker_interval_ms: invalid", Config{Repl: ReplConfig{TickerIntervalMs: neg}}, true},
		{"repl.gauge unset: valid", Config{Repl: ReplConfig{}}, false},
		{"repl.gauge = blocks: valid", Config{Repl: ReplConfig{Gauge: "blocks"}}, false},
		{"repl.gauge = braille: valid", Config{Repl: ReplConfig{Gauge: "braille"}}, false},
		{"repl.gauge = ascii: valid", Config{Repl: ReplConfig{Gauge: "ascii"}}, false},
		{"repl.gauge = numeric: valid", Config{Repl: ReplConfig{Gauge: "numeric"}}, false},
		{"repl.gauge = bogus: invalid", Config{Repl: ReplConfig{Gauge: "bogus"}}, true},
		{"negative discord.typing_refresh_sec: invalid", Config{Discord: DiscordConfig{TypingRefreshSec: neg}}, true},
		{"route_confidence_threshold = 0: invalid", Config{Discord: DiscordConfig{RouteConfidenceThreshold: floatPtr(0)}}, true},
		{"route_confidence_threshold > 1: invalid", Config{Discord: DiscordConfig{RouteConfidenceThreshold: floatPtr(1.5)}}, true},
		{"route_confidence_threshold negative: invalid", Config{Discord: DiscordConfig{RouteConfidenceThreshold: floatPtr(-0.1)}}, true},
		{"route_confidence_threshold = 1 (inclusive bound): valid", Config{Discord: DiscordConfig{RouteConfidenceThreshold: floatPtr(1)}}, false},
		{"route_confidence_threshold = 0.5: valid", Config{Discord: DiscordConfig{RouteConfidenceThreshold: floatPtr(0.5)}}, false},
		{"route_confidence_threshold unset (nil): valid", Config{Discord: DiscordConfig{}}, false},
		{"context.* all unset: valid", Config{Context: ContextConfig{}}, false},
		{"context.tail_high_fraction = 0: invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0)}}, true},
		{"context.tail_high_fraction = 1: invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(1)}}, true},
		{"context.tail_drain_fraction negative: invalid", Config{Context: ContextConfig{TailDrainFraction: floatPtr(-0.1)}}, true},
		{"context.outline_fraction > 1: invalid", Config{Context: ContextConfig{OutlineFraction: floatPtr(1.5)}}, true},
		{"context.tail_high_fraction <= tail_drain_fraction (no hysteresis): invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.3), TailDrainFraction: floatPtr(0.3)}}, true},
		{"context.tail_high_fraction < tail_drain_fraction: invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.2), TailDrainFraction: floatPtr(0.4)}}, true},
		{"context dormancy violation (explicit high+outline): invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.7), OutlineFraction: floatPtr(0.2)}}, true},
		{"context dormancy violation (high alone against default outline): invalid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.65)}}, true},
		{"context.* explicit defaults (0.5/0.333/0.125): valid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.5), TailDrainFraction: floatPtr(1.0 / 3.0), OutlineFraction: floatPtr(0.125)}}, false},
		{"context.* tighter tail, more outline: valid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.4), TailDrainFraction: floatPtr(0.25), OutlineFraction: floatPtr(0.15)}}, false},
		{"context.tail_high_fraction alone: valid", Config{Context: ContextConfig{TailHighFraction: floatPtr(0.45)}}, false},
		{"context.outline_fraction alone: valid", Config{Context: ContextConfig{OutlineFraction: floatPtr(0.1)}}, false},
		{"negative skills.index_max: invalid", Config{Skills: SkillsConfig{IndexMax: neg}}, true},
		{"positive skills.index_max: valid", Config{Skills: SkillsConfig{IndexMax: 5}}, false},
		{"skills.* unset: valid", Config{Skills: SkillsConfig{}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConfig(%+v) error = %v, wantErr %v", tt.cfg, err, tt.wantErr)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }

// invalidConfigChildPathEnv is the env-var contract the child process (a
// re-exec of this same test binary, mirroring serve_lock_test.go's
// established idiom) reads to know which config file to load.
const invalidConfigChildPathEnv = "CORTEX_INVALID_CONFIG_TEST_PATH"

// TestReadConfigFileExitsOnInvalidConfig proves invalid config on disk is a
// FATAL load error (docs/configuration.md: "reject nonsensical values with a
// clear error at load"), not a silent fall-through to zero-config —
// exercised via a real subprocess (os.Exit(1) would kill the test binary
// itself if called in-process).
func TestReadConfigFileExitsOnInvalidConfig(t *testing.T) {
	if p := os.Getenv(invalidConfigChildPathEnv); p != "" {
		// Child mode: load the bad config directly. A clean exit (0) here —
		// meaning readConfigFile returned instead of calling os.Exit(1) —
		// is the failure the parent below checks for.
		readConfigFile(p)
		fmt.Println("READ-CONFIG-RETURNED (want fatal exit instead)")
		os.Exit(0)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	body := `{"limits": {"max_tool_iterations": -5}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestReadConfigFileExitsOnInvalidConfig$")
	cmd.Env = append(os.Environ(), invalidConfigChildPathEnv+"="+p)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("child process exited 0 loading an invalid config; want a non-zero (fatal) exit. output=%q", out)
	}
	if strings.Contains(string(out), "READ-CONFIG-RETURNED") {
		t.Fatalf("readConfigFile returned instead of exiting fatally on invalid config; output=%q", out)
	}
	if !strings.Contains(string(out), "invalid config") {
		t.Errorf("expected a clear 'invalid config' message on stderr, got: %q", out)
	}
}

// TestModelSpecTimeoutPrecedence is the P1 unification's core contract: an
// explicit request_timeout_sec always wins; failing that,
// CORTEX_COMPAT_TIMEOUT_SEC (the pre-existing env knob, now honored by every
// transport path, not just pkg/llm's compat client); failing that, the
// caller's own historical default.
func TestModelSpecTimeoutPrecedence(t *testing.T) {
	t.Run("explicit config wins over env and default", func(t *testing.T) {
		t.Setenv("CORTEX_COMPAT_TIMEOUT_SEC", "45")
		spec := ModelSpec{RequestTimeoutSec: 120}
		if got := spec.timeout(9 * time.Minute); got != 120*time.Second {
			t.Errorf("timeout() = %v, want 120s (explicit config)", got)
		}
	})
	t.Run("env wins over default when config unset", func(t *testing.T) {
		t.Setenv("CORTEX_COMPAT_TIMEOUT_SEC", "45")
		spec := ModelSpec{}
		if got := spec.timeout(9 * time.Minute); got != 45*time.Second {
			t.Errorf("timeout() = %v, want 45s (env)", got)
		}
	})
	t.Run("default when neither set", func(t *testing.T) {
		spec := ModelSpec{}
		if got := spec.timeout(9 * time.Minute); got != 9*time.Minute {
			t.Errorf("timeout() = %v, want 9m (default)", got)
		}
	})
	t.Run("maxAttempts and backoff follow the same config>default precedence", func(t *testing.T) {
		spec := ModelSpec{MaxSendAttempts: 7, RetryBackoffMs: 1500}
		if got := spec.maxAttempts(3); got != 7 {
			t.Errorf("maxAttempts() = %d, want 7", got)
		}
		if got := spec.backoff(500 * time.Millisecond); got != 1500*time.Millisecond {
			t.Errorf("backoff() = %v, want 1.5s", got)
		}
		empty := ModelSpec{}
		if got := empty.maxAttempts(3); got != 3 {
			t.Errorf("maxAttempts() default = %d, want 3", got)
		}
		if got := empty.backoff(500 * time.Millisecond); got != 500*time.Millisecond {
			t.Errorf("backoff() default = %v, want 500ms", got)
		}
	})
}

// TestRequestForStampsTransportBudget is a wiring spot-check (per-role
// timeout actually reaches the transport client): requestFor is the single
// build site every subagent request goes through, and NewCortexSession
// stamps the coder's cs.Request the same way — this pins that the
// ModelSpec's resolved Timeout/MaxAttempts/Backoff land on the AgentRequest
// fields Send/SendStream actually read (transport.go's effectiveTimeout /
// effectiveMaxAttempts / effectiveBackoff).
func TestRequestForStampsTransportBudget(t *testing.T) {
	spec := ModelSpec{
		Model:             "m",
		Endpoint:          "http://example.test",
		RequestTimeoutSec: 42,
		MaxSendAttempts:   9,
		RetryBackoffMs:    777,
	}
	req := requestFor(spec, "sys", "seed", nil, 100, dialectFor(false))
	if req.Timeout != 42*time.Second {
		t.Errorf("req.Timeout = %v, want 42s", req.Timeout)
	}
	if req.MaxAttempts != 9 {
		t.Errorf("req.MaxAttempts = %d, want 9", req.MaxAttempts)
	}
	if req.Backoff != 777*time.Millisecond {
		t.Errorf("req.Backoff = %v, want 777ms", req.Backoff)
	}
	if got := req.effectiveTimeout(); got != 42*time.Second {
		t.Errorf("effectiveTimeout() = %v, want 42s", got)
	}
	if got := req.effectiveMaxAttempts(); got != 9 {
		t.Errorf("effectiveMaxAttempts() = %d, want 9", got)
	}
	if got := req.effectiveBackoff(); got != 777*time.Millisecond {
		t.Errorf("effectiveBackoff() = %v, want 777ms", got)
	}

	// Unset spec: falls back to package defaults, unchanged from before P1.
	bare := requestFor(ModelSpec{Model: "m"}, "sys", "seed", nil, 100, dialectFor(false))
	if bare.effectiveTimeout() != requestTimeout {
		t.Errorf("effectiveTimeout() with unset spec = %v, want package default %v", bare.effectiveTimeout(), requestTimeout)
	}
	if bare.effectiveMaxAttempts() != maxSendAttempts {
		t.Errorf("effectiveMaxAttempts() with unset spec = %d, want package default %d", bare.effectiveMaxAttempts(), maxSendAttempts)
	}
	if bare.effectiveBackoff() != retryBackoff {
		t.Errorf("effectiveBackoff() with unset spec = %v, want package default %v", bare.effectiveBackoff(), retryBackoff)
	}
}

// TestSubagentRequestAgentProfileInheritsCoderTransportBudget covers the
// naming-honesty wrinkle called out in config.go: the agent profile runs on
// the coder's model, so it must run on the coder's request_timeout_sec/
// max_send_attempts/retry_backoff_ms too — NOT the study role's, even though
// requestFor's initial build (from cs.specForRole, which always returns
// cs.Study) would otherwise stamp the study role's values.
func TestSubagentRequestAgentProfileInheritsCoderTransportBudget(t *testing.T) {
	study := ModelSpec{Model: "study-m", Endpoint: "http://study.example", RequestTimeoutSec: 111}
	coder := &AgentRequest{
		Model: "coder-live", BaseURL: "http://coder.example", APIKey: "sk-live",
		Timeout: 222 * time.Second, MaxAttempts: 5, Backoff: 3 * time.Second,
	}
	cs := &CortexSession{Study: study, Request: coder}

	agentReq := cs.subagentRequest(tools.Subagent{Role: "agent"}, "seed")
	if agentReq.Timeout != coder.Timeout {
		t.Errorf("agent profile Timeout = %v, want coder's %v", agentReq.Timeout, coder.Timeout)
	}
	if agentReq.MaxAttempts != coder.MaxAttempts {
		t.Errorf("agent profile MaxAttempts = %d, want coder's %d", agentReq.MaxAttempts, coder.MaxAttempts)
	}
	if agentReq.Backoff != coder.Backoff {
		t.Errorf("agent profile Backoff = %v, want coder's %v", agentReq.Backoff, coder.Backoff)
	}

	studyReq := cs.subagentRequest(tools.Subagent{Role: roleStudy}, "seed")
	if studyReq.Timeout != 111*time.Second {
		t.Errorf("study profile Timeout = %v, want study role's 111s", studyReq.Timeout)
	}
}

// TestSubagentRequestLearnProfileStaysOnStudyBinding covers
// docs/learning-loop.md's model-binding decision: Learn (Role=="learn")
// runs on the study role's binding, exactly like Study itself — NOT the
// coder's live model the way Agent does — because a background pass has no
// "coder's current model" to inherit from in the first place (it can run
// with no coder session live at all, e.g. a scheduled loop firing). Proven
// two ways: (1) with a live coder Request present, Learn must NOT pick up
// its model (unlike Agent, which does — the preceding test); (2) with NO
// live coder Request at all (the loop-firing shape), Learn must still
// resolve cleanly off the study binding rather than panicking on a nil
// cs.Request.
func TestSubagentRequestLearnProfileStaysOnStudyBinding(t *testing.T) {
	study := ModelSpec{Model: "study-m", Endpoint: "http://study.example", RequestTimeoutSec: 111}
	coder := &AgentRequest{
		Model: "coder-live", BaseURL: "http://coder.example", APIKey: "sk-live",
		Timeout: 222 * time.Second, MaxAttempts: 5, Backoff: 3 * time.Second,
	}

	withCoder := &CortexSession{Study: study, Request: coder}
	learnReq := withCoder.subagentRequest(tools.Subagent{Role: roleLearn}, "seed")
	if learnReq.Model != study.Model {
		t.Errorf("learn profile Model = %q, want the study binding's %q (not the coder's live %q)", learnReq.Model, study.Model, coder.Model)
	}
	if learnReq.Timeout != 111*time.Second {
		t.Errorf("learn profile Timeout = %v, want study role's 111s", learnReq.Timeout)
	}

	withoutCoder := &CortexSession{Study: study}
	learnReq2 := withoutCoder.subagentRequest(tools.Subagent{Role: roleLearn}, "seed")
	if learnReq2.Model != study.Model {
		t.Errorf("learn profile Model with no live coder request = %q, want the study binding's %q", learnReq2.Model, study.Model)
	}
}

// TestConfigSubagentBoundsOverridesReachProfile is the second wiring
// spot-check: subagents.study/.agent config actually reaches the Study/Agent
// profile's runtime Bounds, via runSubagentStats' local sa.Bounds override —
// NOT by mutating the package-level tools.Study/tools.Agent vars (asserted
// by checking those are untouched after the call, so a second session with
// different config can't see the first session's override).
func TestConfigSubagentBoundsOverridesReachProfile(t *testing.T) {
	originalStudyBounds := tools.Study.Bounds
	originalAgentBounds := tools.Agent.Bounds
	t.Cleanup(func() {
		if tools.Study.Bounds != originalStudyBounds {
			t.Errorf("tools.Study.Bounds was mutated: got %+v, want unchanged %+v", tools.Study.Bounds, originalStudyBounds)
		}
		if tools.Agent.Bounds != originalAgentBounds {
			t.Errorf("tools.Agent.Bounds was mutated: got %+v, want unchanged %+v", tools.Agent.Bounds, originalAgentBounds)
		}
	})

	cfg := &Config{Subagents: SubagentsConfig{
		Study: SubagentProfileConfig{MaxTokens: 4242, MaxIter: 3, ReadBudgetBytes: 5000},
	}}

	// Directly exercise the override math runSubagentStats applies —
	// equivalent to what a live RunSubagent call does before it ever reaches
	// the network, without needing a scripted HTTP round trip.
	sa := tools.Study
	sa.Bounds = cfg.subagentBounds(sa.Role, sa.Bounds)
	if sa.Bounds.MaxTokens != 4242 {
		t.Errorf("overridden Study Bounds.MaxTokens = %d, want 4242", sa.Bounds.MaxTokens)
	}
	if sa.Bounds.MaxIter != 3 {
		t.Errorf("overridden Study Bounds.MaxIter = %d, want 3", sa.Bounds.MaxIter)
	}
	if sa.Bounds.ReadBudgetBytes != 5000 {
		t.Errorf("overridden Study Bounds.ReadBudgetBytes = %d, want 5000", sa.Bounds.ReadBudgetBytes)
	}

	// Agent has no override configured — falls back to its registered
	// default untouched.
	agentSA := tools.Agent
	agentSA.Bounds = cfg.subagentBounds(agentSA.Role, agentSA.Bounds)
	if agentSA.Bounds != originalAgentBounds {
		t.Errorf("Agent Bounds without config override = %+v, want unchanged %+v", agentSA.Bounds, originalAgentBounds)
	}

	// requestFor's maxTokens argument is what actually reaches the wire
	// max_tokens field — confirm the override survives that hop too.
	req := requestFor(ModelSpec{Model: "m", Endpoint: "http://example.test"}, "sys", "seed", nil, sa.Bounds.MaxTokens, dialectFor(false))
	if req.MaxTokens != 4242 {
		t.Errorf("wire req.MaxTokens = %d, want 4242 (the config-overridden bound)", req.MaxTokens)
	}
}

// TestConfigSubagentLearnBoundsOverrideReachesProfile is
// TestConfigSubagentBoundsOverridesReachProfile's Learn counterpart:
// subagents.learn config reaches internal/tools.Learn's runtime Bounds
// independently of subagents.study/.agent — proving Learn didn't
// accidentally alias Study's config section (a real risk since both run on
// the study role's model binding, per the preceding test).
func TestConfigSubagentLearnBoundsOverrideReachesProfile(t *testing.T) {
	originalLearnBounds := tools.Learn.Bounds
	t.Cleanup(func() {
		if tools.Learn.Bounds != originalLearnBounds {
			t.Errorf("tools.Learn.Bounds was mutated: got %+v, want unchanged %+v", tools.Learn.Bounds, originalLearnBounds)
		}
	})

	cfg := &Config{Subagents: SubagentsConfig{
		Study: SubagentProfileConfig{MaxTokens: 4242, MaxIter: 3, ReadBudgetBytes: 5000},
		Learn: SubagentProfileConfig{MaxTokens: 999, MaxIter: 4, ReadBudgetBytes: 6000},
	}}

	sa := tools.Learn
	sa.Bounds = cfg.subagentBounds(sa.Role, sa.Bounds)
	if sa.Bounds.MaxTokens != 999 {
		t.Errorf("overridden Learn Bounds.MaxTokens = %d, want 999 (not Study's 4242 — the sections must not alias)", sa.Bounds.MaxTokens)
	}
	if sa.Bounds.MaxIter != 4 {
		t.Errorf("overridden Learn Bounds.MaxIter = %d, want 4", sa.Bounds.MaxIter)
	}
	if sa.Bounds.ReadBudgetBytes != 6000 {
		t.Errorf("overridden Learn Bounds.ReadBudgetBytes = %d, want 6000", sa.Bounds.ReadBudgetBytes)
	}
}

// TestConfigGrepCapsReachGrep is the third wiring spot-check: tools.grep.*
// config actually changes grep's behavior, via tools.Configure (set once at
// session construction from Config.toolLimits()) — not just the resolver
// math in isolation.
func TestConfigGrepCapsReachGrep(t *testing.T) {
	t.Cleanup(tools.ResetLimits)

	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))+".txt"), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &Config{Tools: ToolConfig{Grep: GrepConfig{MaxHits: 3}}}
	tools.Configure(cfg.toolLimits())

	call := ToolCall{Function: FunctionCall{Name: FunctionGrep, Arguments: `{"pattern":"needle","path":"` + dir + `"}`}}
	out, err := tools.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if got := strings.Count(out, "needle"); got > 3 {
		t.Errorf("grep returned %d hits, want capped at configured max_hits=3 (got output: %s)", got, out)
	}
	if !strings.Contains(out, "capped at 3 matches") {
		t.Errorf("grep output should note the configured cap: %s", out)
	}
}

// TestApplyDiscordConfigSetsPackageVars covers runDiscordCLI's config
// wiring: discord.* config values reach the typingRefresh/
// riskApprovalTimeout/progressEditInterval package vars discord*.go's
// runtime actually reads, and are byte-identical to today's hardcoded
// values when nothing is configured.
func TestApplyDiscordConfigSetsPackageVars(t *testing.T) {
	savedTyping, savedRisk, savedProgress := typingRefresh, riskApprovalTimeout, progressEditInterval
	t.Cleanup(func() {
		typingRefresh, riskApprovalTimeout, progressEditInterval = savedTyping, savedRisk, savedProgress
	})

	applyDiscordConfig(&Config{Discord: DiscordConfig{
		TypingRefreshSec:       15,
		RiskApprovalTimeoutSec: 200,
		ProgressEditIntervalMs: 3000,
	}})
	if typingRefresh != 15*time.Second {
		t.Errorf("typingRefresh = %v, want 15s", typingRefresh)
	}
	if riskApprovalTimeout != 200*time.Second {
		t.Errorf("riskApprovalTimeout = %v, want 200s", riskApprovalTimeout)
	}
	if progressEditInterval != 3000*time.Millisecond {
		t.Errorf("progressEditInterval = %v, want 3000ms", progressEditInterval)
	}

	applyDiscordConfig(nil)
	if typingRefresh != defaultTypingRefresh {
		t.Errorf("typingRefresh after nil config = %v, want default %v", typingRefresh, defaultTypingRefresh)
	}
	if riskApprovalTimeout != defaultRiskApprovalTimeout {
		t.Errorf("riskApprovalTimeout after nil config = %v, want default %v", riskApprovalTimeout, defaultRiskApprovalTimeout)
	}
	if progressEditInterval != defaultProgressEditInterval {
		t.Errorf("progressEditInterval after nil config = %v, want default %v", progressEditInterval, defaultProgressEditInterval)
	}
}

// TestRunServeCLIWiresLoopCadenceFloor covers serve.loop_cadence_floor_min's
// path into internal/loops.CadenceFloorMinutes — a package var runServeCLI
// sets once, read by loops.FileStore.Save's validation and loop_run.go's
// self-pacing clamp.
func TestRunServeCLIWiresLoopCadenceFloor(t *testing.T) {
	saved := loops.CadenceFloorMinutes
	t.Cleanup(func() { loops.CadenceFloorMinutes = saved })

	cfg := &Config{Serve: ServeConfig{LoopCadenceFloorMin: 20}}
	loops.CadenceFloorMinutes = cfg.loopCadenceFloorMin()
	if loops.CadenceFloorMinutes != 20 {
		t.Errorf("loops.CadenceFloorMinutes = %d, want 20", loops.CadenceFloorMinutes)
	}

	loops.CadenceFloorMinutes = (&Config{}).loopCadenceFloorMin()
	if loops.CadenceFloorMinutes != loops.DefaultCadenceFloorMinutes {
		t.Errorf("loops.CadenceFloorMinutes with empty config = %d, want default %d", loops.CadenceFloorMinutes, loops.DefaultCadenceFloorMinutes)
	}
}
