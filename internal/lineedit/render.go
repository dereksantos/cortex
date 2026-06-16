package lineedit

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// renderLine produces the escape-sequence string that redraws the input on a
// single terminal row: carriage-return, clear-to-EOL, prompt, the visible slice
// of the buffer, then the cursor repositioned. Keeping it to one row means a
// plain "\r\033[K" always fully clears the previous render — no multi-row cursor
// math. Two cases: a normal line scrolls horizontally to keep the cursor in
// view; a multi-line paste collapses to a summary (you send it, you don't
// in-line edit a pasted block).
func renderLine(prompt string, buf *buffer, width int) string {
	if width < 1 {
		width = 80
	}
	if buf.hasNewline() {
		return renderSummary(prompt, buf, width)
	}
	return renderScroll(prompt, buf, width)
}

func renderScroll(prompt string, buf *buffer, width int) string {
	promptW := displayWidth(prompt)
	avail := width - promptW
	if avail < 1 {
		avail = 1
	}
	runes := buf.runes

	// Scroll the window start right until the cursor fits within avail-1
	// columns (leaving a cell for the cursor itself at the far edge).
	start := 0
	for widthOf(runes[start:buf.pos]) > avail-1 {
		start++
	}
	// Extend the visible window as far right as fits.
	end, w := start, 0
	for end < len(runes) {
		rw := runewidth.RuneWidth(runes[end])
		if w+rw > avail {
			break
		}
		w += rw
		end++
	}

	cursorCol := promptW + widthOf(runes[start:buf.pos])
	out := "\r\033[K" + prompt + string(runes[start:end]) + "\r"
	if cursorCol > 0 {
		out += fmt.Sprintf("\033[%dC", cursorCol)
	}
	return out
}

// renderSummary shows a one-line digest of a multi-line buffer (a paste):
// the first line, truncated, plus a count of the rest.
func renderSummary(prompt string, buf *buffer, width int) string {
	text := buf.string()
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	extra := strings.Count(text, "\n")
	tag := fmt.Sprintf("  [+%d lines, %d chars]", extra, len([]rune(text)))

	promptW := displayWidth(prompt)
	avail := width - promptW - displayWidth(tag)
	if avail < 0 {
		avail = 0
	}
	first = truncate(first, avail)
	return "\r\033[K" + prompt + first + tag
}

// truncate cuts s to at most w display columns, appending "…" if shortened.
func truncate(s string, w int) string {
	if displayWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteRune('…')
	return b.String()
}

func widthOf(rs []rune) int { return runewidth.StringWidth(string(rs)) }

// displayWidth measures visible columns, ignoring ANSI escape sequences (the
// prompt carries color codes that occupy no cells).
func displayWidth(s string) int { return runewidth.StringWidth(stripANSI(s)) }

// stripANSI removes CSI escape sequences (ESC [ … final-byte) so width math
// counts only visible glyphs.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
			}
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
