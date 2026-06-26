package cognition

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Embedder budget for the Reflex query-embed. A purely local embedder returns
// in single-digit ms, but a network embedder (the fleet's CPU `embedder` over
// HTTP) measures ~50–120ms, so a 100ms cap trips intermittently and silently
// drops semantic search to text. The default below accommodates the network
// embedder; the circuit breaker still protects the path when the server is
// genuinely wedged, and CORTEX_REFLEX_EMBED_TIMEOUT_MS tunes it for slower or
// faster silicon. The foreground retrieve() bounds the whole step at 6s.
const (
	defaultEmbedTimeoutMS = 1000
	embedFailureThreshold = 2
	embedCooldown         = 60 * time.Second
)

// embedTimeout returns the per-embed-call budget, honoring
// CORTEX_REFLEX_EMBED_TIMEOUT_MS when set to a positive integer.
func embedTimeout() time.Duration {
	if v := os.Getenv("CORTEX_REFLEX_EMBED_TIMEOUT_MS"); v != "" {
		var ms int
		if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultEmbedTimeoutMS * time.Millisecond
}

// Reflex implements cognition.Reflexer for fast mechanical retrieval.
// Uses semantic search (embeddings) when available, falls back to text search.
// Target latency: <50ms with embeddings, <10ms without.
type Reflex struct {
	storage  *storage.Storage
	embedder llm.Embedder // optional, for semantic search
	scorer   *Scorer

	embedFailures  atomic.Int32 // consecutive embed failures
	embedSkipUntil atomic.Int64 // unix nanos; if now < this, skip embedder
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

// shouldTryEmbedder reports whether the circuit breaker permits an embed call.
func (r *Reflex) shouldTryEmbedder() bool {
	if r.embedder == nil {
		return false
	}
	return time.Now().UnixNano() >= r.embedSkipUntil.Load()
}

func (r *Reflex) recordEmbedFailure() {
	if r.embedFailures.Add(1) >= embedFailureThreshold {
		r.embedSkipUntil.Store(time.Now().Add(embedCooldown).UnixNano())
	}
}

func (r *Reflex) recordEmbedSuccess() {
	r.embedFailures.Store(0)
	r.embedSkipUntil.Store(0)
}

// Reflex performs fast mechanical retrieval using semantic search or text matching.
// Tries embedding-based vector search first, falls back to text search.
func (r *Reflex) Reflex(ctx context.Context, q cognition.Query) ([]cognition.Result, error) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		// The <50ms target holds for the text-only path. With a network
		// embedder wired in, a query-embed legitimately costs up to the embed
		// budget, so warn only past that (plus margin) — otherwise every
		// semantic call is noise. Must go to stderr: `cortex search --json`
		// owns stdout, and a stdout-bound warning corrupts the JSON contract.
		warnAfter := 50 * time.Millisecond
		if r.embedder != nil {
			warnAfter = embedTimeout() + 250*time.Millisecond
		}
		if elapsed > warnAfter {
			fmt.Fprintf(os.Stderr, "[reflex] warning: took %v (target <%v)\n", elapsed, warnAfter)
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

	// 2. Semantic search via embeddings if available.
	// IsEmbeddingAvailable is intentionally NOT called here: it issues HTTP
	// probes whose http.Client timeout (30s) is not bounded by ctx, so a
	// hung Ollama would blow the latency budget before we even get to Embed.
	// Instead we gate via the circuit breaker and bound Embed with a short
	// context timeout — failures fall through to text search.
	semanticDone := false
	if q.Text != "" && r.shouldTryEmbedder() {
		embedCtx, cancel := context.WithTimeout(ctx, embedTimeout())
		queryVec, err := r.embedder.Embed(embedCtx, q.Text)
		cancel()
		if err != nil {
			r.recordEmbedFailure()
		} else if len(queryVec) > 0 {
			r.recordEmbedSuccess()
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

	// Drop anything a contradiction has retracted (e.g. a stale fact superseded
	// by a fresher, tool-checked one) so it can never be served again.
	candidates = r.filterRetracted(candidates)

	// Deduplicate
	candidates = Deduplicate(candidates)

	// Score and rank
	candidates = r.scorer.ScoreAndRank(candidates, q)

	// Provenance + recency weighting: a tool-checked, recent capture should
	// outrank an unverified or stale one of equal textual relevance, so a
	// confabulated assertion can't be served as fact over reality.
	candidates = weightByProvenance(candidates)

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

// filterRetracted drops candidates whose ID has a recorded retraction. The ID
// space matches what Reflect retracts (cognition.Result.ID), so a contradiction
// resolved on one turn hides the loser on every subsequent retrieval.
func (r *Reflex) filterRetracted(candidates []cognition.Result) []cognition.Result {
	out := candidates[:0]
	for _, c := range candidates {
		if c.ID != "" && r.storage.IsRetracted(c.ID) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Provenance/recency weighting knobs. unverifiedPenalty multiplies the score of
// content not grounded in tool inspection; recencyFloor is the smallest recency
// multiplier (reached at recencyHorizon age), so stale facts decay but never
// vanish. Deliberately gentle — this re-ranks ties, it doesn't hard-filter.
const (
	unverifiedPenalty = 0.6
	recencyFloor      = 0.7
	recencyHorizon    = 30 * 24 * time.Hour
)

// weightByProvenance re-scores candidates so verified, recent captures rank
// above unverified or stale ones, then re-sorts by the adjusted score. Pure and
// mechanical (no model): adjusted = score × verifiedFactor × recencyFactor.
func weightByProvenance(candidates []cognition.Result) []cognition.Result {
	now := time.Now()
	for i := range candidates {
		f := 1.0
		if v, _ := candidates[i].Metadata["verified"].(bool); !v {
			f *= unverifiedPenalty
		}
		f *= recencyFactor(candidates[i].Timestamp, now)
		candidates[i].Score *= f
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}

// recencyFactor decays linearly from 1.0 (now) to recencyFloor (recencyHorizon
// or older). A zero timestamp is treated as neutral (1.0) so unknown-age content
// isn't penalized for missing metadata.
func recencyFactor(ts, now time.Time) float64 {
	if ts.IsZero() {
		return 1.0
	}
	age := now.Sub(ts)
	if age <= 0 {
		return 1.0
	}
	if age >= recencyHorizon {
		return recencyFloor
	}
	frac := float64(age) / float64(recencyHorizon)
	return 1.0 - frac*(1.0-recencyFloor)
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
			"verified":   eventVerified(event),
		},
	}
}

// eventVerified reads an event's "verified" provenance bit (set by the loop
// from whether the turn ran tools). Absent/false → unverified.
func eventVerified(event *events.Event) bool {
	if event.Metadata == nil {
		return false
	}
	v, _ := event.Metadata["verified"].(bool)
	return v
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
			ID:        vr.ContentType + "-" + vr.ContentID,
			Content:   content,
			Category:  vr.ContentType,
			Score:     vr.Similarity,
			Timestamp: vr.CreatedAt,
			Metadata: map[string]any{
				"semantic_match": true,
				"verified":       vr.Verified,
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
