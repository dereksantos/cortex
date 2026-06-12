package study

import (
	"strconv"
	"strings"
)

// Density is the sampling-density knob. It is either a named level
// ("sparse" | "normal" | "dense") or a raw int k (chunks to draw per
// study pass). Anything else — including nil, an empty string, a
// non-positive int, or an unrecognized name — resolves to "normal".
//
// Named levels map to k via ResolveDensity. The names give harness
// callers and the CLI a stable vocabulary; the raw int is the escape
// hatch the budget-derived path uses when the agent loop already has a
// concrete chunk count in hand.
type Density any

const (
	densitySparseK = 4
	densityNormalK = 8
	densityDenseK  = 16
)

// ResolveDensity maps a Density to a concrete chunk count k. The
// mapping is total: every input yields a positive k, defaulting to the
// normal level so a malformed request still samples something sensible.
func ResolveDensity(d Density) int {
	switch v := d.(type) {
	case nil:
		return densityNormalK
	case int:
		if v > 0 {
			return v
		}
		return densityNormalK
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "sparse":
			return densitySparseK
		case "dense":
			return densityDenseK
		case "normal", "":
			return densityNormalK
		}
		// Numeric strings count as raw k — the CLI's --density flag
		// arrives as a string, and "6" silently meaning 8 is a trap.
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
		return densityNormalK
	default:
		return densityNormalK
	}
}
