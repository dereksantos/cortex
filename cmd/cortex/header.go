package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// headerFacts is what the REPL's opening lines report. Gathered from the
// session by startupFacts, rendered by renderHeader — split so the rendering
// is golden-pinned without a live session.
type headerFacts struct {
	Version, Project, Model string
	// Resumed is set for `cortex resume`; the fields below only print then.
	Resumed         bool
	SessionID       string
	Turns, Outlined int
	Notes           int
}

// renderHeader is the REPL's opening (docs/tui-polish.md, track 1): one line
// for what is running where, and on resume one more for how much of the
// session came back. Plain text; the product name alone keeps the default
// foreground so the line has an anchor, the rest is Dim metadata.
func renderHeader(f headerFacts) []string {
	first := "cortex " + style.Paint(joinDot(f.Version, f.Project, f.Model), style.Dim)
	if !f.Resumed {
		return []string{first}
	}
	parts := []string{"resumed " + f.SessionID, tools.CountNoun(f.Turns, "turn")}
	if f.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d outlined, %d live", f.Outlined, f.Turns-f.Outlined))
	}
	if f.Notes > 0 {
		parts = append(parts, tools.CountNoun(f.Notes, "note"))
	}
	return []string{first, style.Paint(joinDot(parts...), style.Dim)}
}

// startupFacts reads the header's facts off the session. Call it after
// EnableMemory so the note count is real.
func (cs *CortexSession) startupFacts(resumed bool) headerFacts {
	f := headerFacts{
		Version:   version(),
		Project:   homeRel(cs.root()),
		Model:     cs.Request.Model,
		Resumed:   resumed,
		SessionID: cs.SessionID,
	}
	if cs.ws != nil {
		f.Turns, f.Outlined = cs.ws.TotalTurns(), cs.ws.Demoted()
	}
	if cs.memory != nil {
		if metas, err := cs.memory.List(); err == nil {
			f.Notes += len(metas)
		}
	}
	if cs.userMemory != nil {
		if metas, err := cs.userMemory.List(); err == nil {
			f.Notes += len(metas)
		}
	}
	return f
}

// homeRel shows path absolute, with the home directory as "~".
func homeRel(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// joinDot joins the non-empty parts with " · ".
func joinDot(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}
