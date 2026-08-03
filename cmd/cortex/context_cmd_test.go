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

	// Header: model, window, turn count (ws.TotalTurns() == 3).
	if !strings.Contains(got, "context — context-report-test-model") {
		t.Errorf("contextReport() missing header model; got:\n%s", got)
	}
	if !strings.Contains(got, humanK(cs.windowSize())+" window") {
		t.Errorf("contextReport() missing window size; got:\n%s", got)
	}
	if !strings.Contains(got, "turn 3") {
		t.Errorf("contextReport() missing turn count; got:\n%s", got)
	}

	// Cache headline: 400 prompt, 380 cached -> 95% hit, 20 evaluated.
	if !strings.Contains(got, "95%") || !strings.Contains(got, "hit last turn") {
		t.Errorf("contextReport() missing the cache hit rate; got:\n%s", got)
	}
	if !strings.Contains(got, "20 evaluated of 400 prompt") {
		t.Errorf("contextReport() missing the evaluated/prompt figures; got:\n%s", got)
	}

	// The grid: 8 rows, each starting with a "<offset>k" gutter as its own
	// field (distinguishing it from a legend row, whose first field is a
	// glyph, or the header/cache lines, whose first field is plain text).
	gridLines := 0
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(stripANSI(line))
		if len(fields) > 0 && strings.HasSuffix(fields[0], "k") {
			gridLines++
		}
	}
	if gridLines < gridRows {
		t.Errorf("contextReport() should render %d grid rows, found %d; got:\n%s", gridRows, gridLines, got)
	}

	// Legend: system row.
	wantSysTok := gridTokenLabel(cs.systemPromptTokens())
	if !strings.Contains(got, "system") || !strings.Contains(got, wantSysTok) {
		t.Errorf("contextReport() missing system legend row (%s); got:\n%s", wantSysTok, got)
	}

	// Legend: outline row — 3 entries (outlineFixture), spanning citation
	// from the first entry's start to the last entry's end.
	if !strings.Contains(got, "outline") || !strings.Contains(got, "3 entries") {
		t.Errorf("contextReport() missing the outline entry count; got:\n%s", got)
	}
	wantCitation := "@session/20260701-143210#m1-20"
	if !strings.Contains(got, "recall") || !strings.Contains(got, wantCitation) {
		t.Errorf("contextReport() missing the outline recall citation %q; got:\n%s", wantCitation, got)
	}

	// Legend: memory row — 2 notes.
	if !strings.Contains(got, "memory") || !strings.Contains(got, "2 notes") {
		t.Errorf("contextReport() missing the memory note count; got:\n%s", got)
	}

	// Legend: tail row folds in the old watermarks row — hydrated count
	// (2 = 3 total - 1 demoted) plus the demote/drain thresholds.
	if !strings.Contains(got, "2 turns verbatim") {
		t.Errorf("contextReport() missing the hydrated turn count; got:\n%s", got)
	}
	high, low := cs.ws.GetWatermarks()
	if !strings.Contains(got, "demote >"+humanK(high)) || !strings.Contains(got, "drain to "+humanK(low)) {
		t.Errorf("contextReport() missing the tail row's folded watermarks; got:\n%s", got)
	}
}

// TestContextReportOmitsUntrackedSections covers the "omit, don't invent"
// rule: a bare session with no memory store, no outline, no working set, and
// no reported usage yet must not fabricate a legend row for any of those —
// the corresponding rows are simply absent, and the call must not panic.
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
		"memory",
		"outline",
		"turns verbatim",
		"demote >",
		"◂ demote",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("contextReport() on a bare session should omit %q, got:\n%s", absent, got)
		}
	}

	// Fresh session: no request made yet, so the cache headline reports
	// zone A's assembled size instead of a misleading "0 / window".
	if !strings.Contains(got, "no requests yet") {
		t.Errorf("contextReport() should show the fresh-session cache headline; got:\n%s", got)
	}
	if !strings.Contains(got, "zone A assembled at "+humanK(cs.headTokens())) {
		t.Errorf("contextReport() fresh-session headline missing the assembled size; got:\n%s", got)
	}

	// The header and system legend row still render (there's a real system
	// prompt), even though every other row is empty.
	if !strings.Contains(got, "context — bare-model") {
		t.Errorf("contextReport() should still render the header; got:\n%s", got)
	}
	if !strings.Contains(got, "system") {
		t.Errorf("contextReport() should still render the system legend row; got:\n%s", got)
	}
}

func TestContextHeaderLine(t *testing.T) {
	cs := seededContextSession(t)
	got := stripANSI(cs.contextHeaderLine())
	want := "context — context-report-test-model · " + humanK(cs.windowSize()) + " window · turn 3"
	if got != want {
		t.Errorf("contextHeaderLine() = %q, want %q", got, want)
	}
}

func TestContextHeaderLineNoWorkingSetYet(t *testing.T) {
	cs := &CortexSession{
		Window:  4000,
		Request: &AgentRequest{Model: "m"},
	}
	got := stripANSI(cs.contextHeaderLine())
	if !strings.HasSuffix(got, "turn 0") {
		t.Errorf("contextHeaderLine() = %q, want a trailing turn 0 (no working set yet)", got)
	}
}

// TestCacheHeadlineLineThresholds pins the hit-rate color thresholds: green
// at/above 80%, yellow at/above 40%, red below.
func TestCacheHeadlineLineThresholds(t *testing.T) {
	tests := []struct {
		name           string
		prompt, cached int
		wantColor      string
	}{
		{"green at exactly 80%", 100, 80, green},
		{"green above 80%", 100, 95, green},
		{"yellow at exactly 40%", 100, 40, yellow},
		{"yellow between thresholds", 100, 60, yellow},
		{"red below 40%", 100, 10, red},
		{"red at zero", 100, 0, red},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &CortexSession{LastPromptTokens: tt.prompt, LastCachedTokens: tt.cached}
			got := cs.cacheHeadlineLine()
			if !strings.Contains(got, tt.wantColor) {
				t.Errorf("cacheHeadlineLine() = %q, want it to carry color %q", got, tt.wantColor)
			}
		})
	}
}

func TestCacheHeadlineLineFreshSession(t *testing.T) {
	cs := &CortexSession{
		Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "short prompt"}}},
	}
	got := stripANSI(cs.cacheHeadlineLine())
	want := "prefix cache  — no requests yet · zone A assembled at " + humanK(cs.headTokens())
	if got != want {
		t.Errorf("cacheHeadlineLine() = %q, want %q", got, want)
	}
}

// TestGridLegendLinesOmitEmptyComponents covers the per-row omission rule
// directly against gridLegendLines, independent of the full report: with
// only a system prompt populated and the window sized to exactly match it
// (no free space left over either), the system row is the only one shown.
func TestGridLegendLinesOmitEmptyComponents(t *testing.T) {
	cs := &CortexSession{
		Request: &AgentRequest{
			Model:    "m",
			Messages: []Message{{Role: RoleSystem, Content: "a system prompt with some content"}},
		},
	}
	cs.Window = cs.systemPromptTokens()

	lines := cs.gridLegendLines()
	if len(lines) != 1 {
		t.Fatalf("gridLegendLines() = %d lines, want 1 (system only); got: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "system") {
		t.Errorf("gridLegendLines()[0] = %q, want the system row", lines[0])
	}
}

func TestOutlineSpanCitation(t *testing.T) {
	cs := &CortexSession{outline: outlineFixture()}
	got := cs.outlineSpanCitation()
	want := "@session/20260701-143210#m1-20"
	if got != want {
		t.Errorf("outlineSpanCitation() = %q, want %q", got, want)
	}
}

func TestOutlineSpanCitationEmpty(t *testing.T) {
	cs := &CortexSession{}
	if got := cs.outlineSpanCitation(); got != "" {
		t.Errorf("outlineSpanCitation() on an empty outline = %q, want \"\"", got)
	}
}
