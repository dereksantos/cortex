package main

import (
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/dereksantos/cortex/internal/lineedit"
	"golang.org/x/term"
)

// renderEnabled reports whether the REPL should markdown-render and
// syntax-highlight assistant output. Disabled when stdout isn't a terminal
// (pipes, CI, the test harness), when NO_COLOR is set, or via the
// CORTEX_LOOP_RENDER=0 escape hatch — all of which fall back to raw token
// streaming, the pre-existing behavior.
func renderEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_RENDER"))) {
	case "0", "false", "no", "off":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return lineedit.IsInteractive(os.Stdout)
}

// terminalWidth is the current stdout column count, for glamour word-wrap.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// markdownRenderer turns a complete markdown block into ANSI-styled output.
// Glamour runs fenced code through chroma, so headings/lists/tables AND syntax
// highlighting come from one Render call.
type markdownRenderer struct {
	tr *glamour.TermRenderer
}

// newMarkdownRenderer builds a renderer word-wrapped to width. Returns nil on
// failure so callers degrade to plain text. WithStandardStyle("dark") — never
// WithAutoStyle, which emits OSC 10/11 escape queries the cbreak input reader
// would swallow (see internal/repltui for the same caveat).
func newMarkdownRenderer(width int) *markdownRenderer {
	if width < 1 {
		width = 80
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	return &markdownRenderer{tr: tr}
}

// render styles one block; on any error it returns the block stripped of
// trailing newlines so nothing is ever lost.
func (m *markdownRenderer) render(block string) string {
	out, err := m.tr.Render(block)
	if err != nil {
		return strings.TrimRight(block, "\n")
	}
	return trimBlockPadding(strings.Trim(out, "\n"))
}

// ansiSuffix matches an ANSI SGR code at the end of a string.
var ansiSuffix = regexp.MustCompile(`\x1b\[[0-9;]*m$`)

// trimBlockPadding strips glamour's right-pad: WithWordWrap fills every line to
// the wrap width with (color-wrapped) trailing spaces, which clutters a plain
// scrolling REPL.
func trimBlockPadding(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = trimLinePadding(line)
	}
	return strings.Join(lines, "\n")
}

// trimLinePadding removes a trailing run of spaces and the SGR codes
// interleaved with them — glamour's padding is exactly that. A run with no
// space (a lone style terminator after visible text) is left intact. When the
// strip exposes still-open styling, a reset is re-appended so color can't bleed
// onto the next line.
func trimLinePadding(line string) string {
	j := len(line)
	sawSpace := false
	for j > 0 {
		if line[j-1] == ' ' {
			j--
			sawSpace = true
			continue
		}
		if loc := ansiSuffix.FindStringIndex(line[:j]); loc != nil {
			j = loc[0]
			continue
		}
		break
	}
	if !sawSpace {
		return line
	}
	trimmed := line[:j]
	if strings.Contains(trimmed, "\x1b[") {
		trimmed += "\x1b[0m"
	}
	return trimmed
}

// splitBlocks segments accumulated stream content into complete markdown blocks
// plus the unfinished remainder, which the caller re-feeds as more arrives.
//
// A block is a maximal run of lines that renders as one markdown unit:
//   - a fenced code block, from an opening ``` (or ~~~) line through its close,
//   - otherwise a paragraph/list/heading group terminated by a blank line.
//
// Only newline-terminated lines are eligible to flush; the trailing partial
// line is always carried forward (more bytes may complete it — including the
// closing fence). This is what makes streaming safe: a half-written code fence
// is never rendered until it closes.
func splitBlocks(pending string) (blocks []string, rest string) {
	lines := strings.Split(pending, "\n")
	tail := lines[len(lines)-1] // partial line after the final '\n' (maybe "")
	complete := lines[:len(lines)-1]

	join := func(ls []string) string { return strings.Join(ls, "\n") }

	inFence := false
	start := 0 // index in complete where the current block begins
	i := 0
	for i < len(complete) {
		trimmed := strings.TrimSpace(complete[i])
		isFence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		switch {
		case isFence && !inFence:
			// Opening fence: flush any prose block accumulated before it.
			if i > start {
				blocks = append(blocks, join(complete[start:i]))
				start = i
			}
			inFence = true
			i++
		case isFence && inFence:
			// Closing fence: the fenced block is complete, fence line included.
			i++
			blocks = append(blocks, join(complete[start:i]))
			start = i
			inFence = false
		case inFence:
			i++ // code line, keep buffering until the close
		case trimmed == "":
			// Blank line ends a prose block; the blank itself is a separator.
			if i > start {
				blocks = append(blocks, join(complete[start:i]))
			}
			i++
			start = i
		default:
			i++ // prose line, keep accumulating
		}
	}

	// Leftover complete lines (an open fence, or a paragraph not yet closed by a
	// blank line) carry forward with the partial tail.
	restParts := append(append([]string{}, complete[start:]...), tail)
	return blocks, join(restParts)
}
