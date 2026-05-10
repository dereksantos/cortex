package measure

import (
	"testing"
)

func TestCountActionVerbs(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"single verb", "Create a new handler", 1},
		{"multiple verbs", "Create the handler, update the config, and delete the old entry", 3},
		{"no verbs", "The authentication middleware", 0},
		{"refactor verb", "Refactor the database layer", 1},
		{"fix and test", "Fix the login bug and test the result", 2},
		{"empty", "", 0},
		{"past tense verb", "The config was updated yesterday", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountActionVerbs(tt.prompt)
			if got != tt.want {
				t.Errorf("CountActionVerbs(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestCountFileReferences(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"go file", "Update pkg/auth/middleware.go", 1},
		{"multiple files", "Edit main.go and handler.go", 2},
		{"path with dirs", "Look at internal/storage/sqlite.go", 1},
		{"no files", "Fix the authentication bug", 0},
		{"typescript file", "Update src/components/App.tsx", 1},
		{"yaml file", "Edit config.yaml", 1},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountFileReferences(tt.prompt)
			if got != tt.want {
				t.Errorf("CountFileReferences(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestCountConditionals(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"no conditionals", "Add a new endpoint", 0},
		{"single if", "If the user is admin, show the panel", 1},
		{"multiple", "If logged in, show dashboard, otherwise show login, unless disabled", 3},
		{"when", "When the cache expires, refresh it", 1},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountConditionals(tt.prompt)
			if got != tt.want {
				t.Errorf("CountConditionals(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestCountConcerns(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"single concern", "Add JWT validation to the auth endpoint", 1},
		{"two concerns with and", "Add validation and update the tests", 2},
		{"three concerns", "Add validation and update tests and fix the docs", 3},
		{"numbered list", "1. Add auth\n2. Update tests\n3. Fix docs", 2},
		{"bullet list", "- Add auth\n- Update tests\n- Fix docs", 2},
		{"also separator", "Add auth, also update the tests", 2},
		{"empty", "", 1}, // base concern
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountConcerns(tt.prompt)
			if got != tt.want {
				t.Errorf("CountConcerns(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestScoreSpecificity(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		wantMin float64
		wantMax float64
	}{
		{"very specific", "Update handleLogin() in pkg/auth/handler.go at line 42", 0.8, 1.0},
		{"somewhat specific", "Update the handler in pkg/auth/handler.go", 0.3, 0.8},
		{"vague", "Fix the bug in the code somewhere", 0.0, 0.3},
		{"empty", "", 0.0, 0.0},
		{"inline code", "Change `userID` to `user_id` in the struct", 0.3, 0.7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScoreSpecificity(tt.prompt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ScoreSpecificity(%q) = %.2f, want [%.2f, %.2f]", tt.prompt, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCountConstraints(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"no constraints", "Add a login page", 0},
		{"must", "The response must include a status code", 1},
		{"must not", "Must not use external libraries", 1},
		{"multiple", "Must use Go, never use globals, always return errors", 3},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountConstraints(tt.prompt)
			if got != tt.want {
				t.Errorf("CountConstraints(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestHasExamples(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"code block", "Do this:\n```go\nfunc main() {}\n```", true},
		{"no examples", "Add error handling to the function", false},
		{"output marker", "Expected:\n> {\"status\": \"ok\"}", true},
		{"inline code name only", "Rename `foo` to `bar`", false},
		{"inline code with operators", "Use `x := foo()` pattern", true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasExamples(tt.prompt)
			if got != tt.want {
				t.Errorf("HasExamples(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestCountQuestions(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"no questions", "Add error handling", 0},
		{"one question", "Should this use a mutex?", 1},
		{"multiple", "Should this use a mutex? Or a channel? What about sync.Once?", 3},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountQuestions(tt.prompt)
			if got != tt.want {
				t.Errorf("CountQuestions(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestScopeScore(t *testing.T) {
	tests := []struct {
		name         string
		verbs, files int
		conds, conc  int
		wantMin      float64
		wantMax      float64
	}{
		{"minimal", 1, 0, 0, 1, 0.0, 0.4},
		{"medium", 2, 2, 1, 2, 0.4, 0.8},
		{"large", 5, 4, 3, 4, 0.9, 1.0},
		{"zero", 0, 0, 0, 0, 0.0, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScopeScore(tt.verbs, tt.files, tt.conds, tt.conc)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ScopeScore(%d,%d,%d,%d) = %.2f, want [%.2f, %.2f]",
					tt.verbs, tt.files, tt.conds, tt.conc, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestClarityScore(t *testing.T) {
	tests := []struct {
		name        string
		specificity float64
		constraints int
		hasExamples bool
		questions   int
		wantMin     float64
		wantMax     float64
	}{
		{"high clarity", 0.9, 2, true, 0, 0.7, 1.0},
		{"low clarity", 0.1, 0, false, 3, 0.0, 0.15},
		{"medium", 0.5, 1, false, 1, 0.2, 0.6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClarityScore(tt.specificity, tt.constraints, tt.hasExamples, tt.questions)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ClarityScore(%.1f,%d,%v,%d) = %.2f, want [%.2f, %.2f]",
					tt.specificity, tt.constraints, tt.hasExamples, tt.questions, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestDecompositionScore(t *testing.T) {
	tests := []struct {
		name     string
		concerns int
		scope    float64
		clarity  float64
		wantMin  float64
		wantMax  float64
	}{
		{"single concern low scope", 1, 0.2, 0.8, 0.7, 1.0},
		{"many concerns high scope", 5, 0.9, 0.3, 0.0, 0.3},
		{"single concern high scope", 1, 0.8, 0.5, 0.4, 0.7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecompositionScore(tt.concerns, tt.scope, tt.clarity)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("DecompositionScore(%d,%.1f,%.1f) = %.2f, want [%.2f, %.2f]",
					tt.concerns, tt.scope, tt.clarity, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEstimateOutputTokens(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		wantMin int
		wantMax int
	}{
		{"simple", "Fix the typo", 100, 500},
		{"complex", "Create a new REST endpoint and add tests and update the docs", 400, 1500},
		{"with code", "Use this pattern:\n```go\nfunc foo() {}\n```\nApply to handler.go", 300, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateOutputTokens(tt.prompt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("EstimateOutputTokens(%q) = %d, want [%d, %d]", tt.prompt, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestPromptability(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		wantMin float64
		wantMax float64
	}{
		{
			"well decomposed",
			"Add JWT validation to handleLogin() in pkg/auth/handler.go. Must return 401 on invalid token.",
			0.5, 1.0,
		},
		{
			"vague and broad",
			"Fix the bug",
			0.1, 0.55,
		},
		{
			"multi-concern",
			"Refactor the database layer to use connection pooling and add retry logic and update all callers and write tests",
			0.0, 0.5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil)
			result, err := m.Measure(nil, tt.prompt)
			if err != nil {
				t.Fatalf("Measure() error: %v", err)
			}
			if result.Promptability < tt.wantMin || result.Promptability > tt.wantMax {
				t.Errorf("Promptability(%q) = %.2f, want [%.2f, %.2f]",
					tt.prompt, result.Promptability, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestGrade(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{0.95, "A"},
		{0.80, "A"},
		{0.70, "B"},
		{0.60, "B"},
		{0.50, "C"},
		{0.30, "D"},
		{0.10, "F"},
		{0.0, "F"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := Grade(tt.score)
			if got != tt.want {
				t.Errorf("Grade(%.2f) = %q, want %q", tt.score, got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	got := EstimateTokens("hello world test string")
	// 22 chars / 4 = 5
	if got != 5 {
		t.Errorf("EstimateTokens() = %d, want 5", got)
	}
}
