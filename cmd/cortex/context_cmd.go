// context_cmd.go — the /context slash command: a square glyph-grid map of
// the current session's context window, making the two-zone architecture
// (docs/context-architecture.md) visible. This is both a debugging surface
// and a teaching one: it shows, in real numbers pulled straight from the
// harness's own tracked state, what "stable prefix" and "hydrated tail"
// actually mean for the session in front of you.
//
// The report is three stages: a header + a prefix-cache health headline
// (context_cmd.go, this file), a fixed 128-cell (8×16) grid of the whole
// model window (context_grid.go's pure placement/row arithmetic, colored
// here), and a legend translating each glyph to its token size and detail.
//
// Every figure here comes from state the harness already tracks (the
// working set, the outline, the memory index, the last request's reported
// usage) — never an invented estimate. A legend row is omitted rather than
// shown with a guessed number when its component is empty (e.g. no memory
// store wired, or no turns completed yet).
package main

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/skills"
)

// agentsMarker is the exact separator systemPromptContent (session_core.go)
// inserts between the base SystemPrompt and an injected AGENTS.md body.
const agentsMarker = "\n\n# Project instructions (AGENTS.md)\n\n"

// cacheHitGreenPct / cacheHitYellowPct: the prefix-cache headline's hit-rate
// color thresholds — green at/above cacheHitGreenPct, yellow at/above
// cacheHitYellowPct, red below.
const (
	cacheHitGreenPct  = 80
	cacheHitYellowPct = 40
)

// contextReport renders the /context map described in the package doc
// comment above: header, cache-health headline, the 128-cell grid, then the
// legend. This is the plain scrolling form — what a pipe, NO_COLOR, or
// CORTEX_LOOP_RENDER=0 gets, unchanged. The interactive TTY path wraps the
// same lines in the alternate-screen inspector (context_view.go).
func (cs *CortexSession) contextReport() string {
	return strings.Join(cs.contextReportLines(), "\n")
}

// contextReportLines is contextReport split into its rows, so the inspector
// can page them without re-splitting a joined string (and so the report's
// blank-line structure is stated once, here, rather than as scattered "\n\n"
// concatenations). Joining these with "\n" reproduces the pre-inspector
// report byte for byte: the trailing-blank trim below stands in for the
// TrimRight the builder form ended with.
func (cs *CortexSession) contextReportLines() []string {
	lines := []string{
		cs.contextHeaderLine(),
		"",
		cs.cacheHeadlineLine(),
		"",
	}

	win := cs.windowSize()
	placement := computeContextGrid(cs.gridComponents(), cs.tailTokens(), win)
	lines = append(lines, coloredContextGridLines(placement, win, cs.gridHighWatermark())...)
	lines = append(lines, "")

	lines = append(lines, cs.gridLegendLines()...)

	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// contextHeaderLine renders "context — <model> · <window>k window · turn
// <N>", gray. N is the working set's total tracked turn count (0 before the
// first turn completes — cs.ws is nil then).
func (cs *CortexSession) contextHeaderLine() string {
	turn := 0
	if cs.ws != nil {
		turn = cs.ws.TotalTurns()
	}
	return withColor(fmt.Sprintf("context — %s · %s window · turn %d", cs.Request.Model, humanK(cs.windowSize()), turn), gray)
}

// cacheHeadlineLine renders the prefix-cache health line. Once a request
// has been made (LastPromptTokens > 0), the figures are the provider's
// actual billed usage (session_core.go's LastPromptTokens/LastCachedTokens
// — no chars/4 estimate here): hit% = cached/prompt, evaluated =
// prompt-cached. Before any request, it reports what zone A (the stable
// prefix) was assembled to instead — headTokens() — so a fresh session
// never shows the old, misleading "0 / <window>" figure.
func (cs *CortexSession) cacheHeadlineLine() string {
	if cs.LastPromptTokens <= 0 {
		return withColor(fmt.Sprintf("prefix cache  — no requests yet · zone A assembled at %s", humanK(cs.headTokens())), gray)
	}
	cached := cs.LastCachedTokens
	evaluated := cs.LastPromptTokens - cached
	if evaluated < 0 {
		evaluated = 0
	}
	pct := 100 * cached / cs.LastPromptTokens
	color := red
	switch {
	case pct >= cacheHitGreenPct:
		color = green
	case pct >= cacheHitYellowPct:
		color = yellow
	}
	return withColor("prefix cache  ", gray) +
		withColor(fmt.Sprintf("%d%%", pct), color) +
		withColor(fmt.Sprintf(" hit last turn · %s evaluated of %s prompt", humanK(evaluated), humanK(cs.LastPromptTokens)), gray)
}

// gridComponents returns zone A's four pieces in wire order — the same
// order they're actually assembled into the prompt (system prompt, session
// outline, memory index, skills index) — sized by the token-counting
// helpers below, which the grid and the legend both call so neither
// recomputes the arithmetic independently.
func (cs *CortexSession) gridComponents() []gridComponent {
	return []gridComponent{
		{glyphSystem, cs.systemPromptTokens()},
		{glyphOutline, cs.outlineTokens()},
		{glyphMemory, cs.memoryIndexTokens()},
		{glyphSkills, cs.skillsIndexTokens()},
	}
}

// gridHighWatermark returns the working set's demote threshold, 0 when
// there is no working set yet (cs.ws nil) — read as "nothing to tick".
func (cs *CortexSession) gridHighWatermark() int {
	if cs.ws == nil {
		return 0
	}
	high, _ := cs.ws.GetWatermarks()
	return high
}

// contextGridCellColor picks the ANSI color for one grid cell by its glyph
// (system/outline/memory/skills/free are fixed colors; tail alone depends
// on position — green before the demote watermark, red at/after it, per
// context_grid.go's tailCellPastWatermark). System renders in the
// terminal's own default color (no wrap) — "bright/default" per the design.
func contextGridCellColor(glyph rune, idx, window, hiWatermark int) string {
	switch glyph {
	case glyphOutline:
		return blue
	case glyphMemory:
		return magenta
	case glyphSkills:
		return yellow
	case glyphTail:
		if tailCellPastWatermark(idx, window, hiWatermark) {
			return red
		}
		return green
	case glyphFree:
		return gray
	default: // glyphSystem
		return ""
	}
}

// coloredContextGridLines composes renderContextGrid's uncolored 8×16
// layout with per-cell ANSI: the gutter and the demote tick stay gray
// (structure, not content); each glyph is wrapped in contextGridCellColor's
// color. Kept next to contextReport (rather than in context_grid.go) since
// it's the one piece of the grid rendering that isn't pure — context_grid.go
// stays plain-glyph testable without ANSI.
func coloredContextGridLines(placement contextGridPlacement, window, hiWatermark int) []string {
	demoteRow := demoteRowIndex(window, hiWatermark)
	cellSize := gridCellSize(window)
	width := gridGutterWidth(window)
	lines := make([]string, 0, gridRows)
	for r := 0; r < gridRows; r++ {
		offset := gridRowOffset(r, cellSize)
		var cells strings.Builder
		for c := 0; c < gridCols; c++ {
			idx := r*gridCols + c
			glyph := placement.glyphs[idx]
			if color := contextGridCellColor(glyph, idx, window, hiWatermark); color != "" {
				cells.WriteString(withColor(string(glyph), color))
			} else {
				cells.WriteRune(glyph)
			}
		}
		line := withColor(gridGutterLabel(offset, width), gray) + "  " + cells.String()
		if r == demoteRow {
			line += withColor("  ◂ demote", gray)
		}
		lines = append(lines, line)
	}
	return lines
}

// gridLegendRow formats one legend line: "<glyph> <name>  <tokens>[   <detail>]".
// color (may be "") wraps the glyph only — name/tokens/detail render in the
// terminal's default color except for whatever the detail string itself
// colors (e.g. the outline row's cyan recall citation).
func gridLegendRow(glyph rune, color, name string, tokens int, detail string) string {
	g := string(glyph)
	if color != "" {
		g = withColor(g, color)
	}
	line := fmt.Sprintf("%s %-7s %6s", g, name, gridTokenLabel(tokens))
	if detail != "" {
		line += "   " + detail
	}
	return line
}

// gridTokenLabel formats a legend row's token size as a decimal-K figure —
// "2.1k", "0.5k" — always scaled to thousands even under 1000, unlike
// humanK (loopui.HumanK), which passes small counts through bare ("500")
// since it's tuned for the prompt row's tight space. The legend has room to
// spend on consistency: every row reads as "<n.n>k" regardless of size.
func gridTokenLabel(tokens int) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(tokens)/1000), ".0") + "k"
}

// gridLegendLines renders one row per non-empty grid-order component —
// system, outline, memory, skills, tail (folding the old separate
// watermarks row into this one), free — reusing the same token-counting
// helpers the grid itself sizes cells from. A component with zero tokens is
// omitted entirely (matching the pre-redesign *Line functions' "omit,
// don't invent" behavior).
func (cs *CortexSession) gridLegendLines() []string {
	var lines []string

	if t := cs.systemPromptTokens(); t > 0 {
		lines = append(lines, gridLegendRow(glyphSystem, "", "system", t, ""))
	}
	if t := cs.outlineTokens(); t > 0 {
		lines = append(lines, gridLegendRow(glyphOutline, blue, "outline", t, cs.outlineLegendDetail()))
	}
	if t := cs.memoryIndexTokens(); t > 0 {
		lines = append(lines, gridLegendRow(glyphMemory, magenta, "memory", t, cs.memoryLegendDetail()))
	}
	if t := cs.skillsIndexTokens(); t > 0 {
		count := len(skills.Discover(cs.skillsDirs()))
		lines = append(lines, gridLegendRow(glyphSkills, yellow, "skills", t, fmt.Sprintf("%d skills", count)))
	}
	if cs.ws != nil && cs.ws.TotalTurns() > 0 {
		lines = append(lines, gridLegendRow(glyphTail, green, "tail", cs.ws.TailTokens(), cs.tailLegendDetail()))
	}
	if free := cs.freeTokens(cs.windowSize()); free > 0 {
		lines = append(lines, gridLegendRow(glyphFree, gray, "free", free, ""))
	}

	return lines
}

// outlineLegendDetail reports the demoted-turn outline's entry count plus a
// recall citation spanning its first through last entry, cyan like every
// other citation the agent sees in the outline itself. "" when there is no
// outline yet (guarded by the t > 0 caller check, but kept defensive).
func (cs *CortexSession) outlineLegendDetail() string {
	if len(cs.outline) == 0 {
		return ""
	}
	detail := fmt.Sprintf("%d entries", len(cs.outline))
	if span := cs.outlineSpanCitation(); span != "" {
		detail += " · recall " + withColor(span, cyan)
	}
	return detail
}

// outlineSpanCitation builds one spanning @session/<id>#m<first>-<last>
// citation from the outline's first and last entries — the same shape
// MergeOutlineEntries produces (session_core.go), computed here read-only
// (no mutation, just a legend string) via the shared citationRe parser
// (tool_deps.go). "" if either endpoint doesn't carry a well-formed
// citation, e.g. a hand-built test fixture.
func (cs *CortexSession) outlineSpanCitation() string {
	if len(cs.outline) == 0 {
		return ""
	}
	first := citationRe.FindStringSubmatch(cs.outline[0].Citation)
	last := citationRe.FindStringSubmatch(cs.outline[len(cs.outline)-1].Citation)
	if first == nil || last == nil || first[1] != last[1] {
		return ""
	}
	return fmt.Sprintf("@session/%s#m%s-%s", first[1], first[2], last[3])
}

// memoryLegendDetail reports the injected memory index's note count. ""
// when memory isn't wired or listing fails.
func (cs *CortexSession) memoryLegendDetail() string {
	if cs.memory == nil {
		return ""
	}
	notes, err := cs.memory.List()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d notes", len(notes))
}

// tailLegendDetail folds the old separate watermarks row into the tail
// legend line: how many turns are hydrated verbatim, plus the demote/drain
// thresholds in tokens (the same figures the demote tick on the grid above
// plots).
func (cs *CortexSession) tailLegendDetail() string {
	hydrated := cs.ws.TotalTurns() - cs.ws.Demoted()
	high, low := cs.ws.GetWatermarks()
	return fmt.Sprintf("%d turns verbatim · demote >%s · drain to %s", hydrated, humanK(high), humanK(low))
}

// freeTokens is the model window minus everything currently assembled
// (zone A's four components plus the hydrated tail), clamped to zero for
// an over-full window (the grid's own clamp — computeContextGrid — can
// leave no free cells even where this arithmetic would go negative).
func (cs *CortexSession) freeTokens(window int) int {
	free := window - cs.headTokens() - cs.tailTokens()
	if free < 0 {
		free = 0
	}
	return free
}

// systemPromptTokens returns the system message's token size, 0 when there
// is no system message yet (e.g. a bare CortexSession in a test).
func (cs *CortexSession) systemPromptTokens() int {
	if cs.Request == nil || len(cs.Request.Messages) == 0 {
		return 0
	}
	content := cs.Request.Messages[0].Content
	if content == "" {
		return 0
	}
	return cache.TokensOf(len(content))
}

// outlineTokens returns the demoted-turn outline's rendered token size, 0
// when there is nothing demoted yet.
func (cs *CortexSession) outlineTokens() int {
	if len(cs.outline) == 0 && cs.outlineFolded == "" {
		return 0
	}
	return cache.TokensOf(len(cs.renderOutlineBlock()))
}

// memoryIndexTokens returns the injected memory-index note's token size, 0
// when memory isn't wired for this session or has no notes yet.
func (cs *CortexSession) memoryIndexTokens() int {
	if cs.memory == nil {
		return 0
	}
	notes, err := cs.memory.List()
	if err != nil || len(notes) == 0 {
		return 0
	}
	return cache.TokensOf(len(cs.memoryIndexNote()))
}

// skillsIndexTokens returns the injected skills-index note's token size, 0
// when skills discovery is disabled (skills.enabled: false) or finds
// nothing.
func (cs *CortexSession) skillsIndexTokens() int {
	return cache.TokensOf(len(cs.skillsIndexNote()))
}

// headTokens sums zone A's stable-prefix token cost — system prompt +
// session outline + memory index + skills index — the same components
// gridComponents breaks out cell-by-cell for the grid. Also the context
// gauge bar's (contextbar.go) left (head) segment size, so both consumers
// share this one sum instead of each recomputing the cache.TokensOf
// arithmetic.
func (cs *CortexSession) headTokens() int {
	return cs.systemPromptTokens() + cs.outlineTokens() + cs.memoryIndexTokens() + cs.skillsIndexTokens()
}
