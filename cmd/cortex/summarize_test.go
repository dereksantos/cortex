package main

import (
	"strings"
	"testing"
)

func TestSplitChunks(t *testing.T) {
	// Small input → one chunk, returned whole.
	if got := splitChunks("a\nb\n", 100); len(got) != 1 || got[0] != "a\nb\n" {
		t.Errorf("small input should be one whole chunk, got %q", got)
	}

	// 10 lines of ~10 chars at a 25-char budget → multiple chunks, each under
	// budget, and the concatenation reproduces the input (no data lost).
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("0123456789\n") // 11 bytes/line
	}
	in := sb.String()
	chunks := splitChunks(in, 25)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 25 {
			t.Errorf("chunk %d is %d bytes, over the 25 budget", i, len(c))
		}
	}
	if strings.Join(chunks, "") != in {
		t.Error("chunks must reconstruct the input exactly (no lines lost or split)")
	}

	// A single line longer than the budget becomes its own oversized chunk
	// rather than being cut mid-line.
	long := strings.Repeat("x", 100) + "\n"
	got := splitChunks(long, 25)
	if len(got) != 1 || got[0] != long {
		t.Errorf("an over-budget line should stay whole, got %d chunks", len(got))
	}
}
