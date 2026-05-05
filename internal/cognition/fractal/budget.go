package fractal

import (
	"math/rand"
	"sync"
)

const (
	// YieldWindow is the number of recent cycles tracked for yield smoothing.
	YieldWindow = 8
	// EpsilonGreedyRate is the probability that, in a given cycle, one
	// randomly chosen source receives an exploration bonus regardless of
	// its recent yield.
	EpsilonGreedyRate = 0.10
	// EpsilonBonus is the extra slot allocation given to the chosen source.
	EpsilonBonus = 2
)

// SourceWeights tracks per-source yield over a rolling window and
// produces budget allocations weighted by smoothed yield, with an
// ε-greedy exploration bonus so silent sources don't get permanently
// starved.
type SourceWeights struct {
	mu      sync.Mutex
	history map[string][]cycleStats
}

type cycleStats struct {
	Items    int
	Insights int
}

// NewSourceWeights constructs an empty weights tracker.
func NewSourceWeights() *SourceWeights {
	return &SourceWeights{
		history: make(map[string][]cycleStats),
	}
}

// Update appends one cycle's per-source counts to the rolling window.
func (w *SourceWeights) Update(perSource map[string]cycleStats) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for name, stats := range perSource {
		hist := w.history[name]
		hist = append(hist, stats)
		if len(hist) > YieldWindow {
			hist = hist[len(hist)-YieldWindow:]
		}
		w.history[name] = hist
	}
}

// Allocate splits `budget` across `sources` using the formula:
//  1. shuffle source order;
//  2. each source gets a floor of max(1, budget/(2*N));
//  3. the remainder is distributed proportional to smoothed yield
//     ((insights+1)/(items+1)) — Laplace smoothing prevents lockout;
//  4. with probability EpsilonGreedyRate, one source is randomly
//     boosted by EpsilonBonus to keep exploration alive.
func (w *SourceWeights) Allocate(budget int, sources []string, rng *rand.Rand) map[string]int {
	out := make(map[string]int, len(sources))
	if budget <= 0 || len(sources) == 0 {
		return out
	}

	// Shuffle the working order so iteration isn't deterministic.
	order := make([]string, len(sources))
	copy(order, sources)
	if rng != nil {
		rng.Shuffle(len(order), func(i, j int) {
			order[i], order[j] = order[j], order[i]
		})
	}

	floor := budget / (2 * len(order))
	if floor < 1 {
		floor = 1
	}
	used := 0
	for _, name := range order {
		// Don't overshoot the budget on small N.
		take := floor
		if used+take > budget {
			take = budget - used
		}
		if take < 0 {
			take = 0
		}
		out[name] = take
		used += take
	}
	remaining := budget - used
	if remaining < 0 {
		remaining = 0
	}

	// Distribute remainder by smoothed yield.
	w.mu.Lock()
	yields := make(map[string]float64, len(order))
	var total float64
	for _, name := range order {
		hist := w.history[name]
		var items, insights int
		for _, c := range hist {
			items += c.Items
			insights += c.Insights
		}
		y := float64(insights+1) / float64(items+1)
		yields[name] = y
		total += y
	}
	w.mu.Unlock()

	if total > 0 && remaining > 0 {
		distributed := 0
		for _, name := range order {
			share := int(float64(remaining) * yields[name] / total)
			out[name] += share
			distributed += share
		}
		// Hand any rounding leftover to the first source in shuffled order.
		if leftover := remaining - distributed; leftover > 0 && len(order) > 0 {
			out[order[0]] += leftover
		}
	}

	// ε-greedy exploration bonus.
	if rng != nil && rng.Float64() < EpsilonGreedyRate && len(order) > 0 {
		pick := order[rng.Intn(len(order))]
		out[pick] += EpsilonBonus
	}

	return out
}

// Reset clears the rolling history (used in tests).
func (w *SourceWeights) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.history = make(map[string][]cycleStats)
}

// Stats reports per-source totals across the current window (used in
// tests and observability).
func (w *SourceWeights) Stats() map[string]cycleStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]cycleStats, len(w.history))
	for name, hist := range w.history {
		var s cycleStats
		for _, c := range hist {
			s.Items += c.Items
			s.Insights += c.Insights
		}
		out[name] = s
	}
	return out
}

// CycleStats is the exported form of per-cycle counts (for callers).
type CycleStats = cycleStats
