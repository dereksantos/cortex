// webui_dashboard.go — M5.2a: the project dashboard view-model (GOAL.md §6
// M5.2, split into M5.2a/b/c/d per STATE.md; GOAL.md §2 names
// cmd/cortex/webui*.go as home for the web UI's Go view-model builders).
// Per GOAL.md §3 P5, rendering logic lives in golden-tested Go view-models
// — this file is pure data assembly, no HTML/JS.
//
// The dashboard is "registry + per-project session list + change status"
// (docs/cortex-web.md Phase 5): every registered project (internal/registry,
// Phase 3), its session listing (reusing serve_routes.go's listSessions +
// sessionSummary — the same seam GET /api/projects/{name}/sessions already
// exposes), and its git change status (change.go's changeStatusFor, the
// same logic `cortex change status` prints, scoped to the project's root
// instead of the process CWD).
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/dereksantos/cortex/internal/registry"
)

// dashboardProject is one registered project's dashboard row.
type dashboardProject struct {
	Name         string           `json:"name"`
	Root         string           `json:"root"`
	Branch       string           `json:"branch"`
	ActiveChange bool             `json:"active_change"`
	Clean        bool             `json:"clean"`
	ChangeError  string           `json:"change_error,omitempty"`
	Sessions     []sessionSummary `json:"sessions"`
}

// dashboardViewModel is the full project dashboard screen.
type dashboardViewModel struct {
	Projects []dashboardProject `json:"projects"`
}

// buildDashboardViewModel assembles the dashboard from the shared registry
// seam, sorted by project name for a deterministic, stable rendering order
// (the registry itself makes no ordering guarantee — FileRegistry.List
// doesn't sort, and fakeRegistry.List iterates a map).
//
// A project whose git change status can't be read (not a git repo, git
// missing, etc.) still renders — with its ChangeError populated rather than
// failing the whole dashboard; a session-listing failure other than "no
// sessions directory yet" is treated as a genuine error and fails the
// build, mirroring handleListProjectSessions' existing os.ErrNotExist
// special-case.
func buildDashboardViewModel(reg registry.Registry) (dashboardViewModel, error) {
	projects, err := reg.List()
	if err != nil {
		return dashboardViewModel{}, fmt.Errorf("failed to list projects: %w", err)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })

	vm := dashboardViewModel{Projects: make([]dashboardProject, 0, len(projects))}
	for _, p := range projects {
		dp := dashboardProject{Name: p.Name, Root: p.Root, Sessions: []sessionSummary{}}

		ws, err := NewWorkspace(p.Root)
		if err != nil {
			return dashboardViewModel{}, fmt.Errorf("failed to resolve workspace for project %q: %w", p.Name, err)
		}
		infos, err := listSessions(ws.SessionsDir(), projectSessionsLimit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return dashboardViewModel{}, fmt.Errorf("failed to list sessions for project %q: %w", p.Name, err)
		}
		for _, info := range infos {
			s := sessionSummary(info)
			// Normalize to UTC: os.Stat's ModTime is returned in the host's
			// local zone, which would make the JSON wire shape (and this
			// package's own golden test) depend on the server machine's
			// timezone otherwise.
			s.ModTime = s.ModTime.UTC()
			dp.Sessions = append(dp.Sessions, s)
		}

		branch, active, clean, err := changeStatusFor(p.Root)
		if err != nil {
			dp.ChangeError = err.Error()
		} else {
			dp.Branch = branch
			dp.ActiveChange = active
			dp.Clean = clean
		}

		vm.Projects = append(vm.Projects, dp)
	}
	return vm, nil
}
