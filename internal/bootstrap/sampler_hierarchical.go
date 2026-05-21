package bootstrap

import (
	"math"
	"math/rand"
	"sort"
)

// HierarchicalSampler is the day-one Sampler: recursive weighted draw
// over the project's module tree with an anti-coverage bias. Fractal
// by construction — the same weighted-subdivision rule applies at
// every depth.
//
// Tuning knobs:
//
//   - AntiCoverageWeight (default 3.0) scales the (1 + aw·u) factor
//     where u is the module's uncovered fraction. Higher values push
//     the sampler harder toward unexplored modules.
//   - SizeWeightExp (default 0.5) scales pow(uncovered_eff_lines, sw).
//     0.5 ≈ sqrt; smaller exponents flatten the size advantage.
//
// Determinism: only `rng` produces randomness. Same BoundaryOutput +
// same covered set + same seed → identical chunk-ID sequence.
type HierarchicalSampler struct {
	AntiCoverageWeight float64
	SizeWeightExp      float64
}

// Name identifies the sampler in bootstrap metadata / journal entries.
func (h *HierarchicalSampler) Name() string { return "hierarchical-v1" }

// Next returns up to k chunk IDs drawn from out, never repeating an
// ID already in `covered` and never repeating an ID within this call.
// May return fewer than k IDs when uncovered chunks are exhausted.
//
// Algorithm per pick:
//  1. Sum module weights: w_m = pow(uncovered_eff_lines_m, sw) *
//     (1 + aw * uncovered_fraction_m); modules with 0 uncovered
//     chunks have weight 0.
//  2. Pick a module via cumulative-weight + uniform draw.
//  3. Pick a uniformly random uncovered chunk from that module.
//  4. Mark the chunk as drawn-this-call; recompute weights next pick.
//
// Map iteration is forbidden in the hot path — module IDs are
// sorted lexically before any draw.
func (h *HierarchicalSampler) Next(out *BoundaryOutput, covered map[string]bool, k int, rng *rand.Rand) []string {
	if k <= 0 || out == nil || len(out.Chunks) == 0 {
		return nil
	}
	aw := h.AntiCoverageWeight
	if aw <= 0 {
		aw = 3.0
	}
	sw := h.SizeWeightExp
	if sw <= 0 {
		sw = 0.5
	}

	// Per-module chunk buckets, each sorted by Chunk.ID for
	// deterministic iteration.
	moduleChunks := make(map[string][]Chunk, len(out.Modules))
	for _, c := range out.Chunks {
		moduleChunks[c.ModuleID] = append(moduleChunks[c.ModuleID], c)
	}
	moduleIDs := make([]string, 0, len(moduleChunks))
	for id, chs := range moduleChunks {
		sort.Slice(chs, func(i, j int) bool { return chs[i].ID < chs[j].ID })
		moduleChunks[id] = chs
		moduleIDs = append(moduleIDs, id)
	}
	sort.Strings(moduleIDs)

	drawn := make(map[string]bool)
	picked := make([]string, 0, k)

	for i := 0; i < k; i++ {
		// Compute per-module weights (recomputed every pick so anti-
		// coverage reflects this call's drawn set).
		weights := make([]float64, len(moduleIDs))
		total := 0.0
		for mi, mid := range moduleIDs {
			uncoveredEff := 0
			uncoveredChunks := 0
			totalChunks := 0
			for _, c := range moduleChunks[mid] {
				totalChunks++
				if covered[c.ID] || drawn[c.ID] {
					continue
				}
				uncoveredEff += c.EffLines
				uncoveredChunks++
			}
			if uncoveredChunks == 0 {
				continue
			}
			frac := float64(uncoveredChunks) / float64(totalChunks)
			// pow(0, 0.5) = 0, so a module with chunks but zero
			// effective lines (e.g., all-comment file) gets a small
			// floor weight from the anti-coverage term alone.
			sizeTerm := math.Pow(float64(uncoveredEff), sw)
			if uncoveredEff == 0 {
				sizeTerm = 1.0 // floor so a module is still drawable
			}
			w := sizeTerm * (1.0 + aw*frac)
			weights[mi] = w
			total += w
		}
		if total <= 0 {
			break
		}

		// Cumulative-weight draw for module.
		u := rng.Float64() * total
		chosen := ""
		cum := 0.0
		for mi, mid := range moduleIDs {
			if weights[mi] == 0 {
				continue
			}
			cum += weights[mi]
			if u < cum {
				chosen = mid
				break
			}
		}
		if chosen == "" {
			// Floating-point edge: pick the last non-zero-weight
			// module.
			for mi := len(moduleIDs) - 1; mi >= 0; mi-- {
				if weights[mi] > 0 {
					chosen = moduleIDs[mi]
					break
				}
			}
		}
		if chosen == "" {
			break
		}

		// Uniform draw among the chosen module's uncovered chunks.
		avail := make([]string, 0, len(moduleChunks[chosen]))
		for _, c := range moduleChunks[chosen] {
			if covered[c.ID] || drawn[c.ID] {
				continue
			}
			avail = append(avail, c.ID)
		}
		if len(avail) == 0 {
			continue
		}
		idx := rng.Intn(len(avail))
		drawn[avail[idx]] = true
		picked = append(picked, avail[idx])
	}

	return picked
}
