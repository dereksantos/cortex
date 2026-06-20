package study

import "fmt"

// P3 — findings-directed sampling (docs/working-memory-study.md).
//
// Without it, each deepening pass draws NEW regions blindly (the covered set +
// the sampler's anti-coverage bias). P3 instead steers the next pass toward the
// open threads the investigation already surfaced: the Leads accumulated across
// ALL prior findings, not just the previous pass's. The most recent unexplored
// lead becomes the next pass's Focus, so the sample budget is spent where the
// model said "look here" rather than on an arbitrary uncovered region.
//
// Opt-in via StudyRequest.DirectedSampling (and requires working memory), so the
// eval can compare directed vs blind draws before it becomes a default.

// directedFocusWindow is the half-height (in lines) of the focus region placed
// around a lead's near-line — wide enough to land the lead's coherence unit in
// the focused sample, narrow enough to stay directed.
const directedFocusWindow = 25

// nextDirectedFocus returns a Focus aimed at the most recent accumulated lead
// that hasn't been used yet, plus its key (for the caller to mark used). Most
// recent first, because recency tracks the deepening front. Returns (nil, "")
// when no unused, well-formed lead remains — the caller then falls back to the
// curator's blind decision.
func nextDirectedFocus(findings []Finding, used map[string]bool) (*Focus, string) {
	for i := len(findings) - 1; i >= 0; i-- {
		for _, l := range findings[i].Leads {
			if l.RelPath == "" || l.NearLine <= 0 {
				continue
			}
			key := leadKey(l)
			if used[key] {
				continue
			}
			lo := l.NearLine - directedFocusWindow
			if lo < 1 {
				lo = 1
			}
			return &Focus{
				Path:  l.RelPath,
				Lines: [2]int{lo, l.NearLine + directedFocusWindow},
				Query: l.Why,
			}, key
		}
	}
	return nil, ""
}

// leadKey is a lead's identity for the used-set (path + near-line).
func leadKey(l Lead) string {
	return fmt.Sprintf("%s:%d", l.RelPath, l.NearLine)
}
