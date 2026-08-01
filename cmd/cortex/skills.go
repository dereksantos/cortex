// skills.go wires Agent Skills discovery (internal/skills, agentskills.io)
// onto the coder session: resolving the discovery roots and rendering the
// turn-start index note injected alongside the memory index (turn.go). This
// is coder-only — the Study/Learn/Agent subagent profiles are seeded from
// their own static System prompt + a caller-built seed string
// (subagentRequest, study.go) and never call skillsIndexNote, so the index
// never reaches a subagent's context.
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/skills"
	"github.com/dereksantos/cortex/internal/userhome"
)

// skillsDirs resolves the discovery roots, in precedence order (project
// wins over user, cortex-native wins over compat — docs/configuration.md):
// ./.cortex/skills, ./.claude/skills, ./.agents/skills, ~/.cortex/skills.
// The compat dirs (.claude, .agents) let Cortex pick up skills a repo
// already has set up for Claude Code / Codex without duplicating them.
// skills.dirs (config) overrides this list ENTIRELY when set.
func (cs *CortexSession) skillsDirs() []string {
	if override := cs.Config.skillsDirsOverride(); len(override) > 0 {
		return override
	}
	root := cs.root()
	dirs := []string{
		filepath.Join(root, ".cortex", "skills"),
		filepath.Join(root, ".claude", "skills"),
		filepath.Join(root, ".agents", "skills"),
	}
	if userDir, err := userhome.Path("skills"); err == nil {
		dirs = append(dirs, userDir)
	}
	return dirs
}

// skillsIndexNote renders the turn-start Agent Skills injection: a short,
// principle-level header explaining what skills are and how to use one
// (read its SKILL.md with read_file for the full instructions; anything it
// points at — scripts/references/assets — loads the same way, on demand —
// the standard's progressive-disclosure design), then one line per
// discovered skill. Capped at skills.index_max entries (default
// skillsIndexMaxDefault); an "N more omitted" line replaces the rest when
// discovery finds more. "" when discovery finds nothing (or skills.enabled
// is false) — zero token cost, mirroring memoryIndexNote's empty behavior.
func (cs *CortexSession) skillsIndexNote() string {
	if !cs.Config.skillsEnabled() {
		return ""
	}
	found := skills.Discover(cs.skillsDirs())
	if len(found) == 0 {
		return ""
	}
	max := cs.Config.skillsIndexMax()
	shown := found
	omitted := 0
	if max > 0 && len(found) > max {
		shown = found[:max]
		omitted = len(found) - max
	}
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	b.WriteString("These are Agent Skills discovered in this workspace — on-demand playbooks " +
		"(agentskills.io). To use one, read its SKILL.md with read_file for the full " +
		"instructions; any scripts, references, or assets it points at load the same way, " +
		"on demand.\n\n")
	for _, s := range shown {
		fmt.Fprintf(&b, "- %s: %s (SKILL.md: %s)\n", s.Name, s.Description, s.Path)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "… (%d more skill(s) omitted; raise skills.index_max to see them)\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}
