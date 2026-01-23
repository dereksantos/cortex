package tui

import "fmt"

// ANSI escape code constants for terminal control.
const (
	// Screen control
	ClearScreen = "\033[2J"  // Clear entire screen
	CursorHome  = "\033[H"   // Move cursor to home position (1,1)
	ClearLine   = "\033[2K"  // Clear entire line
	ClearToEnd  = "\033[K"   // Clear from cursor to end of line

	// Alternate screen buffer (for full-screen TUI apps)
	AltScreenEnter = "\033[?1049h" // Enter alternate screen buffer
	AltScreenLeave = "\033[?1049l" // Leave alternate screen buffer

	// Cursor visibility
	CursorHide = "\033[?25l" // Hide cursor
	CursorShow = "\033[?25h" // Show cursor

	// Text styles
	Bold       = "\033[1m"
	Dim        = "\033[2m"
	Italic     = "\033[3m"
	Underline  = "\033[4m"
	Blink      = "\033[5m"
	Reverse    = "\033[7m"
	Hidden     = "\033[8m"
	Strike     = "\033[9m"

	// Reset
	Reset     = "\033[0m"
	ResetBold = "\033[22m"

	// Foreground colors (standard)
	Black   = "\033[30m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"

	// Bright foreground colors
	BrightBlack   = "\033[90m"
	BrightRed     = "\033[91m"
	BrightGreen   = "\033[92m"
	BrightYellow  = "\033[93m"
	BrightBlue    = "\033[94m"
	BrightMagenta = "\033[95m"
	BrightCyan    = "\033[96m"
	BrightWhite   = "\033[97m"

	// Background colors (standard)
	BgBlack   = "\033[40m"
	BgRed     = "\033[41m"
	BgGreen   = "\033[42m"
	BgYellow  = "\033[43m"
	BgBlue    = "\033[44m"
	BgMagenta = "\033[45m"
	BgCyan    = "\033[46m"
	BgWhite   = "\033[47m"

	// Bright background colors
	BgBrightBlack   = "\033[100m"
	BgBrightRed     = "\033[101m"
	BgBrightGreen   = "\033[102m"
	BgBrightYellow  = "\033[103m"
	BgBrightBlue    = "\033[104m"
	BgBrightMagenta = "\033[105m"
	BgBrightCyan    = "\033[106m"
	BgBrightWhite   = "\033[107m"
)

// ClearAndHome returns combined clear screen and cursor home sequence.
// This is the most common combination for refreshing a full-screen TUI.
func ClearAndHome() string {
	return ClearScreen + CursorHome
}

// MoveTo returns an ANSI sequence to move the cursor to the specified position.
// Row and column are 1-indexed (terminal standard).
func MoveTo(row, col int) string {
	return fmt.Sprintf("\033[%d;%dH", row, col)
}

// MoveUp returns an ANSI sequence to move the cursor up n lines.
func MoveUp(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dA", n)
}

// MoveDown returns an ANSI sequence to move the cursor down n lines.
func MoveDown(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dB", n)
}

// MoveRight returns an ANSI sequence to move the cursor right n columns.
func MoveRight(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dC", n)
}

// MoveLeft returns an ANSI sequence to move the cursor left n columns.
func MoveLeft(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\033[%dD", n)
}

// SaveCursor returns the ANSI sequence to save the cursor position.
func SaveCursor() string {
	return "\033[s"
}

// RestoreCursor returns the ANSI sequence to restore the saved cursor position.
func RestoreCursor() string {
	return "\033[u"
}

// Color256 returns an ANSI sequence for 256-color foreground.
// Valid color values are 0-255.
func Color256(color int) string {
	return fmt.Sprintf("\033[38;5;%dm", color)
}

// BgColor256 returns an ANSI sequence for 256-color background.
// Valid color values are 0-255.
func BgColor256(color int) string {
	return fmt.Sprintf("\033[48;5;%dm", color)
}

// ColorRGB returns an ANSI sequence for true color (24-bit) foreground.
func ColorRGB(r, g, b int) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// BgColorRGB returns an ANSI sequence for true color (24-bit) background.
func BgColorRGB(r, g, b int) string {
	return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
}

// Styled wraps text with the given style codes and resets after.
// Example: Styled("error", Red, Bold) returns "\033[31m\033[1merror\033[0m"
func Styled(text string, styles ...string) string {
	if len(styles) == 0 {
		return text
	}
	var result string
	for _, style := range styles {
		result += style
	}
	return result + text + Reset
}
