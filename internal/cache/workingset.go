package cache

import "fmt"

// CharsPerToken is the rough bytes-per-token estimate used across the repo.
const CharsPerToken = 4

// TokensOf returns the number of tokens for the given character count.
func TokensOf(chars int) int {
	return chars / CharsPerToken
}

// TurnSpan is one completed turn in the message log — the half-open index range
// [Start, End) of its messages, plus the estimated token size of the whole span.
// Spans are contiguous: each turn starts where the previous one ended.
type TurnSpan struct {
	Start, End int
	Tokens     int
}

// WorkingSet tracks the demotion frontier over an append-only message log.
// Turns before the frontier are demoted (they live on as outline entries);
// turns at or after it are the hydrated tail, sent verbatim. Demotion is
// batched (hysteresis): nothing moves until the tail crosses the high watermark,
// then whole turns demote oldest-first until the tail is at or under the low watermark.
type WorkingSet struct {
	turns    []TurnSpan
	frontier int
	base     int
	highWM   int
	lowWM    int
}

// New creates a WorkingSet with the given parameters.
// base is the message-log index where turn content begins (after the system message).
// If lowWM > highWM or either is <= 0, panic with a clear message (programmer error).
func New(base, highWM, lowWM int) *WorkingSet {
	if lowWM > highWM || highWM <= 0 || lowWM <= 0 {
		panic("workingset: watermarks must be positive and lowWM <= highWM")
	}
	return &WorkingSet{
		turns:    []TurnSpan{},
		frontier: 0,
		base:     base,
		highWM:   highWM,
		lowWM:    lowWM,
	}
}

// AddTurn records a completed turn.
// Panic if span.Start does not equal the End of the previous span (or base for the first span).
func (ws *WorkingSet) AddTurn(span TurnSpan) {
	if len(ws.turns) == 0 {
		if span.Start != ws.base {
			panic("workingset: first turn must start at base")
		}
	} else {
		last := ws.turns[len(ws.turns)-1]
		if span.Start != last.End {
			panic("workingset: turn spans must be contiguous")
		}
	}
	ws.turns = append(ws.turns, span)
}

// TailTokens returns the sum of Tokens over turns[frontier:].
func (ws *WorkingSet) TailTokens() int {
	tokens := 0
	for i := ws.frontier; i < len(ws.turns); i++ {
		tokens += ws.turns[i].Tokens
	}
	return tokens
}

// Base returns the message-log index where turn content begins. Messages
// before it (system, a compaction summary, unstamped resumed history) are
// outside the working set entirely: never demoted, always sent.
func (ws *WorkingSet) Base() int {
	return ws.base
}

// FrontierMsg returns the message-log index where the hydrated tail begins.
// Returns base when frontier == 0, else turns[frontier-1].End.
func (ws *WorkingSet) FrontierMsg() int {
	if ws.frontier == 0 {
		return ws.base
	}
	return ws.turns[ws.frontier-1].End
}

// DemoteBatch advances the frontier oldest-first until TailTokens() <= lowWM.
// Returns the newly demoted spans in order. Never demotes the most recent turn.
func (ws *WorkingSet) DemoteBatch() []TurnSpan {
	if ws.TailTokens() <= ws.highWM {
		return nil
	}

	demoted := []TurnSpan{}
	// Always keep at least one turn hydrated
	for ws.frontier < len(ws.turns)-1 && ws.TailTokens() > ws.lowWM {
		demoted = append(demoted, ws.turns[ws.frontier])
		ws.frontier++
	}

	return demoted
}

// Demoted returns how many turns are behind the frontier.
func (ws *WorkingSet) Demoted() int {
	return ws.frontier
}

// TotalTurns returns the total number of turns tracked.
func (ws *WorkingSet) TotalTurns() int {
	return len(ws.turns)
}

// GetWatermarks returns the current high and low watermarks.
func (ws *WorkingSet) GetWatermarks() (int, int) {
	return ws.highWM, ws.lowWM
}

// RestoreState restores a previously persisted demotion frontier and watermarks.
// It validates the complete snapshot before mutating the working set.
func (ws *WorkingSet) RestoreState(frontier, highWM, lowWM int) error {
	if frontier < 0 || frontier > len(ws.turns) {
		return fmt.Errorf("workingset: frontier %d is outside [0,%d]", frontier, len(ws.turns))
	}
	if highWM <= 0 || lowWM <= 0 || lowWM > highWM {
		return fmt.Errorf("workingset: restored watermarks must be positive and lowWM <= highWM")
	}
	ws.frontier = frontier
	ws.highWM = highWM
	ws.lowWM = lowWM
	return nil
}

// ReorderTail reorders the hydrated tail turns based on the given metric.
// Only rearranges order—it does not evict or compress.
// Supported metrics: "salience", "recency", "task-relevance".
// Returns the new order of turns.
func (ws *WorkingSet) ReorderTail(metric string) []TurnSpan {
	if ws.frontier >= len(ws.turns) {
		return nil // nothing to reorder
	}

	tails := make([]TurnSpan, len(ws.turns)-ws.frontier)
	copy(tails, ws.turns[ws.frontier:])

	// For now, only "recency" matters (most recent first = no change needed)
	// Other metrics would require external salience scoring
	switch metric {
	case "recency":
		// Most recent first - already in order (oldest to newest in tail)
		// So we reverse to get newest first
		for i, j := 0, len(tails)-1; i < j; i, j = i+1, j-1 {
			tails[i], tails[j] = tails[j], tails[i]
		}
	case "salience", "task-relevance":
		// Without external scoring, we can't properly order by these
		// For now, keep as-is (same as recency for most recent first)
		for i, j := 0, len(tails)-1; i < j; i, j = i+1, j-1 {
			tails[i], tails[j] = tails[j], tails[i]
		}
	default:
		// Unknown metric - return empty slice to indicate no change
		return nil
	}

	return tails
}

// AdjustWatermarks adjusts the working set watermarks by the given deltas.
// Deltas are bounded to ±W/4 to prevent abuse.
// Returns (newHighWM, newLowWM, error).
func (ws *WorkingSet) AdjustWatermarks(highDelta, lowDelta int) (int, int, error) {
	// Calculate max delta (±W/4 where W is the window size)
	// Window size = highWM * 2 (since highWM is ~W/2)
	maxDelta := ws.highWM / 2

	// Validate deltas are within bounds
	if highDelta < -maxDelta || highDelta > maxDelta {
		return ws.highWM, ws.lowWM, fmt.Errorf("high_delta %d is out of bounds (±%d)", highDelta, maxDelta)
	}
	if lowDelta < -maxDelta || lowDelta > maxDelta {
		return ws.highWM, ws.lowWM, fmt.Errorf("low_delta %d is out of bounds (±%d)", lowDelta, maxDelta)
	}

	// Calculate new watermarks
	newHigh := ws.highWM + highDelta
	newLow := ws.lowWM + lowDelta

	// Ensure watermarks remain positive
	if newHigh <= 0 {
		return ws.highWM, ws.lowWM, fmt.Errorf("new high watermark %d must be positive", newHigh)
	}
	if newLow <= 0 {
		return ws.highWM, ws.lowWM, fmt.Errorf("new low watermark %d must be positive", newLow)
	}

	// Ensure low <= high invariant
	if newLow > newHigh {
		return ws.highWM, ws.lowWM, fmt.Errorf("watermark adjustment would violate lowWM <= highWM: low=%d, high=%d", newLow, newHigh)
	}

	ws.highWM = newHigh
	ws.lowWM = newLow

	return newHigh, newLow, nil
}
