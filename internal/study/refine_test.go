package study

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLinesFile writes n lines of the form "line %03d\n" (a uniform 9
// bytes each: 8 chars + newline). It returns the path and a helper that
// maps a 1-indexed line number to its starting byte offset, so tests can
// construct byte-grid chunks with known line layouts.
func writeLinesFile(t *testing.T, n int) (path string, lineByte func(N int) int64) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "lines.txt")
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path, func(N int) int64 { return int64(9 * (N - 1)) }
}

func TestRefineChunk_MiddleChunk_RealLineBounds(t *testing.T) {
	path, lb := writeLinesFile(t, 500)
	ch := &Chunk{
		Path:       path,
		RelPath:    "lines.txt",
		ByteOffset: lb(200),
		ByteLength: int(lb(261) - lb(200)), // bytes spanning lines 200..260
		Lang:       "txt",
	}
	if err := RefineChunk(ch, streamingLineBase(path)); err != nil {
		t.Fatalf("RefineChunk: %v", err)
	}
	if ch.LineStart != 200 {
		t.Errorf("LineStart = %d, want 200", ch.LineStart)
	}
	if ch.LineEnd != 260 {
		t.Errorf("LineEnd = %d, want 260", ch.LineEnd)
	}
	if ch.EffLines != 61 {
		t.Errorf("EffLines = %d, want 61", ch.EffLines)
	}
	if !ch.Refined {
		t.Error("Refined flag not set")
	}
}

func TestRefineChunk_HeadChunk(t *testing.T) {
	path, lb := writeLinesFile(t, 500)
	ch := &Chunk{Path: path, RelPath: "lines.txt", ByteOffset: 0, ByteLength: int(lb(51)), Lang: "txt"}
	if err := RefineChunk(ch, streamingLineBase(path)); err != nil {
		t.Fatalf("RefineChunk: %v", err)
	}
	if ch.LineStart != 1 {
		t.Errorf("head chunk LineStart = %d, want 1", ch.LineStart)
	}
	if ch.LineEnd != 50 {
		t.Errorf("head chunk LineEnd = %d, want 50", ch.LineEnd)
	}
}

func TestRefineChunk_SnapsToNewlineBoundaries(t *testing.T) {
	path, lb := writeLinesFile(t, 500)
	// Deliberately start 3 bytes into line 100 (mid-line).
	ch := &Chunk{Path: path, RelPath: "lines.txt", ByteOffset: lb(100) + 3, ByteLength: 9 * 5, Lang: "txt"}
	if err := RefineChunk(ch, streamingLineBase(path)); err != nil {
		t.Fatalf("RefineChunk: %v", err)
	}
	if ch.ByteOffset%9 != 0 {
		t.Errorf("snapped ByteOffset = %d, not aligned to a line boundary", ch.ByteOffset)
	}
	if ch.LineStart != 101 {
		t.Errorf("LineStart = %d, want 101 (partial leading line dropped)", ch.LineStart)
	}
}

func TestRefineChunk_EOFChunk(t *testing.T) {
	path, lb := writeLinesFile(t, 500)
	size := int64(500 * 9)
	ch := &Chunk{Path: path, RelPath: "lines.txt", ByteOffset: lb(496), ByteLength: int(size - lb(496)), Lang: "txt"}
	if err := RefineChunk(ch, streamingLineBase(path)); err != nil {
		t.Fatalf("RefineChunk: %v", err)
	}
	if ch.LineEnd != 500 {
		t.Errorf("EOF chunk LineEnd = %d, want 500", ch.LineEnd)
	}
}

func TestRefineChunk_Idempotent(t *testing.T) {
	path, lb := writeLinesFile(t, 500)
	ch := &Chunk{Path: path, RelPath: "lines.txt", ByteOffset: lb(200), ByteLength: int(lb(261) - lb(200)), Lang: "txt"}
	lbf := streamingLineBase(path)
	if err := RefineChunk(ch, lbf); err != nil {
		t.Fatalf("first RefineChunk: %v", err)
	}
	snap := *ch
	if err := RefineChunk(ch, lbf); err != nil {
		t.Fatalf("second RefineChunk: %v", err)
	}
	if *ch != snap {
		t.Errorf("second RefineChunk mutated an already-refined chunk:\n before=%+v\n after =%+v", snap, *ch)
	}
}

func TestStreamingLineBase_Offsets(t *testing.T) {
	path, lb := writeLinesFile(t, 300)
	fn := streamingLineBase(path)
	cases := []struct {
		off  int64
		want int
	}{
		{0, 1},
		{lb(1), 1},
		{lb(50), 50},
		{lb(300), 300},
	}
	for _, c := range cases {
		got, err := fn(c.off)
		if err != nil {
			t.Fatalf("lineBase(%d): %v", c.off, err)
		}
		if got != c.want {
			t.Errorf("lineBase(%d) = %d, want %d", c.off, got, c.want)
		}
	}
}
