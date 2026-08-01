package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
)

// TestRenderContextBarBraille pins exact rendered strings for representative
// head/tail/window/cells combinations in the default braille style: full
// cells solid ('⣿'), a boundary cell at a proportional density level, blank
// (space) past both segments, and the literal '|' divider exactly at the
// head/tail token boundary.
func TestRenderContextBarBraille(t *testing.T) {
	tests := []struct {
		name                      string
		head, tail, window, cells int
		want                      string
	}{
		{"sample state: 9k head, 22k tail, 128k window, 12 cells", 9000, 22000, 128000, 12, "[⣷|⣿⣿⡀        ]"},
		{"half window each, exact half-cell boundary", 500, 500, 2000, 10, "[⣿⣿⡇|⣿⣿⡇    ]"},
		{"zero tail: nothing hydrated yet", 500, 0, 2000, 10, "[⣿⣿⡇|       ]"},
		{"zero head: nothing in zone A yet", 0, 500, 2000, 10, "[|⣿⣿⡇       ]"},
		{"exact cell boundary: no partial cell either side", 1000, 1000, 2000, 10, "[⣿⣿⣿⣿⣿|⣿⣿⣿⣿⣿]"},
		{"head alone exceeds window: divider pinned at right edge", 3000, 500, 2000, 10, "[⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿|]"},
		{"head+tail exceed window, head alone fits: tail clamps", 1200, 1200, 2000, 10, "[⣿⣿⣿⣿⣿⣿|⣿⣿⣿⣿]"},
		{"small cell count", 5, 5, 100, 4, "[⡄|⡄  ]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderContextBar(tt.head, tt.tail, tt.window, tt.cells, gaugeBraille)
			if got != tt.want {
				t.Errorf("renderContextBar(%d, %d, %d, %d, braille) = %q, want %q", tt.head, tt.tail, tt.window, tt.cells, got, tt.want)
			}
		})
	}
}

// TestRenderContextBarASCII mirrors TestRenderContextBarBraille's cases in
// the ASCII fallback ramp (" .:=#") — structure-safe, no unicode.
func TestRenderContextBarASCII(t *testing.T) {
	tests := []struct {
		name                      string
		head, tail, window, cells int
		want                      string
	}{
		{"sample state: 9k head, 22k tail, 128k window, 12 cells", 9000, 22000, 128000, 12, "[=|##         ]"},
		{"half window each, exact half-cell boundary", 500, 500, 2000, 10, "[##:|##:    ]"},
		{"zero tail: nothing hydrated yet", 500, 0, 2000, 10, "[##:|       ]"},
		{"zero head: nothing in zone A yet", 0, 500, 2000, 10, "[|##:       ]"},
		{"exact cell boundary: no partial cell either side", 1000, 1000, 2000, 10, "[#####|#####]"},
		{"head alone exceeds window: divider pinned at right edge", 3000, 500, 2000, 10, "[##########|]"},
		{"head+tail exceed window, head alone fits: tail clamps", 1200, 1200, 2000, 10, "[######|####]"},
		{"small cell count", 5, 5, 100, 4, "[.|.  ]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderContextBar(tt.head, tt.tail, tt.window, tt.cells, gaugeASCII)
			if got != tt.want {
				t.Errorf("renderContextBar(%d, %d, %d, %d, ascii) = %q, want %q", tt.head, tt.tail, tt.window, tt.cells, got, tt.want)
			}
		})
	}
}

// TestRenderContextBarDividerPosition checks the '|' divider's rune index
// lands exactly at 1 (the '[') + the number of head fill cells rendered —
// the "sits at the head/tail boundary" requirement, independent of what
// density characters surround it.
func TestRenderContextBarDividerPosition(t *testing.T) {
	// head=400 tail=200 window=2000 cells=10: cellSpan=200, so head lands on
	// an exact 2-cell boundary (no partial) and tail on an exact 1-cell
	// boundary — an unambiguous case to locate the divider in.
	got := renderContextBar(400, 200, 2000, 10, gaugeBraille)
	const want = "[⣿⣿|⣿       ]"
	if got != want {
		t.Fatalf("renderContextBar = %q, want %q", got, want)
	}
	runes := []rune(got)
	idx := -1
	for i, r := range runes {
		if r == '|' {
			idx = i
			break
		}
	}
	if idx != 3 { // '[' at 0, 2 solid head cells at 1-2, divider at 3
		t.Errorf("divider rune index = %d, want 3 (right after '[' + 2 head cells)", idx)
	}
}

// TestRenderContextBarWindowZeroFallback covers the "window <= 0" edge case:
// there's no fixed span to draw a bar against, so it falls back to the old
// "used/window" numeric text (used = head+tail, the bar's own total fill —
// renderContextBar has no other notion of "current context size").
func TestRenderContextBarWindowZeroFallback(t *testing.T) {
	tests := []struct {
		name               string
		head, tail, window int
		want               string
	}{
		{"window exactly zero", 100, 50, 0, "150/0"},
		{"window negative (defensive)", 100, 50, -5, "150/-5"},
	}
	for _, tt := range tests {
		for _, style := range []gaugeStyle{gaugeBraille, gaugeASCII} {
			got := renderContextBar(tt.head, tt.tail, tt.window, 12, style)
			if got != tt.want {
				t.Errorf("%s (style=%d): renderContextBar = %q, want %q", tt.name, style, got, tt.want)
			}
		}
	}
}

// TestRenderContextBarNumericStyle covers repl.gauge == "numeric": always
// the scalar "used/window" form (used = head+tail), regardless of window
// size or the cells parameter — the old pre-bar prompt-row gauge, still
// selectable.
func TestRenderContextBarNumericStyle(t *testing.T) {
	for _, cells := range []int{1, 12, 32, 99} {
		got := renderContextBar(9000, 22000, 128000, cells, gaugeNumeric)
		const want = "31k/128k"
		if got != want {
			t.Errorf("renderContextBar(numeric, cells=%d) = %q, want %q", cells, got, want)
		}
	}
}

// TestRenderContextBarNonPositiveCells guards against a misconfigured
// cells<=0 (shouldn't happen via the two call sites' constants, but the
// function must not divide by zero or panic).
func TestRenderContextBarNonPositiveCells(t *testing.T) {
	for _, cells := range []int{0, -3} {
		got := renderContextBar(0, 0, 100, cells, gaugeBraille)
		const want = "[| ]"
		if got != want {
			t.Errorf("renderContextBar(cells=%d) = %q, want %q", cells, got, want)
		}
	}
}

// TestResolveGaugeStyle covers the repl.gauge string -> gaugeStyle mapping,
// including case-insensitivity/trimming and the "unset or unrecognized
// falls back to braille" default (validateConfig is the actual gate against
// bogus values reaching here at all).
func TestResolveGaugeStyle(t *testing.T) {
	tests := []struct {
		in   string
		want gaugeStyle
	}{
		{"", gaugeBraille},
		{"braille", gaugeBraille},
		{"ascii", gaugeASCII},
		{"ASCII", gaugeASCII},
		{"  ascii  ", gaugeASCII},
		{"numeric", gaugeNumeric},
		{"bogus", gaugeBraille},
	}
	for _, tt := range tests {
		if got := resolveGaugeStyle(tt.in); got != tt.want {
			t.Errorf("resolveGaugeStyle(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestCortexSessionRenderGaugeUsesHeadTokensAndTailTokens is a thin
// integration check that renderGauge (the CortexSession-level wrapper) wires
// headTokens()/cs.ws.TailTokens()/windowSize() into renderContextBar
// correctly, and that a nil cs.ws (no turn completed yet) is treated as
// zero tail rather than panicking.
func TestCortexSessionRenderGaugeUsesHeadTokensAndTailTokens(t *testing.T) {
	cs := &CortexSession{
		Window: 2000,
		Request: &AgentRequest{
			Model:    "m",
			Messages: []Message{{Role: RoleSystem, Content: "system"}},
		},
	}
	// No cs.ws yet — must not panic, and must render with tail=0.
	got := cs.renderGauge(10)
	if !strings.Contains(got, "[") || !strings.Contains(got, "]") {
		t.Fatalf("renderGauge() with nil cs.ws = %q, want a rendered bar", got)
	}
	want := renderContextBar(cs.headTokens(), 0, cs.windowSize(), 10, gaugeBraille)
	if got != want {
		t.Errorf("renderGauge() = %q, want %q (headTokens/0 tail/windowSize)", got, want)
	}

	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 300})
	got = cs.renderGauge(10)
	want = renderContextBar(cs.headTokens(), cs.ws.TailTokens(), cs.windowSize(), 10, gaugeBraille)
	if got != want {
		t.Errorf("renderGauge() after AddTurn = %q, want %q", got, want)
	}
}
