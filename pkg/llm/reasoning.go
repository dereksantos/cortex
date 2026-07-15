package llm

import "strings"

// Some OpenAI-compatible servers (raw llama.cpp without --reasoning-format,
// Ollama running R1-style models) don't split chain-of-thought into a
// separate reasoning_content/reasoning field — they wrap it inline in the
// content channel with a <think>...</think> fence instead. The helpers below
// pull that fence back out so callers can treat it as reasoning uniformly,
// whichever shape the backend used.
const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// StripLeadingThinkFence splits a complete (non-streamed) message body into
// (content, reasoning) by pulling a single <think>...</think> fence off the
// very front. Bound (intentional, not a general parser): only a fence at the
// very start of the message is recognized — a <think> appearing after real
// prose has already streamed is left untouched in content, matching
// thinkFenceFilter's streaming behavior below. An unclosed fence (the
// max-tokens-clamp signature: the model burned its whole budget inside
// <think> and never got to close it) routes everything after the opening tag
// to reasoning, leaving content empty.
func StripLeadingThinkFence(s string) (content, reasoning string) {
	if !strings.HasPrefix(s, thinkOpen) {
		return s, ""
	}
	rest := s[len(thinkOpen):]
	if idx := strings.Index(rest, thinkClose); idx >= 0 {
		return rest[idx+len(thinkClose):], rest[:idx]
	}
	return "", rest
}

// thinkFenceState is where a streaming thinkFenceFilter is in recognizing (or
// ruling out) a leading <think>...</think> fence.
type thinkFenceState int

const (
	// thinkStart: no content delta has arrived yet, or what has arrived so far
	// is still a viable prefix of "<think>" — undecided.
	thinkStart thinkFenceState = iota
	// thinkInFence: the opening tag matched; bytes route to reasoning until
	// the closing tag is found (or the stream ends, unclosed).
	thinkInFence
	// thinkAfter: the fence closed, or was ruled out from the very first
	// bytes; everything from here on is content, unexamined.
	thinkAfter
)

// thinkFenceFilter is a small stateful filter that pulls an inline
// <think>...</think> fence out of a streamed content channel and routes its
// bytes to reasoning instead of content. See StripLeadingThinkFence for the
// non-streaming equivalent and the shared bound: only a fence at the very
// start of the message is recognized. Once the filter has ruled out a
// leading fence (or closed the one fence it found), every later byte —
// including a literal "<think>" appearing after real prose — flows straight
// to content unexamined; this is deliberately not a general nested parser.
// Tags may be split across arbitrarily many feed() calls (SSE chunk
// boundaries); a fence still open when flush() runs (the max-tokens-clamp
// signature) routes everything buffered to reasoning.
type thinkFenceFilter struct {
	state thinkFenceState
	buf   string // undecided/held-back bytes; bounded by the longer tag's length
}

// feed processes one content delta, returning the portions that are safe to
// route to content and to reasoning right now. Either return may be empty;
// bytes that might still be part of a split tag are held in buf for the next
// feed (or flush) call.
func (f *thinkFenceFilter) feed(s string) (content, reasoning string) {
	f.buf += s
	for {
		switch f.state {
		case thinkStart:
			if len(f.buf) < len(thinkOpen) {
				if strings.HasPrefix(thinkOpen, f.buf) {
					return content, reasoning // still an undecided prefix; hold everything
				}
				f.state = thinkAfter
				continue
			}
			if strings.HasPrefix(f.buf, thinkOpen) {
				f.buf = f.buf[len(thinkOpen):]
				f.state = thinkInFence
				continue
			}
			f.state = thinkAfter
			continue
		case thinkInFence:
			if idx := strings.Index(f.buf, thinkClose); idx >= 0 {
				reasoning += f.buf[:idx]
				f.buf = f.buf[idx+len(thinkClose):]
				f.state = thinkAfter
				continue
			}
			// Hold back a possible partial close tag straddling the next chunk.
			safe := len(f.buf) - (len(thinkClose) - 1)
			if safe < 0 {
				safe = 0
			}
			reasoning += f.buf[:safe]
			f.buf = f.buf[safe:]
			return content, reasoning
		case thinkAfter:
			content += f.buf
			f.buf = ""
			return content, reasoning
		}
	}
}

// flush drains any bytes still held back when the stream ends. An undecided
// thinkStart prefix (the stream ended mid "<thi", never enough to decide) was
// never a real fence, so it flows to content; bytes still buffered in
// thinkInFence are an unclosed fence — the whole remainder routes to
// reasoning (the max-tokens-clamp signature).
func (f *thinkFenceFilter) flush() (content, reasoning string) {
	switch f.state {
	case thinkInFence:
		reasoning, f.buf = f.buf, ""
	default:
		content, f.buf = f.buf, ""
	}
	return content, reasoning
}
