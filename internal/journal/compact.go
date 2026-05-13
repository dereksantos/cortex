package journal

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CompactSegment gzips an uncompressed segment file and removes the
// original, leaving a `.jsonl.gz` in its place. The active (highest-
// numbered) segment is skipped — only fully closed segments are eligible.
// Idempotent: if the segment is already gzipped, no-op.
func CompactSegment(classDir string, n int) error {
	src := segmentPath(classDir, n)
	dst := segmentPathGZ(classDir, n)

	if _, err := os.Stat(dst); err == nil {
		// Already gzipped — clean up any stray uncompressed sibling.
		if _, err := os.Stat(src); err == nil {
			return os.Remove(src)
		}
		return nil
	}

	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("journal: source %s: %w", src, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", src, err)
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal: create %s: %w", tmp, err)
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("journal: gzip %s: %w", src, err)
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("journal: gzip close: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("journal: fsync compacted %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("journal: close compacted %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("journal: rename %s -> %s: %w", tmp, dst, err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("journal: remove original %s: %w", src, err)
	}
	return nil
}

// CompactClosedSegments gzips every uncompressed segment in classDir
// except the highest-numbered one (which is presumed to be still active).
// Returns the count of segments newly compacted.
func CompactClosedSegments(classDir string) (int, error) {
	nums, err := listSegments(classDir)
	if err != nil {
		return 0, err
	}
	if len(nums) < 2 {
		return 0, nil
	}
	closed := nums[:len(nums)-1] // exclude active tail

	compacted := 0
	for _, n := range closed {
		src := segmentPath(classDir, n)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // already gzipped
		}
		if err := CompactSegment(classDir, n); err != nil {
			return compacted, fmt.Errorf("compact segment %d: %w", n, err)
		}
		compacted++
	}
	return compacted, nil
}

// CompactClassDirAll is a convenience that walks every writer-class
// directory under journalRoot and compacts closed segments in each.
// Returns the total count of segments newly compacted.
func CompactClassDirAll(journalRoot string) (int, error) {
	entries, err := os.ReadDir(journalRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	total := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := CompactClosedSegments(filepath.Join(journalRoot, e.Name()))
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
