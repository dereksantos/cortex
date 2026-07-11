package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
)

func outlineFixture() []cache.OutlineEntry {
	return []cache.OutlineEntry{
		{Turn: 1, User: "fix the eval", Actions: []string{"edit a.go [ok]"}, ReplyHead: "fixed", Citation: "@session/20260701-143210#m1-8"},
		{Turn: 2, User: "run the tests", Actions: []string{"bash go test [ok]"}, ReplyHead: "green", Citation: "@session/20260701-143210#m8-14"},
		{Turn: 3, User: "commit it", Actions: []string{"bash git commit [ok]"}, ReplyHead: "committed", Citation: "@session/20260701-143210#m14-20"},
	}
}

func TestMergeOutlineEntries(t *testing.T) {
	t.Run("merges a range into one entry with a spanning citation", func(t *testing.T) {
		cs := &CortexSession{outline: outlineFixture()}
		got, err := cs.MergeOutlineEntries("@session/20260701-143210#m1-8", "@session/20260701-143210#m8-14")
		if err != nil {
			t.Fatalf("MergeOutlineEntries: %v", err)
		}
		if got != "@session/20260701-143210#m1-14" {
			t.Errorf("spanning citation = %q, want @session/20260701-143210#m1-14", got)
		}
		if len(cs.outline) != 2 {
			t.Fatalf("outline has %d entries, want 2", len(cs.outline))
		}
		merged := cs.outline[0]
		if merged.Citation != got {
			t.Errorf("merged entry citation = %q, want %q", merged.Citation, got)
		}
		if merged.Turn != 1 {
			t.Errorf("merged entry turn = %d, want 1", merged.Turn)
		}
		if !strings.Contains(merged.User, "fix the eval") || !strings.Contains(merged.User, "run the tests") {
			t.Errorf("merged user text lost a head: %q", merged.User)
		}
		if len(merged.Actions) != 2 {
			t.Errorf("merged actions = %v, want both turns' actions", merged.Actions)
		}
		if merged.ReplyHead != "green" {
			t.Errorf("merged reply head = %q, want the last one (green)", merged.ReplyHead)
		}
		// The untouched entry survives in order.
		if cs.outline[1].Citation != "@session/20260701-143210#m14-20" {
			t.Errorf("trailing entry = %q, want the commit turn", cs.outline[1].Citation)
		}
	})

	t.Run("rejections leave the outline untouched", func(t *testing.T) {
		tests := []struct {
			name       string
			start, end string
			wantErr    string
		}{
			{"start not in outline", "@session/20260701-143210#m99-100", "@session/20260701-143210#m8-14", "not found"},
			{"end not in outline", "@session/20260701-143210#m1-8", "@session/20260701-143210#m99-100", "not found"},
			{"end before start", "@session/20260701-143210#m8-14", "@session/20260701-143210#m1-8", "must come after"},
			{"single entry range", "@session/20260701-143210#m1-8", "@session/20260701-143210#m1-8", "must come after"},
			{"different sessions", "@session/20260701-143210#m1-8", "@session/other#m8-14", "different sessions"},
			{"malformed citation", "t1", "@session/20260701-143210#m8-14", "coordinates"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cs := &CortexSession{outline: outlineFixture()}
				_, err := cs.MergeOutlineEntries(tt.start, tt.end)
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				if len(cs.outline) != 3 {
					t.Errorf("failed merge mutated the outline: %d entries", len(cs.outline))
				}
			})
		}
	})

	t.Run("caps the merged user text", func(t *testing.T) {
		entries := outlineFixture()
		entries[0].User = strings.Repeat("x", outlineUserCap)
		entries[1].User = strings.Repeat("y", outlineUserCap)
		cs := &CortexSession{outline: entries}
		if _, err := cs.MergeOutlineEntries(entries[0].Citation, entries[1].Citation); err != nil {
			t.Fatalf("MergeOutlineEntries: %v", err)
		}
		if r := []rune(cs.outline[0].User); len(r) > outlineUserCap+80 {
			t.Errorf("merged user text is %d runes, want capped near %d", len(r), outlineUserCap)
		}
		if !strings.Contains(cs.outline[0].User, "truncated") {
			t.Error("capped user text should say it was truncated")
		}
	})
}

func TestRemoveOutlineEntry(t *testing.T) {
	cs := &CortexSession{outline: outlineFixture()}
	if !cs.RemoveOutlineEntry("@session/20260701-143210#m8-14") {
		t.Fatal("RemoveOutlineEntry should find the middle entry")
	}
	if len(cs.outline) != 2 {
		t.Fatalf("outline has %d entries after evict, want 2", len(cs.outline))
	}
	// Idempotent: a second evict of the same citation is a no-op.
	if cs.RemoveOutlineEntry("@session/20260701-143210#m8-14") {
		t.Error("second evict of the same citation should report not-found")
	}
	if cs.outline[0].Turn != 1 || cs.outline[1].Turn != 3 {
		t.Errorf("evict disturbed the surviving order: %v", cs.outline)
	}
}

func TestIsToolEnabledConfigGate(t *testing.T) {
	no := false
	cs := &CortexSession{Config: &Config{Tools: ToolConfig{EnableContextMerge: &no}}}
	if cs.IsToolEnabled("context_merge") {
		t.Error("context_merge should be disabled by config")
	}
	if !cs.IsToolEnabled("context_evict") {
		t.Error("context_evict should default to enabled when its key is nil")
	}
	if !(&CortexSession{}).IsToolEnabled("context_merge") {
		t.Error("no config at all should mean enabled")
	}
}

// TestIsToolEnabledAgentGate covers GOAL.md §3 slice 3b's config gate: the
// agent tool follows the same nil-means-enabled pattern as
// EnableWeb/EnableContext* (docs/IMPLEMENTATION-PATTERN.md) — a missing
// enable_agent key must not disable a shipped tool.
func TestIsToolEnabledAgentGate(t *testing.T) {
	no := false
	off := &CortexSession{Config: &Config{Tools: ToolConfig{EnableAgent: &no}}}
	if off.IsToolEnabled("agent") {
		t.Error("agent should be disabled when EnableAgent is explicitly false")
	}
	if !(&CortexSession{Config: &Config{}}).IsToolEnabled("agent") {
		t.Error("agent should default to enabled when EnableAgent is nil")
	}
	if !(&CortexSession{}).IsToolEnabled("agent") {
		t.Error("no config at all should mean agent is enabled")
	}

	// mergeTools threads a project-level override over the user-level default,
	// same as every other *bool tool gate.
	yes := true
	merged := mergeTools(ToolConfig{EnableAgent: &yes}, ToolConfig{EnableAgent: &no})
	if merged.EnableAgent == nil || *merged.EnableAgent {
		t.Error("mergeTools should let the override (project config) turn EnableAgent off")
	}
}

func TestAdjustWatermarksBounds(t *testing.T) {
	cs := &CortexSession{Window: 4000}
	cs.ws = cs.newWorkingSet(1)

	oldHigh, oldLow, newHigh, newLow, err := cs.AdjustWatermarks(100, -100)
	if err != nil {
		t.Fatalf("AdjustWatermarks: %v", err)
	}
	if newHigh != oldHigh+100 || newLow != oldLow-100 {
		t.Errorf("watermarks = (%d,%d), want (%d,%d)", newHigh, newLow, oldHigh+100, oldLow-100)
	}

	// An out-of-bounds delta is refused and leaves the watermarks alone.
	if _, _, _, _, err := cs.AdjustWatermarks(1<<20, 0); err == nil {
		t.Error("oversized high_delta should error")
	}
	h, l := cs.ws.GetWatermarks()
	if h != newHigh || l != newLow {
		t.Errorf("failed adjust mutated watermarks: (%d,%d)", h, l)
	}

	// ValidateToolCall pre-screens the same bound with a friendly message.
	tc := ToolCall{}
	tc.Function.Name = "context_adjust_watermarks"
	tc.Function.Arguments = `{"high_delta":999999,"low_delta":0}`
	if ok, msg := cs.ValidateToolCall(tc); ok || !strings.Contains(msg, "out of bounds") {
		t.Errorf("ValidateToolCall = (%v, %q), want an out-of-bounds rejection", ok, msg)
	}
}
