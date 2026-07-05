package cache

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
