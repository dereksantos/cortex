package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadE2EJourney_Valid(t *testing.T) {
	// Create a temporary journey file
	content := `
id: test-journey-001
type: e2e
name: "Test Journey"
description: "A test journey for unit tests"

project:
  name: "test-project"
  scaffold: "test/evals/projects/test-project"

sessions:
  - id: session-01
    phase: foundation
    context: "Project kickoff"
    events:
      - type: decision
        id: cache-redis
        content: "Use Redis for caching, NOT in-memory"
        rationale: "Shared across instances"
        tags: [cache, architecture]
      - type: pattern
        id: error-wrap
        content: "Always wrap errors with context"
        tags: [errors, patterns]

  - id: session-08
    phase: feature
    context: "Add caching to product service"
    task:
      description: "Implement caching for GetProduct"
      files_to_modify:
        - "internal/product/service.go"
      max_turns: 15
      acceptance:
        tests_pass:
          - "TestGetProductCached"
        patterns_required:
          - "redis.Client"
        patterns_forbidden:
          - "sync.Map"
        code_review:
          - "Uses Redis, not in-memory"

metadata:
  author: test
`

	tmpDir := t.TempDir()
	journeyPath := filepath.Join(tmpDir, "test-journey.yaml")
	if err := os.WriteFile(journeyPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(journeyPath)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	// Verify journey fields
	if journey.ID != "test-journey-001" {
		t.Errorf("Expected ID 'test-journey-001', got '%s'", journey.ID)
	}
	if journey.Type != "e2e" {
		t.Errorf("Expected Type 'e2e', got '%s'", journey.Type)
	}
	if journey.Name != "Test Journey" {
		t.Errorf("Expected Name 'Test Journey', got '%s'", journey.Name)
	}

	// Verify project
	if journey.Project.Name != "test-project" {
		t.Errorf("Expected project name 'test-project', got '%s'", journey.Project.Name)
	}
	if journey.Project.Language != "go" {
		t.Errorf("Expected project language 'go' (default), got '%s'", journey.Project.Language)
	}

	// Verify sessions
	if len(journey.Sessions) != 2 {
		t.Fatalf("Expected 2 sessions, got %d", len(journey.Sessions))
	}

	// Verify first session (foundation with events)
	s1 := journey.Sessions[0]
	if s1.ID != "session-01" {
		t.Errorf("Expected session ID 'session-01', got '%s'", s1.ID)
	}
	if s1.Phase != PhaseFoundation {
		t.Errorf("Expected phase 'foundation', got '%s'", s1.Phase)
	}
	if len(s1.Events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(s1.Events))
	}
	if s1.Task != nil {
		t.Errorf("Expected no task in session-01")
	}

	// Verify event defaults
	if s1.Events[0].Importance != 5 {
		t.Errorf("Expected default importance 5, got %d", s1.Events[0].Importance)
	}

	// Verify second session (feature with task)
	s2 := journey.Sessions[1]
	if s2.ID != "session-08" {
		t.Errorf("Expected session ID 'session-08', got '%s'", s2.ID)
	}
	if s2.Phase != PhaseFeature {
		t.Errorf("Expected phase 'feature', got '%s'", s2.Phase)
	}
	if s2.Task == nil {
		t.Fatalf("Expected task in session-08")
	}

	// Verify task
	task := s2.Task
	if task.Description != "Implement caching for GetProduct" {
		t.Errorf("Expected task description, got '%s'", task.Description)
	}
	if task.MaxTurns != 15 {
		t.Errorf("Expected max_turns 15, got %d", task.MaxTurns)
	}
	if len(task.FilesToModify) != 1 {
		t.Errorf("Expected 1 file to modify, got %d", len(task.FilesToModify))
	}

	// Verify acceptance criteria
	acc := task.Acceptance
	if len(acc.TestsPass) != 1 || acc.TestsPass[0] != "TestGetProductCached" {
		t.Errorf("Expected TestGetProductCached in tests_pass, got %v", acc.TestsPass)
	}
	if len(acc.PatternsRequired) != 1 || acc.PatternsRequired[0] != "redis.Client" {
		t.Errorf("Expected redis.Client in patterns_required, got %v", acc.PatternsRequired)
	}
	if len(acc.PatternsForbidden) != 1 || acc.PatternsForbidden[0] != "sync.Map" {
		t.Errorf("Expected sync.Map in patterns_forbidden, got %v", acc.PatternsForbidden)
	}

	// Verify helper methods
	if journey.TotalEvents() != 2 {
		t.Errorf("Expected TotalEvents() = 2, got %d", journey.TotalEvents())
	}
	if journey.TotalTasks() != 1 {
		t.Errorf("Expected TotalTasks() = 1, got %d", journey.TotalTasks())
	}

	taskSessions := journey.GetTaskSessions()
	if len(taskSessions) != 1 {
		t.Errorf("Expected 1 task session, got %d", len(taskSessions))
	}

	eventSessions := journey.GetEventSessions()
	if len(eventSessions) != 1 {
		t.Errorf("Expected 1 event session, got %d", len(eventSessions))
	}

	// Verify GetEventByID
	event := journey.GetEventByID("cache-redis")
	if event == nil {
		t.Error("Expected to find event 'cache-redis'")
	}
	if event != nil && event.Content != "Use Redis for caching, NOT in-memory" {
		t.Errorf("Wrong content for event 'cache-redis'")
	}

	// Verify metadata
	if journey.Metadata["author"] != "test" {
		t.Errorf("Expected metadata author 'test', got '%s'", journey.Metadata["author"])
	}
}

func TestLoadE2EJourney_WithSupersedes(t *testing.T) {
	content := `
id: pivot-journey
type: e2e
name: "Journey with Supersedes"

project:
  name: "api"
  scaffold: "test/evals/projects/api"

sessions:
  - id: session-01
    phase: foundation
    events:
      - type: decision
        id: auth-jwt
        content: "Use JWT for authentication"
        tags: [auth]

  - id: session-10
    phase: pivot
    events:
      - type: decision
        id: auth-oauth2
        content: "Switch to OAuth2 for enterprise SSO"
        supersedes: auth-jwt
        tags: [auth, enterprise]

  - id: session-11
    phase: feature
    task:
      description: "Update auth middleware"
      acceptance:
        patterns_required:
          - "oauth2"
    queries:
      - text: "What's our auth approach?"
        expected_recall: [auth-oauth2]
        expected_not_recall: [auth-jwt]
`

	tmpDir := t.TempDir()
	journeyPath := filepath.Join(tmpDir, "pivot.yaml")
	if err := os.WriteFile(journeyPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(journeyPath)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	// Verify supersedes relationship
	oauth2Event := journey.GetEventByID("auth-oauth2")
	if oauth2Event == nil {
		t.Fatal("Expected to find auth-oauth2 event")
	}
	if oauth2Event.Supersedes != "auth-jwt" {
		t.Errorf("Expected auth-oauth2 to supersede auth-jwt, got '%s'", oauth2Event.Supersedes)
	}

	// Verify GetSupersededEvents
	superseded := journey.GetSupersededEvents()
	if len(superseded) != 1 {
		t.Fatalf("Expected 1 superseded event, got %d", len(superseded))
	}
	if superseded[0].ID != "auth-jwt" {
		t.Errorf("Expected superseded event 'auth-jwt', got '%s'", superseded[0].ID)
	}

	// Verify queries in session
	s3 := journey.Sessions[2]
	if len(s3.Queries) != 1 {
		t.Fatalf("Expected 1 query in session-11, got %d", len(s3.Queries))
	}
	query := s3.Queries[0]
	if len(query.ExpectedRecall) != 1 || query.ExpectedRecall[0] != "auth-oauth2" {
		t.Errorf("Expected expected_recall [auth-oauth2], got %v", query.ExpectedRecall)
	}
	if len(query.ExpectedNotRecall) != 1 || query.ExpectedNotRecall[0] != "auth-jwt" {
		t.Errorf("Expected expected_not_recall [auth-jwt], got %v", query.ExpectedNotRecall)
	}

	// Validate should pass for valid supersedes references
	if err := journey.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

func TestLoadE2EJourney_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "missing id",
			content: `
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: d1
        content: "test"
  - id: s2
    phase: feature
    task:
      description: "do something"
      acceptance: {}
`,
			wantErr: "missing required field: id",
		},
		{
			name: "wrong type",
			content: `
id: test
type: linear
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions: []
`,
			wantErr: "type must be 'e2e'",
		},
		{
			name: "missing project name",
			content: `
id: test
type: e2e
name: "Test"
project:
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: feature
    task:
      description: "do something"
      acceptance: {}
`,
			wantErr: "missing required field: project.name",
		},
		{
			name: "no sessions",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions: []
`,
			wantErr: "at least one session",
		},
		{
			name: "no task in journey",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: d1
        content: "test"
`,
			wantErr: "at least one session with a task",
		},
		{
			name: "duplicate session id",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: d1
        content: "test"
  - id: s1
    phase: feature
    task:
      description: "test"
      acceptance: {}
`,
			wantErr: "duplicate session id",
		},
		{
			name: "duplicate event id",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: d1
        content: "test"
      - type: pattern
        id: d1
        content: "duplicate"
  - id: s2
    phase: feature
    task:
      description: "test"
      acceptance: {}
`,
			wantErr: "duplicate event id",
		},
		{
			name: "event missing content",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: d1
  - id: s2
    phase: feature
    task:
      description: "test"
      acceptance: {}
`,
			wantErr: "missing required field: content",
		},
		{
			name: "task missing description",
			content: `
id: test
type: e2e
name: "Test"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: feature
    task:
      acceptance:
        tests_pass: [Test1]
`,
			wantErr: "missing required field: description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "test.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			_, err := LoadE2EJourney(path)
			if err == nil {
				t.Fatal("Expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("Expected error containing '%s', got '%s'", tt.wantErr, err.Error())
			}
		})
	}
}

func TestLoadE2EJourney_AllEventTypes(t *testing.T) {
	content := `
id: all-event-types
type: e2e
name: "All Event Types"

project:
  name: "test"
  scaffold: "test/scaffold"

sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: e1
        content: "A decision"
      - type: pattern
        id: e2
        content: "A pattern"
      - type: correction
        id: e3
        content: "A correction"
      - type: constraint
        id: e4
        content: "A constraint"
      - type: preference
        id: e5
        content: "A preference"

  - id: s2
    phase: feature
    task:
      description: "Implement feature"
      acceptance:
        patterns_required: [required]
`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(path)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	// Verify all event types
	events := journey.Sessions[0].Events
	expectedTypes := []E2EEventType{
		EventDecision,
		EventPattern,
		EventCorrection,
		EventConstraint,
		EventPreference,
	}

	for i, expected := range expectedTypes {
		if events[i].Type != expected {
			t.Errorf("Event %d: expected type %s, got %s", i, expected, events[i].Type)
		}
	}
}

func TestLoadE2EJourney_AllPhases(t *testing.T) {
	content := `
id: all-phases
type: e2e
name: "All Phases"

project:
  name: "test"
  scaffold: "test/scaffold"

sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: e1
        content: "content"

  - id: s2
    phase: feature
    task:
      description: "task"
      acceptance: {}

  - id: s3
    phase: fix
    events:
      - type: correction
        id: e2
        content: "content"

  - id: s4
    phase: refactor
    events:
      - type: pattern
        id: e3
        content: "content"

  - id: s5
    phase: pivot
    events:
      - type: decision
        id: e4
        content: "content"

  - id: s6
    phase: maintenance
    events:
      - type: preference
        id: e5
        content: "content"
`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(path)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	expectedPhases := []E2EPhase{
		PhaseFoundation,
		PhaseFeature,
		PhaseFix,
		PhaseRefactor,
		PhasePivot,
		PhaseMaintenance,
	}

	for i, expected := range expectedPhases {
		if journey.Sessions[i].Phase != expected {
			t.Errorf("Session %d: expected phase %s, got %s", i, expected, journey.Sessions[i].Phase)
		}
	}
}

func TestValidate_InvalidSupersedes(t *testing.T) {
	content := `
id: invalid-supersedes
type: e2e
name: "Invalid Supersedes"

project:
  name: "test"
  scaffold: "test/scaffold"

sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: auth-oauth2
        content: "Use OAuth2"
        supersedes: nonexistent-event

  - id: s2
    phase: feature
    task:
      description: "task"
      acceptance: {}
`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(path)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	// LoadE2EJourney doesn't validate supersedes, but Validate should
	err = journey.Validate()
	if err == nil {
		t.Fatal("Expected Validate to fail for invalid supersedes reference")
	}
	if !contains(err.Error(), "supersedes unknown event") {
		t.Errorf("Expected error about unknown supersedes, got: %v", err)
	}
}

func TestValidate_InvalidQueryReferences(t *testing.T) {
	content := `
id: invalid-query-refs
type: e2e
name: "Invalid Query References"

project:
  name: "test"
  scaffold: "test/scaffold"

sessions:
  - id: s1
    phase: foundation
    events:
      - type: decision
        id: real-event
        content: "A real event"

  - id: s2
    phase: feature
    task:
      description: "task"
      acceptance: {}
    queries:
      - text: "test query"
        expected_recall: [nonexistent-event]
`

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	journey, err := LoadE2EJourney(path)
	if err != nil {
		t.Fatalf("LoadE2EJourney failed: %v", err)
	}

	err = journey.Validate()
	if err == nil {
		t.Fatal("Expected Validate to fail for invalid query reference")
	}
	if !contains(err.Error(), "unknown event") {
		t.Errorf("Expected error about unknown event, got: %v", err)
	}
}

func TestLoadE2EJourneys_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid journey
	journey1 := `
id: journey-1
type: e2e
name: "Journey 1"
project:
  name: "test"
  scaffold: "test/scaffold"
sessions:
  - id: s1
    phase: feature
    task:
      description: "task"
      acceptance: {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "journey1.yaml"), []byte(journey1), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create another valid journey
	journey2 := `
id: journey-2
type: e2e
name: "Journey 2"
project:
  name: "test2"
  scaffold: "test/scaffold2"
sessions:
  - id: s1
    phase: feature
    task:
      description: "another task"
      acceptance: {}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "journey2.yml"), []byte(journey2), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create a non-journey YAML (should be skipped)
	nonJourney := `
id: not-a-journey
type: linear
name: "Not a journey"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "not-journey.yaml"), []byte(nonJourney), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Create a subdirectory with another journey
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}
	journey3 := `
id: journey-3
type: e2e
name: "Journey 3"
project:
  name: "test3"
  scaffold: "test/scaffold3"
sessions:
  - id: s1
    phase: feature
    task:
      description: "subdir task"
      acceptance: {}
`
	if err := os.WriteFile(filepath.Join(subDir, "journey3.yaml"), []byte(journey3), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Load all journeys
	journeys, err := LoadE2EJourneys(tmpDir)
	if err != nil {
		t.Fatalf("LoadE2EJourneys failed: %v", err)
	}

	// Should find 3 journeys (skipping the non-journey)
	if len(journeys) != 3 {
		t.Errorf("Expected 3 journeys, got %d", len(journeys))
	}

	// Verify we got the right journeys
	ids := make(map[string]bool)
	for _, j := range journeys {
		ids[j.ID] = true
	}
	for _, expected := range []string{"journey-1", "journey-2", "journey-3"} {
		if !ids[expected] {
			t.Errorf("Expected to find journey '%s'", expected)
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
