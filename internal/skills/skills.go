// Package skills discovers Agent Skills (the open standard at
// agentskills.io, Linux Foundation governed): a skill is a directory
// "<name>/" containing a SKILL.md whose YAML frontmatter declares a `name`
// and `description`. Progressive disclosure is the whole point of the
// format — only name+description are meant to live in context at all times;
// the SKILL.md body, and any scripts/references/assets it points at, load on
// demand. This package only does discovery: it returns the Skill{Name,
// Description, Path} triples a caller injects into an index; reading the
// body/linked files on demand is the caller's existing read_file path, not
// this package's concern.
//
// Frontmatter parsing here is hand-rolled — the repo's stdlib-only
// discipline (matching internal/agent) rules out a yaml dependency — and
// honors only what Cortex needs: a leading "---" block containing single-line
// `name:` and `description:` keys, each optionally wrapped in single or
// double quotes. Every other frontmatter field the spec defines (license,
// compatibility, metadata, allowed-tools, disable-model-invocation,
// user-invocable) is tolerated but ignored in v1 — it is neither parsed nor
// validated. LIMITATION: a description folded across multiple lines (YAML
// block scalars "|"/">", or a bare line continuation) is explicitly OUT OF
// SCOPE; such a skill is skipped (not truncated, not mis-parsed) with a
// warning naming why.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Skill is one discovered Agent Skill.
type Skill struct {
	Name        string
	Description string
	// Path is the absolute path to the skill's SKILL.md.
	Path string
}

// nameRe enforces the spec's name charset: lowercase letters, digits, and
// hyphens only. Length (1-64) is checked separately so the two failure modes
// report distinct messages.
var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

const (
	maxNameLen = 64
	maxDescLen = 1024
)

// Discover scans dirs, in order, for immediate subdirectories containing a
// SKILL.md, parses and validates each, and returns the accepted skills in
// discovery order (dirs order, then directory-listing order within a dir —
// os.ReadDir already returns entries sorted by filename, so this is
// deterministic run to run).
//
// A dir that doesn't exist (or isn't readable) is skipped silently — the
// default discovery roots are opportunistic, not required. A subdirectory
// with no SKILL.md is simply not a skill and is skipped silently too. A
// SKILL.md that fails to parse or fails spec validation (bad name charset,
// name/directory mismatch, description out of range, missing frontmatter, a
// multi-line description) is skipped with exactly one warning line on
// stderr naming the skill directory and the reason.
//
// Skills are deduped by name: the FIRST dir in dirs that defines a given
// name wins: later dirs' same-named skill is silently shadowed. Callers rely
// on this to encode discovery precedence (e.g. project skills winning over
// user skills) purely through the order they pass to dirs.
func Discover(dirs []string) []Skill {
	seen := make(map[string]bool)
	var out []Skill
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dirName := e.Name()
			skillDir := filepath.Join(dir, dirName)
			mdPath := filepath.Join(skillDir, "SKILL.md")
			data, err := os.ReadFile(mdPath)
			if err != nil {
				continue // not a skill directory
			}
			sk, err := parseAndValidate(mdPath, dirName, data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "cortex: skipping skill %q (%s): %v\n", dirName, mdPath, err)
				continue
			}
			if seen[sk.Name] {
				continue
			}
			seen[sk.Name] = true
			out = append(out, sk)
		}
	}
	return out
}

// parseAndValidate parses mdPath's frontmatter and checks it against the
// spec (name charset/length, name==dirName, description length). abs is
// resolved best-effort — a failure to make mdPath absolute (only possible if
// os.Getwd itself fails) falls back to mdPath as given rather than erroring
// the whole skill out over an unrelated syscall failure.
func parseAndValidate(mdPath, dirName string, data []byte) (Skill, error) {
	name, description, err := parseFrontmatter(data)
	if err != nil {
		return Skill{}, err
	}
	if len(name) < 1 || len(name) > maxNameLen {
		return Skill{}, fmt.Errorf("name %q must be 1-%d characters (got %d)", name, maxNameLen, len(name))
	}
	if !nameRe.MatchString(name) {
		return Skill{}, fmt.Errorf("name %q must contain only lowercase letters, numbers, and hyphens", name)
	}
	if name != dirName {
		return Skill{}, fmt.Errorf("name %q does not match its directory name %q", name, dirName)
	}
	if len(description) < 1 || len(description) > maxDescLen {
		return Skill{}, fmt.Errorf("description must be 1-%d characters (got %d)", maxDescLen, len(description))
	}
	abs, absErr := filepath.Abs(mdPath)
	if absErr != nil {
		abs = mdPath
	}
	return Skill{Name: name, Description: description, Path: abs}, nil
}

// parseFrontmatter extracts the `name` and `description` values from a
// SKILL.md's leading YAML frontmatter block. It understands only top-level
// (unindented) single-line "key: value" pairs for those two keys — every
// other key, and any value shape besides a single-line scalar, is either
// ignored (other keys) or rejected with a specific error (name/description
// spanning multiple lines).
func parseFrontmatter(data []byte) (name, description string, err error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("missing frontmatter (SKILL.md must start with a --- block)")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", fmt.Errorf("frontmatter block is not closed with a trailing \"---\" line")
	}

	values := map[string]string{}
	for i := 1; i < end; i++ {
		raw := lines[i]
		if strings.TrimSpace(raw) == "" || isIndented(raw) {
			// Blank, or an indented continuation of the previous key's value —
			// the folded-multi-line case is caught below via the lookahead on
			// the key's own line, not here.
			continue
		}
		trimmed := strings.TrimSpace(raw)
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		val := strings.TrimSpace(trimmed[idx+1:])
		if key != "name" && key != "description" {
			continue
		}
		multiline := val == "" || val == "|" || val == ">" || strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">")
		if !multiline && i+1 < end {
			next := lines[i+1]
			if isIndented(next) && strings.TrimSpace(next) != "" {
				multiline = true
			}
		}
		if multiline {
			return "", "", fmt.Errorf("%s spans multiple lines (folded/block-scalar values are not supported; use a single-line value)", key)
		}
		values[key] = unquote(val)
	}

	name = values["name"]
	description = values["description"]
	if name == "" {
		return "", "", fmt.Errorf("missing required frontmatter field \"name\"")
	}
	if description == "" {
		return "", "", fmt.Errorf("missing required frontmatter field \"description\"")
	}
	return name, description, nil
}

func isIndented(s string) bool {
	return strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t")
}

// unquote strips one layer of matching single or double quotes, tolerating
// unquoted values (the common case) unchanged.
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
