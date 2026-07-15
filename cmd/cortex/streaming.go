package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
)

// toolMarker is Qwen's native tool-call opener. When the proxy doesn't
// normalize tool calls they arrive as this markup inside content; it's stripped
// from the stored message, so the live echo must hold back at this point too.
const toolMarker = "<tool_call"

// streamPrinter echoes assistant prose to the terminal as it streams. It prints
// the gutter lazily on the first visible byte (stopping the spinner first), and
// suppresses output once a tool-call marker appears so raw markup never shows.
// All printing happens on the calling goroutine (StreamChat invokes onContent
// synchronously), so it never races the spinner once that's stopped.
type streamPrinter struct {
	spinner    *Spinner          // stopped on first visible byte; nil to skip (tests)
	out        io.Writer         // destination; nil means os.Stdout
	buf        strings.Builder   // all content seen so far
	reason     strings.Builder   // accumulated reasoning, for the live ticker tail
	printed    int               // bytes of buf already written
	suppress   bool              // a tool-call marker appeared; stop echoing
	began      bool              // gutter printed (and spinner stopped)
	md         *markdownRenderer // nil → raw token streaming; set → block-buffered render
	pending    string            // md path: prose not yet flushed as a complete block
	gutterOpen bool              // md path: gutter printed, first block not yet joined to it
	// onStatus drives a "thinking…" indicator when there's no standalone spinner
	// (the anchored REPL): on=true with the latest reasoning tail, on=false when
	// the answer starts. nil in the normal spinner path.
	onStatus func(on bool, tail string)
	// start is when the model call began — the base for the elapsed-seconds
	// shown in the live "thinking… Ns · tail" label (onReasoning) and the
	// turn-end "thought Ns · tok" stat (thoughtStat). Zero (test-constructed
	// printers that skip it) reads as 0s rather than a nonsensical duration.
	start time.Time
	// crumbed is set once breadcrumb has printed the reasoning trace for this
	// step, so thoughtStat knows not to add a second, redundant gray line for
	// the same event.
	crumbed bool
}

// reasoningTailWidth caps the live "thinking…" ticker to one line on typical
// terminals — we show the most recent runes, not the whole chain-of-thought.
const reasoningTailWidth = 80

// reasoningTail collapses whitespace and returns the last width runes, so the
// ticker stays a single, bounded line as reasoning streams.
func reasoningTail(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > width {
		r = r[len(r)-width:]
	}
	return string(r)
}

// elapsedTail prefixes the reasoning tail with the whole seconds elapsed
// since start, e.g. "12s · reasoning excerpt", or just "12s" before any
// reasoning text has arrived. A zero start (a printer built without one, e.g.
// tests) reads as 0s rather than a nonsensical multi-year duration.
func elapsedTail(start time.Time, tail string) string {
	secs := 0
	if !start.IsZero() {
		if d := time.Since(start); d > 0 {
			secs = int(d.Seconds())
		}
	}
	if tail == "" {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%ds · %s", secs, tail)
}

// onReasoning is the StreamChat reasoning callback: it feeds the spinner a dim
// live tail of the chain-of-thought, prefixed with elapsed seconds so a long
// deliberation reads as "thinking… 12s · …" rather than sitting silent.
// Reasoning is never printed to the transcript — once the answer starts, emit
// stops the spinner and the ticker is erased. No-op after the answer has
// begun (or with no spinner, e.g. tests).
func (p *streamPrinter) onReasoning(s string) {
	if p.began {
		return
	}
	p.reason.WriteString(s)
	tail := reasoningTail(p.reason.String(), reasoningTailWidth)
	et := elapsedTail(p.start, tail)
	switch {
	case p.spinner != nil:
		p.spinner.SetLabel(withColor("thinking… "+et, gray))
	case p.onStatus != nil:
		p.onStatus(true, et)
	}
}

// writer returns the configured sink, defaulting to stdout.
func (p *streamPrinter) writer() io.Writer {
	if p.out != nil {
		return p.out
	}
	return os.Stdout
}

// onContent is the StreamChat callback: accumulate, then print the portion that
// is safe to show — everything before a tool-call marker, holding back a
// possible partial marker straddling the chunk boundary.
func (p *streamPrinter) onContent(s string) {
	p.buf.WriteString(s)
	if p.suppress {
		return
	}
	full := p.buf.String()
	if i := strings.Index(full, toolMarker); i >= 0 {
		p.emit(full[p.printed:i])
		p.printed = len(full) // skip the markup entirely
		p.suppress = true
		return
	}
	// Hold back len(toolMarker)-1 trailing bytes: they might be the start of a
	// marker that completes in the next chunk.
	safe := len(full) - (len(toolMarker) - 1)
	if safe < p.printed {
		safe = p.printed
	}
	p.emit(full[p.printed:safe])
	p.printed = safe
}

// begin stops the spinner and prints the assistant gutter once, on the first
// visible content. The gutter is left open (no trailing newline) so the first
// fragment — raw bytes, or the first rendered block with its leading margin
// trimmed — sits on the same line as the timestamp.
func (p *streamPrinter) begin() {
	if p.began {
		return
	}
	if p.spinner != nil {
		p.spinner.Stop()
	}
	if p.onStatus != nil {
		p.onStatus(false, "") // answer started — clear the thinking status
	}
	icon, color := Message{Role: "assistant"}.gutter()
	fmt.Fprint(p.writer(), gutterPrefix(icon, color, time.Now()))
	p.gutterOpen = p.md != nil // render mode: first block joins this line
	p.began = true
}

// emit writes a fragment. In raw mode (md nil) it streams bytes straight
// through, as before. In render mode it accumulates prose and flushes each
// complete markdown block through glamour as soon as it closes.
func (p *streamPrinter) emit(s string) {
	if s == "" {
		return
	}
	if p.md == nil {
		p.begin()
		fmt.Fprint(p.writer(), s)
		return
	}
	p.pending += s
	blocks, rest := splitBlocks(p.pending)
	p.pending = rest
	for _, b := range blocks {
		p.writeBlock(b)
	}
}

// writeBlock renders one complete markdown block and prints it. Blank blocks
// are skipped so separators don't leave gaps. The first block after the gutter
// has its leading margin trimmed so it joins the timestamp line; later blocks
// flow beneath at glamour's own indent.
func (p *streamPrinter) writeBlock(b string) {
	if strings.TrimSpace(b) == "" {
		return
	}
	p.begin()
	out := p.md.render(b)
	if p.gutterOpen {
		out = trimLeadingIndent(out)
		p.gutterOpen = false
	}
	fmt.Fprintln(p.writer(), out)
}

// finish flushes any held-back tail (when no marker ever appeared) plus, in
// render mode, the final unterminated block, then closes the line. Returns
// whether the gutter was printed (i.e. anything was shown).
func (p *streamPrinter) finish() bool {
	if !p.suppress {
		if full := p.buf.String(); p.printed < len(full) {
			p.emit(full[p.printed:])
			p.printed = len(full)
		}
	}
	if p.md != nil && strings.TrimSpace(p.pending) != "" {
		p.writeBlock(p.pending)
		p.pending = ""
	}
	// Raw mode terminates the streamed line here; render mode already newline-
	// terminated every block in writeBlock. A trailing blank line gives the
	// answer breathing room before the next prompt or status notice.
	if p.began && p.md == nil {
		fmt.Fprintln(p.writer())
		fmt.Fprintln(p.writer())
	}
	return p.began
}

// breadcrumbCap bounds the persisted reasoning trace to a short single line.
const breadcrumbCap = 140

// breadcrumb persists a one-line trace of the model's reasoning when a step made
// tool calls but produced no answer prose. Models like north/glm stream their
// "let me do X" narration as reasoning_content, which onReasoning only shows on
// the transient "thinking…" ticker — so without this the narration flashes by
// and vanishes, leaving only the ▸ tool-action lines. No-op when prose was
// printed (it already persisted), when there were no tool calls (a final answer
// path), or when there was no reasoning. Prints to writer() (the anchor pipe in
// anchored mode, real stdout otherwise — the spinner is already stopped by the
// caller before this runs).
func (p *streamPrinter) breadcrumb(res *AgentResponse) {
	if p.began || res == nil || len(res.Choices) == 0 {
		return
	}
	if len(res.Choices[0].Message.ToolCalls) == 0 {
		return
	}
	line := collapseLine(p.reason.String(), breadcrumbCap)
	if line == "" {
		return
	}
	fmt.Fprintf(p.writer(), "%s%s\n",
		gutterPrefix(iconThought, gray, time.Now()), withColor(line, gray))
	p.crumbed = true // thoughtStat: skip, this step's trace already showed
}

// thoughtStatMinSeconds / thoughtStatMinTokens gate the turn-end thought-stat
// line to "meaningful" deliberation — a two-second or 200-token chain of
// thought is worth a line, a token or two of filler narration isn't.
const (
	thoughtStatMinSeconds = 2
	thoughtStatMinTokens  = 200
)

// estimatedReasoningTokens approximates a reasoning-token count from the
// accumulated trace length when the backend didn't report
// completion_tokens_details.reasoning_tokens — the same chars/4 heuristic
// used elsewhere in the session for untokenized text (see
// emitSessionMetrics's InjectedContextTokens: cs.injectedChars / 4).
func estimatedReasoningTokens(trace string) int {
	return len(trace) / 4
}

// thoughtStat prints one dim gutter line summarizing how long and how much a
// model call spent deliberating — "thought 14s · 1.1k tok" — mirroring
// breadcrumb's format. Uses res.Usage's reported reasoning-token count when
// the backend provides one, otherwise estimates from the accumulated trace
// length. No-op with no reasoning at all, when neither the elapsed-time nor
// token threshold is met (a quick deliberation isn't worth a dedicated
// line), or when breadcrumb already printed the trace for this same step (one
// gray gutter line per step, not two). Callers only construct a streamPrinter
// outside quiet mode, so this is implicitly never shown headless.
func (p *streamPrinter) thoughtStat(res *AgentResponse) {
	if p.crumbed {
		return
	}
	trace := p.reason.String()
	if trace == "" {
		return
	}
	var elapsed time.Duration
	if !p.start.IsZero() {
		elapsed = time.Since(p.start)
	}
	tok := 0
	if res != nil {
		tok = res.Usage.ReasoningTokens()
	}
	if tok == 0 {
		tok = estimatedReasoningTokens(trace)
	}
	if elapsed < thoughtStatMinSeconds*time.Second && tok <= thoughtStatMinTokens {
		return
	}
	line := fmt.Sprintf("thought %ds · %s tok", int(elapsed.Seconds()), humanK(tok))
	fmt.Fprintf(p.writer(), "%s%s\n",
		gutterPrefix(iconThought, gray, time.Now()), withColor(line, gray))
}

// collapseLine flattens s to a single whitespace-collapsed line, capped at cap
// runes with an ellipsis — the short reasoning trace breadcrumb shows.
func collapseLine(s string, cap int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > cap {
		return strings.TrimRight(string(r[:cap]), " ") + "…"
	}
	return s
}

// send runs one model call. In streaming mode (the default) it echoes prose
// live via a streamPrinter and returns streamed=true so Resolve doesn't
// re-print. The blocking fallback keeps the old spinner-around-the-call
// behavior and returns streamed=false (Resolve prints). Either way the spinner
// is fully stopped and the line is clean before this returns.
func (cs *CortexSession) send(ctx context.Context) (res *AgentResponse, streamed bool, err error) {
	// Headless: no spinner, no live token echo — the caller reads the reply
	// from TurnResult and owns all output. Plain blocking Send, UNLESS a
	// served session has wired onThinking (serve_stream.go) to signal the web
	// UI while it deliberates — then stream instead so the reasoning/content
	// transition is observable, still without printing anything to a
	// terminal that doesn't exist.
	if cs.quiet {
		if cs.onThinking == nil {
			res, err = cs.Request.Send(ctx)
			return res, false, err
		}
		res, err = cs.sendQuietObserved(ctx)
		return res, false, err
	}
	// Anchored REPL: no standalone spinner — the "thinking" indicator lives on
	// the pinned status row, and prose streams above it (stdout is redirected to
	// the anchor's pipe). Always streaming here (anchored mode requires it).
	if cs.live != nil {
		cs.live.SetThinking(true, "")
		p := &streamPrinter{md: cs.markdown(), onStatus: cs.live.SetThinking, start: time.Now()}
		res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
		p.finish()
		cs.live.SetThinking(false, "")
		p.breadcrumb(res)  // persist the reasoning trace of a silent tool step
		p.thoughtStat(res) // else: a turn-end "thought Ns · tok" stat, if reasoning was substantial
		return res, true, err
	}
	s := NewSpinner()
	s.Start()
	if !streamingEnabled() {
		res, err = cs.Request.Send(ctx)
		s.Stop()
		return res, false, err
	}
	p := &streamPrinter{spinner: s, md: cs.markdown(), start: time.Now()}
	res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
	p.finish()
	if !p.began {
		s.Stop() // stop before the breadcrumb so the line is clean
	}
	p.breadcrumb(res)  // persist the reasoning trace of a silent tool step
	p.thoughtStat(res) // else: a turn-end "thought Ns · tok" stat, if reasoning was substantial
	return res, true, err
}

// sendQuietObserved runs one model call over SSE with no terminal echo at
// all (quiet mode), but throttles cs.onThinking(true)/(false) around the
// reasoning phase: true on the first reasoning delta, false once answer
// content starts (or once, at the end, if the call produced reasoning but no
// content — e.g. a tool-call-only step). Never forwards the reasoning or
// content text itself, only the on/off transition — see docs on
// CortexSession.onThinking (session_core.go).
func (cs *CortexSession) sendQuietObserved(ctx context.Context) (*AgentResponse, error) {
	thinking := false
	onReasoning := func(string) {
		if !thinking {
			thinking = true
			cs.onThinking(true)
		}
	}
	onContent := func(string) {
		if thinking {
			thinking = false
			cs.onThinking(false)
		}
	}
	res, err := cs.Request.SendStream(ctx, onContent, onReasoning)
	if thinking {
		cs.onThinking(false)
	}
	return res, err
}

// runAnchoredTurn runs one turn with the prompt pinned to the bottom row and
// every byte of turn output funneled above it. os.Stdout is redirected through
// a pipe whose lines feed the anchor (so ad-hoc fmt.Print output, tool-action
// lines, and the streamed answer all land above the prompt); the anchor draws
// the input and "thinking" status straight to the real terminal. Keystrokes
// typed during the turn edit the pinned line live and are returned to seed the
// next prompt. ESC/Ctrl-C cancels via the anchor's context.
func runAnchoredTurn(session *CortexSession, editor *lineedit.Terminal, input, seed string) (string, error) {
	anchor, ctx := editor.Anchor(session.Prompt(), seed)
	r, w, err := os.Pipe()
	if err != nil {
		// Pipe setup failed (rare): fall back to the silent-capture path so the
		// turn still runs and cancels cleanly.
		anchor.Stop()
		c, stop := editor.Interruptible(context.Background())
		_, e := session.Turn(c, input)
		return stop(), e
	}
	realStdout := os.Stdout
	os.Stdout = w
	session.live = anchor

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			anchor.EmitLine(sc.Text())
		}
	}()

	_, turnErr := session.Turn(ctx, input)

	// Restore stdout, then close the write end so the drain goroutine sees EOF
	// and flushes the last line before we erase the pinned block.
	os.Stdout = realStdout
	session.live = nil
	w.Close()
	<-drained
	r.Close()
	return anchor.Stop(), turnErr
}
