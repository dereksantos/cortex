// Package lineedit is a small raw-mode line editor for the Cortex REPL: cursor
// movement, emacs-style editing, bracketed paste, and (via Terminal) ESC- or
// Ctrl-C-to-interrupt a running turn. It owns the TTY in "cbreak" mode —
// byte-at-a-time input with no echo — while leaving output post-processing on
// so the harness's existing "\n" prints still render correctly.
//
// The editing logic (buffer), key decoding (keys.go), and rendering (render.go)
// are pure and unit-tested; only the driver (lineedit.go) touches the terminal.
package lineedit

// buffer is the edited line: a rune slice plus a cursor index in [0,len]. It
// may contain '\n' (from a paste); rendering handles that case specially.
type buffer struct {
	runes []rune
	pos   int
}

func (b *buffer) string() string { return string(b.runes) }

func (b *buffer) hasNewline() bool {
	for _, r := range b.runes {
		if r == '\n' {
			return true
		}
	}
	return false
}

// insert adds runes at the cursor and advances past them.
func (b *buffer) insert(rs ...rune) {
	tail := append([]rune{}, b.runes[b.pos:]...)
	b.runes = append(b.runes[:b.pos], rs...)
	b.runes = append(b.runes, tail...)
	b.pos += len(rs)
}

func (b *buffer) backspace() {
	if b.pos == 0 {
		return
	}
	b.runes = append(b.runes[:b.pos-1], b.runes[b.pos:]...)
	b.pos--
}

func (b *buffer) deleteForward() {
	if b.pos >= len(b.runes) {
		return
	}
	b.runes = append(b.runes[:b.pos], b.runes[b.pos+1:]...)
}

func (b *buffer) left() {
	if b.pos > 0 {
		b.pos--
	}
}

func (b *buffer) right() {
	if b.pos < len(b.runes) {
		b.pos++
	}
}

// home and end move within the cursor's line — in a single-line buffer that
// is the whole buffer; in a multi-line draft, the line on screen.
func (b *buffer) home() {
	start, _, _, _ := b.lineBounds()
	b.pos = start
}

func (b *buffer) end() {
	_, end, _, _ := b.lineBounds()
	b.pos = end
}

// wordLeft moves to the start of the previous word: skip spaces, then word.
func (b *buffer) wordLeft() {
	for b.pos > 0 && isWordSep(b.runes[b.pos-1]) {
		b.pos--
	}
	for b.pos > 0 && !isWordSep(b.runes[b.pos-1]) {
		b.pos--
	}
}

// wordRight moves to the end of the next word: skip spaces, then word.
func (b *buffer) wordRight() {
	n := len(b.runes)
	for b.pos < n && isWordSep(b.runes[b.pos]) {
		b.pos++
	}
	for b.pos < n && !isWordSep(b.runes[b.pos]) {
		b.pos++
	}
}

// killToEnd (Ctrl-K) and killToStart (Ctrl-U) cut within the cursor's line,
// so in a multi-line draft they never remove lines that aren't on screen.
func (b *buffer) killToEnd() {
	_, end, _, _ := b.lineBounds()
	b.runes = append(b.runes[:b.pos:b.pos], b.runes[end:]...)
}

func (b *buffer) killToStart() {
	start, _, _, _ := b.lineBounds()
	b.runes = append(b.runes[:start:start], b.runes[b.pos:]...)
	b.pos = start
}

// killWord deletes the word before the cursor (Ctrl-W).
func (b *buffer) killWord() {
	start := b.pos
	for start > 0 && isWordSep(b.runes[start-1]) {
		start--
	}
	for start > 0 && !isWordSep(b.runes[start-1]) {
		start--
	}
	b.runes = append(b.runes[:start], b.runes[b.pos:]...)
	b.pos = start
}

func isWordSep(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// lineBounds returns the [start, end) rune range of the line holding the
// cursor in a multi-line buffer, its 1-based number, and the line count.
func (b *buffer) lineBounds() (start, end, line, lines int) {
	line, lines = 1, 1
	for i, r := range b.runes {
		if r != '\n' {
			continue
		}
		lines++
		if i < b.pos {
			line++
			start = i + 1
		}
	}
	end = len(b.runes)
	for i := b.pos; i < len(b.runes); i++ {
		if b.runes[i] == '\n' {
			end = i
			break
		}
	}
	return start, end, line, lines
}

// lineUp moves the cursor to the same column of the previous line, clamped to
// its length. Reports false on the first line (the caller falls through to
// history).
func (b *buffer) lineUp() bool {
	start, _, line, _ := b.lineBounds()
	if line == 1 {
		return false
	}
	col := b.pos - start
	prevEnd := start - 1 // the '\n' ending the previous line
	prevStart := prevEnd
	for prevStart > 0 && b.runes[prevStart-1] != '\n' {
		prevStart--
	}
	b.pos = prevStart + min(col, prevEnd-prevStart)
	return true
}

// lineDown moves the cursor to the same column of the next line, clamped to
// its length. Reports false on the last line.
func (b *buffer) lineDown() bool {
	start, end, line, lines := b.lineBounds()
	if line == lines {
		return false
	}
	col := b.pos - start
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(b.runes) && b.runes[nextEnd] != '\n' {
		nextEnd++
	}
	b.pos = nextStart + min(col, nextEnd-nextStart)
	return true
}
