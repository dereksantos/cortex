package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// FsyncMode controls how aggressively a Writer fsyncs to disk.
type FsyncMode int

const (
	// FsyncPerEntry fsyncs after every appended entry. Use for the input
	// boundary (capture) where loss is permanent. See principle 4 in
	// docs/journal.md.
	FsyncPerEntry FsyncMode = iota
	// FsyncPerBatch fsyncs only on explicit Flush() or Close(). Use for
	// cognitive modes (dream/reflect/resolve/think/eval) where derivations
	// are regeneratable from a journal replay.
	FsyncPerBatch
)

const (
	defaultMaxSegmentBytes   int64 = 10 * 1024 * 1024 // 10 MiB
	defaultMaxSegmentEntries int   = 10000
)

// WriterOpts configures a Writer.
type WriterOpts struct {
	// ClassDir is the directory holding this writer-class's segments,
	// e.g. ".cortex/journal/capture".
	ClassDir string
	// Fsync controls durability vs throughput. See FsyncMode.
	Fsync FsyncMode
	// MaxSegmentBytes triggers rotation when exceeded. Zero uses default.
	MaxSegmentBytes int64
	// MaxSegmentEntries triggers rotation when exceeded. Zero uses default.
	MaxSegmentEntries int
}

// Writer appends entries to a writer-class's segments. Safe for concurrent
// callers within the same process via an internal mutex.
//
// Cross-process concurrent appends to the same class are NOT yet supported.
// Capture (per-hook, short-lived processes) is the case that needs this and
// will be handled with a per-segment flock in slice C1.
type Writer struct {
	opts WriterOpts

	mu         sync.Mutex
	f          *os.File
	segmentN   int
	nextOffset Offset
	written    int64
	entries    int
}

// NewWriter creates the class directory if needed, scans existing segments
// to recover the next offset, and opens the latest segment for append.
func NewWriter(opts WriterOpts) (*Writer, error) {
	if opts.MaxSegmentBytes <= 0 {
		opts.MaxSegmentBytes = defaultMaxSegmentBytes
	}
	if opts.MaxSegmentEntries <= 0 {
		opts.MaxSegmentEntries = defaultMaxSegmentEntries
	}
	if err := os.MkdirAll(opts.ClassDir, 0o755); err != nil {
		return nil, fmt.Errorf("journal: mkdir %s: %w", opts.ClassDir, err)
	}
	w := &Writer{opts: opts}
	if err := w.recover(); err != nil {
		return nil, err
	}
	return w, nil
}

// recover prepares the writer by scanning the highest existing segment for
// the last offset and entry count. If no segments exist, segment 1 is
// created.
func (w *Writer) recover() error {
	nums, err := listSegments(w.opts.ClassDir)
	if err != nil {
		return err
	}
	if len(nums) == 0 {
		return w.openSegment(1)
	}
	last := nums[len(nums)-1]
	path := segmentPath(w.opts.ClassDir, last)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("journal: stat %s: %w", path, err)
	}
	lastOffset, entries, err := scanSegmentTail(path)
	if err != nil {
		return fmt.Errorf("journal: scan tail %s: %w", path, err)
	}
	w.segmentN = last
	w.nextOffset = lastOffset + 1
	w.written = info.Size()
	w.entries = entries

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal: open %s for append: %w", path, err)
	}
	w.f = f
	return nil
}

// openSegment closes the current segment (if any) and opens segment n.
// The new segment starts empty; nextOffset is preserved across rotation.
func (w *Writer) openSegment(n int) error {
	if w.f != nil {
		_ = w.f.Sync()
		_ = w.f.Close()
		w.f = nil
	}
	path := segmentPath(w.opts.ClassDir, n)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", path, err)
	}
	w.f = f
	w.segmentN = n
	w.written = 0
	w.entries = 0
	if w.nextOffset == 0 {
		w.nextOffset = 1
	}
	return nil
}

// Append writes an entry. The entry's Offset is assigned by the writer;
// TS defaults to time.Now().UTC() if zero; V defaults to 1 if zero. The
// returned Offset is the offset that was written.
//
// fsync is applied per the writer's FsyncMode.
func (w *Writer) Append(e *Entry) (Offset, error) {
	if e == nil {
		return 0, fmt.Errorf("journal: nil entry")
	}
	if e.Type == "" {
		return 0, fmt.Errorf("journal: entry missing type")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	e.Offset = w.nextOffset
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	} else {
		e.TS = e.TS.UTC()
	}
	if e.V == 0 {
		e.V = 1
	}

	line, err := json.Marshal(e)
	if err != nil {
		return 0, fmt.Errorf("journal: marshal entry: %w", err)
	}
	line = append(line, '\n')

	// Rotate if this write would exceed limits AND the current segment
	// already holds at least one entry. (An entry larger than the byte
	// limit still goes into its own segment, never blocks the writer.)
	if w.entries > 0 && (w.written+int64(len(line)) > w.opts.MaxSegmentBytes ||
		w.entries+1 > w.opts.MaxSegmentEntries) {
		if err := w.openSegment(w.segmentN + 1); err != nil {
			return 0, err
		}
	}

	n, err := w.f.Write(line)
	if err != nil {
		return 0, fmt.Errorf("journal: write: %w", err)
	}
	w.written += int64(n)
	w.entries++

	if w.opts.Fsync == FsyncPerEntry {
		if err := w.f.Sync(); err != nil {
			return 0, fmt.Errorf("journal: fsync: %w", err)
		}
	}

	off := e.Offset
	w.nextOffset++
	return off, nil
}

// Flush fsyncs the current segment regardless of FsyncMode. Required before
// process exit for batch-mode writers; harmless for per-entry writers.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.f.Sync()
}

// Close flushes and closes the current segment.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Sync()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f = nil
	return err
}

// NextOffset returns the offset that the next Append will be assigned.
// Useful for tests and for indexer cursor reconciliation.
func (w *Writer) NextOffset() Offset {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.nextOffset
}

// scanSegmentTail returns the highest offset present in a segment and the
// total entry count. A torn (incomplete JSON) final line is silently
// dropped; this matches Postgres-WAL style truncate-on-recovery.
func scanSegmentTail(path string) (Offset, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	if len(data) == 0 {
		return 0, 0, nil
	}
	var lastOffset Offset
	count := 0
	for _, ln := range bytes.Split(data, []byte{'\n'}) {
		if len(ln) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(ln, &e); err != nil {
			continue
		}
		lastOffset = e.Offset
		count++
	}
	return lastOffset, count, nil
}
