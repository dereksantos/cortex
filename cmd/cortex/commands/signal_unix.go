//go:build !windows

package commands

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyTermSignals registers for interrupt and termination signals.
func notifyTermSignals(c chan<- os.Signal) {
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
}
