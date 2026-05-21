package repltui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// Run drives the TUI. work is invoked in a separate goroutine
// immediately after the Bubble Tea program is wired to the sink;
// it represents the REPL's main loop — reads from sink.ReadLine,
// writes via sink.Info/Warn/Error/etc., and returns when the loop
// exits. When work returns, the program quits.
//
// initialStatus is the seed status-line text (model · workdir,
// etc.). The caller updates it later via sink.Banner.
//
// The TUI program runs on the calling goroutine — Bubble Tea
// requires this because it owns the terminal's raw mode. The REPL
// goroutine pushes messages through the sink; the program receives
// them in its Update handler.
//
// Returns the work function's error (or nil) once both the program
// and the worker have exited.
func Run(sink *TUISink, initialStatus string, work func() error) error {
	model := New(sink, initialStatus)
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	sink.SetProgram(program)

	// Worker goroutine — the REPL loop. When it exits we send a
	// quitMsg so the program tears down cleanly even if the user
	// hasn't Ctrl-D'd.
	workErr := make(chan error, 1)
	go func() {
		err := work()
		workErr <- err
		program.Send(quitMsg{})
	}()

	if _, err := program.Run(); err != nil {
		// Drain the worker so the goroutine doesn't leak; we still
		// return the program error because that's the structural
		// failure.
		sink.Close()
		<-workErr
		return fmt.Errorf("tui program: %w", err)
	}
	// Program exited (user typed /quit or Ctrl-D). Tell the worker
	// to wrap up — Close() unblocks any pending ReadLine.
	sink.Close()
	return <-workErr
}
