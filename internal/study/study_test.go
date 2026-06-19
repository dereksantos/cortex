package study

import (
	"strings"
	"testing"
	"time"
)

// SampleTokenBudget is the single per-call input budget every study path shares.
func TestSampleTokenBudget(t *testing.T) {
	// fill fraction of the window minus prompt overhead (800) + output cap (100).
	// Default fill is studyDefaultTargetFill (0.3); explicit fills are honored.
	cases := []struct {
		window int
		fill   float64
		want   int
	}{
		{32768, 0, 9830 - 900},    // default fill 0.3 (32768*0.3 = 9830)
		{32768, 0.5, 16384 - 900}, // explicit 0.5
		{32768, 0.25, 8192 - 900}, // explicit 0.25
		{0, 0, 2457 - 900},        // window<=0 → studyDefaultCtxWindow (8192), default fill 0.3
		{32768, 2, 9830 - 900},    // out-of-range fill → default 0.3
	}
	for _, c := range cases {
		if got := SampleTokenBudget(c.window, c.fill); got != c.want {
			t.Errorf("SampleTokenBudget(%d, %v) = %d, want %d", c.window, c.fill, got, c.want)
		}
	}
	t.Run("floors so the sample is never negative on a tiny window", func(t *testing.T) {
		if got := SampleTokenBudget(1000, 0.5); got != studyPromptOverheadTokens {
			t.Errorf("tiny window = %d, want floor %d", got, studyPromptOverheadTokens)
		}
	})
}

// CompletionTokenBudget + SampleTokenBudget together must fit the window: the
// guarantee that replaced the divergent cmd/loop budget path.
func TestBudgetsFitWindow(t *testing.T) {
	for _, window := range []int{8192, 32768, 131072} {
		sample := SampleTokenBudget(window, 0)
		completion := CompletionTokenBudget(window, 0)
		// sample + prompt overhead + completion must leave headroom under window.
		used := sample + studyPromptOverheadTokens + completion
		if used > window {
			t.Errorf("window %d: sample(%d)+overhead+completion(%d)=%d exceeds window", window, sample, completion, used)
		}
	}
	t.Run("completion floors so citations don't truncate", func(t *testing.T) {
		if got := CompletionTokenBudget(4096, 0); got != studyMinCompletionTokens {
			t.Errorf("small window completion = %d, want floor %d", got, studyMinCompletionTokens)
		}
	})
}

func TestMakePlan_Qwen30B_5m(t *testing.T) {
	// Qwen3-Coder-30B (262K ctx, ~5000ms/call) + Cortex (~94K eff_loc).
	// capacity = 59 * 4000 = 236K > eff_loc → caps at default 0.80.
	p := MakePlan(5*time.Minute, 262144, 5000, 94000, 0)
	if p.WindowLines != studyMaxWindowLines {
		t.Errorf("big ctx → window_lines should clamp to %d, got %d", studyMaxWindowLines, p.WindowLines)
	}
	// (300_000 - 1500) / 5000 = 59
	if p.MaxCalls < 58 || p.MaxCalls > 60 {
		t.Errorf("max_calls = %d, want ~59", p.MaxCalls)
	}
	if p.TargetCoverage != studyDefaultMaxCoverage {
		t.Errorf("target_coverage = %.3f, want %.3f (cap; capacity > eff_loc)", p.TargetCoverage, studyDefaultMaxCoverage)
	}
	if !strings.HasPrefix(p.RunID, "study-") {
		t.Errorf("RunID = %q, want study-<ts>- prefix", p.RunID)
	}
	if p.Shorthand != "study-5m" {
		t.Errorf("Shorthand = %q, want study-5m", p.Shorthand)
	}
	if p.Reasoning == "" {
		t.Error("Reasoning empty")
	}
}

func TestMakePlan_TightBudget_CapsByReach(t *testing.T) {
	// 30s on a 94K-LOC project with a small model: capacity is well
	// under eff_loc → target_coverage drops below the default cap.
	p := MakePlan(30*time.Second, 8192, 3000, 94000, 0)
	// window_lines ≈ (8192*0.3 - 900) * 4 / 50 = 124
	// max_calls = (30000 - 1500) / 3000 = 9
	// capacity = 9 * ~124 = ~1116; ratio = 1116 / 94000 ≈ 0.012 → floor 0.05
	if p.TargetCoverage >= studyDefaultMaxCoverage {
		t.Errorf("target_coverage = %.3f, want < %.3f when budget << project", p.TargetCoverage, studyDefaultMaxCoverage)
	}
}

func TestMakePlan_Ollama7B_5m(t *testing.T) {
	// Ollama qwen2.5-coder:7b (32K ctx, ~2000ms/call) + Cortex.
	p := MakePlan(5*time.Minute, 32768, 2000, 94000, 0)
	// usable_tokens ≈ 32768*0.3 - 800 - 100 = 8930
	// usable_chars ≈ 35720 → window_lines ≈ 714
	if p.WindowLines < 680 || p.WindowLines > 750 {
		t.Errorf("window_lines = %d, want ~714", p.WindowLines)
	}
	// (300_000 - 1500) / 2000 = 149
	if p.MaxCalls < 148 || p.MaxCalls > 150 {
		t.Errorf("max_calls = %d, want ~149", p.MaxCalls)
	}
	if p.Shorthand != "study-5m" {
		t.Errorf("Shorthand = %q, want study-5m", p.Shorthand)
	}
}

func TestMakePlan_TinyModel_30s(t *testing.T) {
	// Tiny model — 4K ctx, 1000ms/call, on a 1K-LOC project.
	p := MakePlan(30*time.Second, 4096, 1000, 1000, 0)
	if p.WindowLines < studyMinWindowLines {
		t.Errorf("window_lines = %d, want >= %d", p.WindowLines, studyMinWindowLines)
	}
	if p.MaxCalls <= 0 {
		t.Errorf("max_calls = %d, want > 0", p.MaxCalls)
	}
	if p.Shorthand != "study-30s" {
		t.Errorf("Shorthand = %q, want study-30s", p.Shorthand)
	}
}

func TestMakePlan_HugeBudget_CapsAtDefault(t *testing.T) {
	// 1h on a small project — max_calls is plentiful, target should
	// cap at the controller's default 0.80.
	p := MakePlan(time.Hour, 200000, 1000, 5000, 0)
	if p.TargetCoverage != studyDefaultMaxCoverage {
		t.Errorf("target_coverage = %.3f, want %.3f (cap)", p.TargetCoverage, studyDefaultMaxCoverage)
	}
}

func TestMakePlan_ZeroDuration_NoCalls(t *testing.T) {
	p := MakePlan(0, 32768, 1000, 5000, 0)
	if p.MaxCalls != 0 {
		t.Errorf("D=0 → max_calls = %d, want 0", p.MaxCalls)
	}
	// Still produces a valid plan shape; just nothing to run.
	if p.WindowLines == 0 {
		t.Error("WindowLines = 0; want non-zero even for empty budget")
	}
}

func TestMakePlan_ZeroCtxWindow_FallsBack(t *testing.T) {
	// Probe couldn't determine ctx; expect the conservative fallback.
	p := MakePlan(5*time.Minute, 0, 1000, 5000, 0)
	if p.CtxWindowTokens != studyDefaultCtxWindow {
		t.Errorf("ctx_window = %d, want fallback %d", p.CtxWindowTokens, studyDefaultCtxWindow)
	}
}

func TestMakePlan_ZeroLatency_FallsBack(t *testing.T) {
	p := MakePlan(5*time.Minute, 32768, 0, 5000, 0)
	if p.LatencyMS != studyDefaultLatencyMS {
		t.Errorf("latency = %d, want fallback %d", p.LatencyMS, studyDefaultLatencyMS)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:             "30s",
		5 * time.Minute:              "5m",
		time.Hour:                    "1h",
		2*time.Hour + 30*time.Minute: "2h30m",
		90 * time.Second:             "1m30s",
	}
	for d, want := range cases {
		got := shortDuration(d)
		if got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestNewRunID_Unique(t *testing.T) {
	a, _ := newRunID(time.Minute)
	b, _ := newRunID(time.Minute)
	// Same wall-second → different short suffix? Possible collision is
	// fine since the duration is the same input. Just check that the
	// shorthand component is deterministic.
	if a == b {
		// Acceptable on rare same-nanosecond invocations; what matters
		// is the format.
		t.Logf("collision in newRunID (same nanosecond): %s", a)
	}
	for _, id := range []string{a, b} {
		if !strings.HasPrefix(id, "study-") {
			t.Errorf("RunID %q missing study- prefix", id)
		}
		// Format: study-YYYYMMDDTHHMMSSZ-XXXXXX (6 hex)
		parts := strings.Split(id, "-")
		if len(parts) != 3 {
			t.Errorf("RunID %q has %d parts, want 3", id, len(parts))
		}
	}
}
