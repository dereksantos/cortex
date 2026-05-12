package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const segmentExt = ".jsonl"

// segmentPath returns the path for segment n within classDir. Numbers are
// zero-padded to 4 digits and widen cleanly past 9999.
func segmentPath(classDir string, n int) string {
	return filepath.Join(classDir, fmt.Sprintf("%04d%s", n, segmentExt))
}

// listSegments returns segment numbers present in classDir, sorted ascending.
// Files that don't match the segment naming convention are ignored. A
// missing directory yields a nil slice (not an error).
func listSegments(classDir string) ([]int, error) {
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var nums []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, segmentExt) {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(name, "%d"+segmentExt, &n); err == nil && n > 0 {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums, nil
}
