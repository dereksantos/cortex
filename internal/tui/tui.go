// Package tui provides composable terminal UI primitives for Cortex.
//
// This package centralizes TUI functionality including:
//   - Buffer: Composable screen rendering
//   - Text: UTF-8 safe text operations
//   - Box: Box drawing primitives
//   - ANSI: Escape code constants
//   - Spinner: Animation frame management
package tui

import (
	"fmt"
	"os"
	"strings"
)

// Buffer provides composable screen rendering.
// Lines can be set in any order and the buffer handles
// proper positioning and output.
type Buffer struct {
	width int
	lines []string
}

// NewBuffer creates a new buffer with the specified width.
// Width is used for text operations like padding and truncation.
func NewBuffer(width int) *Buffer {
	if width <= 0 {
		width = 80 // sensible default
	}
	return &Buffer{
		width: width,
		lines: make([]string, 0),
	}
}

// Width returns the buffer width.
func (b *Buffer) Width() int {
	return b.width
}

// Height returns the current number of lines in the buffer.
func (b *Buffer) Height() int {
	return len(b.lines)
}

// Line sets the content at the specified line (0-indexed).
// If the line doesn't exist, the buffer expands to accommodate it.
// Empty lines are filled with empty strings.
func (b *Buffer) Line(y int, content string) {
	if y < 0 {
		return
	}

	// Expand buffer if needed
	for len(b.lines) <= y {
		b.lines = append(b.lines, "")
	}

	b.lines[y] = content
}

// Append adds a new line at the end of the buffer.
func (b *Buffer) Append(content string) {
	b.lines = append(b.lines, content)
}

// Clear resets the buffer to empty state.
func (b *Buffer) Clear() {
	b.lines = b.lines[:0]
}

// String returns the buffer contents as a single string
// with newlines between each line.
func (b *Buffer) String() string {
	return strings.Join(b.lines, "\n")
}

// Write flushes the buffer contents to stdout.
func (b *Buffer) Write() {
	fmt.Fprint(os.Stdout, b.String())
}

// WriteTo writes the buffer contents to the specified writer.
func (b *Buffer) WriteTo(w *os.File) {
	fmt.Fprint(w, b.String())
}

// Lines returns a copy of all lines in the buffer.
func (b *Buffer) Lines() []string {
	result := make([]string, len(b.lines))
	copy(result, b.lines)
	return result
}
