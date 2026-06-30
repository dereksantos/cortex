// Package loopui contains runtime terminal widgets for the interactive loop.
package loopui

import (
	"fmt"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

// spinnerChars is the sequence of frames for the in-place spinner.
var spinnerChars = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Spinner renders a simple in-place rotating-character animation on stdout
// while we wait on the model. It uses a single mutex to serialize all stdout
// writes (spinner goroutine + main thread), so no frame can ever interleave
// with real output. Stop blocks until the goroutine has actually exited and
// then erases the line, so no frame can bleed into output printed afterward.
type Spinner struct {
	stopChan chan struct{}
	doneChan chan struct{}
	mu       sync.Mutex // serializes all stdout writes + guards label
	label    string     // optional suffix (already colored) shown after the glyph
}

// NewSpinner returns an idle spinner.
func NewSpinner() *Spinner { return &Spinner{} }

// SetLabel updates the text shown after the spinner glyph. The string is
// printed verbatim, so callers apply their own color/truncation.
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Start begins rendering spinner frames.
func (s *Spinner) Start() {
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	idx := 0
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
				glyph := tools.Color(string(spinnerChars[idx%len(spinnerChars)]), tools.Cyan)
				if s.label != "" {
					// \033[K clears any residue when the label shrinks.
					fmt.Printf("\r%s %s\033[K", glyph, s.label)
				} else {
					fmt.Printf("\r%s\033[K", glyph)
				}
				s.mu.Unlock()
				idx++
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
