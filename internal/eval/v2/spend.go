//go:build !windows

// Spend tracking for the eval grid. Implements the three-tier ceiling
// system that bounds the user's $20 OpenRouter top-up, plus model-tier
// classification, free-tier preference, and the frontier guard.
//
// All ceilings default to env-driven values; tests inject via t.Setenv.
// Ceiling values are checked BEFORE each cell's call so the harness
// never issues a request the runner would have blocked.
package eval

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env vars for spend control. The CLI surface and runner read these.
const (
	EnvRunUSDCeiling      = "CORTEX_EVAL_RUN_USD_CEILING"
	EnvDailyUSDCeiling    = "CORTEX_EVAL_DAILY_USD_CEILING"
	EnvLifetimeUSDCeiling = "CORTEX_EVAL_LIFETIME_USD_CEILING"
	EnvAllowSpend         = "CORTEX_EVAL_ALLOW_SPEND"
	EnvAllowFrontier      = "CORTEX_EVAL_ALLOW_FRONTIER"
	EnvNoFreePreference   = "CORTEX_EVAL_NO_FREE_PREFERENCE"
)

// Defaults for the $20 OpenRouter top-up. The lifetime ceiling leaves a
// $2 buffer.
const (
	DefaultRunUSDCeiling      = 5.00
	DefaultDailyUSDCeiling    = 8.00
	DefaultLifetimeUSDCeiling = 18.00

	// FrontierCellCostThreshold is the per-cell tier-floor cost above
	// which CORTEX_EVAL_ALLOW_FRONTIER=1 is required. Picked so frontier
	// (tier_floor=$0.90) is gated and large (tier_floor=$0.30) is not.
	FrontierCellCostThreshold = 0.50
)

// Tier groups models by approximate per-cell cost. Used to estimate
// pre-call spend when no actual cost has been observed yet for a
// (provider, model) pair.
type Tier string

const (
	TierFree     Tier = "free"
	TierSmall    Tier = "small"
	TierMedium   Tier = "medium"
	TierLarge    Tier = "large"
	TierFrontier Tier = "frontier"
)

// tierFloor maps each tier to its conservative per-cell floor (USD).
// Real cells consume tokens that scale this up; the floor is the
// estimator's lower bound when no observation exists.
var tierFloor = map[Tier]float64{
	TierFree:     0.00,
	TierSmall:    0.01,
	TierMedium:   0.05,
	TierLarge:    0.30,
	TierFrontier: 0.90,
}

// modelTier hardcodes the curated set from docs/openrouter-tiers.md.
// Unknown models fall back to TierMedium via heuristic. This is
// deliberately a small list — it covers the models we'd actually pin
// in a sweep. Adding a new model means adding it here.
var modelTier = map[string]Tier{
	// Free (`:free` suffix, zero cost).
	"openai/gpt-oss-20b:free":                TierFree,
	"openai/gpt-oss-120b:free":               TierFree,
	"google/gemma-4-26b-a4b-it:free":         TierFree,
	"nvidia/nemotron-nano-9b-v2:free":        TierFree,
	"meta-llama/llama-3.2-3b-instruct:free":  TierFree,
	"meta-llama/llama-3.3-70b-instruct:free": TierFree,
	"qwen/qwen3-coder:free":                  TierFree,

	// Small-paid (~$0.02–$0.10/M).
	"meta-llama/llama-3.1-8b-instruct": TierSmall,
	"mistralai/mistral-nemo":           TierSmall,
	"qwen/qwen-2.5-7b-instruct":        TierSmall,

	// Medium ($0.20–$0.79/M prompt).
	"qwen/qwen3-coder":          TierMedium,
	"anthropic/claude-3-haiku":  TierMedium,
	"openai/gpt-5-mini":         TierMedium,
	"openai/gpt-5.1-codex-mini": TierMedium,
	"deepseek/deepseek-v3.2":    TierMedium,

	// Large ($0.80–$2.99/M prompt) — the $20-budget ceiling tier.
	"anthropic/claude-haiku-4.5":           TierLarge,
	"anthropic/claude-haiku-latest":        TierLarge,
	"google/gemini-2.5-pro":                TierLarge,
	"openai/gpt-5.1":                       TierLarge,
	"openai/gpt-5":                         TierLarge,
	"nousresearch/hermes-3-llama-3.1-405b": TierLarge,

	// Frontier (≥ $3/M prompt) — gated by EnvAllowFrontier.
	"anthropic/claude-sonnet-4.6":     TierFrontier,
	"anthropic/claude-sonnet-latest":  TierFrontier,
	"anthropic/claude-3.7-sonnet":     TierFrontier,
	"anthropic/claude-opus-4.7":       TierFrontier,
	"anthropic/claude-opus-latest":    TierFrontier,
	"openai/gpt-5.5":                  TierFrontier,
	"openai/gpt-4o-2024-05-13":        TierFrontier,
	"openai/gpt-4-turbo":              TierFrontier,
}

// ClassifyModel returns the tier for a model ID. Unknowns fall through
// to a substring heuristic before defaulting to TierMedium.
func ClassifyModel(model string) Tier {
	if strings.HasSuffix(model, ":free") {
		return TierFree
	}
	if t, ok := modelTier[model]; ok {
		return t
	}
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "sonnet"),
		strings.Contains(lower, "opus"),
		strings.Contains(lower, "gpt-5.5"),
		strings.Contains(lower, "gpt-4-turbo"),
		strings.Contains(lower, "gpt-4-1106"):
		return TierFrontier
	case strings.Contains(lower, "haiku-4"),
		strings.Contains(lower, "haiku-latest"),
		strings.Contains(lower, "gemini-2.5-pro"),
		strings.Contains(lower, "qwen3.6-max"),
		(strings.Contains(lower, "gpt-5") && !strings.Contains(lower, "mini")),
		(strings.Contains(lower, "gpt-5.1") && !strings.Contains(lower, "mini")):
		return TierLarge
	}
	return TierMedium
}

// TierFloor returns the conservative per-cell USD floor for a tier.
// Returns 0 for unknown tiers (treats them as free for safety).
func TierFloor(t Tier) float64 {
	return tierFloor[t]
}

// FrontierGuardRequired reports whether issuing a call to this model
// requires CORTEX_EVAL_ALLOW_FRONTIER=1.
func FrontierGuardRequired(model string) bool {
	return tierFloor[ClassifyModel(model)] > FrontierCellCostThreshold
}

// freePair maps the paid identifier to its `:free` variant for known
// models that have both. Used by PreferFreeVariant when the user hasn't
// explicitly pinned the paid form (i.e., env override not set and
// passed model lacks the `:free` suffix already).
var freePair = map[string]string{
	"qwen/qwen3-coder":                  "qwen/qwen3-coder:free",
	"openai/gpt-oss-20b":                "openai/gpt-oss-20b:free",
	"openai/gpt-oss-120b":               "openai/gpt-oss-120b:free",
	"meta-llama/llama-3.2-3b-instruct":  "meta-llama/llama-3.2-3b-instruct:free",
	"meta-llama/llama-3.3-70b-instruct": "meta-llama/llama-3.3-70b-instruct:free",
	"google/gemma-4-26b-a4b-it":         "google/gemma-4-26b-a4b-it:free",
	"nvidia/nemotron-nano-9b-v2":        "nvidia/nemotron-nano-9b-v2:free",
}

// PreferFreeVariant returns the `:free` form of a model if known and
// the input isn't already free. Pass-through for unknown or already-free
// inputs. Callers can disable globally via CORTEX_EVAL_NO_FREE_PREFERENCE=1
// (the runner consults that env var, not this function).
func PreferFreeVariant(model string) string {
	if strings.HasSuffix(model, ":free") {
		return model
	}
	if free, ok := freePair[model]; ok {
		return free
	}
	return model
}

// SpendCeilings holds the three independent USD limits.
type SpendCeilings struct {
	Run      float64
	Daily    float64
	Lifetime float64
}

// CeilingsFromEnv reads the three env vars, defaulting each independently.
func CeilingsFromEnv() SpendCeilings {
	return SpendCeilings{
		Run:      envFloat(EnvRunUSDCeiling, DefaultRunUSDCeiling),
		Daily:    envFloat(EnvDailyUSDCeiling, DefaultDailyUSDCeiling),
		Lifetime: envFloat(EnvLifetimeUSDCeiling, DefaultLifetimeUSDCeiling),
	}
}

func envFloat(key string, fallback float64) float64 {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return v
		}
	}
	return fallback
}

// SpendTracker holds the current run's spend + last-observed-cost cache
// per (provider, model) pair. Persistence-backed for daily/lifetime.
type SpendTracker struct {
	Ceilings   SpendCeilings
	Persister  *Persister
	runSpend   float64
	knownCosts map[string]float64
}

// NewSpendTracker constructs a tracker bound to a persister. The
// persister's daily_spend table provides daily/lifetime totals across
// invocations.
func NewSpendTracker(p *Persister, c SpendCeilings) *SpendTracker {
	return &SpendTracker{
		Ceilings:   c,
		Persister:  p,
		knownCosts: make(map[string]float64),
	}
}

func costKey(provider, model string) string { return provider + "/" + model }

// EstimateCost returns max(last observed for this pair, 1.5 × tier_floor).
// First-call estimate biases pessimistic so the first ceiling check has
// useful data even before any cell has run.
func (s *SpendTracker) EstimateCost(provider, model string) float64 {
	last := s.knownCosts[costKey(provider, model)]
	floor := 1.5 * tierFloor[ClassifyModel(model)]
	if last > floor {
		return last
	}
	return floor
}

// RunSpend returns the running total for the current grid run.
func (s *SpendTracker) RunSpend() float64 { return s.runSpend }

// CheckBeforeCall reports which ceiling (if any) would be exceeded by
// adding `estimate` USD to the running totals. Returns "" when all
// ceilings have headroom.
//
// Returns the daily/lifetime totals it observed so callers can include
// them in error messages without a second DB roundtrip.
func (s *SpendTracker) CheckBeforeCall(estimate float64) (tripped string, daily, lifetime float64, err error) {
	if s.runSpend+estimate > s.Ceilings.Run {
		return "run", 0, 0, nil
	}
	d, err := s.Persister.GetDailySpendUTC(time.Now())
	if err != nil {
		return "", 0, 0, fmt.Errorf("daily spend lookup: %w", err)
	}
	if d+estimate > s.Ceilings.Daily {
		return "daily", d, 0, nil
	}
	l, err := s.Persister.GetLifetimeSpend()
	if err != nil {
		return "", d, 0, fmt.Errorf("lifetime spend lookup: %w", err)
	}
	if l+estimate > s.Ceilings.Lifetime {
		return "lifetime", d, l, nil
	}
	return "", d, l, nil
}

// RecordCell adds the cell's actual cost to run-level + persists daily.
// Caches per-(provider, model) so future EstimateCost calls in this run
// reflect observed reality.
func (s *SpendTracker) RecordCell(provider, model string, cost float64) error {
	s.runSpend += cost
	s.knownCosts[costKey(provider, model)] = cost
	return s.Persister.AddDailySpendUTC(time.Now(), cost)
}
