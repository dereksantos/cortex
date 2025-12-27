package cognition

import (
	"context"
	"sync"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// Think implements cognition.Thinker for background processing during active work.
// Uses spare cycles to pre-compute results and improve Fast mode quality.
type Think struct {
	mu sync.Mutex

	// Components
	reflex   *Reflex
	reflect  *Reflect
	activity *ActivityTracker

	// Session state
	sessionCtx *cognition.SessionContext

	// Config
	config cognition.ThinkConfig

	// State
	running bool

	// State writer for daemon status updates
	stateWriter *StateWriter
}

// NewThink creates a new Think instance.
func NewThink(reflex *Reflex, reflect *Reflect, activity *ActivityTracker) *Think {
	return &Think{
		reflex:   reflex,
		reflect:  reflect,
		activity: activity,
		sessionCtx: &cognition.SessionContext{
			TopicWeights:           make(map[string]float64),
			RecentQueries:          make([]cognition.Query, 0),
			WarmCache:              make(map[string][]cognition.Result),
			CachedReflect:          make(map[string][]cognition.Result),
			ResolvedContradictions: make(map[string]string),
		},
		config: cognition.DefaultThinkConfig(),
	}
}

// SetStateWriter sets the state writer for daemon status updates.
func (t *Think) SetStateWriter(sw *StateWriter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stateWriter = sw
}

// MaybeThink attempts background processing if spare capacity exists.
func (t *Think) MaybeThink(ctx context.Context) (*cognition.ThinkResult, error) {
	// Check preconditions
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return &cognition.ThinkResult{Status: cognition.ThinkSkippedRunning}, nil
	}
	if t.activity.IsIdle() {
		t.mu.Unlock()
		return &cognition.ThinkResult{Status: cognition.ThinkSkippedIdle}, nil
	}
	t.running = true
	stateWriter := t.stateWriter
	t.mu.Unlock()

	// Write state on start
	if stateWriter != nil {
		stateWriter.WriteMode("think", "learning session patterns")
	}

	defer func() {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()

		// Write idle state on completion
		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
	}()

	start := time.Now()
	budget := t.activity.ThinkBudget(t.config.MinBudget, t.config.MaxBudget)
	ops := 0

	// Operation 1: Update topic weights from recent queries
	t.updateTopicWeights()
	ops++

	// Operation 2: Cache Reflect results for recent queries
	if ops < budget && len(t.sessionCtx.RecentQueries) > 0 {
		ops += t.cacheReflectResults(ctx, budget-ops)
	}

	// Operation 3: Pre-resolve contradictions
	if ops < budget {
		ops += t.resolveContradictions()
	}

	// Update timestamp
	t.mu.Lock()
	t.sessionCtx.LastUpdated = time.Now()
	t.mu.Unlock()

	return &cognition.ThinkResult{
		Status:     cognition.ThinkRan,
		Operations: ops,
		Duration:   time.Since(start),
	}, nil
}

// SessionContext returns the current session's accumulated context.
func (t *Think) SessionContext() *cognition.SessionContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionCtx
}

// RecordQuery adds a query to the recent queries list.
func (t *Think) RecordQuery(q cognition.Query) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sessionCtx.RecentQueries = append(t.sessionCtx.RecentQueries, q)

	// Keep only last 20 queries
	if len(t.sessionCtx.RecentQueries) > 20 {
		t.sessionCtx.RecentQueries = t.sessionCtx.RecentQueries[len(t.sessionCtx.RecentQueries)-20:]
	}
}

// CacheReflectResult stores a Reflect result for fast access.
func (t *Think) CacheReflectResult(queryText string, results []cognition.Result) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sessionCtx.CachedReflect[queryText] = results
}

// updateTopicWeights analyzes recent queries to detect session patterns.
func (t *Think) updateTopicWeights() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.sessionCtx.RecentQueries) == 0 {
		return
	}

	// Count term frequency across queries
	termCounts := make(map[string]int)
	for _, q := range t.sessionCtx.RecentQueries {
		terms := extractTerms(q.Text)
		for _, term := range terms {
			termCounts[term]++
		}
	}

	// Find max count for normalization
	maxCount := 1
	for _, count := range termCounts {
		if count > maxCount {
			maxCount = count
		}
	}

	// Update weights (normalized to 0-1)
	for term, count := range termCounts {
		// Only keep terms that appear multiple times
		if count >= 2 {
			t.sessionCtx.TopicWeights[term] = float64(count) / float64(maxCount)
		}
	}

	// Prune low-weight topics
	for term, weight := range t.sessionCtx.TopicWeights {
		if weight < 0.2 {
			delete(t.sessionCtx.TopicWeights, term)
		}
	}
}

// cacheReflectResults pre-computes Reflect for recent queries.
func (t *Think) cacheReflectResults(ctx context.Context, budget int) int {
	ops := 0

	t.mu.Lock()
	queries := make([]cognition.Query, len(t.sessionCtx.RecentQueries))
	copy(queries, t.sessionCtx.RecentQueries)
	t.mu.Unlock()

	// Process most recent queries first
	for i := len(queries) - 1; i >= 0 && ops < budget; i-- {
		q := queries[i]

		// Skip if already cached
		t.mu.Lock()
		_, exists := t.sessionCtx.CachedReflect[q.Text]
		t.mu.Unlock()

		if exists {
			continue
		}

		// Run Reflex + Reflect
		candidates, err := t.reflex.Reflex(ctx, q)
		if err != nil {
			continue
		}

		if len(candidates) > 0 && t.reflect != nil {
			reflected, err := t.reflect.Reflect(ctx, q, candidates)
			if err == nil {
				t.mu.Lock()
				t.sessionCtx.CachedReflect[q.Text] = reflected
				t.mu.Unlock()
			}
		}

		ops++
	}

	return ops
}

// resolveContradictions finds and resolves conflicts in cached results.
func (t *Think) resolveContradictions() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	ops := 0

	// Look through cached Reflect results for contradictions
	for _, results := range t.sessionCtx.CachedReflect {
		for _, r := range results {
			if r.Metadata == nil {
				continue
			}

			// Check for contradictions marked by Reflect
			if conflicts, ok := r.Metadata["conflicts_with"].([]string); ok {
				for _, conflictID := range conflicts {
					// Create a key for this conflict pair
					key := r.ID + ":" + conflictID
					if r.ID > conflictID {
						key = conflictID + ":" + r.ID
					}

					// If not already resolved, pick the winner (higher score)
					if _, resolved := t.sessionCtx.ResolvedContradictions[key]; !resolved {
						// Winner is the one with higher score
						winner := r.ID
						for _, r2 := range results {
							if r2.ID == conflictID && r2.Score > r.Score {
								winner = r2.ID
							}
						}
						t.sessionCtx.ResolvedContradictions[key] = winner
						ops++
					}
				}
			}
		}
	}

	return ops
}
