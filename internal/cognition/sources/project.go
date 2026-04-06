// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"bufio"
	"context"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// ProjectSource samples files from the project directory.
type ProjectSource struct {
	projectRoot    string
	rng            *rand.Rand
	gitignoreRules []gitignoreRule
}

// gitignoreRule represents a single .gitignore pattern.
type gitignoreRule struct {
	pattern  string
	negation bool // lines starting with !
	dirOnly  bool // lines ending with /
}

// NewProjectSource creates a new ProjectSource.
func NewProjectSource(projectRoot string) *ProjectSource {
	ps := &ProjectSource{
		projectRoot: projectRoot,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	ps.gitignoreRules = ps.loadGitignore()
	return ps
}

// Name returns the source identifier.
func (p *ProjectSource) Name() string {
	return "project"
}

// Sample returns random files from the project.
func (p *ProjectSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	var files []string

	// Priority files (always include if they exist)
	priorityFiles := []string{
		"README.md",
		"CLAUDE.md",
		"LICENSE",
		"go.mod",
		"package.json",
		"Makefile",
		"Dockerfile",
		".env.example",
		"CONTRIBUTING.md",
		"CHANGELOG.md",
	}

	for _, pf := range priorityFiles {
		fullPath := filepath.Join(p.projectRoot, pf)
		if _, err := os.Stat(fullPath); err == nil {
			if !p.isExcluded(fullPath, false) {
				files = append(files, fullPath)
			}
		}
	}

	// Walk to find more files
	err := filepath.WalkDir(p.projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if d.IsDir() {
			name := d.Name()
			// Hard-excluded directories (never enter these)
			if p.isHardExcludedDir(name) {
				return filepath.SkipDir
			}
			// Check gitignore for directories
			if p.isGitignored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if p.isExcluded(path, false) {
			return nil
		}

		files = append(files, path)

		// Limit total files scanned
		if len(files) > 500 {
			return filepath.SkipAll
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Randomly sample n files
	if len(files) > n {
		p.rng.Shuffle(len(files), func(i, j int) {
			files[i], files[j] = files[j], files[i]
		})
		files = files[:n]
	}

	// Convert to DreamItems
	items := make([]cognition.DreamItem, 0, len(files))
	for _, path := range files {
		content, err := p.readFile(path)
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(p.projectRoot, path)

		items = append(items, cognition.DreamItem{
			ID:      "project:" + relPath,
			Source:  "project",
			Content: content,
			Path:    relPath,
			Metadata: map[string]any{
				"full_path": path,
				"ext":       filepath.Ext(path),
			},
		})
	}

	return items, nil
}

// isExcluded returns true if a file should never be sampled.
// Checks hard exclusions first, then gitignore rules.
func (p *ProjectSource) isExcluded(path string, isDir bool) bool {
	if p.isHardExcludedFile(path) {
		return true
	}
	if p.shouldSkipByExtension(path) {
		return true
	}
	return p.isGitignored(path, isDir)
}

// isHardExcludedDir returns true for directories that must never be entered.
func (p *ProjectSource) isHardExcludedDir(name string) bool {
	// Git internals
	if name == ".git" {
		return true
	}

	// Dependencies
	hardExcludedDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".venv":        true,
		"venv":         true,
		"__pycache__":  true,
		".mypy_cache":  true,
		".pytest_cache": true,

		// Build artifacts
		"dist":    true,
		"build":   true,
		"target":  true,
		".next":   true,
		".nuxt":   true,
		".output": true,

		// IDE/editor
		".idea": true,

		// Cortex's own state (but NOT knowledge/)
		// Handled separately in isHardExcludedFile for finer control
	}

	return hardExcludedDirs[name]
}

// isHardExcludedFile returns true for files that must never be sampled,
// regardless of gitignore. These are security-sensitive or noise files.
func (p *ProjectSource) isHardExcludedFile(path string) bool {
	rel, _ := filepath.Rel(p.projectRoot, path)
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	// === SECRETS / CREDENTIALS ===
	// .env files (but allow .env.example, .env.sample, .env.template)
	if strings.HasPrefix(lower, ".env") {
		if lower == ".env.example" || lower == ".env.sample" || lower == ".env.template" {
			return false
		}
		return true
	}

	// Key/certificate files
	secretExts := map[string]bool{
		".key": true, ".pem": true, ".p12": true, ".pfx": true,
		".keystore": true, ".jks": true, ".p8": true,
	}
	if secretExts[strings.ToLower(filepath.Ext(path))] {
		return true
	}

	// Named secret files
	secretFiles := map[string]bool{
		"id_rsa": true, "id_ed25519": true, "id_ecdsa": true, "id_dsa": true,
		"credentials.json": true, "service-account.json": true,
		"secrets.yaml": true, "secrets.yml": true, "secrets.json": true,
		".npmrc": true, ".pypirc": true, ".netrc": true,
		".docker/config.json": true,
		"htpasswd": true, ".htpasswd": true,
	}
	if secretFiles[lower] || secretFiles[rel] {
		return true
	}

	// === CORTEX OWN STATE ===
	// Never read our own queue, db, logs, or runtime state
	if strings.HasPrefix(rel, ".cortex/") || strings.HasPrefix(rel, ".cortex\\") {
		// Allow knowledge/ (that's committed team context)
		if strings.HasPrefix(rel, ".cortex/knowledge/") || strings.HasPrefix(rel, ".cortex\\knowledge\\") {
			return false
		}
		return true
	}

	// === OS JUNK ===
	osJunk := map[string]bool{
		".ds_store": true, "thumbs.db": true, "desktop.ini": true,
	}
	if osJunk[lower] {
		return true
	}

	return false
}

// shouldSkipByExtension returns true for binary, generated, and low-signal files.
func (p *ProjectSource) shouldSkipByExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))

	skipExts := map[string]bool{
		// Binary/compiled
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".o": true, ".obj": true, ".a": true, ".lib": true,
		".class": true, ".pyc": true, ".pyo": true,

		// Images/media
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".ico": true, ".svg": true, ".webp": true, ".bmp": true,
		".mp3": true, ".mp4": true, ".wav": true, ".avi": true,
		".mov": true, ".webm": true, ".ttf": true, ".woff": true,
		".woff2": true, ".eot": true, ".otf": true,

		// Archives
		".zip": true, ".tar": true, ".gz": true, ".bz2": true,
		".rar": true, ".7z": true, ".xz": true,

		// Documents (low signal for code context)
		".pdf": true, ".doc": true, ".docx": true, ".xls": true,
		".xlsx": true, ".pptx": true,

		// Lock files (huge, low signal)
		".lock": true, ".sum": true,

		// Minified
		".min.js": true, ".min.css": true,

		// Database files
		".db": true, ".sqlite": true, ".sqlite3": true,

		// Map files
		".map": true,
	}

	if skipExts[ext] {
		return true
	}

	// Check compound extensions
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}

	// Lock files by name
	lockFiles := map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
		"go.sum": true, "gemfile.lock": true, "poetry.lock": true,
		"composer.lock": true, "cargo.lock": true, "flake.lock": true,
	}
	if lockFiles[strings.ToLower(base)] {
		return true
	}

	return false
}

// loadGitignore parses .gitignore from the project root.
func (p *ProjectSource) loadGitignore() []gitignoreRule {
	gitignorePath := filepath.Join(p.projectRoot, ".gitignore")
	f, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var rules []gitignoreRule
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule := gitignoreRule{}

		// Negation
		if strings.HasPrefix(line, "!") {
			rule.negation = true
			line = line[1:]
		}

		// Directory-only pattern
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		rule.pattern = line
		rules = append(rules, rule)
	}

	return rules
}

// isGitignored checks if a path matches any gitignore rule.
func (p *ProjectSource) isGitignored(path string, isDir bool) bool {
	if len(p.gitignoreRules) == 0 {
		return false
	}

	rel, err := filepath.Rel(p.projectRoot, path)
	if err != nil {
		return false
	}

	// Normalize to forward slashes for matching
	rel = filepath.ToSlash(rel)

	ignored := false
	for _, rule := range p.gitignoreRules {
		if rule.dirOnly && !isDir {
			continue
		}

		if matchGitignore(rule.pattern, rel) {
			if rule.negation {
				ignored = false
			} else {
				ignored = true
			}
		}
	}

	return ignored
}

// matchGitignore performs simplified gitignore pattern matching.
// Supports: *, **, ?, and path-based matching.
func matchGitignore(pattern, path string) bool {
	// If pattern contains no slash, match against basename only
	if !strings.Contains(pattern, "/") {
		base := filepath.Base(path)
		return matchGlob(pattern, base)
	}

	// Pattern with slash — match against full relative path
	pattern = strings.TrimPrefix(pattern, "/")

	// Handle ** (match any number of directories)
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

		// Check if any suffix of path matches the pattern suffix
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

// matchGlob performs simple glob matching with * and ? support.
func matchGlob(pattern, name string) bool {
	// Use filepath.Match for simple glob patterns
	matched, err := filepath.Match(pattern, name)
	if err == nil && matched {
		return true
	}

	// Also check if pattern matches any path component
	// e.g., pattern "build" should match "src/build" and "build/output"
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

// readFile reads a file, truncating if too large.
func (p *ProjectSource) readFile(path string) (string, error) {
	// Check size first
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	// Skip files larger than 100KB
	if info.Size() > 100*1024 {
		return "", nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Truncate content if too long
	s := string(content)
	if len(s) > 5000 {
		s = s[:5000] + "\n...(truncated)"
	}

	return s, nil
}
