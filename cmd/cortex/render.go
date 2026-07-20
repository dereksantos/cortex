package main

import (
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
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

// anchoredInput reports whether the REPL should pin the prompt to the bottom
// row and echo type-ahead live during a turn. Requires the rich-render path
// (TTY + color) and streaming; otherwise the simpler capture-and-seed path runs.
func anchoredInput() bool {
	return renderEnabled() && streamingEnabled()
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

// headingStyle is glamour's built-in dark style with the literal "## "/"### "
// markdown prefixes stripped from H2-H6 — the heading's color/bold already
// carries the hierarchy, so the hashmarks were pure noise. H1 is untouched;
// its colored badge never had a "#" prefix to begin with.
//
// The shared Heading.BlockSuffix ("\n", applied to every level, H1 included)
// is cleared too: since each block streams through its own independent
// Render call, that suffix survives render()'s Trim as a lone-reset blank
// line embedded inside the heading's own output — writeBlock (streaming.go)
// now adds heading padding explicitly and deterministically, so this
// glamour-internal one would only double it up.
var headingStyle = func() ansi.StyleConfig {
	s := styles.DarkStyleConfig
	s.Heading.BlockSuffix = ""
	s.H2.Prefix = ""
	s.H3.Prefix = ""
	s.H4.Prefix = ""
	s.H5.Prefix = ""
	s.H6.Prefix = ""
	return s
}()

// newMarkdownRenderer builds a renderer word-wrapped to width. Returns nil on
// failure so callers degrade to plain text. Uses headingStyle (dark, minus
// hashmark prefixes) via WithStyles — never WithAutoStyle, which emits OSC
// 10/11 escape queries the cbreak input reader would swallow (see
// internal/repltui for the same caveat).
func newMarkdownRenderer(width int) *markdownRenderer {
	if width < 1 {
		width = 80
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStyles(headingStyle),
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

// ansiPrefix matches an ANSI SGR code at the start of a string.
var ansiPrefix = regexp.MustCompile(`^\x1b\[[0-9;]*m`)

// trimLeadingIndent removes glamour's leading margin — newlines, indent spaces,
// and the empty SGR pairs it emits — so a rendered block can sit on the gutter
// line. An SGR code is only dropped when it styles whitespace (what follows is a
// space, newline, or another code); the code that colors the first visible glyph
// is preserved, so the joined text keeps its styling.
func trimLeadingIndent(s string) string {
	for {
		switch {
		case strings.HasPrefix(s, "\n"), strings.HasPrefix(s, " "):
			s = s[1:]
		default:
			if loc := ansiPrefix.FindStringIndex(s); loc != nil {
				rest := s[loc[1]:]
				if rest == "" || rest[0] == ' ' || rest[0] == '\n' || strings.HasPrefix(rest, "\x1b[") {
					s = rest
					continue
				}
			}
			return s
		}
	}
}

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

// headingLinePattern matches an ATX heading opener (CommonMark: 1-6 "#"
// followed by whitespace or end-of-line) — used to isolate a heading into its
// own block even when the model's markdown omits the blank line that
// conventionally follows it, so writeBlock can always pad a heading on sight.
var headingLinePattern = regexp.MustCompile(`^#{1,6}(\s|$)`)

// isHeadingBlock reports whether b (a block from splitBlocks) is a heading —
// always true for a heading block since splitBlocks isolates heading lines,
// but computed from content rather than assumed so a heading-only block is
// still recognized however it arrived.
func isHeadingBlock(b string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(b), "\n")
	return headingLinePattern.MatchString(first)
}

// splitBlocks segments accumulated stream content into complete markdown blocks
// plus the unfinished remainder, which the caller re-feeds as more arrives.
//
// A block is a maximal run of lines that renders as one markdown unit:
//   - a fenced code block, from an opening ``` (or ~~~) line through its close,
//   - an ATX heading line, isolated on its own even without a following blank
//     line, so it can always get its own padding (see isHeadingBlock),
//   - otherwise a paragraph/list group terminated by a blank line.
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
		isHeading := !inFence && headingLinePattern.MatchString(trimmed)
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
		case isHeading:
			// Heading line: flush any prose accumulated before it, then the
			// heading is its own single-line block.
			if i > start {
				blocks = append(blocks, join(complete[start:i]))
			}
			blocks = append(blocks, complete[i])
			i++
			start = i
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
