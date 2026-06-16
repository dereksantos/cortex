package lineedit

import (
	"strings"
	"unicode/utf8"
)

// keyKind enumerates the editor actions a decoded keystroke maps to.
type keyKind int

const (
	keyRune keyKind = iota // a printable rune (in keyEvent.r)
	keyEnter
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp   // reserved for history (currently a no-op)
	keyDown // reserved for history (currently a no-op)
	keyHome
	keyEnd
	keyWordLeft
	keyWordRight
	keyKillToEnd   // Ctrl-K
	keyKillToStart // Ctrl-U
	keyKillWord    // Ctrl-W
	keyInterrupt   // Ctrl-C
	keyEOF         // Ctrl-D
	keyPaste       // bracketed paste (text in keyEvent.paste)
	keyUnknown
)

// keyEvent is one decoded keystroke or paste.
type keyEvent struct {
	kind  keyKind
	r     rune
	paste string
}

// byteSource yields input one byte at a time. Abstracted so decoding is
// testable over an in-memory byte slice without a terminal.
type byteSource interface {
	next() (byte, error)
}

// decodeKey reads and interprets one keystroke (or a full bracketed paste).
func decodeKey(src byteSource) (keyEvent, error) {
	b, err := src.next()
	if err != nil {
		return keyEvent{}, err
	}
	switch b {
	case '\r', '\n':
		return keyEvent{kind: keyEnter}, nil
	case 0x7f, 0x08:
		return keyEvent{kind: keyBackspace}, nil
	case 0x01:
		return keyEvent{kind: keyHome}, nil // Ctrl-A
	case 0x05:
		return keyEvent{kind: keyEnd}, nil // Ctrl-E
	case 0x02:
		return keyEvent{kind: keyLeft}, nil // Ctrl-B
	case 0x06:
		return keyEvent{kind: keyRight}, nil // Ctrl-F
	case 0x0b:
		return keyEvent{kind: keyKillToEnd}, nil // Ctrl-K
	case 0x15:
		return keyEvent{kind: keyKillToStart}, nil // Ctrl-U
	case 0x17:
		return keyEvent{kind: keyKillWord}, nil // Ctrl-W
	case 0x03:
		return keyEvent{kind: keyInterrupt}, nil // Ctrl-C
	case 0x04:
		return keyEvent{kind: keyEOF}, nil // Ctrl-D
	case 0x1b:
		return decodeEscape(src)
	}
	if b < 0x20 {
		return keyEvent{kind: keyUnknown}, nil // other control byte
	}
	return decodeRune(b, src)
}

// decodeRune assembles a UTF-8 rune from its first byte plus continuations.
func decodeRune(first byte, src byteSource) (keyEvent, error) {
	n := utf8Len(first)
	buf := make([]byte, 1, 4)
	buf[0] = first
	for i := 1; i < n; i++ {
		c, err := src.next()
		if err != nil {
			return keyEvent{}, err
		}
		buf = append(buf, c)
	}
	r, _ := utf8.DecodeRune(buf)
	return keyEvent{kind: keyRune, r: r}, nil
}

func utf8Len(b byte) int {
	switch {
	case b&0x80 == 0x00:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	}
	return 1
}

// decodeEscape handles ESC-prefixed input: CSI (ESC [), SS3 (ESC O), and the
// Alt-b / Alt-f word moves. An unrecognized sequence is keyUnknown.
func decodeEscape(src byteSource) (keyEvent, error) {
	b, err := src.next()
	if err != nil {
		return keyEvent{}, err
	}
	switch b {
	case '[':
		return decodeCSI(src)
	case 'O':
		c, err := src.next()
		if err != nil {
			return keyEvent{}, err
		}
		switch c {
		case 'H':
			return keyEvent{kind: keyHome}, nil
		case 'F':
			return keyEvent{kind: keyEnd}, nil
		case 'C':
			return keyEvent{kind: keyRight}, nil
		case 'D':
			return keyEvent{kind: keyLeft}, nil
		}
		return keyEvent{kind: keyUnknown}, nil
	case 'b':
		return keyEvent{kind: keyWordLeft}, nil // Alt-b
	case 'f':
		return keyEvent{kind: keyWordRight}, nil // Alt-f
	}
	return keyEvent{kind: keyUnknown}, nil
}

// decodeCSI reads a control sequence's parameter bytes up to its final byte
// (0x40–0x7e), then interprets it.
func decodeCSI(src byteSource) (keyEvent, error) {
	var params []byte
	for {
		b, err := src.next()
		if err != nil {
			return keyEvent{}, err
		}
		if b >= 0x40 && b <= 0x7e {
			return interpretCSI(string(params), b, src)
		}
		params = append(params, b)
	}
}

// interpretCSI maps a parsed CSI sequence to a key. A modifier (params contain
// ";", e.g. "1;5C" for Ctrl-Right) turns an arrow into a word move.
func interpretCSI(params string, final byte, src byteSource) (keyEvent, error) {
	modified := strings.Contains(params, ";")
	switch final {
	case 'A':
		return keyEvent{kind: keyUp}, nil
	case 'B':
		return keyEvent{kind: keyDown}, nil
	case 'C':
		if modified {
			return keyEvent{kind: keyWordRight}, nil
		}
		return keyEvent{kind: keyRight}, nil
	case 'D':
		if modified {
			return keyEvent{kind: keyWordLeft}, nil
		}
		return keyEvent{kind: keyLeft}, nil
	case 'H':
		return keyEvent{kind: keyHome}, nil
	case 'F':
		return keyEvent{kind: keyEnd}, nil
	case '~':
		switch params {
		case "1", "7":
			return keyEvent{kind: keyHome}, nil
		case "4", "8":
			return keyEvent{kind: keyEnd}, nil
		case "3":
			return keyEvent{kind: keyDelete}, nil
		case "200":
			return decodePaste(src)
		}
	}
	return keyEvent{kind: keyUnknown}, nil
}

// decodePaste reads a bracketed paste body: everything up to the closing
// "ESC [ 201 ~" marker, returned verbatim (newlines preserved) as one event.
func decodePaste(src byteSource) (keyEvent, error) {
	const end = "\x1b[201~"
	var buf []byte
	for {
		b, err := src.next()
		if err != nil {
			return keyEvent{}, err
		}
		buf = append(buf, b)
		if len(buf) >= len(end) && string(buf[len(buf)-len(end):]) == end {
			buf = buf[:len(buf)-len(end)]
			break
		}
	}
	return keyEvent{kind: keyPaste, paste: string(buf)}, nil
}
