// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// GitSource samples from git history (commits, diffs, blame).
type GitSource struct {
	projectRoot string
	rng         *rand.Rand
}

// NewGitSource creates a new GitSource.
func NewGitSource(projectRoot string) *GitSource {
	return &GitSource{
		projectRoot: projectRoot,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Name returns the source identifier.
func (g *GitSource) Name() string {
	return "git"
}

// Sample returns random items from git history.
func (g *GitSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	// Check if this is a git repo
	if !g.isGitRepo() {
		return nil, nil // Not a git repo, return empty
	}

	var items []cognition.DreamItem

	// Sample commits, diffs, and file history in proportions
	commitCount := n / 2
	if commitCount < 1 {
		commitCount = 1
	}
	diffCount := n - commitCount

	// Get recent commits with messages
	commits, err := g.sampleCommits(ctx, commitCount)
	if err == nil {
		items = append(items, commits...)
	}

	// Get recent diffs (changes)
	diffs, err := g.sampleDiffs(ctx, diffCount)
	if err == nil {
		items = append(items, diffs...)
	}

	// Shuffle results
	g.rng.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})

	// Limit to n items
	if len(items) > n {
		items = items[:n]
	}

	return items, nil
}

// isGitRepo checks if the project root is a git repository.
func (g *GitSource) isGitRepo() bool {
	cmd := exec.Command("git", "-C", g.projectRoot, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// sampleCommits retrieves recent commit messages for analysis.
func (g *GitSource) sampleCommits(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	// Get recent commits with full messages
	// Format: hash|author|date|subject|body
	cmd := exec.CommandContext(ctx, "git", "-C", g.projectRoot,
		"log", "--format=%H|%an|%ai|%s|%b", "-n", fmt.Sprintf("%d", n*3), // Get extra to filter
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var items []cognition.DreamItem
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var currentCommit strings.Builder
	var currentHash, currentAuthor, currentDate, currentSubject string

	for scanner.Scan() {
		line := scanner.Text()

		// Check if this is a new commit (starts with hash pattern)
		parts := strings.SplitN(line, "|", 5)
		if len(parts) >= 4 && len(parts[0]) == 40 && isHex(parts[0]) {
			// Save previous commit if exists
			if currentHash != "" && currentCommit.Len() > 0 {
				items = append(items, g.makeCommitItem(currentHash, currentAuthor, currentDate, currentSubject, currentCommit.String()))
			}

			currentHash = parts[0]
			currentAuthor = parts[1]
			currentDate = parts[2]
			currentSubject = parts[3]
			currentCommit.Reset()
			if len(parts) == 5 {
				currentCommit.WriteString(parts[4])
			}
		} else if currentHash != "" {
			// Continuation of commit body
			currentCommit.WriteString("\n")
			currentCommit.WriteString(line)
		}
	}

	// Don't forget the last commit
	if currentHash != "" {
		items = append(items, g.makeCommitItem(currentHash, currentAuthor, currentDate, currentSubject, currentCommit.String()))
	}

	// Filter to commits with meaningful messages
	var filtered []cognition.DreamItem
	for _, item := range items {
		// Skip merge commits and trivial messages
		subject := item.Metadata["subject"].(string)
		if strings.HasPrefix(strings.ToLower(subject), "merge ") {
			continue
		}
		// Keep commits with substantive messages
		if len(subject)+len(item.Content) > 20 {
			filtered = append(filtered, item)
		}
	}

	// Limit results
	if len(filtered) > n {
		filtered = filtered[:n]
	}

	return filtered, nil
}

// makeCommitItem creates a DreamItem from commit data.
func (g *GitSource) makeCommitItem(hash, author, date, subject, body string) cognition.DreamItem {
	content := fmt.Sprintf("Commit: %s\nAuthor: %s\nDate: %s\n\n%s\n\n%s",
		hash[:8], author, date, subject, strings.TrimSpace(body))

	// Detect type of commit
	commitType := classifyCommit(subject)

	return cognition.DreamItem{
		ID:      "git:commit:" + hash[:8],
		Source:  "git",
		Content: content,
		Path:    hash[:8],
		Metadata: map[string]any{
			"type":        "commit",
			"commit_type": commitType,
			"hash":        hash,
			"author":      author,
			"date":        date,
			"subject":     subject,
		},
	}
}

// sampleDiffs retrieves recent file changes.
func (g *GitSource) sampleDiffs(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	// Get list of recently changed files
	cmd := exec.CommandContext(ctx, "git", "-C", g.projectRoot,
		"log", "--name-only", "--format=%H", "-n", "20",
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Parse output to get commit-file pairs
	type fileChange struct {
		commit string
		file   string
	}
	var changes []fileChange
	var currentCommit string

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(line) == 40 && isHex(line) {
			currentCommit = line
		} else if currentCommit != "" && line != "" {
			changes = append(changes, fileChange{commit: currentCommit, file: line})
		}
	}

	// Randomly select some changes
	if len(changes) > n*2 {
		g.rng.Shuffle(len(changes), func(i, j int) {
			changes[i], changes[j] = changes[j], changes[i]
		})
		changes = changes[:n*2]
	}

	// Get diffs for selected changes
	var items []cognition.DreamItem
	for _, change := range changes {
		if len(items) >= n {
			break
		}

		// Skip sensitive file paths in diffs
		if isSensitivePath(change.file) {
			continue
		}

		diff, err := g.getDiff(ctx, change.commit, change.file)
		if err != nil || diff == "" {
			continue
		}

		// Truncate large diffs
		if len(diff) > 3000 {
			diff = diff[:3000] + "\n...(truncated)"
		}

		items = append(items, cognition.DreamItem{
			ID:      fmt.Sprintf("git:diff:%s:%s", change.commit[:8], filepath.Base(change.file)),
			Source:  "git",
			Content: diff,
			Path:    change.file,
			Metadata: map[string]any{
				"type":   "diff",
				"commit": change.commit[:8],
				"file":   change.file,
			},
		})
	}

	return items, nil
}

// getDiff retrieves the diff for a specific file in a commit.
func (g *GitSource) getDiff(ctx context.Context, commit, file string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", g.projectRoot,
		"show", "--format=", commit, "--", file,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// isHex checks if a string contains only hex characters.
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isSensitivePath returns true for file paths that should never be analyzed.
// Used by both ProjectSource and GitSource to filter out secrets and noise.
func isSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))

	// .env files (not .env.example/.env.sample/.env.template)
	if strings.HasPrefix(base, ".env") {
		return base != ".env.example" && base != ".env.sample" && base != ".env.template"
	}

	// Key/cert files
	sensitiveExts := map[string]bool{
		".key": true, ".pem": true, ".p12": true, ".pfx": true,
		".keystore": true, ".jks": true, ".p8": true,
	}
	if sensitiveExts[ext] {
		return true
	}

	// Named secret files
	sensitiveFiles := map[string]bool{
		"id_rsa": true, "id_ed25519": true, "id_ecdsa": true,
		"credentials.json": true, "service-account.json": true,
		"secrets.yaml": true, "secrets.yml": true, "secrets.json": true,
		".npmrc": true, ".pypirc": true, ".netrc": true, "htpasswd": true,
	}
	if sensitiveFiles[base] {
		return true
	}

	// Cortex queue/state files
	lower := strings.ToLower(path)
	if strings.Contains(lower, ".cortex/queue/") || strings.Contains(lower, ".cortex/db/") ||
		strings.Contains(lower, ".cortex/logs/") {
		return true
	}

	return false
}

// classifyCommit determines the type of commit from its subject.
func classifyCommit(subject string) string {
	lower := strings.ToLower(subject)

	// Conventional commit prefixes
	prefixes := map[string]string{
		"feat":     "feature",
		"fix":      "bugfix",
		"docs":     "documentation",
		"style":    "style",
		"refactor": "refactor",
		"perf":     "performance",
		"test":     "test",
		"build":    "build",
		"ci":       "ci",
		"chore":    "chore",
	}

	for prefix, commitType := range prefixes {
		if strings.HasPrefix(lower, prefix+":") || strings.HasPrefix(lower, prefix+"(") {
			return commitType
		}
	}

	// Pattern matching for non-conventional commits
	patterns := map[string][]string{
		"feature":       {"add", "implement", "create", "new"},
		"bugfix":        {"fix", "bug", "issue", "resolve", "patch"},
		"refactor":      {"refactor", "rename", "move", "restructure", "cleanup"},
		"documentation": {"doc", "readme", "comment"},
		"test":          {"test", "spec"},
	}

	for commitType, keywords := range patterns {
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return commitType
			}
		}
	}

	return "other"
}
