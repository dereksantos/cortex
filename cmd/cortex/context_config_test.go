// context_config_test.go covers the POINT of Derek's option 2 decision: every
// context.* fraction combo validateContextConfig accepts must actually keep
// the hardcoded 0.8 compact trigger (compactThreshold, main.go) unreachable
// under normal demotion — not just satisfy the arithmetic inequality on
// paper. It also pins that /context (context_cmd.go) renders the resolved,
// configured watermark numbers rather than a label baked in for the
// historical hardcoded W/2, W/3, W/8 values. See docs/configuration.md's
// `context.*` section and docs/context-architecture.md's dated amendment.
package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
)

// worstCaseFillRatio builds a session-shaped scenario (reusing the same
// internal/cache fixtures context_eval_test.go and context_cmd_test.go use —
// cache.OutlineEntry, cache.TurnSpan, cache.RenderOutline, cs.newWorkingSet,
// cs.renderOutlineBlock) at window w under cfg, fills every zone to its
// worst-case ceiling under NORMAL demotion, and returns the resulting
// simulated prompt fraction of w:
//
//   - envelope: the system prompt at maxInstructionBytes (a maximally-sized
//     AGENTS.md — the worst case the envelope half of contextPrefixHeadroom
//     covers).
//   - outline: filled with realistic entries up to (but not over) its
//     resolved budget — the ceiling foldOutlineIfNeeded enforces.
//   - memory index: memoryIndexCap chars (the worst case the index half of
//     contextPrefixHeadroom covers).
//   - tail: one turn sized to exactly the resolved high watermark — the
//     ceiling DemoteBatch enforces before it fires and drains.
//
// This is precisely the "envelope + outline cap + index + tail high-watermark"
// bound docs/context-architecture.md's Budgets section names, computed from
// the SAME functions the live session uses (not a duplicated formula).
func worstCaseFillRatio(t *testing.T, cfg ContextConfig, w int) float64 {
	t.Helper()
	c := &Config{Context: cfg}
	cs := &CortexSession{Window: w, Config: c}
	sys := strings.Repeat("a", maxInstructionBytes)
	cs.Request = &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: sys}}}
	cs.ws = cs.newWorkingSet(1)

	// Outline: pack realistic entries up to just under the resolved budget —
	// the same shape turnOutlineEntry produces (demote.go).
	outlineBudget := c.outlineBudget(w)
	sample := cache.OutlineEntry{
		Turn:      1,
		User:      strings.Repeat("u", 200),
		Actions:   []string{"edit a.go [ok]", "bash go test ./... [ok]"},
		ReplyHead: "done, tests green",
		Citation:  "@session/20260719-abcdef#m1-8",
	}
	sampleTok := cache.TokensOf(len(cache.RenderOutline([]cache.OutlineEntry{sample})))
	if sampleTok < 1 {
		sampleTok = 1
	}
	n := outlineBudget / sampleTok
	entries := make([]cache.OutlineEntry, n)
	for i := range entries {
		e := sample
		e.Turn = i + 1
		entries[i] = e
	}
	cs.outline = entries

	// Memory index: worst case at its cap (limits.memory_index_cap_chars'
	// historical default — tool_deps.go's memoryIndexCap).
	indexTokens := cache.TokensOf(memoryIndexCap)

	// Tail: one turn right at the resolved high watermark — DemoteBatch's own
	// ceiling immediately before it fires and drains to the low watermark.
	high, _ := cs.ws.GetWatermarks()
	if high > 0 {
		cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: high})
	}

	total := cache.TokensOf(len(sys)) + cache.TokensOf(len(cs.renderOutlineBlock())) + indexTokens + cs.ws.TailTokens()
	return float64(total) / float64(w)
}

// TestContextConfigDormancyInvariant is the point of option 2: for every
// context.* combo validateContextConfig ACCEPTS, a worst-case-but-normal
// zone fill must stay strictly under the compact trigger (compactThreshold =
// 0.8) — never merely "the inequality balances on paper". Checked at
// fallbackWindow (the window contextPrefixHeadroom's own derivation is
// pinned to — see config.go) and a larger window, where the fixed-byte
// envelope/index overhead is an even smaller fraction and so strictly safer.
func TestContextConfigDormancyInvariant(t *testing.T) {
	f := floatPtr
	accepted := []struct {
		name string
		cfg  ContextConfig
	}{
		{"today's defaults (all unset)", ContextConfig{}},
		{"explicit defaults (0.5/0.333/0.125)", ContextConfig{TailHighFraction: f(0.5), TailDrainFraction: f(1.0 / 3.0), OutlineFraction: f(0.125)}},
		{"tighter tail, more outline", ContextConfig{TailHighFraction: f(0.4), TailDrainFraction: f(0.25), OutlineFraction: f(0.15)}},
		{"generous tail, thin outline", ContextConfig{TailHighFraction: f(0.55), TailDrainFraction: f(0.3), OutlineFraction: f(0.08)}},
		{"only tail_high_fraction set", ContextConfig{TailHighFraction: f(0.45)}},
		{"only outline_fraction set", ContextConfig{OutlineFraction: f(0.1)}},
		{"right at the dormancy boundary", ContextConfig{TailHighFraction: f(0.5), OutlineFraction: f(0.14)}},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateContextConfig(tc.cfg); err != nil {
				t.Fatalf("precondition failed: %+v should be accepted by validateContextConfig, got %v", tc.cfg, err)
			}
			for _, w := range []int{fallbackWindow, 131072} {
				ratio := worstCaseFillRatio(t, tc.cfg, w)
				if ratio >= compactThreshold {
					t.Errorf("window %d: worst-case normal-demotion zone fill reaches %.4f of the window — the compact trigger (%.2f) would be reachable, defeating the point of the dormancy inequality", w, ratio, compactThreshold)
				}
			}
		})
	}
}

// TestContextReportRendersConfiguredFractions pins that /context's tail
// legend row (context_cmd.go's tailLegendDetail, which folds the old
// separate watermarks row in) reflects a NON-default context.* config end
// to end — the resolved absolute watermarks it reads straight off
// ws.GetWatermarks() — rather than the historical hardcoded "W/2"/"W/3"
// figures that would silently go stale once these fractions became
// configurable.
func TestContextReportRendersConfiguredFractions(t *testing.T) {
	f := floatPtr
	cfg := &Config{Context: ContextConfig{TailHighFraction: f(0.4), TailDrainFraction: f(0.2), OutlineFraction: f(0.1)}}
	cs := &CortexSession{
		Window: 10000,
		Config: cfg,
		Request: &AgentRequest{
			Model:    "context-report-fraction-test",
			Messages: []Message{{Role: RoleSystem, Content: "sys"}},
		},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 10})
	cs.outline = []cache.OutlineEntry{{Turn: 1, User: "u", Actions: []string{"edit a.go [ok]"}, ReplyHead: "done", Citation: "@session/x#m1-2"}}

	w := cs.windowSize()
	wantHigh := cfg.tailHighWatermark(w)
	wantLow := cfg.tailDrainWatermark(w)
	wantOutlineCap := cfg.outlineBudget(w)
	if wantHigh != 4000 || wantLow != 2000 || wantOutlineCap != 1000 {
		t.Fatalf("resolver sanity check failed: high=%d low=%d outlineCap=%d, want 4000/2000/1000", wantHigh, wantLow, wantOutlineCap)
	}

	got := cs.contextReport()
	if !strings.Contains(got, "demote >"+humanK(wantHigh)) {
		t.Errorf("contextReport() should render the configured high watermark (%d); got:\n%s", wantHigh, got)
	}
	if !strings.Contains(got, "drain to "+humanK(wantLow)) {
		t.Errorf("contextReport() should render the configured drain watermark (%d); got:\n%s", wantLow, got)
	}
	// Negative check: the historical hardcoded fraction labels must never
	// reappear now that a non-default config is in play.
	for _, stale := range []string{"(W/2)", "(W/3)", "= W/8", "×W"} {
		if strings.Contains(got, stale) {
			t.Errorf("contextReport() still contains the stale hardcoded label %q with a configured non-default context; got:\n%s", stale, got)
		}
	}
}
