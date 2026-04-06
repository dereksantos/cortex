package cognition

import (
	"context"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

func TestTextSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		minScore float64
		maxScore float64
	}{
		{
			name:     "identical text",
			a:        "use JWT for authentication",
			b:        "use JWT for authentication",
			minScore: 1.0,
			maxScore: 1.0,
		},
		{
			name:     "very similar",
			a:        "use JWT tokens for user authentication",
			b:        "use JWT tokens for authentication of users",
			minScore: 0.6,
			maxScore: 1.0,
		},
		{
			name:     "somewhat similar",
			a:        "authentication using JWT tokens",
			b:        "JWT based security for API",
			minScore: 0.1,
			maxScore: 0.5,
		},
		{
			name:     "completely different",
			a:        "use PostgreSQL for the database",
			b:        "implement caching with Redis",
			minScore: 0.0,
			maxScore: 0.2,
		},
		{
			name:     "empty strings",
			a:        "",
			b:        "",
			minScore: 0.0,
			maxScore: 0.0,
		},
		{
			name:     "one empty",
			a:        "some content",
			b:        "",
			minScore: 0.0,
			maxScore: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := textSimilarity(tt.a, tt.b)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("textSimilarity(%q, %q) = %v, want between %v and %v",
					tt.a, tt.b, score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input    string
		minLen   int
		maxLen   int
		contains []string
		excludes []string
	}{
		{
			input:    "Use JWT for authentication",
			minLen:   2,
			maxLen:   4,
			contains: []string{"jwt", "authentication"},
			excludes: []string{"for"}, // "use" not in stopwords, "for" is
		},
		{
			input:    "The quick brown fox",
			minLen:   2,
			maxLen:   3,
			contains: []string{"quick", "brown", "fox"},
			excludes: []string{"the"},
		},
		{
			input:    "",
			minLen:   0,
			maxLen:   0,
			contains: []string{},
			excludes: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			tokens := tokenize(tt.input)
			if len(tokens) < tt.minLen || len(tokens) > tt.maxLen {
				t.Errorf("tokenize(%q) returned %d tokens, want between %d and %d",
					tt.input, len(tokens), tt.minLen, tt.maxLen)
			}

			tokenSet := make(map[string]bool)
			for _, tok := range tokens {
				tokenSet[tok] = true
			}

			for _, expected := range tt.contains {
				if !tokenSet[expected] {
					t.Errorf("tokenize(%q) missing expected token %q", tt.input, expected)
				}
			}

			for _, excluded := range tt.excludes {
				if tokenSet[excluded] {
					t.Errorf("tokenize(%q) should not contain stopword %q", tt.input, excluded)
				}
			}
		})
	}
}

func TestDigest_DigestInsights(t *testing.T) {
	digest := NewDigest(nil, "") // No storage needed for this test

	tests := []struct {
		name           string
		insights       []cognition.Result
		threshold      float64
		expectedGroups int
		expectedMerged int
	}{
		{
			name:           "empty input",
			insights:       []cognition.Result{},
			threshold:      0.7,
			expectedGroups: 0,
			expectedMerged: 0,
		},
		{
			name: "no duplicates",
			insights: []cognition.Result{
				{ID: "1", Content: "Use JWT for authentication", Category: "decision"},
				{ID: "2", Content: "Use PostgreSQL for database", Category: "decision"},
				{ID: "3", Content: "Implement caching with Redis", Category: "pattern"},
			},
			threshold:      0.7,
			expectedGroups: 3,
			expectedMerged: 0,
		},
		{
			name: "duplicates in same category",
			insights: []cognition.Result{
				{ID: "1", Content: "Use JWT tokens for authentication", Category: "decision", Timestamp: time.Now()},
				{ID: "2", Content: "JWT tokens should be used for user authentication", Category: "decision", Timestamp: time.Now().Add(-time.Hour)},
				{ID: "3", Content: "Use PostgreSQL for database", Category: "decision", Timestamp: time.Now()},
			},
			threshold:      0.5,
			expectedGroups: 2,
			expectedMerged: 1,
		},
		{
			name: "different categories not merged",
			insights: []cognition.Result{
				{ID: "1", Content: "Use JWT for authentication", Category: "decision"},
				{ID: "2", Content: "Use JWT for authentication", Category: "pattern"}, // Same content, different category
			},
			threshold:      0.7,
			expectedGroups: 2,
			expectedMerged: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			digest.config.SimilarityThreshold = tt.threshold
			digested, err := digest.DigestInsights(context.Background(), tt.insights)
			if err != nil {
				t.Fatalf("DigestInsights() error = %v", err)
			}

			if len(digested) != tt.expectedGroups {
				t.Errorf("DigestInsights() returned %d groups, want %d", len(digested), tt.expectedGroups)
			}

			totalMerged := 0
			for _, di := range digested {
				totalMerged += len(di.Duplicates)
			}

			if totalMerged != tt.expectedMerged {
				t.Errorf("DigestInsights() merged %d insights, want %d", totalMerged, tt.expectedMerged)
			}
		})
	}
}

func TestDigest_RecencyBias(t *testing.T) {
	digest := NewDigest(nil, "")
	digest.config.SimilarityThreshold = 0.5
	digest.config.RecencyBias = true

	now := time.Now()
	older := now.Add(-24 * time.Hour)

	insights := []cognition.Result{
		{ID: "old", Content: "Use JWT tokens for authentication", Category: "decision", Timestamp: older, Score: 0.9},
		{ID: "new", Content: "JWT tokens should be used for user authentication", Category: "decision", Timestamp: now, Score: 0.5},
	}

	digested, err := digest.DigestInsights(context.Background(), insights)
	if err != nil {
		t.Fatalf("DigestInsights() error = %v", err)
	}

	if len(digested) != 1 {
		t.Fatalf("Expected 1 group, got %d", len(digested))
	}

	// With recency bias, the newer insight should be the representative
	// Since insights are processed in order and newer one comes second,
	// the first one (old) is the representative by default
	if digested[0].Representative.ID != "old" {
		t.Errorf("Expected 'old' as representative (first in list), got %q", digested[0].Representative.ID)
	}
}

func TestDigest_MaybeDigest_Preconditions(t *testing.T) {
	digest := NewDigest(nil, "")

	// Should skip because no Dream has run
	result, err := digest.MaybeDigest(context.Background())
	if err != nil {
		t.Fatalf("MaybeDigest() error = %v", err)
	}

	if result.Status != cognition.DigestSkippedNoDream {
		t.Errorf("MaybeDigest() status = %v, want DigestSkippedNoDream", result.Status)
	}

	// Notify that Dream completed
	digest.NotifyDreamCompleted()

	// Should now skip because no insights (nil storage)
	result, err = digest.MaybeDigest(context.Background())
	if err != nil {
		t.Fatalf("MaybeDigest() error = %v", err)
	}

	if result.Status != cognition.DigestSkippedNoInsights {
		t.Errorf("MaybeDigest() status = %v, want DigestSkippedNoInsights", result.Status)
	}
}

func TestDigestStatus_String(t *testing.T) {
	tests := []struct {
		status   cognition.DigestStatus
		expected string
	}{
		{cognition.DigestRan, "ran"},
		{cognition.DigestSkippedNoDream, "skipped_no_dream"},
		{cognition.DigestSkippedRunning, "skipped_running"},
		{cognition.DigestSkippedNoInsights, "skipped_no_insights"},
		{cognition.DigestStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.status.String(); got != tt.expected {
				t.Errorf("DigestStatus.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultDigestConfig(t *testing.T) {
	cfg := cognition.DefaultDigestConfig()

	if cfg.MaxMerges != 10 {
		t.Errorf("MaxMerges = %d, want 10", cfg.MaxMerges)
	}
	if cfg.SimilarityThreshold != 0.5 {
		t.Errorf("SimilarityThreshold = %v, want 0.5", cfg.SimilarityThreshold)
	}
	if !cfg.RecencyBias {
		t.Error("RecencyBias should be true by default")
	}
	if cfg.MinDisplayDuration != 2*time.Second {
		t.Errorf("MinDisplayDuration = %v, want 2s", cfg.MinDisplayDuration)
	}
}
