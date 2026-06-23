package study

import (
	"context"
	"fmt"
	"strings"
)

// StudyLoop realizes the deepening loop: study → curate → study-deeper.
// Each pass studies the file, the curator decides DONE / DENSIFY / TARGET
// from the digest+leads+coverage, and the decision is applied to the next
// pass — reusing the session's covered set so deepening samples NEW
// regions instead of repeating. It stops on DONE, exhaustion, read-mode,
// or maxPasses.
//
// This is the in-process driver; the harness equivalent is the agent loop
// re-calling study_file with deepen.densify/target between turns. Both
// share the same StudyFile + Curator, so behavior matches.

// StudyPass records one iteration: the study result and the curator's
// decision on it (zero-valued Decision on the terminal pass).
type StudyPass struct {
	Response StudyResponse
	Decision Decision
}

// StudyLoopResult is the accumulated outcome across passes.
type StudyLoopResult struct {
	Passes      []StudyPass
	Digests     []string   // per-pass digests, in order
	Citations   []Citation // union of validated citations across passes
	CoveragePct float64    // cumulative, over the union of sampled regions
	Stopped     string     // "done" | "exhausted" | "read" | "budget"
	// FindingRelays is the cross-pass total of citations admitted by relaying a
	// prior finding (the working-memory continuity signal). 0 when working
	// memory is off or no later pass cited through to an earlier one.
	FindingRelays int
	// SynthesisTerms is the cross-pass total carry-forward count: digest terms
	// drawn from prior findings but not from the pass's own sample. The
	// disjoint-sampling continuity signal; 0 with working memory off.
	SynthesisTerms int
	// PrefixWarmPasses / PrefixBreaks measure cross-pass cache warmth (P4): a
	// pass is "warm" when its [system][goal][findings] prefix has the previous
	// pass's as a byte-prefix (append-only → a cache hit), and a "break" when
	// the prefix changed shape (a curation rewrite or head drop → a cache miss).
	// Pass 0 counts as neither.
	PrefixWarmPasses int
	PrefixBreaks     int
	// UncoveredFiles lists relpaths that had chunks in the boundary but were
	// never sampled across ANY pass — the gaps a follow-up study should target.
	// Computed from the last pass's per-pass uncovered set minus every file
	// sampled in any earlier pass. Empty in read mode or when every file was
	// reached.
	UncoveredFiles []string
}

// StudyLoop runs the loop. A nil curator defaults to HeuristicCurator;
// maxPasses <= 0 defaults to 4.
func StudyLoop(ctx context.Context, req StudyRequest, curator Curator, maxPasses int) (StudyLoopResult, error) {
	if curator == nil {
		curator = HeuristicCurator{}
	}
	if maxPasses <= 0 {
		maxPasses = 4
	}

	covered := req.Covered
	if covered == nil {
		covered = map[string]bool{}
	}

	var res StudyLoopResult
	cumEff := 0
	total := 0
	seen := map[string]bool{}
	sampledRelPaths := map[string]bool{} // any file sampled in ANY pass
	usedLeads := map[string]bool{}       // P3: leads already turned into a Focus
	prevPrefix := ""                     // P4: previous pass's cacheable prefix
	havePrev := false
	// Working memory: each pass's distilled result accumulates here and rides
	// the next pass's prompt front (PriorFindings), so deepening builds on what
	// earlier passes found instead of re-deriving it. Append-only in P1; the
	// budget carve-out + recency trim live in sampleAndInfer.
	var findings []Finding

	for pass := 0; pass < maxPasses; pass++ {
		req.Covered = covered
		if !req.NoWorkingMemory {
			req.PriorFindings = findings
		}
		resp, err := StudyFile(ctx, req)
		if err != nil {
			return res, err
		}

		res.Passes = append(res.Passes, StudyPass{Response: resp})
		res.FindingRelays += resp.FindingRelays
		// P4: is this pass's cacheable prefix a clean extension of the previous?
		if havePrev {
			if strings.HasPrefix(resp.CachePrefix, prevPrefix) {
				res.PrefixWarmPasses++
			} else {
				res.PrefixBreaks++
			}
		}
		prevPrefix, havePrev = resp.CachePrefix, true
		if resp.Digest != "" {
			// Synthesis continuity: terms this digest carries from PRIOR digests
			// (accumulated regardless of injection) but not from its own sample.
			// Measured for both on/off so on−off isolates working memory's effect.
			res.SynthesisTerms += synthesisCarryForward(resp.Digest, res.Digests, resp.Sampled)
			res.Digests = append(res.Digests, resp.Digest)
			findings = append(findings, Finding{
				Pass:      pass,
				Digest:    resp.Digest,
				Citations: resp.Citations,
				Leads:     resp.Leads,
			})
		}
		res.Citations = append(res.Citations, resp.Citations...)

		// Cumulative coverage over the union of sampled regions.
		if resp.Coverage.EffLinesTotal > 0 {
			total = resp.Coverage.EffLinesTotal
		}
		for _, s := range resp.Sampled {
			key := fmt.Sprintf("%s:%d", s.RelPath, s.ByteOffset)
			if !seen[key] {
				seen[key] = true
				cumEff += s.EffLines
			}
			sampledRelPaths[s.RelPath] = true
		}
		if total > 0 {
			res.CoveragePct = float64(cumEff) / float64(total)
			// Refined numerator vs estimated denominator can exceed 1
			// on short-line files — clamp (see StudyFile's cov calc).
			if res.CoveragePct > 1 {
				res.CoveragePct = 1
			}
		}

		// Whole-file read → nothing to deepen.
		if resp.Mode == "read" {
			res.Stopped = "read"
			return res, nil
		}

		// Uncovered files: the last pass's per-pass gaps minus any file an
		// earlier pass reached. This is the set a follow-up study should
		// target. Recomputed each pass so the terminal value reflects the
		// final state.
		res.UncoveredFiles = filterUncovered(resp.UncoveredFiles, sampledRelPaths)

		// The final pass's decision could never be applied — don't spend
		// a curator call (an LLM round for ModelCurator) computing it.
		// The terminal pass keeps the zero-valued Decision the StudyPass
		// doc promises; Stopped reads "exhausted" or "budget" as usual.
		if pass == maxPasses-1 {
			if resp.Exhausted {
				res.Stopped = "exhausted"
				return res, nil
			}
			break
		}

		dec := curator.Decide(resp, req.Goal)
		res.Passes[len(res.Passes)-1].Decision = dec

		switch dec.Kind {
		case DecisionDone:
			res.Stopped = "done"
			return res, nil
		case DecisionDensify:
			if dec.Density != nil {
				req.Density = dec.Density
			}
			req.Focus = nil // broaden: densify samples more of the whole file
		case DecisionTarget:
			req.Focus = dec.Focus
			if dec.Density != nil {
				req.Density = dec.Density
			}
		}

		// P3: override the curator's (blind) focus with the most recent
		// unexplored lead the investigation surfaced, so the next pass samples
		// where the model pointed rather than an arbitrary uncovered region.
		// Falls back to the curator's decision when no unused lead remains.
		if req.DirectedSampling && !req.NoWorkingMemory {
			if f, key := nextDirectedFocus(findings, usedLeads); f != nil {
				req.Focus = f
				usedLeads[key] = true
			}
		}

		// A study that hit the coverage knee can't deepen further.
		if resp.Exhausted {
			res.Stopped = "exhausted"
			return res, nil
		}
	}

	res.Stopped = "budget"
	return res, nil
}

// filterUncovered drops relpaths that were sampled in any pass from the
// last pass's per-pass uncovered list, leaving only files never reached
// across the whole loop.
func filterUncovered(lastPassUncovered []string, sampledRelPaths map[string]bool) []string {
	var out []string
	for _, f := range lastPassUncovered {
		if !sampledRelPaths[f] {
			out = append(out, f)
		}
	}
	return out
}
