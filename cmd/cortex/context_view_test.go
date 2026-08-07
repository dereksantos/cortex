package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
)

// TestContextReportLinesJoinsToTheReport pins the refactor that let /context
// feed the inspector: the plain scrolling report must remain exactly the join
// of the lines the inspector pages, so the two surfaces cannot drift and the
// non-TTY output is unchanged from before the inspector existed.
func TestContextReportLinesJoinsToTheReport(t *testing.T) {
	cs := seededContextSession(t)
	if got, want := strings.Join(cs.contextReportLines(), "\n"), cs.contextReport(); got != want {
		t.Errorf("join(contextReportLines()) = %q, want contextReport() = %q", got, want)
	}
}

// TestContextReportLinesHasNoTrailingBlank guards the trailing-blank trim that
// stands in for the old builder's TrimRight — a trailing empty line would show
// up as a stray newline in the piped report.
func TestContextReportLinesHasNoTrailingBlank(t *testing.T) {
	tests := []struct {
		name string
		cs   func(t *testing.T) *CortexSession
	}{
		{"fully seeded", seededContextSession},
		// A fresh session — no memory, no outline, no turns — omits most legend
		// rows, so the report can end right after the grid. That is the case
		// the trailing-blank trim exists for.
		{"fresh session", func(t *testing.T) *CortexSession {
			return &CortexSession{
				Window:  8000,
				Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := tc.cs(t).contextReportLines()
			if len(lines) == 0 {
				t.Fatal("contextReportLines() is empty")
			}
			if last := lines[len(lines)-1]; last == "" {
				t.Errorf("report ends with a blank line: %q", lines)
			}
			if strings.HasSuffix(tc.cs(t).contextReport(), "\n") {
				t.Error("contextReport() ends with a newline")
			}
		})
	}
}

func TestContextViewSplitsTitleFromBody(t *testing.T) {
	cs := seededContextSession(t)
	v := contextView{cs: cs}

	if got, want := v.Title(), cs.contextHeaderLine(); got != want {
		t.Errorf("Title() = %q, want the report's header line %q", got, want)
	}

	body := v.Lines(80)
	if len(body) == 0 {
		t.Fatal("Lines() is empty")
	}
	// The header belongs to the title row only; repeating it in the body would
	// waste a row and read as a duplicate.
	for i, line := range body {
		if line == v.Title() {
			t.Errorf("body row %d repeats the title: %q", i, line)
		}
	}
	if body[0] == "" {
		t.Errorf("body opens with a blank row: %q", body)
	}
	// Everything else the report renders must survive into the body.
	full := cs.contextReportLines()
	if got, want := len(body), len(full)-2; got != want { // header + its blank
		t.Errorf("body rows = %d, want %d (report minus header and its blank)", got, want)
	}
	if !strings.Contains(strings.Join(body, "\n"), cs.cacheHeadlineLine()) {
		t.Error("body lost the prefix-cache headline")
	}
}

// TestContextViewLinesIgnoresWidth documents the fixed-frame contract: the
// 8x16 grid never reflows, so the same rows come back at any terminal width
// and the harness is the one that clamps.
func TestContextViewLinesIgnoresWidth(t *testing.T) {
	v := contextView{cs: seededContextSession(t)}
	narrow := strings.Join(v.Lines(20), "\n")
	wide := strings.Join(v.Lines(200), "\n")
	if narrow != wide {
		t.Errorf("Lines() reflowed with width: 20-col = %q, 200-col = %q", narrow, wide)
	}
}

// TestContextInspectableGate is the graceful-degradation contract: the
// inspector opens only for an interactive editor on the rich-render path.
// Every row here must be false — a test binary's stdout is a pipe, which is
// exactly the scripted/piped case that has to keep the plain scrolling report.
// Supplying a non-nil editor as well proves the gate is not satisfied by an
// interactive *stdin* alone.
func TestContextInspectableGate(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		editor *lineedit.Terminal
	}{
		{"no interactive editor", nil, nil},
		{"editor but stdout is not a tty", nil, &lineedit.Terminal{}},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}, &lineedit.Terminal{}},
		{"CORTEX_LOOP_RENDER=0", map[string]string{"CORTEX_LOOP_RENDER": "0"}, &lineedit.Terminal{}},
		{"CORTEX_LOOP_RENDER=off", map[string]string{"CORTEX_LOOP_RENDER": "off"}, &lineedit.Terminal{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if contextInspectable(tc.editor) {
				t.Error("contextInspectable() = true, want false — the plain report must be used")
			}
		})
	}
}

// TestContextFallbackReportUnchanged is the byte-identity check for the
// non-TTY path: with the inspector gated off, what /context prints is exactly
// what contextReport() produces, as it did before the inspector landed.
func TestContextFallbackReportUnchanged(t *testing.T) {
	cs := seededContextSession(t)
	if contextInspectable(&lineedit.Terminal{}) {
		t.Fatal("test environment unexpectedly satisfies the inspector gate")
	}
	report := cs.contextReport()
	if report == "" {
		t.Fatal("contextReport() is empty")
	}
	// The inspector consumes the same lines; joining them back must reproduce
	// the printed report byte for byte.
	if got := strings.Join(cs.contextReportLines(), "\n"); got != report {
		t.Errorf("piped report drifted from the inspector's lines:\n got %q\nwant %q", got, report)
	}
}
