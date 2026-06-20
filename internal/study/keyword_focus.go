package study

import (
	"strings"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
)

// Keyword focus is the mechanical-search "aimed wide net" (docs/working-memory-
// study.md): the study tool's natural-language goal can't steer the sampler
// (Focus.Query is prompt-only), so instead we extract the goal's significant
// terms, scan the file's chunks for them, and bias sampling toward the regions
// that actually mention them. It's "search without an agent" — semantic-ish
// targeting that stays bounded and predictable (no model tool-use loop), which
// is what makes a tight curation budget spend its tokens on what was asked.
//
// Fallback is graceful: terms that don't appear (e.g. abstract goals like
// "assess the architecture") mark nothing, so the sampler reverts to breadth —
// exactly right, since there's nothing specific to aim at.

// keywordScanMaxChunks bounds the content scan: above this the file's grid has
// too many chunks to read affordably for targeting, so keyword focus is skipped
// and sampling stays blind. Keeps the feature cheap on huge byte grids while
// covering the common case (files modestly over the curation budget).
const keywordScanMaxChunks = 4000

// markKeywordChunks marks every chunk whose bytes contain a (case-insensitive)
// match for any term as in-focus. Reads chunk content via the same region
// reader the sampler uses; read errors skip that chunk (never fatal). A no-op
// when there are too many chunks or no usable terms.
func markKeywordChunks(fs *FocusSampler, out *BoundaryOutput, terms []string) {
	if out == nil || len(out.Chunks) == 0 || len(out.Chunks) > keywordScanMaxChunks {
		return
	}
	low := make([]string, 0, len(terms))
	for _, t := range terms {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			low = append(low, t)
		}
	}
	if len(low) == 0 {
		return
	}
	for i := range out.Chunks {
		c := &out.Chunks[i]
		body, err := fractal.ReadRegion(c.Path, c.ByteOffset, c.ByteLength)
		if err != nil {
			continue
		}
		lb := strings.ToLower(body)
		for _, t := range low {
			if strings.Contains(lb, t) {
				fs.inFocus[c.ID] = true
				break
			}
		}
	}
}

// keywordFocusFromGoal builds a keyword-only Focus from a goal's significant
// terms, or nil when the goal yields none. The seam StudyFile uses to aim pass-1
// sampling at the query.
func keywordFocusFromGoal(goal string) *Focus {
	terms := significantWords(goal)
	if len(terms) == 0 {
		return nil
	}
	return &Focus{Keywords: terms}
}
