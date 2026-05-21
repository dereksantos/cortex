package projectscan

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// GitignoreRule represents a single .gitignore pattern.
type GitignoreRule struct {
	Pattern  string
	Negation bool // lines starting with !
	DirOnly  bool // lines ending with /
}

// loadGitignore parses .gitignore from the given root. Missing file
// returns nil rules (callers treat as "no rules"). Lines starting with
// # or empty lines are ignored.
func loadGitignore(root string) []GitignoreRule {
	path := filepath.Join(root, ".gitignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var rules []GitignoreRule
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule := GitignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.Negation = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.DirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		rule.Pattern = line
		rules = append(rules, rule)
	}
	return rules
}

// IsGitignored returns true if absPath matches any rule in the set.
// isDir lets DirOnly rules apply selectively.
func (s *IgnoreSet) IsGitignored(absPath string, isDir bool) bool {
	if len(s.GitignoreRules) == 0 {
		return false
	}
	rel, err := filepath.Rel(s.Root, absPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)

	ignored := false
	for _, rule := range s.GitignoreRules {
		if rule.DirOnly && !isDir {
			continue
		}
		if matchGitignore(rule.Pattern, rel) {
			if rule.Negation {
				ignored = false
			} else {
				ignored = true
			}
		}
	}
	return ignored
}

// matchGitignore performs simplified gitignore pattern matching.
// Supports: *, **, ?, path-based matching, and basename-only matching
// when the pattern contains no /.
func matchGitignore(pattern, path string) bool {
	if !strings.Contains(pattern, "/") {
		base := filepath.Base(path)
		return matchGlob(pattern, base)
	}

	pattern = strings.TrimPrefix(pattern, "/")

	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimSuffix(parts[0], "/")
		suffix := strings.TrimPrefix(parts[1], "/")

		if prefix == "" && suffix == "" {
			return true
		}
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			return false
		}
		if suffix == "" {
			return true
		}

		pathParts := strings.Split(path, "/")
		for i := range pathParts {
			subpath := strings.Join(pathParts[i:], "/")
			if matchGlob(suffix, subpath) {
				return true
			}
		}
		return false
	}

	return matchGlob(pattern, path)
}

// matchGlob performs simple glob matching with * and ? support, plus
// path-component matching when pattern has no /.
func matchGlob(pattern, name string) bool {
	matched, err := filepath.Match(pattern, name)
	if err == nil && matched {
		return true
	}
	if !strings.Contains(pattern, "/") {
		parts := strings.Split(name, "/")
		for _, part := range parts {
			if m, _ := filepath.Match(pattern, part); m {
				return true
			}
		}
	}
	return false
}
