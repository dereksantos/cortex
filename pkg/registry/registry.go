// Package registry manages the global project registry at ~/.cortex/projects.json.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProjectEntry represents a registered project.
type ProjectEntry struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	GitRemote    string    `json:"git_remote,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
	LastActive   time.Time `json:"last_active"`
}

// Registry manages the set of registered projects.
type Registry struct {
	path     string // path to projects.json
	projects []ProjectEntry
}

// GlobalDir returns the default global Cortex directory (~/.cortex/).
func GlobalDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".cortex")
	}
	return filepath.Join(home, ".cortex")
}

// EnsureGlobalDir creates ~/.cortex/ and required subdirectories if they don't exist.
func EnsureGlobalDir() (string, error) {
	dir := GlobalDir()
	dirs := []string{
		dir,
		filepath.Join(dir, "data"),
		filepath.Join(dir, "sessions"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return "", fmt.Errorf("failed to create %s: %w", d, err)
		}
	}
	return dir, nil
}

// Open loads the registry from ~/.cortex/projects.json.
// Creates the file if it doesn't exist.
func Open() (*Registry, error) {
	dir, err := EnsureGlobalDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "projects.json")
	return OpenAt(path)
}

// OpenAt loads the registry from a specific path.
func OpenAt(path string) (*Registry, error) {
	r := &Registry{path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			r.projects = []ProjectEntry{}
			return r, nil
		}
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	if err := json.Unmarshal(data, &r.projects); err != nil {
		return nil, fmt.Errorf("failed to parse registry: %w", err)
	}
	return r, nil
}

// Register adds a project to the registry. If the project path is already
// registered, it updates the entry and returns the existing ID.
func (r *Registry) Register(projectPath string) (*ProjectEntry, error) {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	// Check if already registered
	for i, p := range r.projects {
		if p.Path == absPath {
			r.projects[i].LastActive = time.Now()
			if err := r.save(); err != nil {
				return nil, err
			}
			return &r.projects[i], nil
		}
	}

	entry := ProjectEntry{
		ID:           slugFromPath(absPath),
		Path:         absPath,
		Name:         filepath.Base(absPath),
		GitRemote:    detectGitRemote(absPath),
		RegisteredAt: time.Now(),
		LastActive:   time.Now(),
	}

	// Ensure slug uniqueness
	entry.ID = r.uniqueSlug(entry.ID)

	r.projects = append(r.projects, entry)
	if err := r.save(); err != nil {
		return nil, err
	}
	return &r.projects[len(r.projects)-1], nil
}

// Unregister removes a project by ID.
func (r *Registry) Unregister(id string) error {
	for i, p := range r.projects {
		if p.ID == id {
			r.projects = append(r.projects[:i], r.projects[i+1:]...)
			return r.save()
		}
	}
	return fmt.Errorf("project %q not found", id)
}

// List returns all registered projects.
func (r *Registry) List() []ProjectEntry {
	result := make([]ProjectEntry, len(r.projects))
	copy(result, r.projects)
	return result
}

// FindByPath returns the project entry matching the given path, or nil.
// Matches if the path equals or is a subdirectory of a registered project.
func (r *Registry) FindByPath(path string) *ProjectEntry {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	// Exact match first
	for i, p := range r.projects {
		if p.Path == absPath {
			return &r.projects[i]
		}
	}
	// Subdirectory match (longest prefix wins)
	var best *ProjectEntry
	bestLen := 0
	for i, p := range r.projects {
		if strings.HasPrefix(absPath, p.Path+string(filepath.Separator)) && len(p.Path) > bestLen {
			best = &r.projects[i]
			bestLen = len(p.Path)
		}
	}
	return best
}

// FindByID returns the project entry with the given ID, or nil.
func (r *Registry) FindByID(id string) *ProjectEntry {
	for i, p := range r.projects {
		if p.ID == id {
			return &r.projects[i]
		}
	}
	return nil
}

// UpdateLastActive updates the last_active timestamp for a project.
func (r *Registry) UpdateLastActive(id string) error {
	for i, p := range r.projects {
		if p.ID == id {
			r.projects[i].LastActive = time.Now()
			return r.save()
		}
	}
	return fmt.Errorf("project %q not found", id)
}

func (r *Registry) save() error {
	data, err := json.MarshalIndent(r.projects, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}
	// Atomic write
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write registry: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to rename registry: %w", err)
	}
	return nil
}

// slugFromPath creates a URL-safe slug from the last component of a path.
func slugFromPath(absPath string) string {
	base := filepath.Base(absPath)
	slug := strings.ToLower(base)
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r == '_' || r == ' ' || r == '.' {
			return '-'
		}
		return -1
	}, slug)
	// Trim leading/trailing dashes
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "project"
	}
	return slug
}

// uniqueSlug ensures the slug doesn't collide with existing projects.
func (r *Registry) uniqueSlug(slug string) string {
	exists := func(s string) bool {
		for _, p := range r.projects {
			if p.ID == s {
				return true
			}
		}
		return false
	}
	if !exists(slug) {
		return slug
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", slug, i)
		if !exists(candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", slug, time.Now().UnixNano())
}

// detectGitRemote tries to find the git remote origin URL for a path.
func detectGitRemote(path string) string {
	cmd := exec.Command("git", "-C", path, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
