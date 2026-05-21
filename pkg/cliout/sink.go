// sink.go — the REPL's (and other interactive commands') I/O seam.
//
// The bare `cortex` REPL today prints status, warnings, errors,
// banners, and slash-command output via direct fmt.Println /
// fmt.Printf calls — and reads input via bufio.Scanner straight off
// os.Stdin. That works for a line-based readline UX but cements two
// design problems:
//
//  1. Every print site has to know the formatting convention (two-
//     space indent, "→" arrows, the verbose-mode gate, etc.). Drift
//     is mechanical; consistency is by convention only.
//  2. A future TUI (bubbletea / lipgloss) that owns the screen
//     can't coexist with arbitrary fmt.Println calls — they corrupt
//     the rendered display.
//
// Sink fixes both. Call sites stop knowing about formatting and
// instead describe what they're saying semantically (Info, Warn,
// Error, …). Implementations decide whether that's a stdout line
// with ANSI escapes, a tea.Msg pushed onto a Bubble Tea program, or
// a JSON event for log scraping. The 30-ish print sites in
// cmd/cortex/commands/repl.go switch from `fmt.Println("  model →
// …")` to `s.ui.Info("model → …")` — same content, one source of
// truth on how it renders.
//
// The interface also owns the input read side: a TUI sink can't let
// the main loop read from os.Stdin directly (Bubble Tea owns the
// terminal). ReadLine encapsulates "give me one line of user input"
// so both impls share the contract.
package cliout

// Sink is the per-session I/O interface for interactive commands.
// Construct one at REPL/command start; pass through to anywhere
// that previously did fmt.Println-style output or bufio.Scanner
// input.
//
// Implementations:
//   - StdoutSink: writes to os.Stdout / os.Stderr, reads from
//     os.Stdin. Preserves pre-TUI behavior verbatim — the
//     --no-tui safety valve.
//   - TUISink (internal/repltui): pushes Sink messages onto a
//     Bubble Tea program's tea.Msg channel; reads come back via
//     the program's input handling.
//
// Method semantics:
//   - Info: normal status / output line (slash-command results,
//     model swaps, "session saved", etc.). Always shown.
//   - Warn: non-fatal anomaly the user should notice (Ollama
//     unreachable, role-pinning warning). Distinguished from Info
//     so a TUI can color-mark it; StdoutSink writes to os.Stderr.
//   - Error: a failure tied to a specific action (verify failed,
//     harness error). Distinguished from Warn so the TUI can use
//     a different color and the StdoutSink can route to stderr.
//   - Event: a structured DAG / tool-call event. The existing
//     harness.Loop.Notify callback signature (kind string,
//     payload any) maps 1:1 — wire SetNotify(s.ui.Event) and the
//     same event stream flows through.
//   - Banner: once-per-session welcome line ("cortex · <wd> ·
//     <model> · /help"). Separated from Info because a TUI may
//     render it pinned at the top of the transcript or in the
//     status line rather than scrolling away.
//   - ReadLine: blocks until one line of input is available;
//     returns it with the trailing newline stripped. Returns
//     ("", io.EOF) on Ctrl-D or when the underlying reader closes.
//     Prompt is the leading text shown before the input cursor
//     (may be empty). Both the main loop and mid-turn user gates
//     (verify-fail retry prompt) call this; the sink synchronizes
//     concurrent reads internally.
type Sink interface {
	Info(msg string)
	Warn(msg string)
	Error(err error)
	Event(kind string, payload any)
	Banner(text string)
	ReadLine(prompt string) (string, error)

	// Markdown is for content that may contain markdown formatting
	// — agent final responses, slash-command output that wants
	// tables or code blocks. TUI sinks render with glamour;
	// StdoutSink falls through to Info so plain terminals still
	// show the content (just unstyled).
	Markdown(text string)

	// SetVerbose changes the verbose-rendering gate mid-session.
	// Used by the /verbose slash command. Implementations must
	// reflect the change on subsequent Event() calls so the user
	// sees per-turn tokens / tool_result detail flip immediately.
	SetVerbose(v bool)
}
