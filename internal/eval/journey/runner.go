// Package journey loads the 10 e2e scenarios under test/evals/
// journeys/ and reports per-scenario runnability status.
//
// Full execution (drive a coding agent through the multi-session
// scaffold and score against the expected behavior) requires a
// harness adapter that bridges journey YAML → v2 coding harness; that
// adapter is the bulk of Phase D's deferred work.
//
// This package implements the audit + validation step: load each
// scenario, verify its scaffold + structure, and emit a structured
// per-scenario status (`runnable` / `pending_adapter` / `invalid`).
// Phase 5's DAG executor will eventually subsume this when the
// journey-as-DAG mapping lands; until then, this surface gives a
// reliable per-scenario status without faking telemetry.
package journey

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Scenario mirrors the test/evals/journeys/*.yaml shape (the subset
// this loader consumes — unknown fields ignored).
type Scenario struct {
	ID          string    `yaml:"id"`
	Type        string    `yaml:"type"` // expect "e2e"
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Project     Project   `yaml:"project"`
	Sessions    []Session `yaml:"sessions"`
}

// Project describes the scaffold the journey operates on.
type Project struct {
	Name     string `yaml:"name"`
	Scaffold string `yaml:"scaffold"` // path relative to repo root
	Language string `yaml:"language"`
}

// Session is one phase of the multi-session journey.
type Session struct {
	ID      string  `yaml:"id"`
	Phase   string  `yaml:"phase"`
	Context string  `yaml:"context"`
	Events  []Event `yaml:"events"`
}

// Event is a captured decision / pattern / etc. authored into the
// scenario as the starting context for that session.
type Event struct {
	Type       string   `yaml:"type"`
	ID         string   `yaml:"id"`
	Content    string   `yaml:"content"`
	Rationale  string   `yaml:"rationale,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
	Importance int      `yaml:"importance,omitempty"`
}

// ScenarioStatus is the runnability verdict for one scenario.
type ScenarioStatus struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Status         string `json:"status"` // pending_adapter | scaffold_missing | invalid
	ScaffoldPath   string `json:"scaffold_path,omitempty"`
	ScaffoldExists bool   `json:"scaffold_exists"`
	SessionCount   int    `json:"session_count"`
	EventCount     int    `json:"event_count"`
	Message        string `json:"message,omitempty"`
}

// SuiteResult aggregates per-scenario statuses.
type SuiteResult struct {
	Suite          string           `json:"suite"`
	Total          int              `json:"total"`
	PendingAdapter int              `json:"pending_adapter"`
	ScaffoldMissing int             `json:"scaffold_missing"`
	Invalid        int              `json:"invalid"`
	Scenarios      []ScenarioStatus `json:"scenarios"`
}

// RunSuite loads every *.yaml under dir and returns per-scenario
// runnability statuses. Does not actually execute any agent runs —
// that needs the harness adapter (deferred follow-up).
func RunSuite(dir string) (*SuiteResult, error) {
	pattern := filepath.Join(dir, "*.yaml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no journey scenarios in %s", dir)
	}
	sort.Strings(matches)

	suite := &SuiteResult{Suite: "journeys", Total: len(matches)}

	for _, path := range matches {
		st := ScenarioStatus{
			ID:   filepath.Base(path),
			Path: path,
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			st.Status = "invalid"
			st.Message = fmt.Sprintf("read failed: %v", rerr)
			suite.Invalid++
			suite.Scenarios = append(suite.Scenarios, st)
			continue
		}
		var s Scenario
		if uerr := yaml.Unmarshal(data, &s); uerr != nil {
			st.Status = "invalid"
			st.Message = fmt.Sprintf("parse failed: %v", uerr)
			suite.Invalid++
			suite.Scenarios = append(suite.Scenarios, st)
			continue
		}

		st.ID = s.ID
		st.Name = s.Name
		st.SessionCount = len(s.Sessions)
		for _, sess := range s.Sessions {
			st.EventCount += len(sess.Events)
		}
		st.ScaffoldPath = s.Project.Scaffold

		// Validate scaffold exists (relative to repo root).
		if s.Project.Scaffold == "" {
			st.Status = "invalid"
			st.Message = "project.scaffold not set"
			suite.Invalid++
		} else if _, ferr := os.Stat(s.Project.Scaffold); ferr != nil {
			st.ScaffoldExists = false
			st.Status = "scaffold_missing"
			st.Message = fmt.Sprintf("scaffold dir not found: %s", s.Project.Scaffold)
			suite.ScaffoldMissing++
		} else {
			st.ScaffoldExists = true
			st.Status = "pending_adapter"
			st.Message = "scenario parses + scaffold present; awaiting v2 harness adapter for full execution"
			suite.PendingAdapter++
		}

		suite.Scenarios = append(suite.Scenarios, st)
	}

	return suite, nil
}
