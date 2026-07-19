package tools

import "testing"

// TestDefaultLimitsMatchHistoricalConstants pins that DefaultLimits()
// reproduces every constant it replaced — the zero-config contract every
// caller (including every other test in this package) relies on.
func TestDefaultLimitsMatchHistoricalConstants(t *testing.T) {
	def := DefaultLimits()
	want := Limits{
		CurationBudgetTokens: 16000,
		MaxToolOutput:        10000,
		OutlineDefaultBudget: 4000,
		DefaultRangeLines:    200,
		MaxRangeLines:        800,
		MaxReadBytes:         24000,
		GrepMaxHits:          100,
		GrepLineCap:          1200,
		GrepMaxOutputBytes:   6000,
		FetchTimeoutSec:      20,
		FetchMaxRedirects:    5,
		FetchMaxBodyBytes:    1 << 20,
		DefaultSearchMax:     5,
		MaximumSearchMax:     10,
	}
	if def != want {
		t.Errorf("DefaultLimits() = %+v, want %+v", def, want)
	}
}

// TestConfigureThenResetLimits covers the active-Limits override seam
// cmd/cortex's Config.toolLimits() drives (via NewCortexSession's
// tools.Configure call): a partial override fills unset fields from
// DefaultLimits(), and ResetLimits restores the exact zero-config state —
// the cleanup every test in this package that calls Configure relies on.
func TestConfigureThenResetLimits(t *testing.T) {
	t.Cleanup(ResetLimits)

	Configure(Limits{GrepMaxHits: 7}) // every other field left zero
	if active.GrepMaxHits != 7 {
		t.Errorf("active.GrepMaxHits = %d, want 7", active.GrepMaxHits)
	}
	if active.CurationBudgetTokens != defaultCurationBudgetTokens {
		t.Errorf("active.CurationBudgetTokens = %d, want the untouched default %d (partial Configure must not zero other fields)",
			active.CurationBudgetTokens, defaultCurationBudgetTokens)
	}

	ResetLimits()
	if active != DefaultLimits() {
		t.Errorf("after ResetLimits, active = %+v, want DefaultLimits() %+v", active, DefaultLimits())
	}
}

// TestHeadlessDepsSeedBudgetDefaultsToStudySeedBudget covers the
// SeedBudgeter seam's nil-safe fallback: a tool dispatched without a
// session (headlessDeps) must still get the historical StudySeedBudget, not
// a zero-value budget that would starve a subagent's seed outline.
func TestHeadlessDepsSeedBudgetDefaultsToStudySeedBudget(t *testing.T) {
	var d headlessDeps
	if got := d.SeedBudget(); got != StudySeedBudget {
		t.Errorf("headlessDeps.SeedBudget() = %d, want %d", got, StudySeedBudget)
	}
}
