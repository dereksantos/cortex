package journal

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactSegment_GzipsAndRemovesOriginal(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{
		ClassDir:          dir,
		Fsync:             FsyncPerBatch,
		MaxSegmentEntries: 2,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := w.Append(&Entry{Type: "capture.event", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	// 5 entries / 2 per segment = 3 segments. Compact the closed ones.
	n, err := CompactClosedSegments(dir)
	if err != nil {
		t.Fatalf("CompactClosedSegments: %v", err)
	}
	if n != 2 {
		t.Errorf("compacted = %d, want 2 (segments 1 and 2 — not 3)", n)
	}

	// Verify original .jsonl removed for compacted segments.
	if _, err := os.Stat(segmentPath(dir, 1)); !os.IsNotExist(err) {
		t.Errorf("segment 1 .jsonl should be gone after compaction")
	}
	if _, err := os.Stat(segmentPathGZ(dir, 1)); err != nil {
		t.Errorf("segment 1 .jsonl.gz should exist after compaction: %v", err)
	}

	// Active segment (3) remains uncompressed.
	if _, err := os.Stat(segmentPath(dir, 3)); err != nil {
		t.Errorf("active segment 3 .jsonl should still exist: %v", err)
	}
}

func TestReader_ReadsCompactedSegmentsTransparently(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{
		ClassDir:          dir,
		Fsync:             FsyncPerBatch,
		MaxSegmentEntries: 2,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	const total = 5
	for i := 0; i < total; i++ {
		if _, err := w.Append(&Entry{Type: "capture.event", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	if _, err := CompactClosedSegments(dir); err != nil {
		t.Fatalf("compact: %v", err)
	}

	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	seen := 0
	var lastOffset Offset
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if e.Offset != lastOffset+1 {
			t.Errorf("offset jump: lastOffset=%d, got=%d", lastOffset, e.Offset)
		}
		lastOffset = e.Offset
		seen++
	}
	if seen != total {
		t.Errorf("read %d, want %d (must read across compressed + uncompressed segments)", seen, total)
	}
}

func TestCompactSegment_Idempotent(t *testing.T) {
	dir := newClassDir(t)
	w, _ := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch, MaxSegmentEntries: 1})
	w.Append(&Entry{Type: "capture.event", Payload: json.RawMessage(`{}`)})
	w.Append(&Entry{Type: "capture.event", Payload: json.RawMessage(`{}`)})
	w.Close()

	if _, err := CompactClosedSegments(dir); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	// Run again — should be a no-op.
	n, err := CompactClosedSegments(dir)
	if err != nil {
		t.Fatalf("second compact: %v", err)
	}
	if n != 0 {
		t.Errorf("second compact = %d, want 0 (already compacted)", n)
	}
}

func TestCompactClassDirAll(t *testing.T) {
	root, err := os.MkdirTemp("", "journal-compact-all-")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(root)

	for _, class := range []string{"capture", "dream"} {
		classDir := filepath.Join(root, class)
		w, _ := NewWriter(WriterOpts{ClassDir: classDir, Fsync: FsyncPerBatch, MaxSegmentEntries: 1})
		for i := 0; i < 3; i++ {
			w.Append(&Entry{Type: "x.event", Payload: json.RawMessage(`{}`)})
		}
		w.Close()
	}

	n, err := CompactClassDirAll(root)
	if err != nil {
		t.Fatalf("CompactClassDirAll: %v", err)
	}
	// 2 classes × 2 closed segments each = 4
	if n != 4 {
		t.Errorf("compacted = %d, want 4", n)
	}
}
