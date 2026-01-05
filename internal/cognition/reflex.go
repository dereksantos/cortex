package cognition

import (
	"context"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Reflex implements cognition.Reflexer for fast mechanical retrieval.
// Uses semantic search (embeddings) when available, falls back to text search.
// Target latency: <50ms with embeddings, <10ms without.
type Reflex struct {
	storage  *storage.Storage
	embedder llm.Embedder // optional, for semantic search
	scorer   *Scorer
}

// NewReflex creates a new Reflex instance.
// embedder is optional - if nil, falls back to text-based search.
func NewReflex(store *storage.Storage, embedder llm.Embedder) *Reflex {
	return &Reflex{
		storage:  store,
		embedder: embedder,
		scorer:   NewScorer(),
	}
}

// Reflex performs fast mechanical retrieval using semantic search or text matching.
// Tries embedding-based vector search first, falls back to text search.
func (r *Reflex) Reflex(ctx context.Context, q cognition.Query) ([]cognition.Result, error) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		if elapsed > 50*time.Millisecond {
			// Log warning but don't fail - latency target is aspirational
			fmt.Printf("[reflex] warning: took %v (target <50ms)\n", elapsed)
		}
	}()

	// Set default limit
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}

	var candidates []cognition.Result

	// 1. Category-based insight retrieval (fastest path)
	if len(q.Categories) > 0 {
		for _, cat := range q.Categories {
			insights, err := r.storage.GetInsightsByCategory(cat, limit)
			if err == nil {
				candidates = append(candidates, r.insightsToResults(insights)...)
			}
		}
	}

	// 2. Semantic search via embeddings if available
	semanticDone := false
	if q.Text != "" && r.embedder != nil && r.embedder.IsEmbeddingAvailable() {
		queryVec, err := r.embedder.Embed(ctx, q.Text)
		if err == nil && len(queryVec) > 0 {
			vectorResults, err := r.storage.SearchByVector(queryVec, limit, 0.3)
			if err == nil && len(vectorResults) > 0 {
				candidates = append(candidates, r.vectorResultsToResults(vectorResults)...)
				semanticDone = true
			}
		}
	}

	// 3. Text search fallback if semantic search unavailable or returned nothing
	if q.Text != "" && !semanticDone {
		terms := ExtractTerms(q.Text)
		if len(terms) > 0 {
			// Search insights by text
			insights, err := r.storage.GetRecentInsights(limit * 3)
			if err == nil {
				for _, insight := range insights {
					if r.matchesText(insight, terms) {
						candidates = append(candidates, r.insightToResult(insight))
					}
				}
			}

			// Also search events for broader context (search each term with OR logic)
			eventList, err := r.storage.SearchEventsMultiTerm(terms, limit)
			if err == nil {
				candidates = append(candidates, r.eventsToResults(eventList)...)
			}
		}
	}

	// 4. If still low on candidates, add recent important insights
	if len(candidates) < limit {
		important, err := r.storage.GetImportantInsights(5, limit)
		if err == nil {
			candidates = append(candidates, r.insightsToResults(important)...)
		}
	}

	// 5. If still low, add recent insights as fallback
	if len(candidates) < limit/2 {
		recent, err := r.storage.GetRecentInsights(limit)
		if err == nil {
			candidates = append(candidates, r.insightsToResults(recent)...)
		}
	}

	// Deduplicate
	candidates = Deduplicate(candidates)

	// Score and rank
	candidates = r.scorer.ScoreAndRank(candidates, q)

	// Apply threshold filter
	if q.Threshold > 0 {
		var filtered []cognition.Result
		for _, c := range candidates {
			if c.Score >= q.Threshold {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	// Apply limit
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Apply time filter if specified
	if !q.Since.IsZero() {
		var filtered []cognition.Result
		for _, c := range candidates {
			if c.Timestamp.After(q.Since) || c.Timestamp.Equal(q.Since) {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	return candidates, nil
}

// matchesText checks if an insight matches any of the search terms.
func (r *Reflex) matchesText(insight *storage.Insight, terms []string) bool {
	content := insight.Summary + " " + insight.Category
	for _, tag := range insight.Tags {
		content += " " + tag
	}

	for _, term := range terms {
		if containsIgnoreCase(content, term) {
			return true
		}
	}

	return false
}

// insightToResult converts a storage.Insight to cognition.Result.
func (r *Reflex) insightToResult(insight *storage.Insight) cognition.Result {
	// Use EventID as the result ID if it's set (e.g., corpus item IDs),
	// otherwise fall back to insight-N format
	id := fmt.Sprintf("insight-%d", insight.ID)
	if insight.EventID != "" {
		id = insight.EventID
	}

	return cognition.Result{
		ID:        id,
		Content:   insight.Summary,
		Category:  insight.Category,
		Timestamp: insight.CreatedAt,
		Tags:      insight.Tags,
		Metadata: map[string]any{
			"importance": insight.Importance,
			"event_id":   insight.EventID,
			"reasoning":  insight.Reasoning,
		},
	}
}

// insightsToResults converts multiple insights to results.
func (r *Reflex) insightsToResults(insights []*storage.Insight) []cognition.Result {
	results := make([]cognition.Result, 0, len(insights))
	for _, insight := range insights {
		results = append(results, r.insightToResult(insight))
	}
	return results
}

// eventToResult converts a storage event to cognition.Result.
func (r *Reflex) eventToResult(event *events.Event) cognition.Result {
	// Extract meaningful content from event
	content := event.ToolResult
	if len(content) > 500 {
		content = content[:500] + "..."
	}

	return cognition.Result{
		ID:        "event-" + event.ID,
		Content:   content,
		Category:  string(event.EventType),
		Source:    event.Source,
		Timestamp: event.Timestamp,
		Metadata: map[string]any{
			"tool_name":  event.ToolName,
			"tool_input": event.ToolInput,
		},
	}
}

// eventsToResults converts multiple events to results.
func (r *Reflex) eventsToResults(eventList []*events.Event) []cognition.Result {
	results := make([]cognition.Result, 0, len(eventList))
	for _, event := range eventList {
		results = append(results, r.eventToResult(event))
	}
	return results
}

// vectorResultsToResults converts vector search results to cognition results.
func (r *Reflex) vectorResultsToResults(vectorResults []storage.VectorSearchResult) []cognition.Result {
	results := make([]cognition.Result, 0, len(vectorResults))
	for _, vr := range vectorResults {
		content := vr.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		results = append(results, cognition.Result{
			ID:       vr.ContentType + "-" + vr.ContentID,
			Content:  content,
			Category: vr.ContentType,
			Score:    vr.Similarity,
			Metadata: map[string]any{
				"semantic_match": true,
			},
		})
	}
	return results
}

// containsIgnoreCase checks if s contains substr (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	sLower := make([]byte, len(s))
	substrLower := make([]byte, len(substr))

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			sLower[i] = c + 32
		} else {
			sLower[i] = c
		}
	}

	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			substrLower[i] = c + 32
		} else {
			substrLower[i] = c
		}
	}

	return bytesContains(sLower, substrLower)
}

// bytesContains checks if a contains b.
func bytesContains(a, b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if len(a) < len(b) {
		return false
	}

	for i := 0; i <= len(a)-len(b); i++ {
		match := true
		for j := 0; j < len(b); j++ {
			if a[i+j] != b[j] {
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

