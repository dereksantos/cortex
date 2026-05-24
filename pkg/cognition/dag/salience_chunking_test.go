package dag

import (
	"strings"
	"testing"
)

func TestSplitLines(t *testing.T) {
	tests := map[string]int{
		"":                       0,
		"a\nb\nc":                3,
		"a\nb\nc\n":              3,
		"\n":                     1,
		"single line no newline": 1,
	}
	for input, wantCount := range tests {
		got := splitLines(input)
		if len(got) != wantCount {
			t.Errorf("splitLines(%q): got %d lines, want %d (%+v)", input, len(got), wantCount, got)
		}
		// Round-trip: joining all parts byte-for-byte must equal input.
		joined := strings.Join(got, "")
		if joined != input {
			t.Errorf("splitLines(%q): round-trip mismatch — got %q", input, joined)
		}
	}
}

func TestBuildChunksByLine_FitsInSingleChunk(t *testing.T) {
	lines := []string{"line a\n", "line b\n", "line c\n"}
	chunks := buildChunksByLine(lines, 1000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small input, got %d", len(chunks))
	}
	if chunks[0].startLine != 1 || chunks[0].endLine != 3 {
		t.Errorf("chunk bounds: got %d-%d want 1-3", chunks[0].startLine, chunks[0].endLine)
	}
}

func TestBuildChunksByLine_SplitsByCap(t *testing.T) {
	// Each line is ~10 chars = ~3 tokens. cap=5 → 1-2 lines per chunk.
	lines := []string{
		"line one\n",   // 9 chars ≈ 2 tokens
		"line two\n",   // 9 chars ≈ 2 tokens
		"line three\n", // 11 chars ≈ 2 tokens
		"line four\n",  // 10 chars ≈ 2 tokens
	}
	chunks := buildChunksByLine(lines, 5)
	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks for cap=5 vs 4 lines, got %d", len(chunks))
	}
	// Round-trip: concatenating all chunk contents must equal original.
	joined := ""
	for _, c := range chunks {
		joined += c.content
	}
	want := strings.Join(lines, "")
	if joined != want {
		t.Errorf("chunks round-trip mismatch:\n got: %q\nwant: %q", joined, want)
	}
	// Line bounds must tile [1, N] without gaps or overlaps.
	expectedStart := 1
	for i, c := range chunks {
		if c.startLine != expectedStart {
			t.Errorf("chunk %d startLine: got %d want %d", i, c.startLine, expectedStart)
		}
		if c.endLine < c.startLine {
			t.Errorf("chunk %d: endLine %d < startLine %d", i, c.endLine, c.startLine)
		}
		expectedStart = c.endLine + 1
	}
	if expectedStart-1 != len(lines) {
		t.Errorf("chunks span %d lines, want %d", expectedStart-1, len(lines))
	}
}

func TestBuildChunksByLine_OversizedSingleLineStandsAlone(t *testing.T) {
	// A single line larger than the cap should not be split mid-line —
	// it stays as its own chunk. Splitting source code mid-line would
	// corrupt it.
	bigLine := strings.Repeat("x", 4000) + "\n"
	smallLine := "small\n"
	lines := []string{bigLine, smallLine}
	chunks := buildChunksByLine(lines, 100)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (big alone, small alone), got %d", len(chunks))
	}
	if chunks[0].content != bigLine {
		t.Errorf("first chunk should be the whole big line; got len=%d want %d", len(chunks[0].content), len(bigLine))
	}
}

func TestJoinChunks_LocationHeaders(t *testing.T) {
	chunks := []chunkRange{
		{startLine: 1, endLine: 10, content: "first chunk content\n"},
		{startLine: 11, endLine: 20, content: "second chunk content\n"},
	}
	out := joinChunks(chunks, 2)
	if !strings.Contains(out, "[chunk 1/2, lines 1-10]") {
		t.Errorf("missing chunk 1 header in:\n%s", out)
	}
	if !strings.Contains(out, "[chunk 2/2, lines 11-20]") {
		t.Errorf("missing chunk 2 header in:\n%s", out)
	}
	if !strings.Contains(out, "first chunk content") {
		t.Errorf("missing chunk 1 content")
	}
	if !strings.Contains(out, "second chunk content") {
		t.Errorf("missing chunk 2 content")
	}
}

func TestJoinChunks_TruncatedTotalShowsRealCount(t *testing.T) {
	// When the executor truncates to maxChunks (8), the joined headers
	// should still display the REAL total so the calling model knows
	// content was withheld.
	chunks := []chunkRange{
		{startLine: 1, endLine: 100, content: "c1\n"},
		{startLine: 101, endLine: 200, content: "c2\n"},
	}
	out := joinChunks(chunks, 12) // total 12 even though we emit 2
	if !strings.Contains(out, "[chunk 1/12, lines 1-100]") {
		t.Errorf("header should show real total of 12, got:\n%s", out)
	}
}
