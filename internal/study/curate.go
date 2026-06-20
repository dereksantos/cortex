package study

import (
	"sort"
	"strings"
)

// P2 — curation of the findings prefix (docs/working-memory-study.md).
//
// P1 bounds the appended findings block with a crude recency drop
// (trimFindingsToBudget). P2 replaces that, under budget pressure, with a
// value-ranked keep/compress/evict triage:
//
//	keep     — high-value findings stay verbatim.
//	compress — lower-value findings are shortened (CompressFunc) but keep their
//	           citation anchors VERBATIM, so a later pass can still cite through
//	           them (the relay contract). This is the curation contract.
//	evict    — the rest leave the working set. They are not deleted: OnEvict
//	           demotes them (the harness journals them, recoverable via search).
//
// Trigger is dynamic/pressure-based: curateFindings is a no-op when the block
// already fits, so most passes pay nothing and the prefix stays append-stable;
// the rewrite (and its one cache miss) happens only when the budget is crossed.
// The newest finding is always retained (compressed if need be) so continuity
// with the immediately-prior pass never breaks.

// CompressFunc shortens a finding to reduce its prefix cost while preserving its
// citation anchors. Injected so the harness can wire the real attend.distill
// model op; tests and the default use a mechanical truncation.
type CompressFunc func(Finding) Finding

// compressedDigestCap is the mechanical compressor's prose ceiling in
// characters. Provisional — the eval (retention quality) owns it.
const compressedDigestCap = 240

// MechanicalCompress is the default CompressFunc: it truncates the digest prose
// and drops leads (the least load-bearing part), but keeps every citation
// anchor intact. No model call.
func MechanicalCompress(f Finding) Finding {
	d := strings.TrimSpace(f.Digest)
	if len(d) > compressedDigestCap {
		// Truncate on a rune boundary so we never split a UTF-8 sequence.
		cut := compressedDigestCap
		for cut > 0 && !utf8Start(d[cut]) {
			cut--
		}
		d = strings.TrimSpace(d[:cut]) + "…"
	}
	return Finding{Pass: f.Pass, Digest: d, Citations: f.Citations}
}

// utf8Start reports whether b is the first byte of a UTF-8 sequence (not a
// continuation byte 0b10xxxxxx).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// curateFindings fits findings within budgetChars under value-ranked
// keep/compress/evict, returning the retained set (chronological order) and the
// evicted set. A no-op (returns findings, nil) when they already fit — the
// dynamic pressure trigger. compress defaults to MechanicalCompress when nil.
func curateFindings(findings []Finding, budgetChars int, goal string, compress CompressFunc) (kept, evicted []Finding) {
	if len(findings) == 0 || budgetChars <= 0 {
		return nil, findings
	}
	if findingsCharsTotal(findings) <= budgetChars {
		return findings, nil
	}
	if compress == nil {
		compress = MechanicalCompress
	}

	maxPass := 0
	for _, f := range findings {
		if f.Pass > maxPass {
			maxPass = f.Pass
		}
	}

	// Process order: the newest finding first (pinned, so it is admitted while
	// the budget is still full — the continuity guarantee), then the rest by
	// value descending.
	newest := len(findings) - 1
	rest := make([]int, 0, len(findings)-1)
	for i := range findings {
		if i != newest {
			rest = append(rest, i)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool {
		return findingValue(findings[rest[a]], goal, maxPass) > findingValue(findings[rest[b]], goal, maxPass)
	})
	order := append([]int{newest}, rest...)

	keptByIdx := make(map[int]Finding, len(findings))
	used := 0
	for _, i := range order {
		f := findings[i]
		if used+findingChars(f) <= budgetChars {
			keptByIdx[i] = f
			used += findingChars(f)
			continue
		}
		cf := compress(f)
		if used+findingChars(cf) <= budgetChars {
			keptByIdx[i] = cf
			used += findingChars(cf)
			continue
		}
		// The newest is pinned first with an empty budget, so it only reaches
		// here if even compressed it exceeds the whole budget — keep it anyway
		// (continuity outranks the soft bound in that degenerate case).
		if i == newest {
			keptByIdx[i] = cf
			used += findingChars(cf)
			continue
		}
		evicted = append(evicted, f)
	}

	idxs := make([]int, 0, len(keptByIdx))
	for i := range keptByIdx {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs) // chronological (Pass order == append order)
	for _, i := range idxs {
		kept = append(kept, keptByIdx[i])
	}
	return kept, evicted
}

// findingValue ranks a finding for retention. Recency dominates (the deepening
// investigation cares most about what it just learned), with citation count and
// goal-term overlap breaking ties. Mechanical and deterministic — the Reflex
// tier of the triage; an LLM value.score can be injected later if the eval shows
// the heuristic mis-ranks.
func findingValue(f Finding, goal string, maxPass int) float64 {
	recency := 0.0
	if maxPass > 0 {
		recency = float64(f.Pass) / float64(maxPass)
	}
	cites := float64(len(f.Citations))
	if cites > 3 {
		cites = 3
	}
	return 2.0*recency + 0.5*(cites/3.0) + 1.0*goalOverlap(f.Digest, goal)
}

// goalOverlap is the fraction of the goal's significant words that appear in the
// digest (case-insensitive). 0 when the goal is empty.
func goalOverlap(digest, goal string) float64 {
	terms := significantWords(goal)
	if len(terms) == 0 {
		return 0
	}
	d := strings.ToLower(digest)
	hits := 0
	for _, w := range terms {
		if strings.Contains(d, w) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}

// significantWords lowercases and de-dupes the goal's words ≥4 chars (skipping
// short stopwords), the tokens worth matching against a digest.
func significantWords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(w) < 4 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
