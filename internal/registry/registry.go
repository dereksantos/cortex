// Package registry implements the Phase 3 project registry (GOAL.md §3
// P3, docs/cortex-web.md D5): a pointer-only, rebuildable-from-scan list of
// named projects backed by a plain JSON file (projects.json) under the
// resolved user home (internal/userhome). The registry holds pointers
// only — per-project state (sessions, journal, config) stays inside each
// project's own .cortex/ directory, so the registry file is trivially
// rebuildable from a scan and never needs to be a journal class.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dereksantos/cortex/internal/userhome"
)

// Project is one registered project: a name pointing at a root path, plus
// optional bookkeeping (the last session id run against it, free-form
// notes).
type Project struct {
	Name        string `json:"name"`
	Root        string `json:"root"`
	LastSession string `json:"last_session,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// ErrProjectNotFound is returned by Lookup and Remove when no project with
// the given name is registered — including when projects.json does not
// exist yet (an empty registry has no names).
var ErrProjectNotFound = errors.New("project not registered")

// Registry is the CRUD seam over the project registry (docs/cortex-web.md:
// "Registry is an interface (lookup/list/save)"). FileRegistry is the
// file-backed implementation; tests may fake it.
type Registry interface {
	List() ([]Project, error)
	Lookup(name string) (Project, error)
	Save(p Project) error
	Remove(name string) error
}

// FileRegistry is the plain-JSON-file-backed Registry implementation,
// rooted at projects.json under the resolved user home.
type FileRegistry struct {
	path string
}

// New constructs a FileRegistry backed by projects.json under the resolved
// user home directory (internal/userhome.Path) — redirect it in tests via
// $CORTEX_HOME.
func New() (*FileRegistry, error) {
	path, err := userhome.Path("projects.json")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve projects.json path: %w", err)
	}
	return &FileRegistry{path: path}, nil
}

// NewAt constructs a FileRegistry backed by an explicit file path,
// bypassing internal/userhome — for callers (and tests) that need a
// registry outside the resolved user home.
func NewAt(path string) *FileRegistry {
	return &FileRegistry{path: path}
}

// List returns every registered project, sorted by name. An empty or
// not-yet-created registry returns an empty (nil) slice, not an error.
func (r *FileRegistry) List() ([]Project, error) {
	projects, err := r.readAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// Lookup returns the project registered under name, or ErrProjectNotFound
// if none exists (including when the registry file doesn't exist yet).
func (r *FileRegistry) Lookup(name string) (Project, error) {
	projects, err := r.readAll()
	if err != nil {
		return Project{}, err
	}
	for _, p := range projects {
		if p.Name == name {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, name)
}

// Save adds a new project, or overwrites the existing entry with the same
// name in place (update, not append).
func (r *FileRegistry) Save(p Project) error {
	projects, err := r.readAll()
	if err != nil {
		return err
	}
	for i, existing := range projects {
		if existing.Name == p.Name {
			projects[i] = p
			return r.writeAll(projects)
		}
	}
	projects = append(projects, p)
	return r.writeAll(projects)
}

// Remove deletes the project registered under name, or returns
// ErrProjectNotFound if none exists.
func (r *FileRegistry) Remove(name string) error {
	projects, err := r.readAll()
	if err != nil {
		return err
	}
	for i, p := range projects {
		if p.Name == name {
			projects = append(projects[:i], projects[i+1:]...)
			return r.writeAll(projects)
		}
	}
	return fmt.Errorf("%w: %s", ErrProjectNotFound, name)
}

// readAll loads the full project list from disk. A missing or empty file
// is treated as an empty registry, not an error.
func (r *FileRegistry) readAll() ([]Project, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", r.path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", r.path, err)
	}
	return projects, nil
}

// writeAll persists the full project list to disk, creating the parent
// directory if needed.
func (r *FileRegistry) writeAll(projects []Project) error {
	data, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal projects: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", r.path, err)
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", r.path, err)
	}
	return nil
}
