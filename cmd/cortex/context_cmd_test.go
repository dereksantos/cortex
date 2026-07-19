package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/memory"
)

// seededContextSession builds a CortexSession with every zone populated —
// system prompt + AGENTS.md, a demoted-turn outline (reusing
// context_tools_test.go's outlineFixture), a memory store with two notes, a
// working set with both demoted and hydrated turns, and a reported last
// request — so /context's report can be checked against known, derived
// numbers rather than an invented golden string.
func seededContextSession(t *testing.T) *CortexSession {
	t.Helper()

	mem, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if _, err := mem.Write("note-one", "first note body", time.Now()); err != nil {
		t.Fatalf("memory.Write: %v", err)
	}
	if _, err := mem.Write("note-two", "second note body", time.Now()); err != nil {
		t.Fatalf("memory.Write: %v", err)
	}

	base := "You are a test agent."
	agentsBody := "Follow these repo rules exactly."
	cs := &CortexSession{
		Window: 8000, // windowSize()/8 = 1000
		Request: &AgentRequest{
			Model: "context-report-test-model",
			Messages: []Message{
				{Role: RoleSystem, Content: base + agentsMarker + agentsBody},
			},
		},
		memory:           mem,
		outline:          outlineFixture(), // 3 entries, see context_tools_test.go
		LastPromptTokens: 400,
		LastCachedTokens: 380,
	}
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 5, Tokens: 30})
	cs.ws.AddTurn(cache.TurnSpan{Start: 5, End: 9, Tokens: 40})
	cs.ws.AddTurn(cache.TurnSpan{Start: 9, End: 13, Tokens: 50})
	// Demote the first turn by hand (DemoteBatch only fires once the tail
	// crosses the high watermark; this is a deliberately small fixture, so
	// the frontier is set directly the way session.go's resume path does).
	if err := cs.ws.RestoreState(1, 1000, 500); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	return cs
}

func TestContextReportStructure(t *testing.T) {
	cs := seededContextSession(t)
	got := cs.contextReport()

	// Section headers — the two-zone architecture made visible.
	for _, want := range []string{
		"context window:",
		"zone A - stable prefix (cached across turns)",
		"zone B - hydrated tail (recent turns verbatim)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("contextReport() missing section %q; got:\n%s", want, got)
		}
	}

	// Header line: model name and the LastPromptTokens/window gauge.
	if !strings.Contains(got, "model: context-report-test-model") {
		t.Errorf("contextReport() missing model name; got:\n%s", got)
	}
	if !strings.Contains(got, humanK(cs.LastPromptTokens)+" / "+humanK(cs.windowSize())) {
		t.Errorf("contextReport() missing the prompt/window gauge; got:\n%s", got)
	}

	// Zone A: system prompt + AGENTS.md split.
	sysContent := cs.Request.Messages[0].Content
	wantSysTok := humanK(cache.TokensOf(len(sysContent)))
	if !strings.Contains(got, "system prompt") || !strings.Contains(got, wantSysTok+" tok") {
		t.Errorf("contextReport() missing system prompt row (%s tok); got:\n%s", wantSysTok, got)
	}
	if !strings.Contains(got, "includes AGENTS.md") {
		t.Errorf("contextReport() should call out the injected AGENTS.md body; got:\n%s", got)
	}

	// Zone A: session outline — 3 entries (outlineFixture), cap = window/8.
	if !strings.Contains(got, "session outline") || !strings.Contains(got, "3 entries") {
		t.Errorf("contextReport() missing the outline entry count; got:\n%s", got)
	}
	if !strings.Contains(got, "cap "+humanK(cs.windowSize()/8)+" = W/8") {
		t.Errorf("contextReport() missing the outline fold cap; got:\n%s", got)
	}

	// Zone A: memory index — 2 notes.
	if !strings.Contains(got, "memory index") || !strings.Contains(got, "2 notes") {
		t.Errorf("contextReport() missing the memory note count; got:\n%s", got)
	}

	// Zone B: hydrated tail — 1 demoted, 2 hydrated (turns 2-3), 70 tail tokens.
	if !strings.Contains(got, "turns 2-3") {
		t.Errorf("contextReport() missing the hydrated turn range; got:\n%s", got)
	}
	if !strings.Contains(got, "2 turns hydrated, 1 demoted to outline") {
		t.Errorf("contextReport() missing the hydrated/demoted counts; got:\n%s", got)
	}
	if !strings.Contains(got, humanK(cs.ws.TailTokens())+" tok") {
		t.Errorf("contextReport() missing the tail token count; got:\n%s", got)
	}

	// Zone B: watermarks, shown in tokens.
	high, low := cs.ws.GetWatermarks()
	if !strings.Contains(got, "demote above "+humanK(high)+" (W/2)") {
		t.Errorf("contextReport() missing the high watermark; got:\n%s", got)
	}
	if !strings.Contains(got, "drain to "+humanK(low)+" (W/3)") {
		t.Errorf("contextReport() missing the low watermark; got:\n%s", got)
	}

	// Last request: prompt/cached/evaluated derived from the reported usage.
	if !strings.Contains(got, "last request: "+humanK(400)+" prompt, "+humanK(380)+" cached (95% cache hit), "+humanK(20)+" evaluated") {
		t.Errorf("contextReport() missing the last-request line; got:\n%s", got)
	}
}

// TestContextReportOmitsUntrackedSections covers the "omit, don't invent"
// rule: a bare session with no memory store, no outline, no working set, and
// no reported usage yet must not fabricate a number for any of those — the
// corresponding rows (and even their zone header, for zone B when there is
// truly nothing to show) are simply absent, and the call must not panic.
func TestContextReportOmitsUntrackedSections(t *testing.T) {
	cs := &CortexSession{
		Window: 4000,
		Request: &AgentRequest{
			Model:    "bare-model",
			Messages: []Message{{Role: RoleSystem, Content: "just a system prompt"}},
		},
	}

	var got string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("contextReport() panicked on a bare session: %v", r)
			}
		}()
		got = cs.contextReport()
	}()

	for _, absent := range []string{
		"memory index",
		"session outline",
		"demoted to outline",
		"watermarks",
		"last request:",
		"AGENTS.md",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("contextReport() on a bare session should omit %q, got:\n%s", absent, got)
		}
	}
	// The always-available header and zone A still render (there's a real
	// system prompt to report), even though every optional row is empty.
	if !strings.Contains(got, "context window:") || !strings.Contains(got, "zone A") {
		t.Errorf("contextReport() should still render the header and zone A; got:\n%s", got)
	}
}
