package study

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Plan is what the planner derives from a duration + model
// capability + observed latency. Pure data — no I/O, no side effects.
//
// All knobs the controller cares about (WindowLines, WindowOverlap,
// BatchSize, TargetCoverage, BudgetMax) can be lifted directly into
// ControllerConfig. RunID + Shorthand flow through Config so every
// emitted dream.insight is tagged for later comparison.
type Plan struct {
	Duration        time.Duration
	WindowLines     int     // derived from model ctx window
	WindowOverlap   int     // proportional to WindowLines
	BatchSize       int     // 1 in v1 (sequential); knob for future parallelism
	MaxCalls        int     // duration / latency_p50
	TargetCoverage  float64 // capped so we don't aim above what MaxCalls can reach
	RunID           string  // study-<ts>-<short>
	Shorthand       string  // study-5m
	Reasoning       string  // human-readable derivation trace
	TargetFill      float64 // fraction of ctx window the chunk targets
	CtxWindowTokens int
	LatencyMS       int
	ProjectEffLOC   int
}

// MakePlan derivation constants. Inline-documented so the algorithm
// is self-explanatory at the call site.
const (
	studyPromptOverheadTokens = 800  // extract_overview prompt + system
	studyOutputCapTokens      = 100  // MaxOutputBudget from template loader
	studyDefaultTargetFill    = 0.4  // 40% of the window for the sample; leaves headroom for the prompt envelope + output, and trims per-call tokens for speed/cost (small-window models especially)
	studyDefaultLatencyMS     = 3000 // when probe gives us nothing
	studyDefaultCtxWindow     = 8192 // conservative fallback ctx window
	studyStartupMS            = 1500 // analyzer walk + meta insight emission
	studyMinWindowLines       = 50
	studyMaxWindowLines       = 4000
	studyMinTargetCoverage    = 0.05
	studyDefaultMaxCoverage   = 0.80 // controller's own cap
	studyCharsPerToken        = 4    // rough English/code average
	studyCharsPerLine         = 50   // rough average line width
	studyMinCompletionTokens  = 2048 // completion floor; below this, citation JSON truncates mid-array
)

// SampleTokenBudget is the single per-call INPUT budget shared by every study
// path: how many tokens of sampled content one inference may carry, after
// reserving prompt overhead and the output cap from a conservative fraction
// (fill, default studyDefaultTargetFill = 0.5) of the window. MakePlan and the
// agent study tool both derive from this one formula, so a sample can never
// exceed what the window holds — there is no second, divergent budget. A
// non-positive window or out-of-range fill falls back to the defaults.
func SampleTokenBudget(window int, fill float64) int {
	if window <= 0 {
		window = studyDefaultCtxWindow
	}
	if fill <= 0 || fill > 1 {
		fill = studyDefaultTargetFill
	}
	u := int(float64(window)*fill) - studyPromptOverheadTokens - studyOutputCapTokens
	if u < studyPromptOverheadTokens {
		u = studyPromptOverheadTokens
	}
	return u
}

// CompletionTokenBudget is the matching per-call OUTPUT cap (the inference's
// max_tokens): the room the window has left after the input sample, halved so
// the prompt and the digest+citations response never collide, floored at
// studyMinCompletionTokens so a small window still returns citations instead of
// truncating the JSON mid-array.
func CompletionTokenBudget(window int, fill float64) int {
	if window <= 0 {
		window = studyDefaultCtxWindow
	}
	c := (window - SampleTokenBudget(window, fill)) / 2
	if c < studyMinCompletionTokens {
		c = studyMinCompletionTokens
	}
	return c
}

// MakePlan derives a Plan from a requested duration, the model's
// context window in tokens, the calibrated per-call latency in ms, and
// the project's effective LOC. All inputs come from cheap probes; no
// LLM calls.
//
// targetFill is the fraction of the context window each chunk should
// consume. Pass 0 for the default 0.5.
//
// The derivation is intentionally simple — every step is a single
// arithmetic transform, documented inline so the Reasoning trace can
// reproduce it verbatim for the meta insight.
func MakePlan(d time.Duration, ctxWindowTokens, latencyMS, projectEffLOC int, targetFill float64) Plan {
	if ctxWindowTokens <= 0 {
		ctxWindowTokens = studyDefaultCtxWindow
	}
	if latencyMS <= 0 {
		latencyMS = studyDefaultLatencyMS
	}
	if targetFill <= 0 || targetFill > 1 {
		targetFill = studyDefaultTargetFill
	}

	// Step 1 — usable token budget per call (the shared budget formula:
	// conservative fraction of the window minus prompt overhead + output cap).
	usableTokens := SampleTokenBudget(ctxWindowTokens, targetFill)

	// Step 2 — chars → lines, clamped to [50, 4000] to keep the chunk
	// shape sensible. Tiny window models still get something readable;
	// big-ctx models cap out at one big slice.
	usableChars := usableTokens * studyCharsPerToken
	windowLines := usableChars / studyCharsPerLine
	if windowLines < studyMinWindowLines {
		windowLines = studyMinWindowLines
	}
	if windowLines > studyMaxWindowLines {
		windowLines = studyMaxWindowLines
	}
	windowOverlap := windowLines / 10

	// Step 3 — max LLM calls fit in the wall-clock budget after a
	// fixed startup cost (analyzer walk + meta insight).
	maxCalls := 0
	if latencyMS > 0 {
		budgetMS := int(d.Milliseconds()) - studyStartupMS
		if budgetMS > 0 {
			maxCalls = budgetMS / latencyMS
		}
	}
	if maxCalls < 0 {
		maxCalls = 0
	}

	// Step 4 — target coverage cap.
	//   target = min(default_cap, capacity / eff_loc)
	// capacity = max_calls * window_lines; the ratio is how much of the
	// project a single pass can plausibly touch. Big-ctx or long-D runs
	// have capacity > eff_loc (ratio ≥ 1) so target hits the default
	// 0.80 cap; tiny budgets on big projects get a small ratio and a
	// proportionally smaller target so the controller halts at the
	// realistic ceiling instead of spinning past it.
	targetCoverage := studyDefaultMaxCoverage
	if projectEffLOC > 0 && maxCalls > 0 && windowLines > 0 {
		capacity := float64(maxCalls * windowLines)
		reachable := capacity / float64(projectEffLOC)
		if reachable < targetCoverage {
			targetCoverage = reachable
		}
	}
	if targetCoverage < studyMinTargetCoverage {
		targetCoverage = studyMinTargetCoverage
	}

	runID, shorthand := newRunID(d)
	plan := Plan{
		Duration:        d,
		WindowLines:     windowLines,
		WindowOverlap:   windowOverlap,
		BatchSize:       1,
		MaxCalls:        maxCalls,
		TargetCoverage:  targetCoverage,
		RunID:           runID,
		Shorthand:       shorthand,
		TargetFill:      targetFill,
		CtxWindowTokens: ctxWindowTokens,
		LatencyMS:       latencyMS,
		ProjectEffLOC:   projectEffLOC,
	}
	plan.Reasoning = renderReasoning(plan)
	return plan
}

// renderReasoning produces a single human-readable line describing
// how the plan's knobs came out. Echoed to stdout + inlined into the
// meta insight so post-hoc analysis can see the derivation.
func renderReasoning(p Plan) string {
	return fmt.Sprintf(
		"duration=%s ctx_window=%d tokens latency=%dms target_fill=%.2f → window_lines=%d overlap=%d max_calls=%d target_coverage=%.2f run_id=%s",
		p.Duration, p.CtxWindowTokens, p.LatencyMS, p.TargetFill,
		p.WindowLines, p.WindowOverlap, p.MaxCalls, p.TargetCoverage, p.RunID,
	)
}

// newRunID returns a (run_id, shorthand) pair. run_id is unique per
// invocation: "study-<UTC-yyMMddTHHmmssZ>-<short>". Shorthand is the
// human-readable duration ("study-5m", "study-30s"). The short suffix
// is the first 6 hex chars of sha256(timestamp + duration) — enough
// to disambiguate two studies started in the same wall-second.
func newRunID(d time.Duration) (string, string) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", ts, d)
	short := hex.EncodeToString(h.Sum(nil))[:6]
	runID := "study-" + ts + "-" + short
	shorthand := "study-" + shortDuration(d)
	return runID, shorthand
}

// shortDuration renders d as a compact suffix for the shorthand tag.
// Emits the same form a user would type: 30s, 5m, 1h, 2h30m, 1m30s.
// Sub-second components are dropped — study is wall-clock-budget;
// nobody asks for "5m250ms".
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	total := int(d / time.Second)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	var sb strings.Builder
	if h > 0 {
		fmt.Fprintf(&sb, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&sb, "%dm", m)
	}
	if s > 0 || sb.Len() == 0 {
		fmt.Fprintf(&sb, "%ds", s)
	}
	return sb.String()
}
