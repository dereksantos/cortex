package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	segmentExt   = ".jsonl"
	segmentExtGZ = ".jsonl.gz"
)

// segmentPath returns the uncompressed path for segment n within classDir.
// Numbers are zero-padded to 4 digits and widen cleanly past 9999.
func segmentPath(classDir string, n int) string {
	return filepath.Join(classDir, fmt.Sprintf("%04d%s", n, segmentExt))
}

// segmentPathGZ returns the compressed path for segment n.
func segmentPathGZ(classDir string, n int) string {
	return filepath.Join(classDir, fmt.Sprintf("%04d%s", n, segmentExtGZ))
}

// resolveSegmentPath returns the on-disk path for segment n, preferring
// the uncompressed form when both exist (uncompressed implies actively
// written; compressed is the closed form).
func resolveSegmentPath(classDir string, n int) (string, error) {
	jsonl := segmentPath(classDir, n)
	if _, err := os.Stat(jsonl); err == nil {
		return jsonl, nil
	}
	gz := segmentPathGZ(classDir, n)
	if _, err := os.Stat(gz); err == nil {
		return gz, nil
	}
	return "", fmt.Errorf("journal: segment %d not found in %s", n, classDir)
}

// listSegments returns segment numbers present in classDir (either .jsonl
// or .jsonl.gz), sorted ascending. Files that don't match the segment
// naming convention are ignored. A missing directory yields a nil slice
// (not an error).
func listSegments(classDir string) ([]int, error) {
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := make(map[int]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var n int
		switch {
		case strings.HasSuffix(name, segmentExtGZ):
			if _, err := fmt.Sscanf(name, "%d"+segmentExtGZ, &n); err == nil && n > 0 {
				seen[n] = true
			}
		case strings.HasSuffix(name, segmentExt):
			if _, err := fmt.Sscanf(name, "%d"+segmentExt, &n); err == nil && n > 0 {
				seen[n] = true
			}
		}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums, nil
}
