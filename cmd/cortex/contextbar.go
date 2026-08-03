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
// style: "zones" (default — two humanized numbers, "<headK>|<tailK>"),
// "blocks", "braille", "ascii" (the fixed-spatial bar in three ramps), or
// "numeric" (the old x/y form).
package main

import "strings"

// gaugeStyle selects how the context gauge renders.
type gaugeStyle int

const (
	gaugeZones   gaugeStyle = iota // default: two-zone numeric "<headK>|<tailK>" text
	gaugeBlocks                    // block-element (eighth-block) ramp bar
	gaugeBraille                   // braille density ramp bar
	gaugeASCII                     // ASCII density ramp bar (no unicode)
	gaugeNumeric                   // the old "used/window" scalar text
)

// resolveGaugeStyle maps the repl.gauge config string to a gaugeStyle.
// Unrecognized or unset values fall back to zones — validateConfig already
// rejects anything outside {"", "zones", "blocks", "braille", "ascii",
// "numeric"} before this ever runs, so this fallback is just defense in
// depth.
func resolveGaugeStyle(s string) gaugeStyle {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocks":
		return gaugeBlocks
	case "braille":
		return gaugeBraille
	case "ascii":
		return gaugeASCII
	case "numeric":
		return gaugeNumeric
	default:
		return gaugeZones
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
//   - style == gaugeZones: always renderZoneGauge(head, tail) — the two
//     humanized zone numbers, with no window/cells dependency at all (a
//     window <= 0 has no special meaning for that form, unlike the bar
//     styles below).
//   - window <= 0: there is no fixed span to draw against — fall back to
//     the old "head+tail/window" numeric form.
//   - style == gaugeNumeric: always the numeric form, any window.
//   - head+tail > window, or head alone > window: fills clamp to the cells
//     available; the divider still renders, pinned at the right edge of the
//     fill area when head alone consumes it all.
func renderContextBar(head, tail, window, cells int, style gaugeStyle) string {
	if style == gaugeZones {
		return renderZoneGauge(head, tail)
	}
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

// zoneDivider is the character between the two zone numbers in the
// gaugeZones style — the same '|' the bar styles draw at the head/tail
// boundary, so the divider carries one consistent meaning across styles.
const zoneDivider = "|"

// renderZoneGauge renders the gaugeZones style's plain, color-free text:
// "<headK>|<tailK>", e.g. "10k|100k" for a 10,000-token stable prefix and a
// 100,000-token hydrated tail (both via humanK). Pure so it's unit-testable
// without ANSI; the session-level color composition — gray on the head
// number and the divider, the pressure color on the tail number — lives in
// coloredGauge below, which is the only caller that needs to color the two
// halves differently.
func renderZoneGauge(head, tail int) string {
	return humanK(head) + zoneDivider + humanK(tail)
}

// gaugeStyle resolves this session's configured repl.gauge style. cs.Config
// may be nil (e.g. a bare session in a test) — that's "unset", not an
// error, so it defaults to zones like an empty string would.
func (cs *CortexSession) gaugeStyle() gaugeStyle {
	if cs.Config == nil {
		return gaugeZones
	}
	return resolveGaugeStyle(cs.Config.Repl.Gauge)
}

// renderGauge draws the two-zone context gauge at the given width (for the
// bar styles; ignored by gaugeZones) using this session's current head (zone
// A stable prefix) / tail (zone B hydrated tail) / window figures and
// configured style. Returns plain, color-free text — see coloredGauge for
// the prompt row's colored composition. cs.ws may be nil before the first
// turn completes — tail is 0 then, same as "nothing hydrated yet".
func (cs *CortexSession) renderGauge(cells int) string {
	return renderContextBar(cs.headTokens(), cs.tailTokens(), cs.windowSize(), cells, cs.gaugeStyle())
}

// contextReportGaugeStyle keeps /context always drawing the fixed-spatial
// bar (contextReportGaugeCells wide) even now that the prompt row's default
// flipped to the numeric gaugeZones form: the report's whole reason to
// exist is showing the window as a spatial map, so an unset/zones config
// falls back to blocks here. Any explicitly configured bar or numeric style
// still passes through unchanged — /context has always respected repl.gauge
// for those, and that isn't changing.
func contextReportGaugeStyle(s gaugeStyle) gaugeStyle {
	if s == gaugeZones {
		return gaugeBlocks
	}
	return s
}

// tailTokens is zone B's current token count — cs.ws may be nil before the
// first turn completes, which reads as zero (nothing hydrated yet) rather
// than an error.
func (cs *CortexSession) tailTokens() int {
	if cs.ws == nil {
		return 0
	}
	return cs.ws.TailTokens()
}

// coloredGauge composes the prompt row's colored gauge. Every bar style
// (blocks/braille/ascii/numeric) keeps the single ctxColor wrap that
// predates the zones style — the whole bar shifts green->yellow->red
// together as the window fills. gaugeZones is the one exception the design
// calls for: zone A (head) and the '|' divider render gray — deliberately
// boring, since the stable prefix isn't where risk lives — while zone B
// (tail) alone carries the pressure color, so a near-limit tail is the only
// state that visually shouts. win is the model window (cs.windowSize()) the
// caller already has; ctxColor keys off cs.LastPromptTokens (the last
// request's actual billed size), same as before this style existed.
func (cs *CortexSession) coloredGauge(cells, win int) string {
	pressure := ctxColor(cs.LastPromptTokens, win)
	if cs.gaugeStyle() != gaugeZones {
		return withColor(cs.renderGauge(cells), pressure)
	}
	return withColor(humanK(cs.headTokens()), gray) +
		withColor(zoneDivider, gray) +
		withColor(humanK(cs.tailTokens()), pressure)
}
