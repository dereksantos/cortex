package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/skills"
)

// skillTier is the precedence tier of a skill, in the order the startup
// banner displays them: system first, then project, then user.
type skillTier int

const (
	tierSystem skillTier = iota
	tierProject
	tierUser
)

// Name returns the human-readable tier label for the banner.
func (t skillTier) Name() string {
	switch t {
	case tierSystem:
		return "system"
	case tierProject:
		return "project"
	case tierUser:
		return "user"
	default:
		return "unknown"
	}
}

// skillEntry is one line in the startup banner's skill list.
type skillEntry struct {
	Name   string
	Tier   skillTier
	Source string // the discovery root that defined it ("" for built-in)
}

// startupSkillFacts is the skill + project-summary block the startup banner
// renders below the static header line.
type startupSkillFacts struct {
	ProjectSummary string
	Skills         []skillEntry
}

// skillTierForPath classifies a skill's path into its precedence tier.
// A path equal to defaultConfigSkillPath is system (built-in). A path under
// the user home is user. A path under the project root is project.
//
// User home is checked first because a user home skill (e.g.
// ~/home/.cortex/skills/foo) can also be under the project root (if the
// user home is inside the project, as in tests) — the more specific check
// must win.
func skillTierForPath(path, projectRoot, userHome string) skillTier {
	if path == defaultConfigSkillPath {
		return tierSystem
	}
	// Check user home first (more specific).
	if userHome != "" &&
		(strings.HasPrefix(path, userHome+string(filepath.Separator)) ||
			strings.HasPrefix(path, userHome+"/")) {
		return tierUser
	}
	// Then project root.
	if projectRoot != "" &&
		(strings.HasPrefix(path, projectRoot+string(filepath.Separator)) ||
			strings.HasPrefix(path, projectRoot+"/")) {
		return tierProject
	}
	// Default: treat unknown paths as project (they came from a discovery
	// root, which is always either project or user).
	return tierProject
}

// startupSkillFacts gathers the skill list and one-shot project summary for
// the startup banner. The skill list is ordered by precedence tier: system
// (built-in) first, then project, then user. Within a tier, the order is
// the discovery order. The project summary is a one-shot LLM call; when the
// model is unavailable or the call fails, it is "" and the banner degrades
// to the static header line.
func (cs *CortexSession) startupSkillFacts() startupSkillFacts {
	f := startupSkillFacts{}

	// Discover filesystem skills (same roots as skillsIndexNote).
	found := skills.Discover(cs.skillsDirs())

	// Build the skill list with tiers, in precedence order.
	var systemSkills, projectSkills, userSkills []skillEntry

	// The built-in default skill is always system tier (unless shadowed).
	hasDefault := false
	for _, s := range found {
		if s.Name == defaultConfigSkillName {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		systemSkills = append(systemSkills, skillEntry{
			Name:   defaultConfigSkillName,
			Tier:   tierSystem,
			Source: "", // built-in, no filesystem path
		})
	}

	// Classify filesystem skills by tier.
	for _, s := range found {
		tier := skillTierForPath(s.Path, cs.root(), cs.userHome())
		entry := skillEntry{
			Name:   s.Name,
			Tier:   tier,
			Source: skillSource(s.Path, cs.root(), cs.userHome()),
		}
		switch tier {
		case tierSystem:
			systemSkills = append(systemSkills, entry)
		case tierProject:
			projectSkills = append(projectSkills, entry)
		case tierUser:
			userSkills = append(userSkills, entry)
		}
	}

	// Order: system, project, user.
	f.Skills = append(f.Skills, systemSkills...)
	f.Skills = append(f.Skills, projectSkills...)
	f.Skills = append(f.Skills, userSkills...)

	// One-shot project summary (graceful degradation on failure).
	f.ProjectSummary = cs.projectSummaryOneShot()

	return f
}

// skillSource returns a short human-readable source label for a skill's
// path, relative to the project root or user home.
func skillSource(path, projectRoot, userHome string) string {
	if strings.HasPrefix(path, projectRoot+string(filepath.Separator)) {
		rel, _ := filepath.Rel(projectRoot, path)
		return rel + "/"
	}
	if strings.HasPrefix(path, userHome+string(filepath.Separator)) {
		rel, _ := filepath.Rel(userHome, path)
		return "~/" + rel + "/"
	}
	return path
}

// projectSummaryOneShot makes a single LLM call to produce a short
// natural-language summary of the project. When the model is unavailable
// (no backend, call failed, etc.), it returns "" and the banner degrades
// to the static header line.
//
// The prompt is deliberately short and constrained: the summary must be
// 1-2 sentences, no markdown, no code blocks. This keeps the banner line
// short and the token cost bounded.
func (cs *CortexSession) projectSummaryOneShot() string {
	// Test seam: if a fake summary is set (test-only), return it directly.
	if cs.fakeSummary != "" {
		return cs.fakeSummary
	}

	// No LLM backend → no summary (graceful degradation).
	if cs.Request == nil || cs.Request.Model == "" {
		return ""
	}

	// Build a short prompt that asks for a 1-2 sentence project summary.
	prompt := "In 1-2 sentences, summarize what this project is. " +
		"No markdown, no code blocks, no bullet points. " +
		"Be concrete: name the project, its primary language/framework, " +
		"and its main purpose. " +
		"Example: \"A general coding agent with a two-zone context window, " +
		"memory tools, subagents, and a durable benchmark store.\""

	// Make the one-shot LLM call. This is a single model round-trip
	// (no tool loop), reusing the same cs.Turn path that MaybeGreet uses
	// for its first-run intro.
	result, err := cs.Turn(context.Background(), prompt)
	if err != nil {
		// Call failed → degrade to no summary.
		return ""
	}

	// Extract the text from the turn result. The turn result's Reply field
	// is the model's prose response.
	summary := strings.TrimSpace(result.Reply)
	if summary == "" {
		return ""
	}

	// Cap the summary to a reasonable length (the banner line should be
	// short). If the model produced more than 200 chars, truncate to the
	// last 200 chars (the most specific part is usually at the end).
	const maxSummaryLen = 200
	if len(summary) > maxSummaryLen {
		summary = "…" + summary[len(summary)-maxSummaryLen:]
	}

	return summary
}

// userHome returns the user home directory for skill tier classification.
// Uses the test override when set (test seam), otherwise the real home.
func (cs *CortexSession) userHome() string {
	if cs.userHomeOverride != "" {
		return cs.userHomeOverride
	}
	// The real home directory (best-effort).
	if home, err := userhomeDir(); err == nil {
		return home
	}
	return ""
}

// userhomeDir is a small indirection over os.UserHomeDir so the test can
// override it without touching the session.
func userhomeDir() (string, error) {
	return os.UserHomeDir()
}
