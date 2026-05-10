//go:build !windows

package eval

import (
	"math"
	"testing"
	"time"
)

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestClassifyModel(t *testing.T) {
	tests := []struct {
		model string
		want  Tier
	}{
		// Free
		{"openai/gpt-oss-20b:free", TierFree},
		{"qwen/qwen3-coder:free", TierFree},
		{"some-future-model:free", TierFree},

		// Curated table
		{"meta-llama/llama-3.1-8b-instruct", TierSmall},
		{"qwen/qwen3-coder", TierMedium},
		{"anthropic/claude-3-haiku", TierMedium},
		{"anthropic/claude-haiku-4.5", TierLarge},
		{"google/gemini-2.5-pro", TierLarge},
		{"anthropic/claude-sonnet-4.6", TierFrontier},
		{"anthropic/claude-opus-4.7", TierFrontier},
		{"openai/gpt-5.5", TierFrontier},

		// Heuristic fallback
		{"anthropic/claude-sonnet-X-future", TierFrontier},
		{"anthropic/claude-opus-X-future", TierFrontier},
		{"anthropic/claude-haiku-4-future", TierLarge},
		{"unknown/random-model", TierMedium},
		{"", TierMedium},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			got := ClassifyModel(tc.model)
			if got != tc.want {
				t.Errorf("ClassifyModel(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

func TestFrontierGuardRequired(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"openai/gpt-oss-20b:free", false},               // free: floor=0
		{"meta-llama/llama-3.1-8b-instruct", false},      // small: floor=0.01
		{"qwen/qwen3-coder", false},                      // medium: floor=0.05
		{"anthropic/claude-haiku-4.5", false},            // large: floor=0.30 ≤ 0.50
		{"anthropic/claude-sonnet-4.6", true},            // frontier: floor=0.90 > 0.50
		{"anthropic/claude-opus-4.7", true},
		{"openai/gpt-5.5", true},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			got := FrontierGuardRequired(tc.model)
			if got != tc.want {
				t.Errorf("FrontierGuardRequired(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestPreferFreeVariant(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"qwen/qwen3-coder", "qwen/qwen3-coder:free"},
		{"qwen/qwen3-coder:free", "qwen/qwen3-coder:free"}, // already free
		{"openai/gpt-oss-20b", "openai/gpt-oss-20b:free"},
		{"anthropic/claude-haiku-4.5", "anthropic/claude-haiku-4.5"}, // no free variant
		{"unknown/model", "unknown/model"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := PreferFreeVariant(tc.in)
			if got != tc.want {
				t.Errorf("PreferFreeVariant(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCeilingsFromEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv(EnvRunUSDCeiling, "")
		t.Setenv(EnvDailyUSDCeiling, "")
		t.Setenv(EnvLifetimeUSDCeiling, "")
		c := CeilingsFromEnv()
		if c.Run != DefaultRunUSDCeiling {
			t.Errorf("Run=%v want %v", c.Run, DefaultRunUSDCeiling)
		}
		if c.Daily != DefaultDailyUSDCeiling {
			t.Errorf("Daily=%v want %v", c.Daily, DefaultDailyUSDCeiling)
		}
		if c.Lifetime != DefaultLifetimeUSDCeiling {
			t.Errorf("Lifetime=%v want %v", c.Lifetime, DefaultLifetimeUSDCeiling)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		t.Setenv(EnvRunUSDCeiling, "1.50")
		t.Setenv(EnvDailyUSDCeiling, "2.50")
		t.Setenv(EnvLifetimeUSDCeiling, "10.00")
		c := CeilingsFromEnv()
		if c.Run != 1.50 || c.Daily != 2.50 || c.Lifetime != 10.00 {
			t.Errorf("got %+v, want {1.50, 2.50, 10.00}", c)
		}
	})

	t.Run("malformed env falls back to default", func(t *testing.T) {
		t.Setenv(EnvRunUSDCeiling, "not-a-number")
		c := CeilingsFromEnv()
		if c.Run != DefaultRunUSDCeiling {
			t.Errorf("Run=%v want default %v", c.Run, DefaultRunUSDCeiling)
		}
	})
}

func TestSpendTracker_EstimateCost_FloorBeforeObservation(t *testing.T) {
	p := newTestPersister(t)
	tracker := NewSpendTracker(p, SpendCeilings{Run: 100, Daily: 100, Lifetime: 100})

	// No observations yet — estimate is 1.5 × tier_floor.
	cases := []struct {
		provider, model string
		want            float64
	}{
		{ProviderOpenRouter, "openai/gpt-oss-20b:free", 0.0},                      // free: 1.5 × 0 = 0
		{ProviderOpenRouter, "meta-llama/llama-3.1-8b-instruct", 0.015},           // small: 1.5 × 0.01
		{ProviderOpenRouter, "qwen/qwen3-coder", 0.075},                           // medium: 1.5 × 0.05
		{ProviderOpenRouter, "anthropic/claude-haiku-4.5", 0.45},                  // large: 1.5 × 0.30
		{ProviderOpenRouter, "anthropic/claude-sonnet-4.6", 1.35},                 // frontier: 1.5 × 0.90
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := tracker.EstimateCost(tc.provider, tc.model)
			if !nearlyEqual(got, tc.want) {
				t.Errorf("EstimateCost(%q,%q)=%v want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestSpendTracker_EstimateCost_ObservationOverridesFloor(t *testing.T) {
	p := newTestPersister(t)
	tracker := NewSpendTracker(p, SpendCeilings{Run: 100, Daily: 100, Lifetime: 100})

	if err := tracker.RecordCell(ProviderOpenRouter, "qwen/qwen3-coder", 0.42); err != nil {
		t.Fatalf("RecordCell: %v", err)
	}

	// Observed (0.42) > floor (1.5 × 0.05 = 0.075), so estimate should
	// be 0.42 — the recent reality, not the conservative floor.
	got := tracker.EstimateCost(ProviderOpenRouter, "qwen/qwen3-coder")
	if got != 0.42 {
		t.Errorf("EstimateCost after observation = %v, want 0.42", got)
	}

	// A pair that hasn't been observed still uses the floor.
	got = tracker.EstimateCost(ProviderOpenRouter, "qwen/qwen3-coder:free")
	if got != 0.0 {
		t.Errorf("free model estimate = %v, want 0.0", got)
	}
}

func TestSpendTracker_DailyAndLifetimeAccumulate(t *testing.T) {
	p := newTestPersister(t)
	tracker := NewSpendTracker(p, SpendCeilings{Run: 100, Daily: 100, Lifetime: 100})

	if err := tracker.RecordCell(ProviderOpenRouter, "qwen/qwen3-coder", 0.10); err != nil {
		t.Fatalf("RecordCell: %v", err)
	}
	if err := tracker.RecordCell(ProviderOpenRouter, "qwen/qwen3-coder", 0.15); err != nil {
		t.Fatalf("RecordCell: %v", err)
	}

	daily, err := p.GetDailySpendUTC(time.Now())
	if err != nil {
		t.Fatalf("GetDailySpendUTC: %v", err)
	}
	if daily != 0.25 {
		t.Errorf("daily=%v want 0.25", daily)
	}

	lifetime, err := p.GetLifetimeSpend()
	if err != nil {
		t.Fatalf("GetLifetimeSpend: %v", err)
	}
	if lifetime != 0.25 {
		t.Errorf("lifetime=%v want 0.25", lifetime)
	}

	if got := tracker.RunSpend(); got != 0.25 {
		t.Errorf("RunSpend()=%v want 0.25", got)
	}
}

func TestSpendTracker_CheckBeforeCall(t *testing.T) {
	p := newTestPersister(t)

	// Run ceiling = $0.50, daily = $1.00, lifetime = $5.00. The order
	// CheckBeforeCall evaluates is run → daily → lifetime, so the
	// tightest one trips first.
	tracker := NewSpendTracker(p, SpendCeilings{Run: 0.50, Daily: 1.00, Lifetime: 5.00})

	t.Run("under all ceilings", func(t *testing.T) {
		tripped, _, _, err := tracker.CheckBeforeCall(0.10)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if tripped != "" {
			t.Errorf("tripped=%q want empty", tripped)
		}
	})

	t.Run("run ceiling trips first", func(t *testing.T) {
		// Already at $0.40 in run; another $0.20 would push to $0.60 > $0.50.
		_ = tracker.RecordCell(ProviderOpenRouter, "x", 0.40)
		tripped, _, _, err := tracker.CheckBeforeCall(0.20)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if tripped != "run" {
			t.Errorf("tripped=%q want %q", tripped, "run")
		}
	})
}
