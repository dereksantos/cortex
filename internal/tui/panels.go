package tui

import (
	"fmt"
	"strings"
)

// Panel creates a titled panel with content.
// Returns lines ready to print (including borders).
func Panel(title string, lines []string, width int) []string {
	return BoxWithTitle(title, lines, width, StyleSingle)
}

// PanelNoTitle creates a panel without a title.
func PanelNoTitle(lines []string, width int) []string {
	return Box(lines, width, StyleSingle)
}

// SplitPanel creates a two-column layout within a box.
// The divider is placed after leftWidth characters.
func SplitPanel(leftTitle, rightTitle string, leftLines, rightLines []string, leftWidth, rightWidth int) []string {
	chars := BoxChars(StyleSingle)

	result := make([]string, 0)

	// Top with column headers
	result = append(result, chars.TopLeft+HLine(leftWidth+1, StyleSingle)+chars.HorizontalDown+HLine(rightWidth+1, StyleSingle)+chars.TopRight)
	result = append(result, chars.Vertical+" "+Pad(leftTitle, leftWidth-1)+" "+chars.Vertical+" "+Pad(rightTitle, rightWidth-1)+" "+chars.Vertical)
	result = append(result, chars.VerticalRight+HLine(leftWidth+1, StyleSingle)+chars.Cross+HLine(rightWidth+1, StyleSingle)+chars.VerticalLeft)

	// Content rows - pair up left and right lines
	maxRows := len(leftLines)
	if len(rightLines) > maxRows {
		maxRows = len(rightLines)
	}

	for i := 0; i < maxRows; i++ {
		leftContent := ""
		if i < len(leftLines) {
			leftContent = leftLines[i]
		}
		rightContent := ""
		if i < len(rightLines) {
			rightContent = rightLines[i]
		}
		result = append(result, chars.Vertical+" "+Pad(leftContent, leftWidth-1)+" "+chars.Vertical+" "+Pad(rightContent, rightWidth-1)+" "+chars.Vertical)
	}

	// Bottom
	result = append(result, chars.BottomLeft+HLine(leftWidth+1, StyleSingle)+chars.HorizontalUp+HLine(rightWidth+1, StyleSingle)+chars.BottomRight)

	return result
}

// HeaderPanel creates a header row (typically for mode status).
// Includes icon, title, and description on one or two lines.
func HeaderPanel(icon, title, description string, width int) []string {
	chars := BoxChars(StyleSingle)
	result := make([]string, 0, 4)

	// Top border
	result = append(result, chars.TopLeft+HLine(width-2, StyleSingle)+chars.TopRight)

	// Mode line
	modeLine := fmt.Sprintf(" %s %s", icon, title)
	if description != "" && VisibleWidth(modeLine)+VisibleWidth(description)+4 <= width-2 {
		// Fit description on same line
		modeLine += "  " + Truncate(description, width-2-VisibleWidth(modeLine)-2)
	}
	result = append(result, chars.Vertical+Pad(modeLine, width-2)+chars.Vertical)

	// Description on separate line if needed
	if description != "" && VisibleWidth(modeLine)+VisibleWidth(description)+4 > width-2 {
		descLine := " " + Truncate(description, width-4)
		result = append(result, chars.Vertical+Pad(descLine, width-2)+chars.Vertical)
	}

	return result
}

// MetricRow formats a label-value pair for display in a panel.
// Format: "Label: Value" left-aligned within width.
func MetricRow(label, value string, width int) string {
	content := label + ": " + value
	return Truncate(content, width)
}

// MetricRowRight formats a label-value pair with right-aligned value.
// Format: "Label:      Value" where value is right-aligned.
func MetricRowRight(label, value string, width int) string {
	labelPart := label + ": "
	valueWidth := width - VisibleWidth(labelPart)
	if valueWidth <= 0 {
		return Truncate(labelPart+value, width)
	}
	return labelPart + PadLeft(value, valueWidth)
}

// StatusLine creates a compact status line with multiple metrics.
// Format: "Label1: Val1  Label2: Val2  Label3: Val3"
func StatusLine(metrics map[string]string, width int) string {
	parts := make([]string, 0, len(metrics))
	for label, value := range metrics {
		parts = append(parts, label+": "+value)
	}
	return Truncate(strings.Join(parts, "  "), width)
}

// ActivityRow formats a single activity log entry for display.
// Format: "HH:MM:SS [mode] Description"
func ActivityRow(timeStr, mode, description string, width int) string {
	// Fixed widths for time and mode
	modeWidth := 8 // "[mode] " with brackets and space
	timeWidth := 9 // "HH:MM:SS "

	// Mode icon/badge
	modeBadge := "[" + Pad(mode, 5) + "]"

	// Calculate description width
	descWidth := width - timeWidth - modeWidth - 1

	row := timeStr + " " + modeBadge + " " + Truncate(description, descWidth)
	return Pad(row, width)
}

// SessionRow formats a session entry for display in the sessions list.
// Format: "[selector] HH:MM  "prompt..."  [N events]"
func SessionRow(selector, timeStr, prompt string, eventCount int, width int) string {
	// Fixed parts
	selectorPart := selector + " " // 2 chars
	timePart := timeStr + "  "     // 7 chars (HH:MM + 2 spaces)
	eventsPart := fmt.Sprintf("  [%d]", eventCount)

	// Calculate prompt width
	promptWidth := width - VisibleWidth(selectorPart) - VisibleWidth(timePart) - VisibleWidth(eventsPart) - 2 // 2 for quotes

	promptDisplay := "\"" + Truncate(prompt, promptWidth) + "\""

	row := selectorPart + timePart + Pad(promptDisplay, promptWidth+2) + eventsPart
	return Truncate(row, width)
}

// ProgressBar creates a simple progress bar.
// Example: [=====>    ] 50%
func ProgressBar(progress float64, width int) string {
	if width < 7 { // Minimum: [>] 0%
		return ""
	}

	// Clamp progress
	if progress < 0 {
		progress = 0
	} else if progress > 1 {
		progress = 1
	}

	// Calculate bar width (excluding [] and percentage)
	pctStr := fmt.Sprintf(" %.0f%%", progress*100)
	barWidth := width - 2 - VisibleWidth(pctStr) // -2 for brackets

	if barWidth < 1 {
		return fmt.Sprintf("[>]%.0f%%", progress*100)
	}

	filled := int(float64(barWidth) * progress)
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("=", filled)
	if filled < barWidth {
		bar += ">"
		bar += strings.Repeat(" ", barWidth-filled-1)
	}

	return "[" + bar + "]" + pctStr
}

// TopicsList formats a list of topics with weights.
// Format: "topic1 (0.8), topic2 (0.6), ..."
func TopicsList(topics []struct {
	Name   string
	Weight float64
}, maxTopics int, width int) string {
	if len(topics) == 0 {
		return ""
	}

	parts := make([]string, 0, maxTopics)
	for i, t := range topics {
		if i >= maxTopics {
			break
		}
		parts = append(parts, fmt.Sprintf("%s(%.1f)", t.Name, t.Weight))
	}

	return Truncate(strings.Join(parts, ", "), width)
}

// DividerWithLabel creates a labeled divider line.
// Example: ├─ Label ──────────────────────┤
func DividerWithLabel(label string, width int) string {
	chars := BoxChars(StyleSingle)

	if label == "" {
		return BoxDivider(width, StyleSingle)
	}

	labelPart := " " + label + " "
	labelWidth := VisibleWidth(labelPart)

	// Need at least: ├─ label ─┤
	minWidth := 2 + 1 + labelWidth + 1
	if width < minWidth {
		return BoxDivider(width, StyleSingle)
	}

	remainingWidth := width - 2 - 1 - labelWidth // corners + initial dash + label
	return chars.VerticalRight + chars.Horizontal + labelPart + HLine(remainingWidth, StyleSingle) + chars.VerticalLeft
}

// ContinuationRow creates a row that continues from the previous panel.
// Used for connecting panels vertically.
func ContinuationRow(content string, width int) string {
	chars := BoxChars(StyleSingle)
	return chars.Vertical + Pad(content, width-2) + chars.Vertical
}

// JoinPanels joins multiple panels vertically, removing duplicate borders.
// Each panel should be a slice of lines from Panel or similar functions.
func JoinPanels(panels ...[]string) []string {
	if len(panels) == 0 {
		return nil
	}

	result := make([]string, 0)

	for i, panel := range panels {
		if len(panel) == 0 {
			continue
		}

		if i == 0 {
			// First panel: include everything
			result = append(result, panel...)
		} else {
			// Subsequent panels: skip top border, replace with divider
			if len(panel) > 0 {
				// Convert top border to divider
				topLine := panel[0]
				if len(topLine) >= 2 {
					chars := BoxChars(StyleSingle)
					// Replace corners with T-junctions
					divider := chars.VerticalRight + topLine[3:len(topLine)-3] + chars.VerticalLeft
					result = append(result, divider)
				}
				// Add rest of panel (skip the top border we just converted)
				if len(panel) > 1 {
					result = append(result, panel[1:]...)
				}
			}
		}
	}

	return result
}
