package study

import (
	"bufio"
	"bytes"
	"fmt"
	"os"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
)

// RefineChunk fills a byte-grid chunk's REAL line bounds + EffLines on
// first visit. The byte-grid producer lays chunks from size alone with
// provisional (estimated) line bounds; this is where a region is
// actually read — once — and the bounds become exact.
//
// It snaps the chunk's byte edges to newline boundaries (dropping a
// partial leading line unless the offset already sits at a line start,
// and a partial trailing line), then resolves the absolute LineStart
// via lineBase and counts lines within the snapped body. Idempotent:
// a chunk already marked Refined is left untouched.
//
// lineBase maps a byte offset to its absolute 1-indexed line number;
// streamingLineBase builds one for a path.
func RefineChunk(ch *Chunk, lineBase func(off int64) (int, error)) error {
	if ch == nil || ch.Refined {
		return nil
	}
	off := ch.ByteOffset
	length := ch.ByteLength
	if length <= 0 {
		ch.Refined = true
		return nil
	}

	raw, err := fractal.ReadRegion(ch.Path, off, length)
	if err != nil {
		return fmt.Errorf("study: refine read %s@%d: %w", ch.RelPath, off, err)
	}
	body := []byte(raw)

	// Leading edge: drop a partial first line UNLESS off already sits at
	// a line start (byte 0, or the preceding byte is a newline).
	atStart := off == 0
	if off > 0 {
		if prev, perr := fractal.ReadRegion(ch.Path, off-1, 1); perr == nil && prev == "\n" {
			atStart = true
		}
	}
	startByte := off
	if !atStart {
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			startByte = off + int64(i) + 1
			body = body[i+1:]
		}
	}

	// Trailing edge: snap back to the last newline (drop a partial
	// trailing line) when the body has one.
	if i := bytes.LastIndexByte(body, '\n'); i >= 0 {
		body = body[:i+1]
	}

	ch.ByteOffset = startByte
	ch.ByteLength = len(body)

	base, err := lineBase(startByte)
	if err != nil {
		return fmt.Errorf("study: refine linebase %s@%d: %w", ch.RelPath, startByte, err)
	}
	ch.LineStart = base
	if nl := bytes.Count(body, []byte{'\n'}); nl > 0 {
		ch.LineEnd = base + nl - 1
	} else {
		ch.LineEnd = base
	}
	if ch.LineEnd < ch.LineStart {
		ch.LineEnd = ch.LineStart
	}
	ch.EffLines = effectiveLinesOf(body, ch.Lang)
	ch.Refined = true
	return nil
}

// streamingLineBase returns a function mapping a byte offset to its
// absolute 1-indexed line number, counting newlines from the file head.
// It is O(offset) sequential IO per call (constant memory) — bounded in
// practice by the per-pass chunk count k. A section/AST index can make
// this O(1) later; for the file sizes study targets it is negligible.
func streamingLineBase(path string) func(off int64) (int, error) {
	return func(off int64) (int, error) {
		if off <= 0 {
			return 1, nil
		}
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		r := bufio.NewReader(f)
		lines := 1
		var pos int64
		buf := make([]byte, 32*1024)
		for pos < off {
			n := int64(len(buf))
			if off-pos < n {
				n = off - pos
			}
			read, rerr := r.Read(buf[:n])
			for i := 0; i < read; i++ {
				if buf[i] == '\n' {
					lines++
				}
			}
			pos += int64(read)
			if rerr != nil {
				break
			}
		}
		return lines, nil
	}
}
