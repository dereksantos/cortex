//go:build !windows

package commands

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// notifyTermSignals registers for interrupt and termination signals.
func notifyTermSignals(c chan<- os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}

// sendTermSignal sends a termination signal to the process.
func sendTermSignal(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// detachProcess configures the command to run in its own process group.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}
