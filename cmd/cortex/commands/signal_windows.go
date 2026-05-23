//go:build windows

package commands

import (
	"os"
	"os/signal"
)

// notifyTermSignals registers for interrupt signals on Windows.
func notifyTermSignals(c chan<- os.Signal) {
	signal.Notify(c, os.Interrupt)
}
