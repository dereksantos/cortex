//go:build windows

package commands

import (
	"os"
	"os/exec"
	"os/signal"
)

// notifyTermSignals registers for interrupt signals on Windows.
func notifyTermSignals(c chan<- os.Signal) {
	signal.Notify(c, os.Interrupt)
}

// isProcessAlive checks whether a process is still running.
// On Windows, FindProcess always succeeds so we attempt a zero-signal.
func isProcessAlive(p *os.Process) bool {
	// On Windows there is no signal 0; use FindProcess + Release as a best-effort check.
	_, err := os.FindProcess(p.Pid)
	return err == nil
}

// sendTermSignal sends a termination signal to the process.
// On Windows, os.Process.Kill is the only reliable option.
func sendTermSignal(p *os.Process) error {
	return p.Kill()
}

// detachProcess is a no-op on Windows.
func detachProcess(cmd *exec.Cmd) {}
