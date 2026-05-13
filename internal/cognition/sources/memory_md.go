// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// MemoryMDSource samples entries from Claude Code's MEMORY.md files.
// This complements Auto-Memory rather than competing with it — Dream can
// discover insights in MEMORY.md and index them with embeddings for
// semantic retrieval.
type MemoryMDSource struct {
	homeDir     string
	projectRoot string
	rng         *rand.Rand
	observer    *Observer
}

// NewMemoryMDSource creates a new MemoryMDSource.
func NewMemoryMDSource(homeDir, projectRoot string) *MemoryMDSource {
	return &MemoryMDSource{
		homeDir:     homeDir,
		projectRoot: projectRoot,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetObserver wires the source to emit observation.memory_file journal
// entries on each file read. Pass nil to disable.
func (m *MemoryMDSource) SetObserver(o *Observer) { m.observer = o }

// Name returns the source identifier.
func (m *MemoryMDSource) Name() string {
	return "memory-md"
}

// Sample returns random memory entries from MEMORY.md files.
func (m *MemoryMDSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	var items []cognition.DreamItem

	// Look for MEMORY.md files in known locations
	memoryFiles := m.findMemoryFiles()
	if len(memoryFiles) == 0 {
		return nil, nil
	}

	for _, path := range memoryFiles {
		select {
		case <-ctx.Done():
			return items, ctx.Err()
		default:
		}

		entries, err := m.parseMemoryFile(path)
		if err != nil {
			continue
		}
		items = append(items, entries...)
	}

	// Shuffle and limit to n
	m.rng.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})

	if len(items) > n {
		items = items[:n]
	}

	return items, nil
}

// findMemoryFiles locates MEMORY.md files in Claude Code's memory directories.
func (m *MemoryMDSource) findMemoryFiles() []string {
	var files []string

	// Project-level memory
	projectMemory := filepath.Join(m.projectRoot, ".claude", "memory", "MEMORY.md")
	if _, err := os.Stat(projectMemory); err == nil {
		files = append(files, projectMemory)
	}

	// Also check for individual memory files in the memory directory
	memoryDir := filepath.Join(m.projectRoot, ".claude", "memory")
	if entries, err := os.ReadDir(memoryDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "MEMORY.md" {
				files = append(files, filepath.Join(memoryDir, entry.Name()))
			}
		}
	}

	// User-level memory (home directory)
	if m.homeDir != "" {
		homeMemory := filepath.Join(m.homeDir, ".claude", "memory", "MEMORY.md")
		if _, err := os.Stat(homeMemory); err == nil {
			files = append(files, homeMemory)
		}

		homeMemoryDir := filepath.Join(m.homeDir, ".claude", "memory")
		if entries, err := os.ReadDir(homeMemoryDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "MEMORY.md" {
					files = append(files, filepath.Join(homeMemoryDir, entry.Name()))
				}
			}
		}
	}

	return files
}

// parseMemoryFile reads a MEMORY.md or individual memory file and returns DreamItems.
func (m *MemoryMDSource) parseMemoryFile(path string) ([]cognition.DreamItem, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Record the observation: source pointer + content hash, no copy.
	if m.observer != nil {
		var mod time.Time
		if info, err := os.Stat(path); err == nil {
			mod = info.ModTime()
		}
		m.observer.Observe(journal.TypeObservationMemoryFile, m.Name(),
			"file://"+path, content, mod)
	}

	text := string(content)
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	// For individual memory files with frontmatter, treat the whole file as one item
	if strings.HasPrefix(text, "---") {
		return []cognition.DreamItem{
			{
				ID:       "memory:" + filepath.Base(path),
				Source:   "memory-md",
				Content:  text,
				Path:     path,
				Metadata: map[string]any{"file": filepath.Base(path)},
			},
		}, nil
	}

	// For MEMORY.md index files, split by sections (## headers)
	var items []cognition.DreamItem
	sections := strings.Split(text, "\n## ")
	for i, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		if i > 0 {
			section = "## " + section
		}

		items = append(items, cognition.DreamItem{
			ID:       "memory:" + filepath.Base(path) + ":" + strings.Fields(section)[0],
			Source:   "memory-md",
			Content:  section,
			Path:     path,
			Metadata: map[string]any{"section_index": i},
		})
	}

	return items, nil
}
