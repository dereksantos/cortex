package tui

import (
	"strings"
	"unicode/utf8"
)

// VisibleWidth returns the display width of a string.
// This counts runes (Unicode code points), not bytes.
// Note: This is a simplification that treats all runes as width 1.
// For full East Asian width support, a more sophisticated approach would be needed.
func VisibleWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// Truncate safely truncates a string to maxWidth runes.
// If the string is longer than maxWidth, it is truncated and "..." is appended.
// The result will be at most maxWidth runes (including the ellipsis).
// This is UTF-8 safe and never cuts in the middle of a rune.
func Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)
	if runeCount <= maxWidth {
		return s
	}

	// Need to truncate - leave room for "..."
	if maxWidth <= 3 {
		// Not enough room for ellipsis, just truncate
		return truncateRunes(s, maxWidth)
	}

	return truncateRunes(s, maxWidth-3) + "..."
}

// truncateRunes truncates a string to exactly n runes.
// This is UTF-8 safe.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}

	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// Pad pads a string to exactly the specified width.
// If the string is longer, it is truncated.
// If shorter, spaces are added to the right (left-aligned).
// This is an alias for PadRight.
func Pad(s string, width int) string {
	return PadRight(s, width)
}

// PadRight pads a string to the specified width with spaces on the right.
// The text is left-aligned. If longer than width, it is truncated.
func PadRight(s string, width int) string {
	if width <= 0 {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)

	if runeCount > width {
		return truncateRunes(s, width)
	}

	if runeCount < width {
		return s + strings.Repeat(" ", width-runeCount)
	}

	return s
}

// PadLeft pads a string to the specified width with spaces on the left.
// The text is right-aligned. If longer than width, it is truncated.
func PadLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)

	if runeCount > width {
		return truncateRunes(s, width)
	}

	if runeCount < width {
		return strings.Repeat(" ", width-runeCount) + s
	}

	return s
}

// PadCenter centers a string within the specified width.
// If the string is longer, it is truncated.
// Extra space is distributed with more on the right if odd.
func PadCenter(s string, width int) string {
	if width <= 0 {
		return ""
	}

	runeCount := utf8.RuneCountInString(s)

	if runeCount > width {
		return truncateRunes(s, width)
	}

	if runeCount < width {
		totalPadding := width - runeCount
		leftPadding := totalPadding / 2
		rightPadding := totalPadding - leftPadding
		return strings.Repeat(" ", leftPadding) + s + strings.Repeat(" ", rightPadding)
	}

	return s
}

// TruncateNoEllipsis truncates a string to maxWidth without adding ellipsis.
// Useful when you need exact width without the "..." indicator.
func TruncateNoEllipsis(s string, maxWidth int) string {
	return truncateRunes(s, maxWidth)
}
