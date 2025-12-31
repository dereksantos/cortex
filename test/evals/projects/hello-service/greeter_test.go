package main

import "testing"

func TestGreet(t *testing.T) {
	g := NewGreeter()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "World", "Hello, World!"},
		{"name", "Alice", "Hello, Alice!"},
		{"empty", "", "Hello, !"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.Greet(tt.input)
			if got != tt.expected {
				t.Errorf("Greet(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
