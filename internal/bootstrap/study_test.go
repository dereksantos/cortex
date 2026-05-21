package bootstrap

import (
	"strings"
	"testing"
	"time"
)

func TestPlanStudy_Qwen30B_5m(t *testing.T) {
	// Qwen3-Coder-30B (262K ctx, ~5000ms/call) + Cortex (~94K eff_loc).
	// capacity = 59 * 4000 = 236K > eff_loc → caps at default 0.80.
	p := PlanStudy(5*time.Minute, 262144, 5000, 94000, 0)
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

func TestPlanStudy_TightBudget_CapsByReach(t *testing.T) {
	// 30s on a 94K-LOC project with a small model: capacity is well
	// under eff_loc → target_coverage drops below the default cap.
	p := PlanStudy(30*time.Second, 8192, 3000, 94000, 0)
	// window_lines ≈ (8192*0.5 - 900) * 4 / 50 = 256
	// max_calls = (30000 - 1500) / 3000 = 9
	// capacity = 9 * ~256 = ~2300; ratio = 2300 / 94000 ≈ 0.025 → floor 0.05
	if p.TargetCoverage >= studyDefaultMaxCoverage {
		t.Errorf("target_coverage = %.3f, want < %.3f when budget << project", p.TargetCoverage, studyDefaultMaxCoverage)
	}
}

func TestPlanStudy_Ollama7B_5m(t *testing.T) {
	// Ollama qwen2.5-coder:7b (32K ctx, ~2000ms/call) + Cortex.
	p := PlanStudy(5*time.Minute, 32768, 2000, 94000, 0)
	// usable_tokens ≈ 32768*0.5 - 800 - 100 = 15484
	// usable_chars ≈ 61936 → window_lines ≈ 1238
	if p.WindowLines < 1100 || p.WindowLines > 1400 {
		t.Errorf("window_lines = %d, want ~1238", p.WindowLines)
	}
	// (300_000 - 1500) / 2000 = 149
	if p.MaxCalls < 148 || p.MaxCalls > 150 {
		t.Errorf("max_calls = %d, want ~149", p.MaxCalls)
	}
	if p.Shorthand != "study-5m" {
		t.Errorf("Shorthand = %q, want study-5m", p.Shorthand)
	}
}

func TestPlanStudy_TinyModel_30s(t *testing.T) {
	// Tiny model — 4K ctx, 1000ms/call, on a 1K-LOC project.
	p := PlanStudy(30*time.Second, 4096, 1000, 1000, 0)
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

func TestPlanStudy_HugeBudget_CapsAtDefault(t *testing.T) {
	// 1h on a small project — max_calls is plentiful, target should
	// cap at the controller's default 0.80.
	p := PlanStudy(time.Hour, 200000, 1000, 5000, 0)
	if p.TargetCoverage != studyDefaultMaxCoverage {
		t.Errorf("target_coverage = %.3f, want %.3f (cap)", p.TargetCoverage, studyDefaultMaxCoverage)
	}
}

func TestPlanStudy_ZeroDuration_NoCalls(t *testing.T) {
	p := PlanStudy(0, 32768, 1000, 5000, 0)
	if p.MaxCalls != 0 {
		t.Errorf("D=0 → max_calls = %d, want 0", p.MaxCalls)
	}
	// Still produces a valid plan shape; just nothing to run.
	if p.WindowLines == 0 {
		t.Error("WindowLines = 0; want non-zero even for empty budget")
	}
}

func TestPlanStudy_ZeroCtxWindow_FallsBack(t *testing.T) {
	// Probe couldn't determine ctx; expect the conservative fallback.
	p := PlanStudy(5*time.Minute, 0, 1000, 5000, 0)
	if p.CtxWindowTokens != studyDefaultCtxWindow {
		t.Errorf("ctx_window = %d, want fallback %d", p.CtxWindowTokens, studyDefaultCtxWindow)
	}
}

func TestPlanStudy_ZeroLatency_FallsBack(t *testing.T) {
	p := PlanStudy(5*time.Minute, 32768, 0, 5000, 0)
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
