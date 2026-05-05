// Package fractal provides primitives for region-based, novelty-aware,
// signal-following exploration in Dream mode.
//
// Region windowing replaces whole-file reads with bounded byte windows,
// so Dream can sample arbitrary points inside large files and zoom in
// on neighbors of any region that yielded a high-signal insight.
package fractal

import (
	"errors"
	"io"
	"math/rand"
	"os"
	"sort"
	"unicode/utf8"
)

const (
	// MinWindowChars is the lower bound for a region window.
	MinWindowChars = 2 * 1024
	// MaxWindowChars is the upper bound for a region window.
	MaxWindowChars = 6 * 1024
	// SmallFileThreshold is the size below which a file returns one
	// whole-file region.
	SmallFileThreshold = 8 * 1024
	// HeadInclusionRate is the fraction of regions that always include
	// offset 0 (so the head of a file still gets sampled).
	HeadInclusionRate = 1.0 / 3.0
)

// Region identifies a byte range inside a file.
type Region struct {
	Path   string
	Offset int64
	Length int
}

// ReadRegion reads a window of `length` chars starting at byte `offset`.
// If the window straddles UTF-8 boundaries the returned string is clamped
// on rune boundaries. Returns the actual content and any read error.
func ReadRegion(path string, offset int64, length int) (string, error) {
	if length <= 0 {
		return "", errors.New("fractal: length must be positive")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return "", err
	}
	buf = buf[:n]

	// Clamp on the leading rune boundary.
	if offset > 0 {
		for i := 0; i < len(buf) && i < 4; i++ {
			if utf8.RuneStart(buf[i]) {
				buf = buf[i:]
				break
			}
		}
	}
	// Drop a trailing partial rune.
	for len(buf) > 0 {
		r, size := utf8.DecodeLastRune(buf)
		if r == utf8.RuneError && size <= 1 {
			buf = buf[:len(buf)-1]
			continue
		}
		break
	}
	return string(buf), nil
}

// PickRegions returns up to `count` non-overlapping windows for a file of
// the given size. 1/3 of returned windows always include offset 0 so the
// head of the file keeps getting sampled. The rest are uniform random.
//
// Files <= SmallFileThreshold return a single whole-file region.
func PickRegions(size int64, count int, rng *rand.Rand) []Region {
	if count <= 0 {
		return nil
	}
	if size <= 0 {
		return nil
	}
	if size <= SmallFileThreshold {
		return []Region{{Offset: 0, Length: int(size)}}
	}

	winLen := MinWindowChars
	if rng != nil {
		winLen += rng.Intn(MaxWindowChars - MinWindowChars + 1)
	}
	if int64(winLen) > size {
		winLen = int(size)
	}

	// Decide how many windows must include offset 0.
	headSlots := 0
	for i := 0; i < count; i++ {
		if rng == nil {
			break
		}
		if rng.Float64() < HeadInclusionRate {
			headSlots++
		}
	}
	if headSlots > 1 {
		headSlots = 1 // at most one window from offset 0
	}

	maxOffset := size - int64(winLen)
	if maxOffset < 0 {
		maxOffset = 0
	}

	regions := make([]Region, 0, count)
	if headSlots > 0 {
		regions = append(regions, Region{Offset: 0, Length: winLen})
	}
	tries := 0
	for len(regions) < count && tries < count*10 {
		tries++
		var off int64
		if rng != nil && maxOffset > 0 {
			off = rng.Int63n(maxOffset + 1)
		}
		candidate := Region{Offset: off, Length: winLen}
		if !overlapsAny(candidate, regions) {
			regions = append(regions, candidate)
		}
	}

	sort.Slice(regions, func(i, j int) bool {
		return regions[i].Offset < regions[j].Offset
	})
	return regions
}

// NeighborRegions returns up to four windows adjacent to r, clamped to
// [0, fileSize). It is the "zoom in" primitive: when a region yielded an
// insight, sample its neighbors next cycle.
func NeighborRegions(r Region, fileSize int64) []Region {
	if r.Length <= 0 || fileSize <= 0 {
		return nil
	}
	candidates := []int64{
		r.Offset - int64(r.Length),
		r.Offset + int64(r.Length),
		r.Offset - 2*int64(r.Length),
		r.Offset + 2*int64(r.Length),
	}
	maxOffset := fileSize - int64(r.Length)
	if maxOffset < 0 {
		maxOffset = 0
	}

	out := make([]Region, 0, 4)
	seen := make(map[int64]bool)
	for _, c := range candidates {
		if c < 0 {
			c = 0
		}
		if c > maxOffset {
			c = maxOffset
		}
		if c == r.Offset {
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, Region{Path: r.Path, Offset: c, Length: r.Length})
	}
	return out
}

func overlapsAny(c Region, existing []Region) bool {
	for _, e := range existing {
		ce := c.Offset + int64(c.Length)
		ee := e.Offset + int64(e.Length)
		if c.Offset < ee && e.Offset < ce {
			return true
		}
	}
	return false
}
