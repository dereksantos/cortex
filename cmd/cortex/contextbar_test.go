package main

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
)

// TestRenderContextBarBlocks pins exact rendered strings for representative
// head/tail/window/cells combinations in the default blocks style (the
// Unicode Block Elements eighth-block sparkline ramp): full cells solid
// ('█'), a boundary cell at a proportional level, blank (space) past both
// segments, and the literal '|' divider exactly at the head/tail token
// boundary.
func TestRenderContextBarBlocks(t *testing.T) {
	tests := []struct {
		name                      string
		head, tail, window, cells int
		want                      string
	}{
		{"sample state: 9k head, 22k tail, 128k window, 12 cells", 9000, 22000, 128000, 12, "[▇|██▁        ]"},
		{"half window each, exact half-cell boundary", 500, 500, 2000, 10, "[██▄|██▄    ]"},
		{"zero tail: nothing hydrated yet", 500, 0, 2000, 10, "[██▄|       ]"},
		{"zero head: nothing in zone A yet", 0, 500, 2000, 10, "[|██▄       ]"},
		{"exact cell boundary: no partial cell either side", 1000, 1000, 2000, 10, "[█████|█████]"},
		{"head alone exceeds window: divider pinned at right edge", 3000, 500, 2000, 10, "[██████████|]"},
		{"head+tail exceed window, head alone fits: tail clamps", 1200, 1200, 2000, 10, "[██████|████]"},
		{"small cell count", 5, 5, 100, 4, "[▂|▂  ]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderContextBar(tt.head, tt.tail, tt.window, tt.cells, gaugeBlocks)
			if got != tt.want {
				t.Errorf("renderContextBar(%d, %d, %d, %d, blocks) = %q, want %q", tt.head, tt.tail, tt.window, tt.cells, got, tt.want)
			}
		})
	}
}

// TestRenderContextBarBraille pins the same representative cases in the
// selectable braille density-ramp style.
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
		for _, style := range []gaugeStyle{gaugeBlocks, gaugeBraille, gaugeASCII} {
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
// falls back to zones" default (validateConfig is the actual gate against
// bogus values reaching here at all).
func TestResolveGaugeStyle(t *testing.T) {
	tests := []struct {
		in   string
		want gaugeStyle
	}{
		{"", gaugeZones},
		{"zones", gaugeZones},
		{"blocks", gaugeBlocks},
		{"braille", gaugeBraille},
		{"ascii", gaugeASCII},
		{"ASCII", gaugeASCII},
		{"  ascii  ", gaugeASCII},
		{"numeric", gaugeNumeric},
		{"bogus", gaugeZones},
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
// zero tail rather than panicking. Style is pinned explicitly to gaugeBlocks
// (via repl.gauge = "blocks") so this test's bracket/divider assertions
// don't depend on whatever the default style happens to be.
func TestCortexSessionRenderGaugeUsesHeadTokensAndTailTokens(t *testing.T) {
	cs := &CortexSession{
		Window: 2000,
		Request: &AgentRequest{
			Model:    "m",
			Messages: []Message{{Role: RoleSystem, Content: "system"}},
		},
		Config: &Config{Repl: ReplConfig{Gauge: "blocks"}},
	}
	// No cs.ws yet — must not panic, and must render with tail=0.
	got := cs.renderGauge(10)
	if !strings.Contains(got, "[") || !strings.Contains(got, "]") {
		t.Fatalf("renderGauge() with nil cs.ws = %q, want a rendered bar", got)
	}
	want := renderContextBar(cs.headTokens(), 0, cs.windowSize(), 10, gaugeBlocks)
	if got != want {
		t.Errorf("renderGauge() = %q, want %q (headTokens/0 tail/windowSize)", got, want)
	}

	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 300})
	got = cs.renderGauge(10)
	want = renderContextBar(cs.headTokens(), cs.ws.TailTokens(), cs.windowSize(), 10, gaugeBlocks)
	if got != want {
		t.Errorf("renderGauge() after AddTurn = %q, want %q", got, want)
	}
}

// TestRenderZoneGauge pins renderZoneGauge's exact "<headK>|<tailK>" text
// for representative cases, including the doc example (10k head, 100k
// tail) and humanK's own rounding behavior (sub-1000 stays a bare integer,
// no "k" suffix).
func TestRenderZoneGauge(t *testing.T) {
	tests := []struct {
		name       string
		head, tail int
		want       string
	}{
		{"zero/zero", 0, 0, "0|0"},
		{"doc example: 10k head, 100k tail", 10000, 100000, "10k|100k"},
		{"sub-1000 stays bare (no k suffix)", 500, 999, "500|999"},
		{"humanK rounding: 9000/22000 (sample state)", 9000, 22000, "9k|22k"},
		{"non-round-thousand: humanK keeps one decimal", 1500, 2500, "1.5k|2.5k"},
		{"large: millions", 1_200_000, 500, "1.2M|500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderZoneGauge(tt.head, tt.tail)
			if got != tt.want {
				t.Errorf("renderZoneGauge(%d, %d) = %q, want %q", tt.head, tt.tail, got, tt.want)
			}
			// renderContextBar must delegate to renderZoneGauge for
			// gaugeZones, ignoring window/cells entirely.
			for _, window := range []int{0, -5, 128000} {
				if bar := renderContextBar(tt.head, tt.tail, window, 12, gaugeZones); bar != tt.want {
					t.Errorf("renderContextBar(gaugeZones, window=%d) = %q, want %q", window, bar, tt.want)
				}
			}
		})
	}
}

// TestColoredGaugeZonesColorsEachZoneDifferently checks the prompt row's
// zones composition: zone A (head) and the '|' divider render gray, zone B
// (tail) carries the pressure color (ctxColor) instead — the point of the
// design being that a near-limit tail is the only state that visually
// shouts. Per repo precedent (render_test.go's TestMessageRender), ANSI
// color codes are pinned directly via Contains rather than stripped, but the
// ordering check confirms gray wraps the head number and precedes the tail's
// pressure-colored number.
func TestColoredGaugeZonesColorsEachZoneDifferently(t *testing.T) {
	cs := &CortexSession{
		Window: 128000,
		Request: &AgentRequest{
			Model:    "m",
			Messages: []Message{{Role: RoleSystem, Content: strings.Repeat("x", 40000)}}, // ~10k head tokens
		},
		LastPromptTokens: 110000, // pushes ctxColor into red
	}
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 100000}) // ~100k tail tokens

	got := cs.coloredGauge(promptGaugeCells, cs.windowSize())

	headStr := humanK(cs.headTokens())
	tailStr := humanK(cs.tailTokens())
	pressure := ctxColor(cs.LastPromptTokens, cs.windowSize())

	if !strings.Contains(got, gray+headStr) {
		t.Errorf("coloredGauge() = %q, want gray to wrap the head number %q", got, headStr)
	}
	if !strings.Contains(got, gray+zoneDivider) {
		t.Errorf("coloredGauge() = %q, want gray to wrap the '|' divider", got)
	}
	if !strings.Contains(got, pressure+tailStr) {
		t.Errorf("coloredGauge() = %q, want the pressure color %q to wrap the tail number %q", got, pressure, tailStr)
	}
	if idx := strings.Index(got, gray); idx == -1 || idx > strings.Index(got, pressure+tailStr) {
		t.Errorf("coloredGauge() = %q, want gray (head) to precede the pressure-colored tail", got)
	}
}
