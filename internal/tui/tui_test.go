package tui

import (
	"strings"
	"testing"
)

// ============================================================================
// Buffer Tests
// ============================================================================

func TestNewBuffer(t *testing.T) {
	tests := []struct {
		name     string
		width    int
		wantW    int
	}{
		{"positive width", 80, 80},
		{"zero width defaults to 80", 0, 80},
		{"negative width defaults to 80", -10, 80},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := NewBuffer(tt.width)
			if buf.Width() != tt.wantW {
				t.Errorf("Width() = %d, want %d", buf.Width(), tt.wantW)
			}
			if buf.Height() != 0 {
				t.Errorf("Height() = %d, want 0", buf.Height())
			}
		})
	}
}

func TestBufferLine(t *testing.T) {
	buf := NewBuffer(80)

	buf.Line(0, "first")
	buf.Line(2, "third")

	if buf.Height() != 3 {
		t.Errorf("Height() = %d, want 3", buf.Height())
	}

	lines := buf.Lines()
	if lines[0] != "first" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "first")
	}
	if lines[1] != "" {
		t.Errorf("lines[1] = %q, want empty", lines[1])
	}
	if lines[2] != "third" {
		t.Errorf("lines[2] = %q, want %q", lines[2], "third")
	}
}

func TestBufferLineNegative(t *testing.T) {
	buf := NewBuffer(80)
	buf.Line(-1, "should be ignored")
	if buf.Height() != 0 {
		t.Errorf("Height() = %d, want 0 (negative line should be ignored)", buf.Height())
	}
}

func TestBufferAppend(t *testing.T) {
	buf := NewBuffer(80)
	buf.Append("line1")
	buf.Append("line2")

	if buf.Height() != 2 {
		t.Errorf("Height() = %d, want 2", buf.Height())
	}
	if buf.String() != "line1\nline2" {
		t.Errorf("String() = %q, want %q", buf.String(), "line1\nline2")
	}
}

func TestBufferClear(t *testing.T) {
	buf := NewBuffer(80)
	buf.Append("line1")
	buf.Append("line2")
	buf.Clear()

	if buf.Height() != 0 {
		t.Errorf("Height() = %d after Clear(), want 0", buf.Height())
	}
}

// ============================================================================
// Text Tests
// ============================================================================

func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"unicode", "hello", 5},
		{"emoji", "hi", 2}, // Just the ASCII portion
		{"japanese", "hello", 5},
		{"mixed", "ab", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VisibleWidth(tt.input)
			if got != tt.want {
				t.Errorf("VisibleWidth(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{"no truncation needed", "hello", 10, "hello"},
		{"exact fit", "hello", 5, "hello"},
		{"truncate with ellipsis", "hello world", 8, "hello..."},
		{"very short max", "hello", 3, "hel"},
		{"zero width", "hello", 0, ""},
		{"negative width", "hello", -5, ""},
		{"unicode needs truncation", "hello world", 6, "hel..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{"shorter than width", "hi", 5, "hi   "},
		{"exact width", "hello", 5, "hello"},
		{"longer than width", "hello world", 5, "hello"},
		{"zero width", "hello", 0, ""},
		{"empty string", "", 5, "     "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PadRight(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("PadRight(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestPadLeft(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{"shorter than width", "hi", 5, "   hi"},
		{"exact width", "hello", 5, "hello"},
		{"longer than width", "hello world", 5, "hello"},
		{"zero width", "hello", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PadLeft(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("PadLeft(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestPadCenter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{"odd padding", "hi", 5, " hi  "},
		{"even padding", "hi", 6, "  hi  "},
		{"exact width", "hello", 5, "hello"},
		{"longer than width", "hello world", 5, "hello"},
		{"single char", "a", 5, "  a  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PadCenter(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("PadCenter(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestPadIsAliasForPadRight(t *testing.T) {
	input := "test"
	width := 10
	if Pad(input, width) != PadRight(input, width) {
		t.Error("Pad should be an alias for PadRight")
	}
}

// ============================================================================
// Box Tests
// ============================================================================

func TestBoxChars(t *testing.T) {
	single := BoxChars(StyleSingle)
	if single.Horizontal != "─" {
		t.Errorf("Single horizontal = %q, want %q", single.Horizontal, "─")
	}
	if single.TopLeft != "┌" {
		t.Errorf("Single top left = %q, want %q", single.TopLeft, "┌")
	}

	double := BoxChars(StyleDouble)
	if double.Horizontal != "═" {
		t.Errorf("Double horizontal = %q, want %q", double.Horizontal, "═")
	}
	if double.TopLeft != "╔" {
		t.Errorf("Double top left = %q, want %q", double.TopLeft, "╔")
	}
}

func TestHLine(t *testing.T) {
	tests := []struct {
		name  string
		width int
		style BoxStyle
		want  string
	}{
		{"single 5", 5, StyleSingle, "─────"},
		{"double 3", 3, StyleDouble, "═══"},
		{"zero width", 0, StyleSingle, ""},
		{"negative width", -1, StyleSingle, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HLine(tt.width, tt.style)
			if got != tt.want {
				t.Errorf("HLine(%d, %v) = %q, want %q", tt.width, tt.style, got, tt.want)
			}
		})
	}
}

func TestBoxTop(t *testing.T) {
	got := BoxTop(10, StyleSingle)
	if !strings.HasPrefix(got, "┌") {
		t.Errorf("BoxTop should start with ┌, got %q", got)
	}
	if !strings.HasSuffix(got, "┐") {
		t.Errorf("BoxTop should end with ┐, got %q", got)
	}
	if VisibleWidth(got) != 10 {
		t.Errorf("BoxTop width = %d, want 10", VisibleWidth(got))
	}
}

func TestBoxBottom(t *testing.T) {
	got := BoxBottom(10, StyleDouble)
	if !strings.HasPrefix(got, "╚") {
		t.Errorf("BoxBottom should start with ╚, got %q", got)
	}
	if !strings.HasSuffix(got, "╝") {
		t.Errorf("BoxBottom should end with ╝, got %q", got)
	}
}

func TestBoxRow(t *testing.T) {
	got := BoxRow("test", 10, StyleSingle)
	if !strings.HasPrefix(got, "│") {
		t.Errorf("BoxRow should start with │, got %q", got)
	}
	if !strings.HasSuffix(got, "│") {
		t.Errorf("BoxRow should end with │, got %q", got)
	}
	if !strings.Contains(got, "test") {
		t.Errorf("BoxRow should contain content, got %q", got)
	}
}

func TestBoxDivider(t *testing.T) {
	got := BoxDivider(10, StyleSingle)
	if !strings.HasPrefix(got, "├") {
		t.Errorf("BoxDivider should start with ├, got %q", got)
	}
	if !strings.HasSuffix(got, "┤") {
		t.Errorf("BoxDivider should end with ┤, got %q", got)
	}
}

func TestBox(t *testing.T) {
	lines := Box([]string{"hello", "world"}, 12, StyleSingle)

	if len(lines) != 4 { // top + 2 content + bottom
		t.Errorf("Box should have 4 lines, got %d", len(lines))
	}

	// Check structure
	if !strings.HasPrefix(lines[0], "┌") {
		t.Errorf("First line should be top border")
	}
	if !strings.HasPrefix(lines[1], "│") {
		t.Errorf("Content lines should have vertical borders")
	}
	if !strings.HasPrefix(lines[3], "└") {
		t.Errorf("Last line should be bottom border")
	}
}

func TestBoxWithTitle(t *testing.T) {
	lines := BoxWithTitle("Title", []string{"content"}, 20, StyleSingle)

	if len(lines) != 3 { // top with title + content + bottom
		t.Errorf("BoxWithTitle should have 3 lines, got %d", len(lines))
	}

	if !strings.Contains(lines[0], "Title") {
		t.Errorf("Top line should contain title, got %q", lines[0])
	}
}

func TestBoxSmallWidth(t *testing.T) {
	// Width too small should return nil/empty
	lines := Box([]string{"test"}, 1, StyleSingle)
	if lines != nil {
		t.Errorf("Box with width 1 should return nil, got %v", lines)
	}
}

// ============================================================================
// ANSI Tests
// ============================================================================

func TestClearAndHome(t *testing.T) {
	got := ClearAndHome()
	if got != ClearScreen+CursorHome {
		t.Errorf("ClearAndHome() = %q, want %q", got, ClearScreen+CursorHome)
	}
}

func TestMoveTo(t *testing.T) {
	got := MoveTo(5, 10)
	want := "\033[5;10H"
	if got != want {
		t.Errorf("MoveTo(5, 10) = %q, want %q", got, want)
	}
}

func TestMoveDirections(t *testing.T) {
	tests := []struct {
		name string
		fn   func(int) string
		n    int
		want string
	}{
		{"up 3", MoveUp, 3, "\033[3A"},
		{"down 2", MoveDown, 2, "\033[2B"},
		{"right 5", MoveRight, 5, "\033[5C"},
		{"left 1", MoveLeft, 1, "\033[1D"},
		{"up 0", MoveUp, 0, ""},
		{"down -1", MoveDown, -1, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(tt.n)
			if got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestColor256(t *testing.T) {
	got := Color256(196)
	want := "\033[38;5;196m"
	if got != want {
		t.Errorf("Color256(196) = %q, want %q", got, want)
	}
}

func TestColorRGB(t *testing.T) {
	got := ColorRGB(255, 128, 0)
	want := "\033[38;2;255;128;0m"
	if got != want {
		t.Errorf("ColorRGB(255, 128, 0) = %q, want %q", got, want)
	}
}

func TestStyled(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		styles []string
		want   string
	}{
		{"no styles", "text", nil, "text"},
		{"single style", "text", []string{Red}, Red + "text" + Reset},
		{"multiple styles", "text", []string{Red, Bold}, Red + Bold + "text" + Reset},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Styled(tt.text, tt.styles...)
			if got != tt.want {
				t.Errorf("Styled(%q, %v) = %q, want %q", tt.text, tt.styles, got, tt.want)
			}
		})
	}
}

// ============================================================================
// Spinner Tests
// ============================================================================

func TestNewSpinner(t *testing.T) {
	frames := []string{"a", "b", "c"}
	s := NewSpinner(frames)

	if s.Len() != 3 {
		t.Errorf("Len() = %d, want 3", s.Len())
	}
	if s.Current() != "a" {
		t.Errorf("Current() = %q, want %q", s.Current(), "a")
	}
}

func TestNewSpinnerEmpty(t *testing.T) {
	s := NewSpinner(nil)
	if s.Len() != 1 {
		t.Errorf("Empty spinner should have fallback frame")
	}
}

func TestSpinnerNext(t *testing.T) {
	frames := []string{"a", "b", "c"}
	s := NewSpinner(frames)

	// Should start at "a"
	if s.Current() != "a" {
		t.Errorf("Current() = %q, want %q", s.Current(), "a")
	}

	// Next should return "b"
	if got := s.Next(); got != "b" {
		t.Errorf("Next() = %q, want %q", got, "b")
	}

	// Now current should be "b"
	if s.Current() != "b" {
		t.Errorf("Current() after Next() = %q, want %q", s.Current(), "b")
	}

	// Continue to wrap around
	s.Next() // c
	if got := s.Next(); got != "a" {
		t.Errorf("Next() after wrap = %q, want %q", got, "a")
	}
}

func TestSpinnerFrame(t *testing.T) {
	frames := []string{"a", "b", "c"}
	s := NewSpinner(frames)

	tests := []struct {
		index int
		want  string
	}{
		{0, "a"},
		{1, "b"},
		{2, "c"},
		{3, "a"}, // wrap
		{-1, "c"}, // negative wrap
	}

	for _, tt := range tests {
		got := s.Frame(tt.index)
		if got != tt.want {
			t.Errorf("Frame(%d) = %q, want %q", tt.index, got, tt.want)
		}
	}
}

func TestSpinnerReset(t *testing.T) {
	frames := []string{"a", "b", "c"}
	s := NewSpinner(frames)

	s.Next()
	s.Next()
	s.Reset()

	if s.Current() != "a" {
		t.Errorf("Current() after Reset() = %q, want %q", s.Current(), "a")
	}
}

func TestSpinnerFrames(t *testing.T) {
	frames := []string{"a", "b", "c"}
	s := NewSpinner(frames)

	got := s.Frames()
	if len(got) != len(frames) {
		t.Errorf("Frames() length = %d, want %d", len(got), len(frames))
	}

	// Modify returned slice shouldn't affect original
	got[0] = "modified"
	if s.Frame(0) == "modified" {
		t.Error("Frames() should return a copy, not the original slice")
	}
}

func TestGetModeSpinner(t *testing.T) {
	// Test known modes
	dream := GetModeSpinner("dream")
	if len(dream) == 0 {
		t.Error("dream spinner should not be empty")
	}

	think := GetModeSpinner("think")
	if len(think) == 0 {
		t.Error("think spinner should not be empty")
	}

	// Test unknown mode returns classic
	unknown := GetModeSpinner("unknown")
	if len(unknown) != len(SpinnerClassic) {
		t.Error("unknown mode should return classic spinner")
	}
}

func TestGetModeFrame(t *testing.T) {
	// Should not panic and return valid frames
	for _, mode := range []string{"dream", "think", "reflect", "reflex", "resolve", "unknown"} {
		for i := -2; i < 10; i++ {
			frame := GetModeFrame(mode, i)
			if frame == "" {
				t.Errorf("GetModeFrame(%q, %d) returned empty string", mode, i)
			}
		}
	}
}

func TestNewModeSpinner(t *testing.T) {
	s := NewModeSpinner("dream")
	if s.Len() != len(SpinnerDream) {
		t.Errorf("NewModeSpinner(dream).Len() = %d, want %d", s.Len(), len(SpinnerDream))
	}
}

func TestPredefinedSpinnersNotEmpty(t *testing.T) {
	spinners := map[string][]string{
		"Dream":   SpinnerDream,
		"Think":   SpinnerThink,
		"Reflect": SpinnerReflect,
		"Reflex":  SpinnerReflex,
		"Resolve": SpinnerResolve,
		"Insight": SpinnerInsight,
		"Digest":  SpinnerDigest,
		"Classic": SpinnerClassic,
		"Dots":    SpinnerDots,
		"Braille": SpinnerBraille,
		"Blocks":  SpinnerBlocks,
	}

	for name, frames := range spinners {
		if len(frames) == 0 {
			t.Errorf("Spinner%s should not be empty", name)
		}
	}
}

// ============================================================================
// Integration Tests
// ============================================================================

func TestBufferWithBoxIntegration(t *testing.T) {
	buf := NewBuffer(30)

	// Create a box and add it to buffer
	boxLines := Box([]string{"Hello", "World"}, 20, StyleSingle)
	for i, line := range boxLines {
		buf.Line(i, line)
	}

	result := buf.String()
	if !strings.Contains(result, "Hello") {
		t.Error("Buffer should contain box content")
	}
	if !strings.Contains(result, "┌") {
		t.Error("Buffer should contain box characters")
	}
}

func TestTextWithANSIIntegration(t *testing.T) {
	// Create styled, padded text
	text := Styled(PadRight("Status", 10), Green, Bold)

	// Should contain the styling and content
	if !strings.Contains(text, "Status") {
		t.Error("Styled text should contain content")
	}
	if !strings.Contains(text, Green) {
		t.Error("Styled text should contain color code")
	}
	if !strings.Contains(text, Reset) {
		t.Error("Styled text should end with reset")
	}
}

// ============================================================================
// Panel Tests
// ============================================================================

func TestPanelWidthConsistency(t *testing.T) {
	width := 40
	lines := Panel("Test Title", []string{"Line 1", "Line 2", "Long line that needs truncation here"}, width)

	for i, line := range lines {
		got := VisibleWidth(line)
		if got != width {
			t.Errorf("Panel line %d: width=%d, want %d\nline: %q", i, got, width, line)
		}
	}
}

func TestPanelCleanInputProducesCleanOutput(t *testing.T) {
	// Panel expects clean input (no embedded newlines in content)
	// Caller is responsible for sanitizing input
	lines := Panel("Title", []string{"Clean line 1", "Clean line 2"}, 30)

	for i, line := range lines {
		if strings.Contains(line, "\n") {
			t.Errorf("Panel line %d contains embedded newline: %q", i, line)
		}
		if strings.Contains(line, "\r") {
			t.Errorf("Panel line %d contains embedded carriage return: %q", i, line)
		}
	}
}

func TestSplitPanelWidthConsistency(t *testing.T) {
	leftWidth := 20
	rightWidth := 20
	expectedWidth := leftWidth + rightWidth + 5 // borders + divider + padding

	left := []string{"Left 1", "Left 2"}
	right := []string{"Right 1", "Right 2", "Right 3"}

	lines := SplitPanel("Left", "Right", left, right, leftWidth, rightWidth)

	for i, line := range lines {
		got := VisibleWidth(line)
		if got != expectedWidth {
			t.Errorf("SplitPanel line %d: width=%d, want %d\nline: %q", i, got, expectedWidth, line)
		}
	}
}

func TestSplitPanelStructure(t *testing.T) {
	lines := SplitPanel("L", "R", []string{"a"}, []string{"b"}, 15, 15)

	// Should have: top, header, divider, content, bottom = 5 lines
	if len(lines) != 5 {
		t.Errorf("SplitPanel should have 5 lines, got %d", len(lines))
	}

	// Top: ┌───┬───┐
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Errorf("Invalid top border: %q", lines[0])
	}
	if strings.Count(lines[0], "┬") != 1 {
		t.Errorf("Top should have one ┬: %q", lines[0])
	}

	// Header row: │ L │ R │
	if strings.Count(lines[1], "│") != 3 {
		t.Errorf("Header should have 3 │: %q", lines[1])
	}

	// Divider: ├───┼───┤
	if !strings.HasPrefix(lines[2], "├") || !strings.HasSuffix(lines[2], "┤") {
		t.Errorf("Invalid divider: %q", lines[2])
	}
	if strings.Count(lines[2], "┼") != 1 {
		t.Errorf("Divider should have one ┼: %q", lines[2])
	}

	// Content row: │ a │ b │
	if strings.Count(lines[3], "│") != 3 {
		t.Errorf("Content should have 3 │: %q", lines[3])
	}

	// Bottom: └───┴───┘
	if !strings.HasPrefix(lines[4], "└") || !strings.HasSuffix(lines[4], "┘") {
		t.Errorf("Invalid bottom border: %q", lines[4])
	}
	if strings.Count(lines[4], "┴") != 1 {
		t.Errorf("Bottom should have one ┴: %q", lines[4])
	}
}

func TestHeaderPanelWidthConsistency(t *testing.T) {
	width := 50

	tests := []struct {
		name string
		icon string
		title string
		desc string
	}{
		{"short", "●", "IDLE", ""},
		{"with desc", "●", "THINKING", "processing patterns"},
		{"long desc", "●", "DREAMING", "a very long description that should be truncated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := HeaderPanel(tt.icon, tt.title, tt.desc, width)
			for i, line := range lines {
				got := VisibleWidth(line)
				if got != width {
					t.Errorf("HeaderPanel line %d: width=%d, want %d\nline: %q", i, got, width, line)
				}
			}
		})
	}
}

func TestMetricRow(t *testing.T) {
	row := MetricRow("Events", "1234", 20)
	if !strings.Contains(row, "Events") || !strings.Contains(row, "1234") {
		t.Errorf("MetricRow should contain label and value: %q", row)
	}
	if VisibleWidth(row) > 20 {
		t.Errorf("MetricRow width=%d, should be <= 20", VisibleWidth(row))
	}
}

func TestActivityRow(t *testing.T) {
	width := 50
	row := ActivityRow("15:04:05", "dream", "Processing insights", width)

	got := VisibleWidth(row)
	if got != width {
		t.Errorf("ActivityRow width=%d, want %d\nrow: %q", got, width, row)
	}

	if !strings.Contains(row, "15:04:05") {
		t.Errorf("ActivityRow should contain time: %q", row)
	}
	if !strings.Contains(row, "dream") {
		t.Errorf("ActivityRow should contain mode: %q", row)
	}
}

func TestSessionRow(t *testing.T) {
	width := 50
	row := SessionRow("> ", "15:04", "implement feature", 25, width)

	got := VisibleWidth(row)
	if got != width {
		t.Errorf("SessionRow width=%d, want %d\nrow: %q", got, width, row)
	}

	if !strings.HasPrefix(row, "> ") {
		t.Errorf("SessionRow should start with selector: %q", row)
	}
}

func TestProgressBar(t *testing.T) {
	tests := []struct {
		progress float64
		width    int
	}{
		{0.0, 20},
		{0.5, 20},
		{1.0, 20},
		{0.75, 30},
	}

	for _, tt := range tests {
		bar := ProgressBar(tt.progress, tt.width)
		got := VisibleWidth(bar)
		if got > tt.width {
			t.Errorf("ProgressBar(%.1f, %d) width=%d, should be <= %d\nbar: %q",
				tt.progress, tt.width, got, tt.width, bar)
		}
		if !strings.HasPrefix(bar, "[") {
			t.Errorf("ProgressBar should start with [: %q", bar)
		}
	}
}

func TestDividerWithLabel(t *testing.T) {
	width := 30

	// With label
	div := DividerWithLabel("Section", width)
	if VisibleWidth(div) != width {
		t.Errorf("DividerWithLabel width=%d, want %d", VisibleWidth(div), width)
	}
	if !strings.Contains(div, "Section") {
		t.Errorf("DividerWithLabel should contain label: %q", div)
	}

	// Without label (should be same as BoxDivider)
	divNoLabel := DividerWithLabel("", width)
	if VisibleWidth(divNoLabel) != width {
		t.Errorf("DividerWithLabel (no label) width=%d, want %d", VisibleWidth(divNoLabel), width)
	}
}
