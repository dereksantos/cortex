package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Reader iterates entries within a writer-class in offset (segment then
// line) order.
type Reader struct {
	classDir string
	segments []int
	segIdx   int
	f        *os.File
	br       *bufio.Reader
	closed   bool
}

// NewReader opens a reader over a writer-class's segments.
func NewReader(classDir string) (*Reader, error) {
	nums, err := listSegments(classDir)
	if err != nil {
		return nil, fmt.Errorf("journal: list segments in %s: %w", classDir, err)
	}
	return &Reader{
		classDir: classDir,
		segments: nums,
		segIdx:   -1,
	}, nil
}

// Next returns the next entry. Returns io.EOF when exhausted. A torn
// (incomplete JSON) final line in the last segment is treated as EOF.
// Malformed entries mid-stream surface as an error — callers can choose
// to skip them, but the default is strict.
func (r *Reader) Next() (*Entry, error) {
	if r.closed {
		return nil, io.EOF
	}
	for {
		if r.br == nil {
			if err := r.advanceSegment(); err != nil {
				return nil, err
			}
		}
		line, err := r.br.ReadBytes('\n')
		if err == io.EOF {
			// Partial trailing line — discard and move to next segment.
			if cerr := r.closeCurrent(); cerr != nil {
				return nil, cerr
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("journal: read %s: %w", r.currentPath(), err)
		}
		if len(line) <= 1 {
			continue // blank line
		}
		line = line[:len(line)-1] // strip newline
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("journal: parse line in %s: %w",
				r.currentPath(), err)
		}
		return &e, nil
	}
}

// Close releases resources.
func (r *Reader) Close() error {
	r.closed = true
	return r.closeCurrent()
}

func (r *Reader) advanceSegment() error {
	r.segIdx++
	if r.segIdx >= len(r.segments) {
		return io.EOF
	}
	path := segmentPath(r.classDir, r.segments[r.segIdx])
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", path, err)
	}
	r.f = f
	r.br = bufio.NewReader(f)
	return nil
}

func (r *Reader) closeCurrent() error {
	if r.f != nil {
		err := r.f.Close()
		r.f = nil
		r.br = nil
		return err
	}
	r.br = nil
	return nil
}

func (r *Reader) currentPath() string {
	if r.segIdx < 0 || r.segIdx >= len(r.segments) {
		return ""
	}
	return segmentPath(r.classDir, r.segments[r.segIdx])
}
