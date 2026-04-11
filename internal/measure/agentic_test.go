package measure

import (
	"context"
	"fmt"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// mockProvider implements llm.Provider with canned JSON responses.
type mockProvider struct {
	responses map[string]string // keyword -> response
	available bool
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		responses: map[string]string{
			"classify the scope": `{"classification": "small", "explanation": "Single function change"}`,
			"clarity": `{"clarity_score": 0.8, "ambiguities": [], "missing_constraints": ["error handling approach"]}`,
			"broken into smaller": `{"decomposable": false, "sub_tasks": [], "independent_subs": 0}`,
			"context window": `{"fit_score": 0.9, "explanation": "Simple change fits easily"}`,
		},
		available: true,
	}
}

func (m *mockProvider) Generate(ctx context.Context, prompt string) (string, error) {
	return m.matchResponse(prompt)
}

func (m *mockProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return m.matchResponse(prompt)
}

func (m *mockProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	resp, err := m.matchResponse(prompt)
	return resp, llm.GenerationStats{InputTokens: 100, OutputTokens: 50}, err
}

func (m *mockProvider) IsAvailable() bool { return m.available }
func (m *mockProvider) Name() string       { return "mock" }

func (m *mockProvider) matchResponse(prompt string) (string, error) {
	// Match longest keyword first to avoid ambiguous matches
	bestKey := ""
	for keyword := range m.responses {
		if containsCI(prompt, keyword) && len(keyword) > len(bestKey) {
			bestKey = keyword
		}
	}
	if bestKey != "" {
		return m.responses[bestKey], nil
	}
	return `{"error": "no match"}`, fmt.Errorf("no matching response for prompt")
}

func containsCI(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a != b && a != b+32 && a != b-32 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestClassifyScope(t *testing.T) {
	mock := newMockProvider()
	ctx := context.Background()

	classification, explanation, err := classifyScope(ctx, mock, "Add a login handler")
	if err != nil {
		t.Fatalf("classifyScope() error: %v", err)
	}

	if classification != "small" {
		t.Errorf("classification = %q, want %q", classification, "small")
	}
	if explanation == "" {
		t.Error("explanation should not be empty")
	}
}

func TestAssessClarity(t *testing.T) {
	mock := newMockProvider()
	ctx := context.Background()

	score, ambiguities, missing, err := assessClarity(ctx, mock, "Add validation to the handler")
	if err != nil {
		t.Fatalf("assessClarity() error: %v", err)
	}

	if score < 0 || score > 1 {
		t.Errorf("clarity score = %.2f, want [0, 1]", score)
	}
	if ambiguities == nil {
		t.Error("ambiguities should not be nil")
	}
	if len(missing) != 1 || missing[0] != "error handling approach" {
		t.Errorf("missing constraints = %v, want [error handling approach]", missing)
	}
}

func TestCheckDecomposability(t *testing.T) {
	mock := newMockProvider()
	ctx := context.Background()

	decomposable, subTasks, independent, err := checkDecomposability(ctx, mock, "Add a handler")
	if err != nil {
		t.Fatalf("checkDecomposability() error: %v", err)
	}

	if decomposable {
		t.Error("decomposable = true, want false for simple prompt")
	}
	if len(subTasks) != 0 {
		t.Errorf("subTasks = %v, want empty", subTasks)
	}
	if independent != 0 {
		t.Errorf("independent = %d, want 0", independent)
	}
}

func TestCheckDecomposabilityMultiConcern(t *testing.T) {
	mock := newMockProvider()
	// Override response for decomposable prompt
	mock.responses["broken into smaller"] = `{"decomposable": true, "sub_tasks": ["Add pooling", "Add retry", "Update callers"], "independent_subs": 2}`
	ctx := context.Background()

	decomposable, subTasks, independent, err := checkDecomposability(ctx, mock, "Refactor DB, add pooling, add retry, update callers")
	if err != nil {
		t.Fatalf("checkDecomposability() error: %v", err)
	}

	if !decomposable {
		t.Error("decomposable = false, want true")
	}
	if len(subTasks) != 3 {
		t.Errorf("len(subTasks) = %d, want 3", len(subTasks))
	}
	if independent != 2 {
		t.Errorf("independent = %d, want 2", independent)
	}
}

func TestScoreContextWindowFit(t *testing.T) {
	mock := newMockProvider()
	ctx := context.Background()

	score, explanation, err := scoreContextWindowFit(ctx, mock, "Add a small handler", 8192)
	if err != nil {
		t.Fatalf("scoreContextWindowFit() error: %v", err)
	}

	if score < 0 || score > 1 {
		t.Errorf("fit score = %.2f, want [0, 1]", score)
	}
	if explanation == "" {
		t.Error("explanation should not be empty")
	}
}

func TestMeasureAgenticFull(t *testing.T) {
	mock := newMockProvider()
	m := New(mock)
	ctx := context.Background()

	result, err := m.Measure(ctx, "Add JWT validation to handleLogin() in pkg/auth/handler.go")
	if err != nil {
		t.Fatalf("Measure() error: %v", err)
	}

	if result.Agentic == nil {
		t.Fatal("Agentic result should not be nil when provider available")
	}
	if result.Agentic.ScopeClassification == "" {
		t.Error("ScopeClassification should not be empty")
	}
	if result.Promptability < 0 || result.Promptability > 1 {
		t.Errorf("Promptability = %.2f, want [0, 1]", result.Promptability)
	}
	if result.Grade == "" {
		t.Error("Grade should not be empty")
	}
}

func TestMeasureAgenticUnavailable(t *testing.T) {
	mock := newMockProvider()
	mock.available = false
	m := New(mock)
	ctx := context.Background()

	result, err := m.Measure(ctx, "Add a handler")
	if err != nil {
		t.Fatalf("Measure() error: %v", err)
	}

	if result.Agentic != nil {
		t.Error("Agentic result should be nil when provider unavailable")
	}
	if result.Mechanical == nil {
		t.Error("Mechanical result should not be nil")
	}
}

func TestParseJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
	}{
		{"clean json", `{"classification": "small"}`, false},
		{"markdown wrapped", "```json\n{\"classification\": \"small\"}\n```", false},
		{"text before", "Here's the result: {\"classification\": \"small\"}", false},
		{"no json", "This has no JSON at all", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result scopeResponse
			err := parseJSON(tt.input, &result)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
