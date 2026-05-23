package llm

import (
	"errors"
	"regexp"
	"strconv"
)

// ContextOverflowError signals that the model server rejected a
// request because prompt + max_output exceeded its context window.
// Callers (the harness loop) can catch this via errors.As or the
// AsContextOverflow helper, then learn the server's reported n_ctx
// and retry with a trimmed prompt.
//
// AvailableTokens is the server's n_ctx (the cap that was hit).
// RequestedTokens is the prompt size the server measured. Both are
// 0 when the message couldn't be parsed — the error still presents
// as a ContextOverflowError because the signature matched.
type ContextOverflowError struct {
	Message         string
	AvailableTokens int
	RequestedTokens int
}

// Error implements the error interface — returns the original
// server message so the existing repl-side hint code still surfaces
// the actionable string verbatim.
func (e *ContextOverflowError) Error() string { return e.Message }

// overflowRE matches the llama-server overflow signature. Format
// observed in lemonade-wrapped llama.cpp:
//
//	"request (4946 tokens) exceeds the available context size (4096 tokens)"
//
// The numbers are extracted so callers know what to budget against
// going forward.
var overflowRE = regexp.MustCompile(`request \((\d+) tokens?\) exceeds the available context size \((\d+) tokens?\)`)

// ParseContextOverflow scans msg for the overflow signature and
// returns a populated ContextOverflowError on match. Returns (nil,
// false) when the string doesn't look like an overflow.
func ParseContextOverflow(msg string) (*ContextOverflowError, bool) {
	m := overflowRE.FindStringSubmatch(msg)
	if m == nil {
		return nil, false
	}
	req, _ := strconv.Atoi(m[1])
	avail, _ := strconv.Atoi(m[2])
	return &ContextOverflowError{
		Message:         msg,
		AvailableTokens: avail,
		RequestedTokens: req,
	}, true
}

// AsContextOverflow returns the ContextOverflowError in err's chain
// (via errors.As), or falls back to ParseContextOverflow on the
// error string. The string-fallback is necessary because servers
// wrap us inside multiple layers of fmt.Errorf-style formatting —
// the typed error gets lost in transit, but the signature survives.
func AsContextOverflow(err error) (*ContextOverflowError, bool) {
	if err == nil {
		return nil, false
	}
	var ce *ContextOverflowError
	if errors.As(err, &ce) {
		return ce, true
	}
	return ParseContextOverflow(err.Error())
}
