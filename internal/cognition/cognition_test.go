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
			terms := ExtractTerms(tt.input)
			if len(terms) < tt.minTerms || len(terms) > tt.maxTerms {
				t.Errorf("ExtractTerms(%q) = %v (len=%d), want between %d and %d terms",
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

func TestSessionPersistence(t *testing.T) {
	// Create temp directory
	tempDir := t.TempDir()

	// Create a session context with data
	ctx := &cognition.SessionContext{
		TopicWeights: map[string]float64{
			"authentication": 0.8,
			"database":       0.5,
		},
		RecentQueries: []cognition.Query{
			{Text: "how does auth work"},
		},
		WarmCache: map[string][]cognition.Result{
			"auth": {
				{ID: "1", Content: "Use JWT tokens", Score: 0.9},
			},
		},
		CachedReflect:          make(map[string][]cognition.Result),
		ResolvedContradictions: map[string]string{"a:b": "a"},
		LastUpdated:            time.Now(),
	}

	// Save session
	persister := NewSessionPersister(tempDir)
	if err := persister.Save(ctx); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load session
	loaded, err := persister.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify data
	if len(loaded.TopicWeights) != 2 {
		t.Errorf("TopicWeights len = %d, want 2", len(loaded.TopicWeights))
	}
	if loaded.TopicWeights["authentication"] != 0.8 {
		t.Errorf("TopicWeights[authentication] = %v, want 0.8", loaded.TopicWeights["authentication"])
	}

	// Verify WarmCache was restored
	if len(loaded.WarmCache) != 1 {
		t.Errorf("WarmCache len = %d, want 1", len(loaded.WarmCache))
	}
	if authResults, ok := loaded.WarmCache["auth"]; !ok || len(authResults) != 1 {
		t.Errorf("WarmCache[auth] not properly restored")
	}

	// Verify ResolvedContradictions
	if loaded.ResolvedContradictions["a:b"] != "a" {
		t.Errorf("ResolvedContradictions[a:b] = %v, want 'a'", loaded.ResolvedContradictions["a:b"])
	}
}

func TestSessionPersistence_LoadMissing(t *testing.T) {
	tempDir := t.TempDir()

	persister := NewSessionPersister(tempDir)
	loaded, err := persister.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Should return empty but initialized session
	if loaded == nil {
		t.Fatal("Load() returned nil for missing file")
	}
	if loaded.TopicWeights == nil {
		t.Error("Load() returned session with nil TopicWeights")
	}
}

func TestNewEmptySessionContext(t *testing.T) {
	ctx := NewEmptySessionContext()

	if ctx == nil {
		t.Fatal("NewEmptySessionContext() returned nil")
	}
	if ctx.TopicWeights == nil {
		t.Error("TopicWeights is nil")
	}
	if ctx.RecentQueries == nil {
		t.Error("RecentQueries is nil")
	}
	if ctx.WarmCache == nil {
		t.Error("WarmCache is nil")
	}
	if ctx.CachedReflect == nil {
		t.Error("CachedReflect is nil")
	}
	if ctx.ResolvedContradictions == nil {
		t.Error("ResolvedContradictions is nil")
	}
}

func TestSessionSaver(t *testing.T) {
	tempDir := t.TempDir()
	persister := NewSessionPersister(tempDir)
	saver := NewSessionSaver(persister, 100*time.Millisecond)

	ctx := NewEmptySessionContext()
	ctx.TopicWeights["test"] = 0.5

	// Not dirty initially
	if saver.MaybeSave(ctx) {
		t.Error("MaybeSave() should not save when not dirty")
	}

	// Mark dirty
	saver.MarkDirty()

	// Should save now
	if !saver.MaybeSave(ctx) {
		t.Error("MaybeSave() should save when dirty")
	}

	// Should not save again immediately (interval not passed)
	saver.MarkDirty()
	if saver.MaybeSave(ctx) {
		t.Error("MaybeSave() should not save before interval passes")
	}

	// Wait for interval
	time.Sleep(150 * time.Millisecond)
	saver.MarkDirty()
	if !saver.MaybeSave(ctx) {
		t.Error("MaybeSave() should save after interval passes")
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
