// Package loopui contains runtime terminal widgets for the interactive loop.
package loopui

import (
	"fmt"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

// Spinner renders a static "thinking..." status line on stdout while we wait
// on the model — no animated glyph (2026-07-19 decision: the REPL dropped its
// icon set; the label's own elapsed-seconds tick, driven by the caller via
// SetLabel, is the only motion). It uses a single mutex to serialize all
// stdout writes (spinner goroutine + main thread), so no frame can ever
// interleave with real output. Stop blocks until the goroutine has actually
// exited and then erases the line, so nothing can bleed into output printed
// afterward.
type Spinner struct {
	stopChan chan struct{}
	doneChan chan struct{}
	mu       sync.Mutex // serializes all stdout writes + guards label
	label    string     // status text (already colored), e.g. "thinking... 3s"
}

// NewSpinner returns an idle spinner.
func NewSpinner() *Spinner { return &Spinner{} }

// defaultLabel is shown while no caller-supplied label is set — e.g. the
// blocking (non-streaming) send path, which never calls SetLabel at all.
var defaultLabel = tools.Color("thinking...", tools.Cyan)

// SetLabel updates the status text. The string is printed verbatim, so
// callers apply their own color/truncation (e.g. the live "thinking... 3s"
// elapsed-seconds label built in cmd/cortex/streaming.go).
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Start begins repainting the status line at a fixed cadence. There is no
// glyph animation to cycle — the tick just repaints whatever label is
// current, so a caller's own elapsed-seconds tick (SetLabel) is the only
// visible motion.
func (s *Spinner) Start() {
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	go func() {
		defer close(s.doneChan)
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				s.mu.Lock()
				label := s.label
				if label == "" {
					label = defaultLabel
				}
				// \033[K clears any residue when the label shrinks.
				fmt.Printf("\r%s\033[K", label)
				s.mu.Unlock()
			}
		}
	}()
}

// Stop halts the spinner and clears its line.
func (s *Spinner) Stop() {
	close(s.stopChan)
	<-s.doneChan
	s.mu.Lock()
	fmt.Print("\r\033[K")
	s.mu.Unlock()
}
