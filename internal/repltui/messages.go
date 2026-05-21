// Package repltui implements the Bubble Tea-based interactive REPL
// frontend. It plugs into cmd/cortex/commands/repl.go via the
// cliout.Sink interface: the REPL's main loop (runs in a goroutine)
// calls Sink methods that translate into tea.Msg values; the Bubble
// Tea program (runs on the main goroutine) consumes those messages
// and renders the screen. Input flows the other way — typed lines
// land in a channel the Sink's ReadLine drains.
//
// The package is intentionally narrow: one Model, one View, one
// TUISink, a Run entry point. No widget zoo, no theming system, no
// configuration. Stage 3 v1 — see PROGRESS-REPL.md (when written)
// for the planned v2 additions (mouse, scrollback search,
// configurable colors).
package repltui

// Typed tea.Msg shapes that the TUISink pushes onto the Bubble Tea
// program. Each message corresponds to one Sink method call so the
// Update handler can route them by Go type rather than parsing
// arbitrary strings.
//
// All messages carry strings already formatted for display — the
// REPL's notifier and slash-command code own the formatting; the
// TUI just decides where the line lands and how it's colored.

// infoMsg is delivered by Sink.Info — a normal status / output line
// that should append to the transcript.
type infoMsg struct{ text string }

// warnMsg is delivered by Sink.Warn — a non-fatal anomaly the user
// should notice. Renders with the warn style.
type warnMsg struct{ text string }

// errMsg is delivered by Sink.Error — a failed action. Renders with
// the error style. Empty text means "the err was nil"; the sink
// guards that case but we tolerate it here for safety.
type errMsg struct{ text string }

// eventMsg is delivered by Sink.Event — a structured DAG / tool-call
// event. Carries the kind so the Update handler can pick a color
// per cortex function (sense/attend/decide/act/etc.).
type eventMsg struct {
	kind    string
	payload any
}

// bannerMsg is delivered by Sink.Banner — the once-per-session
// welcome line. v1 renders it as a normal transcript line; later
// the TUI may pin it.
type bannerMsg struct{ text string }

// promptRequestedMsg is delivered when Sink.ReadLine is waiting for
// input. Updates the visible prompt prefix and gates the input
// loop's submission-channel write.
type promptRequestedMsg struct{ prompt string }

// quitMsg signals the program to exit cleanly (Ctrl-D, /quit). The
// Update handler returns tea.Quit on receipt.
type quitMsg struct{}

// verbosityMsg flips the model's verbose flag mid-session. Routed
// through the Bubble Tea send pipeline so it observes the message
// order rather than racing in-flight Event renders.
type verbosityMsg struct{ verbose bool }

// dagTraceMsg is delivered by Sink.Event with kind="dag.trace".
// Carries the structured shape the DAG tracer emits so the
// per-cortex-function color routing can fire on
// qualified_name's prefix.
type dagTraceMsg struct {
	QualifiedName string
	NodeID        string
	OK            bool
	LatencyMs     int
	Spawned       []string
	ErrCause      string
}

// bootstrapProgressMsg is delivered by Sink.Event with
// kind="bootstrap.progress". Updates the ambient status row that
// sits above the regular status line while bootstrap is running.
type bootstrapProgressMsg struct {
	Line string
	// Done flips true on the terminal bootstrap line so the Update
	// handler can clear the ambient row.
	Done bool
}
