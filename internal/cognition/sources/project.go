// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
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
	projectRoot string
	rng         *rand.Rand
}

// NewProjectSource creates a new ProjectSource.
func NewProjectSource(projectRoot string) *ProjectSource {
	return &ProjectSource{
		projectRoot: projectRoot,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
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
		"go.mod",
		"package.json",
		"Makefile",
		"Dockerfile",
		".env.example",
	}

	for _, pf := range priorityFiles {
		fullPath := filepath.Join(p.projectRoot, pf)
		if _, err := os.Stat(fullPath); err == nil {
			files = append(files, fullPath)
		}
	}

	// Walk to find more files
	err := filepath.WalkDir(p.projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Skip directories
		if d.IsDir() {
			// Skip hidden directories and common exclusions
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip large/binary files
		if p.shouldSkip(path) {
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

// shouldSkip returns true for files that shouldn't be sampled.
func (p *ProjectSource) shouldSkip(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))

	// Skip binary and generated files
	skipExts := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
		".pdf": true, ".zip": true, ".tar": true, ".gz": true,
		".lock": true, ".sum": true,
		".min.js": true, ".min.css": true,
	}

	if skipExts[ext] {
		return true
	}

	// Skip hidden files
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") && base != ".env.example" {
		return true
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
