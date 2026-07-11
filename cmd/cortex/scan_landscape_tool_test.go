package main

import (
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// TestScanEnabled mirrors TestConfigToolMerges's "deleteEnabled defaults
// true" subtest for the M2.6 scan_landscape gate: absent (nil) config or nil
// field defaults enabled; an explicit false disables.
func TestScanEnabled(t *testing.T) {
	if !(*Config)(nil).scanEnabled() {
		t.Error("nil config should default scan enabled")
	}
	no := false
	if (&Config{Tools: ToolConfig{EnableScan: &no}}).scanEnabled() {
		t.Error("explicit false should disable scan")
	}
	yes := true
	if !(&Config{Tools: ToolConfig{EnableScan: &yes}}).scanEnabled() {
		t.Error("explicit true should keep scan enabled")
	}
}

// TestScanLandscapeToolRegistrationGate is the "absent ⇒ registered, false ⇒
// absent" GOAL.md M2.6 assertion, at both halves: the base coder tool set
// (toolSet) carries scan_landscape unconditionally (the declaration is
// always present, matching every other tool), and the toolsExcept filtering
// NewCortexSession applies for a disabled tool (same pattern as
// FunctionRemove/allowDelete) actually drops it.
func TestScanLandscapeToolRegistrationGate(t *testing.T) {
	found := false
	for _, tl := range toolSet {
		if tl.Function.Name == tools.FunctionScanLandscape {
			found = true
		}
	}
	if !found {
		t.Fatal("scan_landscape missing from the base coder tool set (absent-config case)")
	}

	no := false
	cfg := &Config{Tools: ToolConfig{EnableScan: &no}}
	filtered := toolSet
	if !cfg.scanEnabled() {
		filtered = toolsExcept(filtered, tools.FunctionScanLandscape)
	}
	for _, tl := range filtered {
		if tl.Function.Name == tools.FunctionScanLandscape {
			t.Error("scan_landscape should have been dropped when tools.enable_scan is false")
		}
	}
	if len(filtered) != len(toolSet)-1 {
		t.Errorf("len = %d, want %d", len(filtered), len(toolSet)-1)
	}
}

// TestIsToolEnabledScanGate mirrors TestIsToolEnabledConfigGate for the
// scan_landscape execution gate (defense in depth alongside the registration
// filter above, matching EnableWeb's pattern).
func TestIsToolEnabledScanGate(t *testing.T) {
	no := false
	cs := &CortexSession{Config: &Config{Tools: ToolConfig{EnableScan: &no}}}
	if cs.IsToolEnabled(tools.FunctionScanLandscape) {
		t.Error("scan_landscape should be disabled by config")
	}
	if !(&CortexSession{}).IsToolEnabled(tools.FunctionScanLandscape) {
		t.Error("no config at all should mean scan_landscape enabled")
	}
}
