package journal

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newClassDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "journal-test-")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "capture")
}

func mustEntry(t *testing.T, payload string) *Entry {
	t.Helper()
	return &Entry{
		Type:    "capture.event",
		Payload: json.RawMessage(payload),
	}
}

func TestWriter_AppendRoundTrip(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	payloads := []string{`{"a":1}`, `{"b":2}`, `{"c":3}`}
	var offsets []Offset
	for _, p := range payloads {
		off, err := w.Append(mustEntry(t, p))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		offsets = append(offsets, off)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i, want := range []Offset{1, 2, 3} {
		if offsets[i] != want {
			t.Errorf("offsets[%d] = %d, want %d", i, offsets[i], want)
		}
	}

	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	for i, want := range payloads {
		e, err := r.Next()
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		if e.Offset != offsets[i] {
			t.Errorf("entry[%d].Offset = %d, want %d", i, e.Offset, offsets[i])
		}
		if string(e.Payload) != want {
			t.Errorf("entry[%d].Payload = %s, want %s", i, e.Payload, want)
		}
		if e.V != 1 {
			t.Errorf("entry[%d].V = %d, want 1 (default)", i, e.V)
		}
		if e.TS.IsZero() {
			t.Errorf("entry[%d].TS not set", i)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("trailing Next: got %v, want io.EOF", err)
	}
}

func TestWriter_RotatesByEntries(t *testing.T) {
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
		if _, err := w.Append(mustEntry(t, `{}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	nums, err := listSegments(dir)
	if err != nil {
		t.Fatalf("listSegments: %v", err)
	}
	// 5 entries / max 2 per segment = 3 segments (2 + 2 + 1).
	if len(nums) != 3 {
		t.Errorf("segment count = %d, want 3 (nums=%v)", len(nums), nums)
	}
}

func TestWriter_RotatesByBytes(t *testing.T) {
	dir := newClassDir(t)
	// Each entry serialized is well over 50 bytes (envelope + payload).
	w, err := NewWriter(WriterOpts{
		ClassDir:        dir,
		Fsync:           FsyncPerBatch,
		MaxSegmentBytes: 200,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := w.Append(mustEntry(t, `{"data":"xxxxxxxxxxxxxx"}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	nums, err := listSegments(dir)
	if err != nil {
		t.Fatalf("listSegments: %v", err)
	}
	if len(nums) < 2 {
		t.Errorf("expected rotation by bytes, got %d segments (nums=%v)",
			len(nums), nums)
	}
}

func TestWriter_RecoverContinuesOffsets(t *testing.T) {
	dir := newClassDir(t)
	w1, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerEntry})
	if err != nil {
		t.Fatalf("NewWriter 1: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := w1.Append(mustEntry(t, `{}`)); err != nil {
			t.Fatalf("Append 1.%d: %v", i, err)
		}
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	w2, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerEntry})
	if err != nil {
		t.Fatalf("NewWriter 2: %v", err)
	}
	defer w2.Close()

	off, err := w2.Append(mustEntry(t, `{}`))
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if off != 4 {
		t.Errorf("offset after recovery = %d, want 4", off)
	}
	if got := w2.NextOffset(); got != 5 {
		t.Errorf("NextOffset after recovery + append = %d, want 5", got)
	}
}

func TestWriter_RecoverFromTornFinalLine(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerEntry})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := w.Append(mustEntry(t, `{}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Append a torn line manually (no newline, no valid JSON closure).
	path := segmentPath(dir, 1)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for torn append: %v", err)
	}
	if _, err := f.WriteString(`{"type":"capture.event","v":1,"of`); err != nil {
		t.Fatalf("write torn: %v", err)
	}
	f.Close()

	// Reopen — recovery should ignore the torn tail and continue at offset 3.
	w2, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerEntry})
	if err != nil {
		t.Fatalf("NewWriter recover: %v", err)
	}
	defer w2.Close()
	off, err := w2.Append(mustEntry(t, `{}`))
	if err != nil {
		t.Fatalf("Append after torn: %v", err)
	}
	if off != 3 {
		t.Errorf("offset after torn recovery = %d, want 3", off)
	}
}

func TestReader_MultiSegment(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{
		ClassDir:          dir,
		Fsync:             FsyncPerBatch,
		MaxSegmentEntries: 2,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	const total = 7
	for i := 0; i < total; i++ {
		if _, err := w.Append(mustEntry(t, `{}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	var seen []Offset
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		seen = append(seen, e.Offset)
	}
	if len(seen) != total {
		t.Fatalf("read %d entries, want %d", len(seen), total)
	}
	for i, off := range seen {
		want := Offset(i + 1)
		if off != want {
			t.Errorf("seen[%d] = %d, want %d", i, off, want)
		}
	}
}

func TestReader_TornFinalLineYieldsEOF(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerEntry})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Append(mustEntry(t, `{}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Manually corrupt the tail.
	path := segmentPath(dir, 1)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"type":"capture.event","v":1`); err != nil {
		t.Fatalf("write torn: %v", err)
	}
	f.Close()

	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	// First entry should read cleanly.
	if _, err := r.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	// Torn tail should surface as EOF (we treat torn as truncation).
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("torn-tail Next: got %v, want io.EOF", err)
	}
}

func TestCursor_GetSetRoundTrip(t *testing.T) {
	dir := newClassDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := OpenCursor(dir)

	got, err := c.Get()
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if got != 0 {
		t.Errorf("fresh Get = %d, want 0", got)
	}

	for _, v := range []Offset{1, 42, 100000} {
		if err := c.Set(v); err != nil {
			t.Fatalf("Set %d: %v", v, err)
		}
		got, err := c.Get()
		if err != nil {
			t.Fatalf("Get after Set %d: %v", v, err)
		}
		if got != v {
			t.Errorf("round-trip: Set %d, Get %d", v, got)
		}
	}
}

func TestCursor_SurvivesReopen(t *testing.T) {
	dir := newClassDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c1 := OpenCursor(dir)
	if err := c1.Set(7); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c2 := OpenCursor(dir)
	got, err := c2.Get()
	if err != nil {
		t.Fatalf("Get from second handle: %v", err)
	}
	if got != 7 {
		t.Errorf("Get after reopen = %d, want 7", got)
	}
}

func TestWriter_FsyncModes(t *testing.T) {
	for _, mode := range []FsyncMode{FsyncPerEntry, FsyncPerBatch} {
		mode := mode
		name := "PerEntry"
		if mode == FsyncPerBatch {
			name = "PerBatch"
		}
		t.Run(name, func(t *testing.T) {
			dir := newClassDir(t)
			w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: mode})
			if err != nil {
				t.Fatalf("NewWriter: %v", err)
			}
			for i := 0; i < 3; i++ {
				if _, err := w.Append(mustEntry(t, `{}`)); err != nil {
					t.Fatalf("Append %d: %v", i, err)
				}
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

func TestWriter_RejectsBadEntries(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Append(nil); err == nil {
		t.Error("Append(nil) succeeded, want error")
	}
	if _, err := w.Append(&Entry{Payload: json.RawMessage(`{}`)}); err == nil {
		t.Error("Append with empty Type succeeded, want error")
	}
}

func TestReader_EmptyDir(t *testing.T) {
	dir := newClassDir(t)
	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("Next on empty: got %v, want io.EOF", err)
	}
}

func TestEntry_TSPreservedWhenProvided(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := &Entry{Type: "capture.event", TS: fixed, Payload: json.RawMessage(`{}`)}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !e.TS.Equal(fixed) {
		t.Errorf("TS mutated: %v, want %v", e.TS, fixed)
	}
}

func TestSegmentPath_ZeroPadded(t *testing.T) {
	got := segmentPath("/tmp/x", 1)
	want := "/tmp/x/0001.jsonl"
	if got != want {
		t.Errorf("segmentPath(1) = %q, want %q", got, want)
	}
	got = segmentPath("/tmp/x", 10000)
	if !strings.HasSuffix(got, "/10000.jsonl") {
		t.Errorf("segmentPath(10000) = %q, want suffix /10000.jsonl", got)
	}
}
