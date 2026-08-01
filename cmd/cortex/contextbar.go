// contextbar.go — the two-zone context gauge bar shown in the prompt row
// (display.go's Prompt()) and the /context report (context_cmd.go's
// contextReport()). Both share one pure renderer (renderContextBar) so the
// two surfaces never drift: same fixed-spatial mapping over the model
// window, different width and (for the prompt row) an added color.
//
// The bar is a fixed mapping over the FULL model window — not a rescaling
// percentage bar — so it does not jump around as content changes the way
// the old "used/window" numeric gauge did (docs/context-architecture.md's
// two-zone design: a stable prefix, a hydrated tail, and the free window
// past both). repl.gauge (docs/configuration.md) selects the rendering
// style: "blocks" (default), "braille", "ascii", or "numeric" (the old x/y
// form).
package main

import "strings"

// gaugeStyle selects how the context gauge renders.
type gaugeStyle int

const (
	gaugeBlocks  gaugeStyle = iota // default: block-element (eighth-block) ramp
	gaugeBraille                   // braille density ramp
	gaugeASCII                     // ASCII density ramp (no unicode)
	gaugeNumeric                   // the old "used/window" scalar text
)

// resolveGaugeStyle maps the repl.gauge config string to a gaugeStyle.
// Unrecognized or unset values fall back to blocks — validateConfig
// already rejects anything outside {"", "blocks", "braille", "ascii",
// "numeric"} before this ever runs, so this fallback is just defense in
// depth.
func resolveGaugeStyle(s string) gaugeStyle {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "braille":
		return gaugeBraille
	case "ascii":
		return gaugeASCII
	case "numeric":
		return gaugeNumeric
	default:
		return gaugeBlocks
	}
}

// blocksRamp, brailleRamp, and asciiRamp are density ramps from "emptiest"
// (index 0) to "solid" (the last index), used to render a fill cell's
// fractional remainder as a single character. Blocks is the Unicode Block
// Elements lower-eighth set (U+2581–U+2588, the sparkline ramp) — 9 levels
// counting the empty space, near-universal font coverage, reads as a solid
// level meter. Braille has 9 levels too (dot count 0..8) but renders as dot
// texture and needs decent font support; ASCII has 5, kept structure-safe
// for terminals/fonts that render neither.
var (
	blocksRamp  = []rune(" ▁▂▃▄▅▆▇█")
	brailleRamp = []rune("⠀⡀⡄⡆⡇⣇⣧⣷⣿")
	asciiRamp   = []rune(" .:=#")
)

// promptGaugeCells and contextReportGaugeCells are the two widths the same
// bar renders at: compact for the always-on prompt row, wider for the
// on-demand /context report.
const (
	promptGaugeCells        = 12
	contextReportGaugeCells = 32
)

// renderContextBar draws the fixed-spatial two-zone context gauge:
//
//	[<head fill>|<tail fill>    ]
//
// head/tail are token counts for zone A (the stable prefix: system prompt +
// session outline + memory index) and zone B (the hydrated tail); window is
// the full model window (cs.windowSize()), not just what's currently used —
// the mapping stays fixed as content changes, only the fill grows. cells is
// the number of fill-cell columns the window is divided into; a literal '|'
// divider is drawn as one extra character exactly at the head/tail token
// boundary, so the rendered interior (between the brackets) is cells+1
// characters wide.
//
// Each fill cell spans window/cells tokens. A segment's full cells render
// solid (the ramp's last level); its own boundary cell — the remainder that
// doesn't fill a whole cell — renders at a proportional density level.
// Cells past both segments are blank (a plain space, not the braille blank
// glyph, so trailing bar content is visually clean). Structure ('[', ']',
// '|') is always plain ASCII, even in braille style.
//
// Edge cases:
//   - window <= 0: there is no fixed span to draw against — fall back to
//     the old "head+tail/window" numeric form.
//   - style == gaugeNumeric: always the numeric form, any window.
//   - head+tail > window, or head alone > window: fills clamp to the cells
//     available; the divider still renders, pinned at the right edge of the
//     fill area when head alone consumes it all.
func renderContextBar(head, tail, window, cells int, style gaugeStyle) string {
	if window <= 0 || style == gaugeNumeric {
		return humanFraction(head+tail, window)
	}
	if cells <= 0 {
		cells = 1
	}

	ramp := blocksRamp
	switch style {
	case gaugeBraille:
		ramp = brailleRamp
	case gaugeASCII:
		ramp = asciiRamp
	}

	cellSpan := float64(window) / float64(cells)

	headCells, headFill := fillSegment(head, cellSpan, cells, ramp)
	tailBudget := cells - headCells
	_, tailFill := fillSegment(tail, cellSpan, tailBudget, ramp)
	blank := tailBudget - len([]rune(tailFill))

	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(headFill)
	b.WriteByte('|')
	b.WriteString(tailFill)
	b.WriteString(strings.Repeat(" ", blank))
	b.WriteByte(']')
	return b.String()
}

// fillSegment renders up to maxCells fill-cell characters representing
// tokens at cellSpan tokens/cell: full cells at the ramp's solid level,
// then — if there's a nonzero remainder and room left — one boundary cell
// at a proportional density (round-half-up over the ramp's levels).
// Returns the number of cells consumed (<= maxCells) and the rendered
// string.
func fillSegment(tokens int, cellSpan float64, maxCells int, ramp []rune) (int, string) {
	if maxCells <= 0 || tokens <= 0 {
		return 0, ""
	}
	cellsFloat := float64(tokens) / cellSpan
	full := int(cellsFloat)
	frac := cellsFloat - float64(full)
	if full >= maxCells {
		full = maxCells
		frac = 0
	}
	var b strings.Builder
	for i := 0; i < full; i++ {
		b.WriteRune(ramp[len(ramp)-1])
	}
	used := full
	if frac > 0 && used < maxCells {
		level := int(frac*float64(len(ramp)-1) + 0.5) // round-half-up
		b.WriteRune(ramp[level])
		used++
	}
	return used, b.String()
}

// humanFraction renders the numeric "used/window" gauge — used for the
// window<=0 fallback and the repl.gauge=="numeric" style.
func humanFraction(used, window int) string {
	return humanK(used) + "/" + humanK(window)
}

// gaugeStyle resolves this session's configured repl.gauge style. cs.Config
// may be nil (e.g. a bare session in a test) — that's "unset", not an
// error, so it defaults to blocks like an empty string would.
func (cs *CortexSession) gaugeStyle() gaugeStyle {
	if cs.Config == nil {
		return gaugeBlocks
	}
	return resolveGaugeStyle(cs.Config.Repl.Gauge)
}

// renderGauge draws the two-zone context bar at the given width using this
// session's current head (zone A stable prefix) / tail (zone B hydrated
// tail) / window figures and configured style. cs.ws may be nil before the
// first turn completes — tail is 0 then, same as "nothing hydrated yet".
func (cs *CortexSession) renderGauge(cells int) string {
	tail := 0
	if cs.ws != nil {
		tail = cs.ws.TailTokens()
	}
	return renderContextBar(cs.headTokens(), tail, cs.windowSize(), cells, cs.gaugeStyle())
}
