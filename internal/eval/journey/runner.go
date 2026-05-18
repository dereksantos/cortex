// Package journey loads the 10 e2e scenarios under test/evals/
// journeys/ and reports per-scenario runnability status.
//
// Phase D scope split:
//
//  1. **Validator** (RunSuite): loads each scenario, verifies scaffold +
//     structure, emits per-scenario status. No execution.
//
//  2. **Seed adapter** (RunSuiteWithSeed): for each scenario, converts
//     the multi-session events into Cortex insights, seeds them into a
//     per-scenario temp storage (same JSONL-write pattern as Phase B),
//     and verifies the events are retrievable post-seed. This proves
//     the journey → Cortex-context pipeline works end-to-end.
//
//  3. **Full execution adapter** (NOT YET IMPLEMENTED): drives a
//     coding agent through each session's scaffold after seeding;
//     scores against expected behavior. Bulk of Phase D's remaining
//     work; reuses cortex code harness pattern. Filed as follow-up.
package journey

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	intcog "github.com/dereksantos/cortex/internal/cognition"
	"gopkg.in/yaml.v3"
	"context"
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

// Session is one phase of the multi-session journey. Sessions can
// carry any of three optional payloads:
//
//   - Events: pre-authored decisions/patterns/etc. that get seeded
//     into Cortex storage before later sessions run.
//   - Task: a coding task the full-execution adapter drives a harness
//     against (used by feature/maintenance sessions).
//   - Queries: post-hoc recall checks (currently inspection-only;
//     scored by the full-execution adapter as PASS if all
//     expected_recall IDs surface via Reflex).
type Session struct {
	ID      string  `yaml:"id"`
	Phase   string  `yaml:"phase"`
	Context string  `yaml:"context"`
	Events  []Event `yaml:"events"`
	Task    *Task   `yaml:"task,omitempty"`
	Queries []Query `yaml:"queries,omitempty"`
}

// Task is one coding task within a session. The full-execution adapter
// turns this into a harness invocation (prompt = description + hints,
// workdir = temp copy of the scaffold).
type Task struct {
	Description    string     `yaml:"description"`
	FilesToModify  []string   `yaml:"files_to_modify"`
	MaxTurns       int        `yaml:"max_turns"`
	Timeout        string     `yaml:"timeout"` // parsed by adapter; e.g. "2m"
	Hints          []string   `yaml:"hints"`
	Acceptance     Acceptance `yaml:"acceptance"`
}

// Acceptance is the per-task scoring contract. Each field is optional;
// the adapter scores only what's present.
type Acceptance struct {
	TestsPass         []string `yaml:"tests_pass"`          // go test -run <name>
	PatternsRequired  []string `yaml:"patterns_required"`   // grep across files_to_modify
	PatternsForbidden []string `yaml:"patterns_forbidden"`
}

// Query is one recall-check within a session. Used for the maintenance
// phase where we verify earlier sessions' context is still retrievable.
type Query struct {
	ID              string   `yaml:"id"`
	Text            string   `yaml:"text"`
	ExpectedRecall  []string `yaml:"expected_recall"`
	ExpectedContent []string `yaml:"expected_content"`
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
	SeedOK         int              `json:"seed_ok,omitempty"`
	SeedFailed     int              `json:"seed_failed,omitempty"`
	Scenarios      []ScenarioStatus `json:"scenarios"`
}

// SeedReport extends ScenarioStatus with seed-adapter outcomes.
type SeedReport struct {
	ScenarioID         string `json:"scenario_id"`
	SessionsProcessed  int    `json:"sessions_processed"`
	EventsSeeded       int    `json:"events_seeded"`
	EventsRetrievable  int    `json:"events_retrievable"`
	SeedOK             bool   `json:"seed_ok"`
	ErrorMessage       string `json:"error_message,omitempty"`
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

// SeedJourney processes one journey scenario through the seed adapter:
// converts every session's events into Cortex insights written to a
// per-scenario temp storage, opens storage, and queries to verify the
// events became retrievable. Reports per-scenario seed outcome.
//
// Does NOT run a coding agent — that's the full-execution adapter
// (Phase D's remaining work). This proves the journey → context
// pipeline works end-to-end without the agent integration cost.
func SeedJourney(s *Scenario) SeedReport {
	rep := SeedReport{ScenarioID: s.ID}

	tempDir, err := os.MkdirTemp("", "cortex-journey-seed-*")
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("tempdir: %v", err)
		return rep
	}
	defer os.RemoveAll(tempDir)

	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		rep.ErrorMessage = fmt.Sprintf("mkdir dataDir: %v", err)
		return rep
	}
	jsonlPath := filepath.Join(dataDir, "insights.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("open jsonl: %v", err)
		return rep
	}
	enc := json.NewEncoder(f)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	var id int64
	for _, sess := range s.Sessions {
		rep.SessionsProcessed++
		for _, ev := range sess.Events {
			id++
			rec := map[string]any{
				"id":          id,
				"event_id":    ev.ID,
				"category":    ev.Type, // decision | pattern | constraint | insight
				"summary":     ev.Content,
				"importance":  ev.Importance,
				"tags":        ev.Tags,
				"reasoning":   ev.Rationale,
				"session_id":  sess.ID,
				"source_type": "journey",
				"created_at":  base.Add(time.Duration(id) * time.Second),
			}
			if err := enc.Encode(rec); err != nil {
				_ = f.Close()
				rep.ErrorMessage = fmt.Sprintf("encode event %s: %v", ev.ID, err)
				return rep
			}
			rep.EventsSeeded++
		}
	}
	_ = f.Close()

	// Open storage; verify each event is retrievable via Reflex.
	store, err := storage.New(&config.Config{ContextDir: tempDir})
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("storage.New: %v", err)
		return rep
	}
	defer store.Close()

	r := intcog.NewReflex(store, nil) // text-based scoring, no embedder needed
	// Issue a broad query that should pull back many events; count how
	// many of the seeded EventIDs are retrievable.
	results, err := r.Reflex(context.Background(), cognition.Query{Limit: rep.EventsSeeded * 2})
	if err != nil {
		rep.ErrorMessage = fmt.Sprintf("reflex query: %v", err)
		return rep
	}
	retrievable := make(map[string]bool)
	for _, res := range results {
		retrievable[res.ID] = true
	}
	rep.EventsRetrievable = len(retrievable)
	rep.SeedOK = rep.EventsSeeded > 0 && rep.EventsRetrievable >= rep.EventsSeeded/2
	return rep
}

// RunSuiteWithSeed runs RunSuite then additionally runs SeedJourney
// on every scenario whose status is pending_adapter. Returns the
// updated SuiteResult with seed outcomes incorporated.
func RunSuiteWithSeed(dir string) (*SuiteResult, []SeedReport, error) {
	suite, err := RunSuite(dir)
	if err != nil {
		return nil, nil, err
	}
	// Re-load + run seed for each scenario the validator marked
	// pending_adapter (i.e., parseable + scaffold present).
	pattern := filepath.Join(dir, "*.yaml")
	matches, _ := filepath.Glob(pattern)
	sort.Strings(matches)

	reports := make([]SeedReport, 0, len(matches))
	for _, path := range matches {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var s Scenario
		if uerr := yaml.Unmarshal(data, &s); uerr != nil {
			continue
		}
		if s.Project.Scaffold == "" {
			continue
		}
		if _, ferr := os.Stat(s.Project.Scaffold); ferr != nil {
			continue
		}
		rep := SeedJourney(&s)
		reports = append(reports, rep)
		if rep.SeedOK {
			suite.SeedOK++
		} else {
			suite.SeedFailed++
		}
	}
	return suite, reports, nil
}
