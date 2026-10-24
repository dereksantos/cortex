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
	// Skills is the skill + project-summary block (the "precedence + show on
	// startup" feature). Zero value means "no skills" and the banner renders
	// the original single line.
	Skills startupSkillFacts
}

// renderHeader is the REPL's opening (docs/tui-polish.md, track 1): one line
// for what is running where, and on resume one more for how much of the
// session came back. Plain text; the product name alone keeps the default
// foreground so the line has an anchor, the rest is Dim metadata.
//
// When f.Skills is non-empty, it renders the skill summary block below the
// header line: the one-shot project summary (if available) and the skill
// list ordered by precedence tier (system, project, user).
func renderHeader(f headerFacts) []string {
	first := "cortex " + style.Paint(joinDot(f.Version, f.Project, f.Model), style.Dim)
	lines := []string{first}

	// Resume line (existing behavior).
	if f.Resumed {
		parts := []string{"resumed " + f.SessionID, tools.CountNoun(f.Turns, "turn")}
		if f.Turns > 0 {
			parts = append(parts, fmt.Sprintf("%d outlined, %d live", f.Outlined, f.Turns-f.Outlined))
		}
		if f.Notes > 0 {
			parts = append(parts, tools.CountNoun(f.Notes, "note"))
		}
		lines = append(lines, style.Paint(joinDot(parts...), style.Dim))
	}

	// Skills block (the "precedence + show on startup" feature).
	if len(f.Skills.Skills) > 0 {
		lines = append(lines, renderSkillBlock(f.Skills)...)
	}

	return lines
}

// renderSkillBlock renders the skill summary block: the one-shot project
// summary (if available) and the skill list ordered by precedence tier.
func renderSkillBlock(f startupSkillFacts) []string {
	var lines []string

	// Project summary (one-shot LLM call; "" when unavailable).
	if f.ProjectSummary != "" {
		lines = append(lines, "Project: "+f.ProjectSummary)
		lines = append(lines, "") // blank line
	}

	// Skill list.
	lines = append(lines, fmt.Sprintf("Skills (%d)", len(f.Skills)))
	for _, s := range f.Skills {
		line := fmt.Sprintf("  %-8s %s", s.Tier.Name(), s.Name)
		if s.Source != "" {
			line += "  " + s.Source
		}
		lines = append(lines, line)
	}

	return lines
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
	// Skills + one-shot project summary (the "precedence + show on startup"
	// feature). Gated on skills.enabled: when the kill-switch is on, the
	// block is empty and the banner renders the original line.
	if cs.Config != nil && cs.Config.skillsEnabled() {
		f.Skills = cs.startupSkillFacts()
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
