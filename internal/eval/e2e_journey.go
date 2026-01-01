// Package eval provides E2E Generative Eval types for journey-based testing.
//
// E2E Generative Evals measure actual development outcomes - not just recall accuracy.
// They simulate multi-session development journeys where:
//  1. Early sessions establish decisions/patterns (stored in Cortex)
//  2. Later sessions have implementation tasks for LLM to complete
//  3. We measure if the LLM follows the established decisions
//
// Key insight: The fundamental question is "Did Cortex help write better code faster?"
// not "Did Cortex recall the right context?"
//
// # Journey Structure
//
// A journey represents weeks/months of development:
//
//	sessions:
//	  - id: session-01
//	    phase: foundation
//	    events: [...decisions, patterns, corrections...]
//
//	  - id: session-08
//	    phase: feature
//	    task:
//	      description: "Implement X"
//	      acceptance:
//	        tests_pass: [...]
//	        patterns_required: [...]
//	        patterns_forbidden: [...]
//
// # Metrics
//
// E2E evals measure:
//   - Task completion rate (did LLM finish?)
//   - Test pass rate (is code correct?)
//   - Pattern adherence (did it follow decisions?)
//   - Violation rate (did it use forbidden patterns?)
//   - Turns to complete (efficiency)
//   - Correction rate (how many mistakes?)
//
// # Comparison
//
// Each journey runs twice:
//   - Treatment: Cortex stores events, injects context for tasks
//   - Baseline: No memory between sessions (fresh storage each session)
//
// The delta measures Cortex's value.
package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CognitionE2E is the scenario type for E2E generative evaluations
const CognitionE2E CognitionScenarioType = "e2e"

// E2EJourney represents a complete development journey spanning multiple sessions.
// It captures the evolution of a project from initial decisions through implementation,
// pivots, and feature additions.
//
// Example:
//
//	id: api-service-evolution
//	type: e2e
//	name: "E-Commerce API Service Evolution"
//	project:
//	  name: "api-service"
//	  scaffold: "test/evals/projects/api-service"
//	sessions:
//	  - id: session-01
//	    phase: foundation
//	    events: [...]
type E2EJourney struct {
	// ID uniquely identifies this journey
	ID string `yaml:"id"`

	// Type must be "e2e" for journey scenarios
	Type string `yaml:"type"`

	// Name is a human-readable title for the journey
	Name string `yaml:"name"`

	// Description explains what this journey tests
	Description string `yaml:"description,omitempty"`

	// Project defines the scaffold codebase for this journey
	Project E2EProject `yaml:"project"`

	// Sessions is the ordered sequence of development sessions
	// Sessions should be ordered chronologically by ID
	Sessions []E2ESession `yaml:"sessions"`

	// Metadata contains optional key-value pairs for categorization
	Metadata map[string]string `yaml:"metadata,omitempty"`
}

// E2EProject defines the scaffold project that tasks operate on.
// The scaffold provides pre-built code structure with stubs and tests.
type E2EProject struct {
	// Name is the project identifier
	Name string `yaml:"name"`

	// Scaffold is the path to the scaffold project directory
	// This should contain go.mod, source files, and test files
	// Example: "test/evals/projects/api-service"
	Scaffold string `yaml:"scaffold"`

	// Language is the primary language of the project (optional, default: "go")
	Language string `yaml:"language,omitempty"`

	// Description explains the project structure (optional)
	Description string `yaml:"description,omitempty"`
}

// E2EPhase categorizes sessions by their role in the project lifecycle.
// This helps understand the journey's narrative arc.
type E2EPhase string

const (
	// PhaseFoundation is for initial decisions and tech stack choices
	PhaseFoundation E2EPhase = "foundation"

	// PhaseFeature is for implementing new features
	PhaseFeature E2EPhase = "feature"

	// PhaseFix is for bug fixes and corrections
	PhaseFix E2EPhase = "fix"

	// PhaseRefactor is for code improvements without behavior change
	PhaseRefactor E2EPhase = "refactor"

	// PhasePivot is for architectural changes and decision reversals
	PhasePivot E2EPhase = "pivot"

	// PhaseMaintenance is for ongoing maintenance tasks
	PhaseMaintenance E2EPhase = "maintenance"
)

// E2ESession represents a single development session in the journey.
// A session can contain events (decisions, patterns), a task (implementation),
// and/or queries (recall tests).
type E2ESession struct {
	// ID uniquely identifies this session within the journey
	// Recommended format: "session-NN" for chronological ordering
	ID string `yaml:"id"`

	// Phase categorizes this session's role (foundation, feature, pivot, etc.)
	Phase E2EPhase `yaml:"phase"`

	// Context provides narrative description for this session
	// Example: "Project kickoff - choosing tech stack"
	Context string `yaml:"context,omitempty"`

	// Events are decisions, patterns, or corrections to store in Cortex
	// These establish context that should influence later tasks
	Events []E2EEvent `yaml:"events,omitempty"`

	// Task is an implementation task for the LLM to complete
	// Only one task per session to keep evals focused
	Task *E2ETask `yaml:"task,omitempty"`

	// Queries test context recall without implementation
	// Useful for verifying that superseded decisions are handled correctly
	Queries []E2EQuery `yaml:"queries,omitempty"`

	// SimulatedTime allows setting a specific timestamp for temporal testing
	// Format: RFC3339 (e.g., "2025-01-15T10:00:00Z")
	SimulatedTime string `yaml:"simulated_time,omitempty"`
}

// E2EEventType categorizes the type of event being stored.
type E2EEventType string

const (
	// EventDecision is an architectural or technical decision
	EventDecision E2EEventType = "decision"

	// EventPattern is a code pattern or convention
	EventPattern E2EEventType = "pattern"

	// EventCorrection is a correction of previous behavior
	EventCorrection E2EEventType = "correction"

	// EventConstraint is a hard constraint that must be followed
	EventConstraint E2EEventType = "constraint"

	// EventPreference is a soft preference (style, naming, etc.)
	EventPreference E2EEventType = "preference"
)

// E2EEvent represents a piece of context to store in Cortex.
// Events establish the knowledge base that should influence later tasks.
type E2EEvent struct {
	// Type categorizes this event (decision, pattern, correction, etc.)
	Type E2EEventType `yaml:"type"`

	// ID uniquely identifies this event for cross-references
	// Used by Supersedes to mark old decisions as outdated
	ID string `yaml:"id"`

	// Content is the actual context to store
	// This should be clear and actionable
	// Example: "Use Redis for caching, NOT in-memory"
	Content string `yaml:"content"`

	// Rationale explains why this decision was made (optional)
	// Helps the LLM understand the reasoning
	Rationale string `yaml:"rationale,omitempty"`

	// Supersedes marks another event ID as outdated
	// When set, the superseded event should not be recalled for new tasks
	// Example: auth-oauth2 supersedes auth-jwt
	Supersedes string `yaml:"supersedes,omitempty"`

	// Tags help with retrieval and categorization
	Tags []string `yaml:"tags,omitempty"`

	// Importance indicates priority (1-10, default 5)
	// Higher importance events should be recalled more readily
	Importance int `yaml:"importance,omitempty"`

	// Scope limits where this event applies
	// Values: "project", "module", "file"
	Scope string `yaml:"scope,omitempty"`

	// ScopeTarget specifies the specific module or file if Scope is set
	// Example: "internal/product" for module scope
	ScopeTarget string `yaml:"scope_target,omitempty"`
}

// E2ETask represents an implementation task for the LLM to complete.
// Tasks are the core of E2E evals - they test if context injection
// actually improves code generation.
type E2ETask struct {
	// Description explains what the LLM should implement
	// This is the prompt given to the LLM
	Description string `yaml:"description"`

	// FilesToModify lists the files the LLM should edit
	// Paths are relative to the scaffold project root
	FilesToModify []string `yaml:"files_to_modify,omitempty"`

	// FilesToCreate lists new files the LLM should create
	// Paths are relative to the scaffold project root
	FilesToCreate []string `yaml:"files_to_create,omitempty"`

	// MaxTurns limits LLM interactions to prevent runaway costs
	// Default: 15. Task fails if not complete within this limit.
	MaxTurns int `yaml:"max_turns,omitempty"`

	// Timeout is the maximum wall-clock time for the task
	// Format: Go duration string (e.g., "5m", "300s")
	Timeout string `yaml:"timeout,omitempty"`

	// Hints are optional nudges if the LLM gets stuck
	// Used only after N failed attempts (configurable)
	Hints []string `yaml:"hints,omitempty"`

	// Acceptance defines the criteria for task success
	Acceptance TaskAcceptance `yaml:"acceptance"`

	// ReferenceImplementation is the expected correct solution (optional)
	// Used for comparison and scoring, not shown to LLM
	ReferenceImplementation string `yaml:"reference_implementation,omitempty"`
}

// TaskAcceptance defines the criteria for determining if a task succeeded.
// Multiple criteria can be combined - all must pass for task success.
type TaskAcceptance struct {
	// TestsPass lists test names that must pass
	// Tests are run with `go test -run <name>`
	TestsPass []string `yaml:"tests_pass,omitempty"`

	// TestsFile specifies a test file to run all tests from
	// Alternative to listing individual test names
	TestsFile string `yaml:"tests_file,omitempty"`

	// PatternsRequired are code patterns that MUST be present
	// Uses simple string matching (grep-style)
	// Example: ["redis.Client", "cache.Get"]
	PatternsRequired []string `yaml:"patterns_required,omitempty"`

	// PatternsForbidden are code patterns that MUST NOT be present
	// Violations indicate the LLM ignored established decisions
	// Example: ["sync.Map", "map[string]"]
	PatternsForbidden []string `yaml:"patterns_forbidden,omitempty"`

	// ImportsRequired lists Go imports that must be present
	// Example: ["github.com/redis/go-redis/v9"]
	ImportsRequired []string `yaml:"imports_required,omitempty"`

	// ImportsForbidden lists Go imports that must not be used
	// Example: ["github.com/patrickmn/go-cache"]
	ImportsForbidden []string `yaml:"imports_forbidden,omitempty"`

	// CodeReview contains human-readable criteria for LLM-as-judge
	// Each item is a criterion that should be met
	// Example: ["Uses Redis client, not in-memory cache", "Implements cache-aside pattern"]
	CodeReview []string `yaml:"code_review,omitempty"`

	// CodeReviewRequired makes code review a pass/fail gate when true.
	// Default: false (code review is informational only)
	CodeReviewRequired *bool `yaml:"code_review_required,omitempty"`

	// BuildsMust specifies that the code must compile successfully
	// Default: true
	BuildsMust *bool `yaml:"builds_must,omitempty"`

	// LintsMust specifies that code must pass linting (go vet)
	// Default: true
	LintsMust *bool `yaml:"lints_must,omitempty"`

	// MinCoverage is the minimum test coverage percentage required
	// Only applicable if tests are run
	MinCoverage float64 `yaml:"min_coverage,omitempty"`

	// CustomChecks are shell commands that must succeed
	// Exit code 0 = pass, non-zero = fail
	// Example: ["./scripts/check_patterns.sh"]
	CustomChecks []string `yaml:"custom_checks,omitempty"`
}

// E2EQuery tests context recall without implementation.
// Useful for verifying that superseded decisions are handled correctly.
type E2EQuery struct {
	// ID uniquely identifies this query
	ID string `yaml:"id,omitempty"`

	// Text is the query to submit to Cortex
	Text string `yaml:"text"`

	// ExpectedRecall lists event IDs that SHOULD be recalled
	// These are events that are still relevant
	ExpectedRecall []string `yaml:"expected_recall,omitempty"`

	// ExpectedNotRecall lists event IDs that should NOT be recalled
	// Typically superseded decisions
	ExpectedNotRecall []string `yaml:"expected_not_recall,omitempty"`

	// ExpectedContent lists strings that should appear in recalled context
	// More flexible than event IDs for partial matches
	ExpectedContent []string `yaml:"expected_content,omitempty"`

	// ForbiddenContent lists strings that should NOT appear
	ForbiddenContent []string `yaml:"forbidden_content,omitempty"`
}

// E2EJourneyResult contains the results from running an E2E journey evaluation.
type E2EJourneyResult struct {
	// JourneyID is the ID of the journey that was run
	JourneyID string `json:"journey_id"`

	// RunID uniquely identifies this specific run
	RunID string `json:"run_id"`

	// StartTime is when the evaluation started
	StartTime time.Time `json:"start_time"`

	// EndTime is when the evaluation completed
	EndTime time.Time `json:"end_time"`

	// Duration is the total time taken
	Duration time.Duration `json:"duration"`

	// TreatmentResults are results with Cortex enabled (context injection)
	TreatmentResults *E2ERunResults `json:"treatment_results"`

	// BaselineResults are results without Cortex (no memory)
	BaselineResults *E2ERunResults `json:"baseline_results"`

	// Comparison shows the delta between treatment and baseline
	Comparison *E2EComparison `json:"comparison"`

	// Pass indicates if the journey met success criteria
	Pass bool `json:"pass"`

	// Reason explains why the journey passed or failed
	Reason string `json:"reason,omitempty"`
}

// E2ERunResults contains aggregate results from one run (treatment or baseline).
type E2ERunResults struct {
	// SessionResults contains results for each session
	SessionResults []E2ESessionResult `json:"session_results"`

	// TasksTotal is the total number of tasks in the journey
	TasksTotal int `json:"tasks_total"`

	// TasksCompleted is how many tasks were completed successfully
	TasksCompleted int `json:"tasks_completed"`

	// TaskCompletionRate is TasksCompleted / TasksTotal
	TaskCompletionRate float64 `json:"task_completion_rate"`

	// TestsTotal is total test assertions across all tasks
	TestsTotal int `json:"tests_total"`

	// TestsPassed is how many tests passed
	TestsPassed int `json:"tests_passed"`

	// TestPassRate is TestsPassed / TestsTotal
	TestPassRate float64 `json:"test_pass_rate"`

	// TotalTurns is the sum of turns across all tasks
	TotalTurns int `json:"total_turns"`

	// AverageTurns is TotalTurns / TasksCompleted
	AverageTurns float64 `json:"average_turns"`

	// TotalTokens is the total tokens used (input + output)
	TotalTokens int `json:"total_tokens"`

	// TotalCost is estimated cost in USD (based on model pricing)
	TotalCost float64 `json:"total_cost,omitempty"`

	// PatternViolations is the count of forbidden patterns found
	PatternViolations int `json:"pattern_violations"`

	// CorrectionsNeeded is how many times user intervention was simulated
	CorrectionsNeeded int `json:"corrections_needed"`

	// EventsStored is how many events were stored in Cortex
	EventsStored int `json:"events_stored"`
}

// E2ESessionResult contains results for a single session.
type E2ESessionResult struct {
	// SessionID is the ID of the session
	SessionID string `json:"session_id"`

	// Phase is the session's phase (foundation, feature, etc.)
	Phase E2EPhase `json:"phase"`

	// EventsStored is how many events were stored this session
	EventsStored int `json:"events_stored"`

	// TaskResult contains task results if this session had a task
	TaskResult *E2ETaskResult `json:"task_result,omitempty"`

	// QueryResults contains query results if this session had queries
	QueryResults []E2EQueryResult `json:"query_results,omitempty"`
}

// E2ETaskResult contains results from executing a single task.
type E2ETaskResult struct {
	// TaskDescription is the task that was attempted
	TaskDescription string `json:"task_description"`

	// Completed indicates if the task finished (regardless of correctness)
	Completed bool `json:"completed"`

	// CompletedSuccessfully indicates if task met all acceptance criteria
	CompletedSuccessfully bool `json:"completed_successfully"`

	// Turns is how many LLM interactions were used
	Turns int `json:"turns"`

	// TokensUsed is total tokens for this task
	TokensUsed int `json:"tokens_used"`

	// Duration is wall-clock time for this task
	Duration time.Duration `json:"duration"`

	// TestResults contains per-test results
	TestResults []TestResult `json:"test_results,omitempty"`

	// TestsPassed is count of passing tests
	TestsPassed int `json:"tests_passed"`

	// TestsFailed is count of failing tests
	TestsFailed int `json:"tests_failed"`

	// PatternMatches lists which required patterns were found
	PatternMatches []PatternMatch `json:"pattern_matches,omitempty"`

	// PatternViolations lists which forbidden patterns were found
	PatternViolations []PatternMatch `json:"pattern_violations,omitempty"`

	// CodeReviewResults contains LLM-as-judge results for code review criteria
	CodeReviewResults []CodeReviewResult `json:"code_review_results,omitempty"`

	// BuildSucceeded indicates if code compiled
	BuildSucceeded bool `json:"build_succeeded"`

	// BuildError contains compilation error if build failed
	BuildError string `json:"build_error,omitempty"`

	// LintSucceeded indicates if code passed linting
	LintSucceeded bool `json:"lint_succeeded"`

	// LintErrors contains lint errors if any
	LintErrors []string `json:"lint_errors,omitempty"`

	// CorrectionsApplied lists corrections that were simulated
	CorrectionsApplied []string `json:"corrections_applied,omitempty"`

	// FailureReason explains why task failed (if applicable)
	FailureReason string `json:"failure_reason,omitempty"`

	// GeneratedCode contains the code that was generated
	GeneratedCode map[string]string `json:"generated_code,omitempty"`
}

// TestResult contains the result of a single test.
type TestResult struct {
	// Name is the test name
	Name string `json:"name"`

	// Passed indicates if the test passed
	Passed bool `json:"passed"`

	// Output contains test output (especially useful for failures)
	Output string `json:"output,omitempty"`

	// Duration is how long the test took
	Duration time.Duration `json:"duration,omitempty"`
}

// PatternMatch records whether a pattern was found.
type PatternMatch struct {
	// Pattern is the pattern being checked
	Pattern string `json:"pattern"`

	// Found indicates if pattern was found
	Found bool `json:"found"`

	// Locations lists where the pattern was found (file:line)
	Locations []string `json:"locations,omitempty"`
}

// CodeReviewResult contains LLM-as-judge assessment for a review criterion.
type CodeReviewResult struct {
	// Criterion is the review criterion being assessed
	Criterion string `json:"criterion"`

	// Passed indicates if the criterion was met
	Passed bool `json:"passed"`

	// Reasoning is the judge's explanation
	Reasoning string `json:"reasoning,omitempty"`

	// Confidence is the judge's confidence (0-1)
	Confidence float64 `json:"confidence,omitempty"`
}

// E2EQueryResult contains results from a recall query.
type E2EQueryResult struct {
	// QueryID is the query's ID
	QueryID string `json:"query_id"`

	// QueryText is the query that was submitted
	QueryText string `json:"query_text"`

	// RecalledEventIDs lists event IDs that were recalled
	RecalledEventIDs []string `json:"recalled_event_ids"`

	// ExpectedRecallHits is how many expected recalls were found
	ExpectedRecallHits int `json:"expected_recall_hits"`

	// ExpectedRecallMisses is how many expected recalls were missed
	ExpectedRecallMisses int `json:"expected_recall_misses"`

	// UnexpectedRecalls lists events that were recalled but shouldn't have been
	UnexpectedRecalls []string `json:"unexpected_recalls,omitempty"`

	// RecallPrecision is hits / (hits + unexpected)
	RecallPrecision float64 `json:"recall_precision"`

	// RecallScore is hits / expected
	RecallScore float64 `json:"recall_score"`

	// Pass indicates if query met expectations
	Pass bool `json:"pass"`
}

// E2EComparison shows the delta between treatment and baseline runs.
type E2EComparison struct {
	// TaskCompletionLift is treatment rate - baseline rate
	TaskCompletionLift float64 `json:"task_completion_lift"`

	// TestPassLift is treatment rate - baseline rate
	TestPassLift float64 `json:"test_pass_lift"`

	// TurnReduction is (baseline turns - treatment turns) / baseline turns
	// Positive means treatment used fewer turns (good)
	TurnReduction float64 `json:"turn_reduction"`

	// TokenReduction is (baseline tokens - treatment tokens) / baseline tokens
	// Positive means treatment used fewer tokens (good)
	TokenReduction float64 `json:"token_reduction"`

	// ViolationReduction is (baseline violations - treatment violations)
	// Positive means treatment had fewer violations (good)
	ViolationReduction int `json:"violation_reduction"`

	// CorrectionReduction is (baseline corrections - treatment corrections)
	// Positive means treatment needed fewer corrections (good)
	CorrectionReduction int `json:"correction_reduction"`

	// CostReduction is (baseline cost - treatment cost) / baseline cost
	// Positive means treatment was cheaper (good)
	CostReduction float64 `json:"cost_reduction"`

	// OverallLift is a composite score representing overall improvement
	// Calculated as weighted average of individual lifts
	OverallLift float64 `json:"overall_lift"`

	// Regression indicates if treatment performed worse than baseline in any metric
	Regression bool `json:"regression"`

	// RegressionDetails explains any regression
	RegressionDetails string `json:"regression_details,omitempty"`
}

// LoadE2EJourney loads an E2E journey from a YAML file.
// It validates required fields and returns an error if the file
// is malformed or missing required data.
func LoadE2EJourney(path string) (*E2EJourney, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read journey file: %w", err)
	}

	var journey E2EJourney
	if err := yaml.Unmarshal(data, &journey); err != nil {
		return nil, fmt.Errorf("failed to parse journey YAML: %w", err)
	}

	// Validate required fields
	if journey.ID == "" {
		return nil, fmt.Errorf("journey missing required field: id")
	}
	if journey.Type != "e2e" {
		return nil, fmt.Errorf("journey type must be 'e2e', got: %s", journey.Type)
	}
	if journey.Name == "" {
		return nil, fmt.Errorf("journey missing required field: name")
	}
	if journey.Project.Name == "" {
		return nil, fmt.Errorf("journey missing required field: project.name")
	}
	if journey.Project.Scaffold == "" {
		return nil, fmt.Errorf("journey missing required field: project.scaffold")
	}
	if len(journey.Sessions) == 0 {
		return nil, fmt.Errorf("journey must have at least one session")
	}

	// Validate sessions
	sessionIDs := make(map[string]bool)
	eventIDs := make(map[string]bool)
	hasTask := false

	for i, session := range journey.Sessions {
		if session.ID == "" {
			return nil, fmt.Errorf("session %d missing required field: id", i)
		}
		if sessionIDs[session.ID] {
			return nil, fmt.Errorf("duplicate session id: %s", session.ID)
		}
		sessionIDs[session.ID] = true

		// Validate events
		for j, event := range session.Events {
			if event.ID == "" {
				return nil, fmt.Errorf("session %s event %d missing required field: id", session.ID, j)
			}
			if eventIDs[event.ID] {
				return nil, fmt.Errorf("duplicate event id: %s", event.ID)
			}
			eventIDs[event.ID] = true

			if event.Type == "" {
				return nil, fmt.Errorf("session %s event %s missing required field: type", session.ID, event.ID)
			}
			if event.Content == "" {
				return nil, fmt.Errorf("session %s event %s missing required field: content", session.ID, event.ID)
			}

			// Validate supersedes references
			if event.Supersedes != "" {
				// Note: We can't validate that the superseded ID exists yet
				// because it might be defined in a later session in the YAML
				// The eval runner should validate this at runtime
			}
		}

		// Validate task if present
		if session.Task != nil {
			hasTask = true
			if session.Task.Description == "" {
				return nil, fmt.Errorf("session %s task missing required field: description", session.ID)
			}
		}

		// Validate queries if present
		for j, query := range session.Queries {
			if query.Text == "" {
				return nil, fmt.Errorf("session %s query %d missing required field: text", session.ID, j)
			}
		}
	}

	// An E2E journey should have at least one task (otherwise it's just a recall test)
	if !hasTask {
		return nil, fmt.Errorf("journey must have at least one session with a task")
	}

	// Set defaults
	for i := range journey.Sessions {
		if journey.Sessions[i].Task != nil {
			if journey.Sessions[i].Task.MaxTurns == 0 {
				journey.Sessions[i].Task.MaxTurns = 15
			}
		}
		for j := range journey.Sessions[i].Events {
			if journey.Sessions[i].Events[j].Importance == 0 {
				journey.Sessions[i].Events[j].Importance = 5
			}
		}
	}

	// Set project language default
	if journey.Project.Language == "" {
		journey.Project.Language = "go"
	}

	return &journey, nil
}

// LoadE2EJourneys loads all E2E journeys from a directory.
// It recursively searches for .yaml and .yml files with type: e2e.
func LoadE2EJourneys(dir string) ([]*E2EJourney, error) {
	var journeys []*E2EJourney

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		path := dir + "/" + entry.Name()

		if entry.IsDir() {
			// Recursively load from subdirectories
			subJourneys, err := LoadE2EJourneys(path)
			if err != nil {
				return nil, err
			}
			journeys = append(journeys, subJourneys...)
			continue
		}

		// Check for YAML files
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		// Try to load as journey
		journey, err := LoadE2EJourney(path)
		if err != nil {
			// Skip files that aren't valid journeys (might be other scenario types)
			continue
		}

		journeys = append(journeys, journey)
	}

	return journeys, nil
}

// GetTaskSessions returns only sessions that have tasks.
func (j *E2EJourney) GetTaskSessions() []E2ESession {
	var taskSessions []E2ESession
	for _, session := range j.Sessions {
		if session.Task != nil {
			taskSessions = append(taskSessions, session)
		}
	}
	return taskSessions
}

// GetEventSessions returns only sessions that have events.
func (j *E2EJourney) GetEventSessions() []E2ESession {
	var eventSessions []E2ESession
	for _, session := range j.Sessions {
		if len(session.Events) > 0 {
			eventSessions = append(eventSessions, session)
		}
	}
	return eventSessions
}

// TotalEvents returns the total number of events across all sessions.
func (j *E2EJourney) TotalEvents() int {
	total := 0
	for _, session := range j.Sessions {
		total += len(session.Events)
	}
	return total
}

// TotalTasks returns the total number of tasks across all sessions.
func (j *E2EJourney) TotalTasks() int {
	total := 0
	for _, session := range j.Sessions {
		if session.Task != nil {
			total++
		}
	}
	return total
}

// GetEventByID finds an event by its ID across all sessions.
func (j *E2EJourney) GetEventByID(id string) *E2EEvent {
	for _, session := range j.Sessions {
		for i := range session.Events {
			if session.Events[i].ID == id {
				return &session.Events[i]
			}
		}
	}
	return nil
}

// GetSupersededEvents returns all events that have been superseded.
func (j *E2EJourney) GetSupersededEvents() []E2EEvent {
	// Build set of superseded IDs
	superseded := make(map[string]bool)
	for _, session := range j.Sessions {
		for _, event := range session.Events {
			if event.Supersedes != "" {
				superseded[event.Supersedes] = true
			}
		}
	}

	// Collect superseded events
	var events []E2EEvent
	for _, session := range j.Sessions {
		for _, event := range session.Events {
			if superseded[event.ID] {
				events = append(events, event)
			}
		}
	}
	return events
}

// Validate performs comprehensive validation on the journey.
// This is more thorough than LoadE2EJourney's basic validation.
func (j *E2EJourney) Validate() error {
	// Validate supersedes references
	eventIDs := make(map[string]bool)
	for _, session := range j.Sessions {
		for _, event := range session.Events {
			eventIDs[event.ID] = true
		}
	}

	for _, session := range j.Sessions {
		for _, event := range session.Events {
			if event.Supersedes != "" && !eventIDs[event.Supersedes] {
				return fmt.Errorf("event %s supersedes unknown event: %s", event.ID, event.Supersedes)
			}
		}

		// Validate query references
		for _, query := range session.Queries {
			for _, expectedID := range query.ExpectedRecall {
				if !eventIDs[expectedID] {
					return fmt.Errorf("query expects unknown event: %s", expectedID)
				}
			}
			for _, notExpectedID := range query.ExpectedNotRecall {
				if !eventIDs[notExpectedID] {
					return fmt.Errorf("query expects not to recall unknown event: %s", notExpectedID)
				}
			}
		}
	}

	// Validate task files exist (if scaffold path is accessible)
	// This is a soft check - scaffold might not exist during YAML authoring
	if _, err := os.Stat(j.Project.Scaffold); err == nil {
		for _, session := range j.Sessions {
			if session.Task != nil {
				for _, file := range session.Task.FilesToModify {
					path := j.Project.Scaffold + "/" + file
					if _, err := os.Stat(path); os.IsNotExist(err) {
						return fmt.Errorf("task file does not exist in scaffold: %s", file)
					}
				}
			}
		}
	}

	return nil
}
