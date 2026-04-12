// Package cognition implements the five cognitive modes for context retrieval.
package cognition

import (
	"math"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// Scorer calculates relevance scores for results.
// Uses a weighted combination of signals since embeddings are not yet available.
type Scorer struct {
	// Weights for different scoring signals (must sum to 1.0)
	TextWeight       float64
	TagWeight        float64
	CategoryWeight   float64
	RecencyWeight    float64
	ImportanceWeight float64
}

// NewScorer creates a scorer with default weights.
func NewScorer() *Scorer {
	return &Scorer{
		TextWeight:       0.40,
		TagWeight:        0.20,
		CategoryWeight:   0.15,
		RecencyWeight:    0.15,
		ImportanceWeight: 0.10,
	}
}

// Score calculates a relevance score for a single result against a query.
// Returns a value between 0.0 and 1.0.
func (s *Scorer) Score(result cognition.Result, q cognition.Query) float64 {
	score := 0.0

	// Text relevance (TF-IDF approximation)
	if q.Text != "" {
		score += s.textRelevance(result.Content, q.Text) * s.TextWeight
	} else {
		// Redistribute text weight if no query text
		score += s.TextWeight * 0.5 // Base score
	}

	// Tag overlap
	score += s.tagOverlap(result.Tags, q.Tags) * s.TagWeight

	// Category match
	if s.categoryMatches(result.Category, q.Categories) {
		score += s.CategoryWeight
	}

	// Recency decay
	score += s.recencyScore(result.Timestamp) * s.RecencyWeight

	// Importance boost (from metadata if available)
	score += s.importanceScore(result.Metadata) * s.ImportanceWeight

	return math.Min(score, 1.0)
}

// ScoreAndRank scores all results and returns them sorted by score descending.
func (s *Scorer) ScoreAndRank(results []cognition.Result, q cognition.Query) []cognition.Result {
	// Score each result
	for i := range results {
		results[i].Score = s.Score(results[i], q)
	}

	// Sort by score descending (simple bubble sort for small lists)
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// textRelevance calculates text similarity using term overlap.
// This is a simple TF-IDF approximation without embeddings.
func (s *Scorer) textRelevance(content, queryText string) float64 {
	if content == "" || queryText == "" {
		return 0.0
	}

	contentLower := strings.ToLower(content)
	queryLower := strings.ToLower(queryText)

	// Extract terms from query
	queryTerms := ExtractTerms(queryLower)
	if len(queryTerms) == 0 {
		return 0.0
	}

	// Count matching terms
	matches := 0
	for _, term := range queryTerms {
		if strings.Contains(contentLower, term) {
			matches++
		}
	}

	// Calculate coverage ratio
	coverage := float64(matches) / float64(len(queryTerms))

	// Boost for exact phrase match
	if strings.Contains(contentLower, queryLower) {
		coverage = math.Min(coverage+0.3, 1.0)
	}

	return coverage
}

// tagOverlap calculates the proportion of query tags found in result tags.
func (s *Scorer) tagOverlap(resultTags, queryTags []string) float64 {
	if len(queryTags) == 0 {
		// No tag filter, give partial credit
		return 0.5
	}

	if len(resultTags) == 0 {
		return 0.0
	}

	// Build set of result tags (lowercase)
	tagSet := make(map[string]bool)
	for _, tag := range resultTags {
		tagSet[strings.ToLower(tag)] = true
	}

	// Count matches
	matches := 0
	for _, tag := range queryTags {
		if tagSet[strings.ToLower(tag)] {
			matches++
		}
	}

	return float64(matches) / float64(len(queryTags))
}

// categoryMatches checks if result category is in the query categories.
func (s *Scorer) categoryMatches(resultCategory string, queryCategories []string) bool {
	if len(queryCategories) == 0 {
		return true // No filter means all categories match
	}

	resultCatLower := strings.ToLower(resultCategory)
	for _, cat := range queryCategories {
		if strings.ToLower(cat) == resultCatLower {
			return true
		}
	}

	return false
}

// recencyScore applies a decay function based on age.
// Returns 1.0 for recent items, decaying toward 0 over time.
func (s *Scorer) recencyScore(timestamp time.Time) float64 {
	if timestamp.IsZero() {
		return 0.5 // Unknown age gets neutral score
	}

	age := time.Since(timestamp)
	days := age.Hours() / 24

	// Exponential decay with half-life of 30 days
	// score = 1 / (1 + days/30)
	return 1.0 / (1.0 + days/30.0)
}

// importanceScore extracts importance from metadata if available.
func (s *Scorer) importanceScore(metadata map[string]any) float64 {
	if metadata == nil {
		return 0.5 // Unknown importance gets neutral score
	}

	// Try to get importance value (stored as 0-10 in insights)
	if imp, ok := metadata["importance"]; ok {
		switch v := imp.(type) {
		case int:
			return float64(v) / 10.0
		case float64:
			return v / 10.0
		case int64:
			return float64(v) / 10.0
		}
	}

	return 0.5
}

// ExtractTerms splits text into searchable terms, filtering stopwords.
// This is the canonical implementation - use this instead of duplicating stop word logic.
func ExtractTerms(text string) []string {
	// Common stopwords to filter
	stopwords := map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"been": true, "being": true, "have": true, "has": true, "had": true,
		"do": true, "does": true, "did": true, "will": true, "would": true,
		"could": true, "should": true, "may": true, "might": true, "must": true,
		"shall": true, "can": true, "this": true, "that": true, "these": true,
		"those": true, "i": true, "you": true, "he": true, "she": true,
		"it": true, "we": true, "they": true, "what": true, "which": true,
		"who": true, "whom": true, "how": true, "when": true, "where": true,
		"why": true, "if": true, "then": true, "else": true, "so": true,
		"as": true, "my": true, "your": true, "our": true, "their": true,
	}

	// Split on whitespace and punctuation
	words := strings.FieldsFunc(text, func(c rune) bool {
		return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9')
	})

	// Filter stopwords and short terms
	var terms []string
	for _, word := range words {
		word = strings.ToLower(word)
		if len(word) >= 2 && !stopwords[word] {
			terms = append(terms, word)
		}
	}

	return terms
}

// Deduplicate removes duplicate results by ID.
func Deduplicate(results []cognition.Result) []cognition.Result {
	seen := make(map[string]bool)
	var unique []cognition.Result

	for _, r := range results {
		if !seen[r.ID] {
			seen[r.ID] = true
			unique = append(unique, r)
		}
	}

	return unique
}
