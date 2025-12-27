package cognition

import (
	"context"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

func TestScorer_Score(t *testing.T) {
	scorer := NewScorer()

	tests := []struct {
		name     string
		result   cognition.Result
		query    cognition.Query
		minScore float64
		maxScore float64
	}{
		{
			name: "exact text match",
			result: cognition.Result{
				Content:   "authentication JWT token security",
				Timestamp: time.Now(),
			},
			query: cognition.Query{
				Text: "authentication JWT",
			},
			minScore: 0.3,
			maxScore: 1.0,
		},
		{
			name: "tag overlap",
			result: cognition.Result{
				Content:   "some content",
				Tags:      []string{"auth", "security"},
				Timestamp: time.Now(),
			},
			query: cognition.Query{
				Tags: []string{"auth"},
			},
			minScore: 0.2,
			maxScore: 1.0,
		},
		{
			name: "category match",
			result: cognition.Result{
				Content:   "some content",
				Category:  "decision",
				Timestamp: time.Now(),
			},
			query: cognition.Query{
				Categories: []string{"decision"},
			},
			minScore: 0.1,
			maxScore: 1.0,
		},
		{
			name: "no match",
			result: cognition.Result{
				Content:   "unrelated content about databases",
				Timestamp: time.Now().Add(-365 * 24 * time.Hour), // Old
			},
			query: cognition.Query{
				Text: "authentication security",
			},
			minScore: 0.0,
			maxScore: 0.5, // Allow for base scores from recency/importance
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scorer.Score(tt.result, tt.query)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("Score() = %v, want between %v and %v", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestScorer_ScoreAndRank(t *testing.T) {
	scorer := NewScorer()

	results := []cognition.Result{
		{ID: "low", Content: "unrelated", Timestamp: time.Now()},
		{ID: "high", Content: "authentication JWT token", Tags: []string{"auth"}, Timestamp: time.Now()},
		{ID: "medium", Content: "security patterns", Timestamp: time.Now()},
	}

	query := cognition.Query{
		Text: "authentication JWT security",
		Tags: []string{"auth"},
	}

	ranked := scorer.ScoreAndRank(results, query)

	if len(ranked) != 3 {
		t.Errorf("ScoreAndRank() returned %d results, want 3", len(ranked))
	}

	// High should be first (best match on both text and tags)
	if ranked[0].ID != "high" {
		t.Errorf("ScoreAndRank() first result = %s, want 'high'", ranked[0].ID)
	}

	// Verify scores are descending
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Score > ranked[i-1].Score {
			t.Errorf("ScoreAndRank() scores not descending: %v > %v at position %d",
				ranked[i].Score, ranked[i-1].Score, i)
		}
	}
}

func TestActivityTracker(t *testing.T) {
	tracker := NewActivityTracker()

	// Initially idle
	if !tracker.IsIdle() {
		t.Error("New tracker should be idle")
	}

	// Record some activity
	tracker.RecordRetrieve()

	// Should no longer be idle
	if tracker.IsIdle() {
		t.Error("Tracker should not be idle after RecordRetrieve")
	}

	// Activity level should be positive
	level := tracker.ActivityLevel()
	if level <= 0 {
		t.Errorf("ActivityLevel() = %v, want > 0", level)
	}
}

func TestDeduplicate(t *testing.T) {
	results := []cognition.Result{
		{ID: "a", Content: "first"},
		{ID: "b", Content: "second"},
		{ID: "a", Content: "duplicate of first"},
		{ID: "c", Content: "third"},
		{ID: "b", Content: "duplicate of second"},
	}

	unique := Deduplicate(results)

	if len(unique) != 3 {
		t.Errorf("Deduplicate() returned %d results, want 3", len(unique))
	}

	// Check first occurrence is kept
	if unique[0].Content != "first" {
		t.Errorf("Deduplicate() kept wrong version of 'a'")
	}
}

func TestExtractTerms(t *testing.T) {
	tests := []struct {
		input    string
		minTerms int
		maxTerms int
	}{
		{"The quick brown fox", 2, 3},                            // "quick", "brown", "fox"
		{"How do I authenticate with JWT?", 2, 3},                // "authenticate", "jwt"
		{"a an the is are", 0, 0},                                // All stopwords
		{"authentication authorization security", 3, 3},          // Technical terms
		{"I want to help me please", 0, 5},                       // Some words may not be stopwords
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			terms := extractTerms(tt.input)
			if len(terms) < tt.minTerms || len(terms) > tt.maxTerms {
				t.Errorf("extractTerms(%q) = %v (len=%d), want between %d and %d terms",
					tt.input, terms, len(terms), tt.minTerms, tt.maxTerms)
			}
		})
	}
}

func TestFormatter_FormatForInjection(t *testing.T) {
	formatter := NewFormatter()

	results := []cognition.Result{
		{
			ID:       "1",
			Content:  "Use JWT for authentication",
			Category: "decision",
			Tags:     []string{"auth", "security"},
		},
		{
			ID:       "2",
			Content:  "Always validate input",
			Category: "pattern",
			Tags:     []string{"security"},
		},
	}

	output := formatter.FormatForInjection(results)

	// Check basic structure
	if output == "" {
		t.Error("FormatForInjection() returned empty string")
	}

	// Check header
	if !containsString(output, "Relevant Context from Cortex") {
		t.Error("Output missing header")
	}

	// Check content is included
	if !containsString(output, "JWT") {
		t.Error("Output missing result content")
	}
}

func TestResolve_MakeDecision(t *testing.T) {
	resolve := NewResolve()

	tests := []struct {
		name     string
		results  []cognition.Result
		expected cognition.Decision
	}{
		{
			name:     "no results",
			results:  []cognition.Result{},
			expected: cognition.Discard,
		},
		{
			name: "high score",
			results: []cognition.Result{
				{ID: "1", Score: 0.8},
				{ID: "2", Score: 0.7},
			},
			expected: cognition.Inject,
		},
		{
			name: "medium score",
			results: []cognition.Result{
				{ID: "1", Score: 0.35},
			},
			expected: cognition.Queue,
		},
		{
			name: "low score",
			results: []cognition.Result{
				{ID: "1", Score: 0.1},
			},
			expected: cognition.Discard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolve.Resolve(context.Background(), cognition.Query{}, tt.results)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if result.Decision != tt.expected {
				t.Errorf("Resolve() decision = %v, want %v", result.Decision, tt.expected)
			}
		})
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
