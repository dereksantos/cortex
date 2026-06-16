// Package lineedit is a small raw-mode line editor for the loop REPL: cursor
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

func (b *buffer) home() { b.pos = 0 }
func (b *buffer) end()  { b.pos = len(b.runes) }

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

func (b *buffer) killToEnd() { b.runes = b.runes[:b.pos] }

func (b *buffer) killToStart() {
	b.runes = append([]rune{}, b.runes[b.pos:]...)
	b.pos = 0
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
