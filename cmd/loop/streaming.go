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

// onReasoning is the StreamChat reasoning callback: it feeds the spinner a dim
// live tail of the chain-of-thought. Reasoning is never printed to the
// transcript — once the answer starts, emit stops the spinner and the ticker is
// erased. No-op after the answer has begun (or with no spinner, e.g. tests).
func (p *streamPrinter) onReasoning(s string) {
	if p.began {
		return
	}
	p.reason.WriteString(s)
	tail := reasoningTail(p.reason.String(), reasoningTailWidth)
	switch {
	case p.spinner != nil:
		p.spinner.SetLabel(withColor("thinking… "+tail, gray))
	case p.onStatus != nil:
		p.onStatus(true, tail)
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
	// from TurnResult and owns all output.
	if cs.quiet {
		res, err = cs.Request.Send(ctx)
		return res, false, err
	}
	// Anchored REPL: no standalone spinner — the "thinking" indicator lives on
	// the pinned status row, and prose streams above it (stdout is redirected to
	// the anchor's pipe). Always streaming here (anchored mode requires it).
	if cs.live != nil {
		cs.live.SetThinking(true, "")
		p := &streamPrinter{md: cs.markdown(), onStatus: cs.live.SetThinking}
		res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
		p.finish()
		cs.live.SetThinking(false, "")
		p.breadcrumb(res) // persist the reasoning trace of a silent tool step
		return res, true, err
	}
	s := NewSpinner()
	s.Start()
	if !streamingEnabled() {
		res, err = cs.Request.Send(ctx)
		s.Stop()
		return res, false, err
	}
	p := &streamPrinter{spinner: s, md: cs.markdown()}
	res, err = cs.Request.SendStream(ctx, p.onContent, p.onReasoning)
	p.finish()
	if !p.began {
		s.Stop() // stop before the breadcrumb so the line is clean
	}
	p.breadcrumb(res) // persist the reasoning trace of a silent tool step
	return res, true, err
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
