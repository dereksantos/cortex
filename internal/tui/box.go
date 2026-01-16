package tui

import "strings"

// BoxStyle defines the style of box drawing characters.
type BoxStyle int

const (
	// StyleSingle uses single-line box drawing characters (─│┌┐└┘).
	StyleSingle BoxStyle = iota
	// StyleDouble uses double-line box drawing characters (═║╔╗╚╝).
	StyleDouble
)

// BoxCharSet contains the box drawing characters for a style.
type BoxCharSet struct {
	Horizontal   string // ─ or ═
	Vertical     string // │ or ║
	TopLeft      string // ┌ or ╔
	TopRight     string // ┐ or ╗
	BottomLeft   string // └ or ╚
	BottomRight  string // ┘ or ╝
	VerticalLeft string // ┤ or ╣ (T from right)
	VerticalRight string // ├ or ╠ (T from left)
	HorizontalUp string // ┴ or ╩ (T from bottom)
	HorizontalDown string // ┬ or ╦ (T from top)
	Cross        string // ┼ or ╬
}

var singleBox = BoxCharSet{
	Horizontal:    "─",
	Vertical:      "│",
	TopLeft:       "┌",
	TopRight:      "┐",
	BottomLeft:    "└",
	BottomRight:   "┘",
	VerticalLeft:  "┤",
	VerticalRight: "├",
	HorizontalUp:  "┴",
	HorizontalDown: "┬",
	Cross:         "┼",
}

var doubleBox = BoxCharSet{
	Horizontal:    "═",
	Vertical:      "║",
	TopLeft:       "╔",
	TopRight:      "╗",
	BottomLeft:    "╚",
	BottomRight:   "╝",
	VerticalLeft:  "╣",
	VerticalRight: "╠",
	HorizontalUp:  "╩",
	HorizontalDown: "╦",
	Cross:         "╬",
}

// BoxChars returns the character set for the given box style.
func BoxChars(style BoxStyle) BoxCharSet {
	if style == StyleDouble {
		return doubleBox
	}
	return singleBox
}

// HLine creates a horizontal line of the specified width using the given style.
// The width includes just the line characters (no corners).
func HLine(width int, style BoxStyle) string {
	if width <= 0 {
		return ""
	}
	chars := BoxChars(style)
	return strings.Repeat(chars.Horizontal, width)
}

// BoxTop creates the top border of a box: ┌───┐ or ╔═══╗
// The width is the total width including corners.
func BoxTop(width int, style BoxStyle) string {
	if width < 2 {
		return ""
	}
	chars := BoxChars(style)
	return chars.TopLeft + HLine(width-2, style) + chars.TopRight
}

// BoxBottom creates the bottom border of a box: └───┘ or ╚═══╝
// The width is the total width including corners.
func BoxBottom(width int, style BoxStyle) string {
	if width < 2 {
		return ""
	}
	chars := BoxChars(style)
	return chars.BottomLeft + HLine(width-2, style) + chars.BottomRight
}

// BoxRow creates a content row with vertical borders: │content│ or ║content║
// The content is padded/truncated to fit exactly within the specified total width.
func BoxRow(content string, width int, style BoxStyle) string {
	if width < 2 {
		return ""
	}
	chars := BoxChars(style)
	innerWidth := width - 2
	paddedContent := Pad(content, innerWidth)
	return chars.Vertical + paddedContent + chars.Vertical
}

// BoxDivider creates a horizontal divider row: ├───┤ or ╠═══╣
// The width is the total width including the T-junction characters.
func BoxDivider(width int, style BoxStyle) string {
	if width < 2 {
		return ""
	}
	chars := BoxChars(style)
	return chars.VerticalRight + HLine(width-2, style) + chars.VerticalLeft
}

// Box creates a complete box around the given lines of content.
// Each line is padded/truncated to fit the specified width.
func Box(lines []string, width int, style BoxStyle) []string {
	if width < 2 {
		return nil
	}

	result := make([]string, 0, len(lines)+2)
	result = append(result, BoxTop(width, style))
	for _, line := range lines {
		result = append(result, BoxRow(line, width, style))
	}
	result = append(result, BoxBottom(width, style))
	return result
}

// BoxWithTitle creates a box with a title in the top border.
// Example: ┌─ Title ─────┐
func BoxWithTitle(title string, lines []string, width int, style BoxStyle) []string {
	if width < 2 {
		return nil
	}

	chars := BoxChars(style)

	// Build title row
	titlePart := " " + title + " "
	titleWidth := VisibleWidth(titlePart)
	remainingWidth := width - 2 - 1 - titleWidth // corners + initial dash + title

	var topRow string
	if remainingWidth < 0 {
		// Title too long, just use normal top
		topRow = BoxTop(width, style)
	} else {
		topRow = chars.TopLeft + chars.Horizontal + titlePart + HLine(remainingWidth, style) + chars.TopRight
	}

	result := make([]string, 0, len(lines)+2)
	result = append(result, topRow)
	for _, line := range lines {
		result = append(result, BoxRow(line, width, style))
	}
	result = append(result, BoxBottom(width, style))
	return result
}
