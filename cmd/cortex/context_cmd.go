// context_cmd.go — the /context slash command: a plain-text map of the
// current session's context window, making the two-zone architecture
// (docs/context-architecture.md) visible. This is both a debugging surface
// and a teaching one: it shows, in real numbers pulled straight from the
// harness's own tracked state, what "stable prefix" and "hydrated tail"
// actually mean for the session in front of you.
//
// Every figure here comes from state the harness already tracks (the
// working set, the outline, the memory index, the last request's reported
// usage) — never an invented estimate. A line is omitted rather than shown
// with a guessed number when its source data isn't available (e.g. no
// memory store wired, or no turns completed yet).
package main

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
)

// agentsMarker is the exact separator systemPromptContent (session_core.go)
// inserts between the base SystemPrompt and an injected AGENTS.md body —
// reused here to split the two back apart for the token breakdown.
const agentsMarker = "\n\n# Project instructions (AGENTS.md)\n\n"

// contextReport renders the /context map described in the package doc
// comment above.
func (cs *CortexSession) contextReport() string {
	var b strings.Builder

	win := cs.windowSize()
	pct := 0
	if win > 0 {
		pct = 100 * cs.LastPromptTokens / win
	}
	fmt.Fprintf(&b, "context window: %s / %s tokens (%d%%)  model: %s\n",
		humanK(cs.LastPromptTokens), humanK(win), pct, cs.Request.Model)
	b.WriteString(cs.renderGauge(contextReportGaugeCells) + "\n")

	b.WriteString("\nzone A - stable prefix (cached across turns)\n")
	for _, line := range []string{cs.systemPromptLine(), cs.outlineSummaryLine(), cs.memoryIndexLine()} {
		if line != "" {
			b.WriteString(line)
		}
	}

	b.WriteString("\nzone B - hydrated tail (recent turns verbatim)\n")
	for _, line := range []string{cs.hydratedTailLine(), cs.watermarksLine()} {
		if line != "" {
			b.WriteString(line)
		}
	}

	if line := cs.lastRequestLine(); line != "" {
		b.WriteString("\n" + line + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// row formats one "  label   tokens   detail" line. label is left-padded to
// a fixed column so the token figures line up; detail is appended verbatim
// (already formatted by the caller) when non-empty.
func row(label, tokens, detail string) string {
	line := fmt.Sprintf("  %-20s %8s", label, tokens)
	if detail != "" {
		line += "   " + detail
	}
	return line + "\n"
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

// systemPromptLine reports the system message's token size, splitting out
// an injected AGENTS.md body (systemPromptContent's agentsMarker) when
// present. "" when there is no system message yet (e.g. a bare CortexSession
// in a test).
func (cs *CortexSession) systemPromptLine() string {
	if cs.Request == nil || len(cs.Request.Messages) == 0 {
		return ""
	}
	content := cs.Request.Messages[0].Content
	if content == "" {
		return ""
	}
	detail := ""
	if i := strings.Index(content, agentsMarker); i >= 0 {
		agents := content[i+len(agentsMarker):]
		detail = fmt.Sprintf("(includes AGENTS.md %s)", humanK(cache.TokensOf(len(agents))))
	}
	return row("system prompt", humanK(cs.systemPromptTokens())+" tok", detail)
}

// outlineTokens returns the demoted-turn outline's rendered token size, 0
// when there is nothing demoted yet.
func (cs *CortexSession) outlineTokens() int {
	if len(cs.outline) == 0 && cs.outlineFolded == "" {
		return 0
	}
	return cache.TokensOf(len(cs.renderOutlineBlock()))
}

// outlineSummaryLine reports the demoted-turn outline's size: entry count,
// the fold budget (window/8 by default, per foldOutlineIfNeeded — configurable
// via context.outline_fraction, cs.Config.outlineBudget), and whether a
// folded digest is currently riding the front of the zone. "" when there is
// nothing demoted yet.
func (cs *CortexSession) outlineSummaryLine() string {
	if len(cs.outline) == 0 && cs.outlineFolded == "" {
		return ""
	}
	w := cs.windowSize()
	outlineCap := cs.Config.outlineBudget(w)
	detail := fmt.Sprintf("%d entries (cap %s = %s)", len(cs.outline), humanK(outlineCap), fractionOfWindow(outlineCap, w))
	if cs.outlineFolded != "" {
		detail += ", folded digest present"
	}
	return row("session outline", humanK(cs.outlineTokens())+" tok", detail)
}

// fractionOfWindow formats part/whole as the resolved fraction actually in
// force — "0.500×W" — rather than a label baked in for the historical
// hardcoded W/2, W/3, W/8 values, so /context stays accurate once
// context.tail_high_fraction / context.tail_drain_fraction /
// context.outline_fraction (docs/configuration.md) are configured away from
// their defaults.
func fractionOfWindow(part, whole int) string {
	if whole <= 0 {
		return ""
	}
	return fmt.Sprintf("%.3f×W", float64(part)/float64(whole))
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

// memoryIndexLine reports the injected memory-index note's size and note
// count. "" when memory isn't wired for this session or has no notes yet.
func (cs *CortexSession) memoryIndexLine() string {
	if cs.memory == nil {
		return ""
	}
	notes, err := cs.memory.List()
	if err != nil || len(notes) == 0 {
		return ""
	}
	return row("memory index", humanK(cs.memoryIndexTokens())+" tok", fmt.Sprintf("%d notes", len(notes)))
}

// headTokens sums zone A's stable-prefix token cost — system prompt +
// session outline + memory index — the same three components
// systemPromptLine/outlineSummaryLine/memoryIndexLine break out line-by-line
// for /context. Also the context gauge bar's (contextbar.go) left (head)
// segment size, so both consumers share this one sum instead of each
// recomputing the cache.TokensOf arithmetic.
func (cs *CortexSession) headTokens() int {
	return cs.systemPromptTokens() + cs.outlineTokens() + cs.memoryIndexTokens()
}

// hydratedTailLine reports the hydrated tail's turn range and size. "" when
// no turn has completed yet (cs.ws nil, or a fresh working set with no
// spans recorded).
func (cs *CortexSession) hydratedTailLine() string {
	if cs.ws == nil || cs.ws.TotalTurns() == 0 {
		return ""
	}
	demoted := cs.ws.Demoted()
	total := cs.ws.TotalTurns()
	label := fmt.Sprintf("turns %d-%d", demoted+1, total)
	detail := fmt.Sprintf("%d turns hydrated, %d demoted to outline", total-demoted, demoted)
	return row(label, humanK(cs.ws.TailTokens())+" tok", detail)
}

// watermarksLine reports the working set's demote/drain thresholds in
// tokens, plus the fraction of the window each resolves to (dynamic — see
// fractionOfWindow — since context.tail_high_fraction / .tail_drain_fraction
// (docs/configuration.md) may have moved them off the historical W/2, W/3).
// "" when there is no working set yet.
func (cs *CortexSession) watermarksLine() string {
	if cs.ws == nil {
		return ""
	}
	high, low := cs.ws.GetWatermarks()
	w := cs.windowSize()
	return fmt.Sprintf("  %-20s demote above %s (%s), drain to %s (%s)\n",
		"watermarks", humanK(high), fractionOfWindow(high, w), humanK(low), fractionOfWindow(low, w))
}

// lastRequestLine reports the most recent model call's prompt/cache usage,
// as the provider reported it (session_core.go's LastPromptTokens /
// LastCachedTokens — no chars/4 estimate here, these are billed figures).
// "" before any request has completed.
func (cs *CortexSession) lastRequestLine() string {
	if cs.LastPromptTokens <= 0 {
		return ""
	}
	cached := cs.LastCachedTokens
	evaluated := cs.LastPromptTokens - cached
	if evaluated < 0 {
		evaluated = 0
	}
	pct := 0
	if cs.LastPromptTokens > 0 {
		pct = 100 * cached / cs.LastPromptTokens
	}
	return fmt.Sprintf("last request: %s prompt, %s cached (%d%% cache hit), %s evaluated",
		humanK(cs.LastPromptTokens), humanK(cached), pct, humanK(evaluated))
}
