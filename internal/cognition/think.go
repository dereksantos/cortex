package cognition

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Think implements cognition.Thinker for background processing during active work.
// Uses spare cycles to pre-compute results and improve Fast mode quality.
type Think struct {
	mu sync.Mutex

	// Components
	reflex   *Reflex
	reflect  *Reflect
	activity *ActivityTracker
	llm      llm.Provider
	storage  *storage.Storage

	// Session state
	sessionCtx *cognition.SessionContext

	// Config
	config cognition.ThinkConfig

	// State
	running bool

	// Cache hit/miss tracking
	cacheHits   int
	cacheMisses int

	// State writer for daemon status updates
	stateWriter *StateWriter

	// Journal output (slice T1). When set, Think emits
	// think.session_context entries to <journalDir>/think/ at the end
	// of each MaybeThink cycle.
	journalDir string

	// SessionID tagged on emitted entries.
	sessionID string
}

// SetJournalDir wires the project's <ContextDir>/journal/ root.
// Empty disables journal emission.
func (t *Think) SetJournalDir(dir string) { t.journalDir = dir }

// SetSessionID tags emitted think.session_context entries with the
// current session.
func (t *Think) SetSessionID(id string) { t.sessionID = id }

// NewThink creates a new Think instance.
func NewThink(reflex *Reflex, reflect *Reflect, activity *ActivityTracker) *Think {
	return &Think{
		reflex:   reflex,
		reflect:  reflect,
		activity: activity,
		sessionCtx: &cognition.SessionContext{
			TopicWeights:           make(map[string]float64),
			RecentQueries:          make([]cognition.Query, 0),
			RecentPrompts:          make([]string, 0),
			WarmCache:              make(map[string][]cognition.Result),
			CachedReflect:          make(map[string][]cognition.Result),
			ResolvedContradictions: make(map[string]string),
			ExtractedNuances:       make(map[string][]cognition.Nuance),
			ProcessedPatternIDs:    make(map[string]bool),
		},
		config: cognition.DefaultThinkConfig(),
	}
}

// SetConfig updates the Think configuration.
func (t *Think) SetConfig(cfg cognition.ThinkConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.config = cfg
}

// SetProvider sets the LLM provider for nuance extraction.
func (t *Think) SetProvider(provider llm.Provider) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.llm = provider
}

// SetStorage sets the storage for accessing patterns.
func (t *Think) SetStorage(store *storage.Storage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.storage = store
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

	// Write state on start with natural language
	if stateWriter != nil {
		stateWriter.WriteMode("think", "Thinking about session patterns...")
	}

	start := time.Now()
	minDisplay := t.config.MinDisplayDuration

	defer func() {
		// Ensure minimum display duration for status visibility
		elapsed := time.Since(start)
		if elapsed < minDisplay {
			time.Sleep(minDisplay - elapsed)
		}

		t.mu.Lock()
		t.running = false
		t.mu.Unlock()

		// Write idle state on completion
		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
	}()
	budget := t.activity.ThinkBudget(t.config.MinBudget, t.config.MaxBudget)
	log.Printf("Think: starting (budget: %d)", budget)
	ops := 0

	// Operation 1: Update topic weights from recent queries
	if stateWriter != nil {
		stateWriter.WriteMode("think", "Learning from recent queries...")
	}
	t.updateTopicWeights()
	ops++

	// Operation 2: Cache Reflect results for recent queries
	if ops < budget && len(t.sessionCtx.RecentQueries) > 0 {
		if stateWriter != nil {
			stateWriter.WriteMode("think", "Caching results for faster lookups...")
		}
		ops += t.cacheReflectResults(ctx, budget-ops)
	}

	// Operation 3: Pre-resolve contradictions
	if ops < budget {
		if stateWriter != nil {
			stateWriter.WriteMode("think", "Sorting out contradictions...")
		}
		ops += t.resolveContradictions()
	}

	// Operation 4: Extract nuances from high-importance patterns
	if ops < budget {
		if stateWriter != nil {
			stateWriter.WriteMode("think", "Extracting implementation nuances...")
		}
		ops += t.extractRecentNuances(ctx, budget-ops)
	}

	// Update timestamp
	t.mu.Lock()
	t.sessionCtx.LastUpdated = time.Now()
	t.mu.Unlock()

	// Snapshot session context to the journal (slice T1).
	t.emitSessionContextToJournal()

	log.Printf("Think: completed (%d ops, %v)", ops, time.Since(start))

	return &cognition.ThinkResult{
		Status:     cognition.ThinkRan,
		Operations: ops,
		Duration:   time.Since(start),
	}, nil
}

// emitSessionContextToJournal best-effort writes a think.session_context
// snapshot. Errors logged, never returned.
func (t *Think) emitSessionContextToJournal() {
	if t.journalDir == "" {
		return
	}
	snap := t.SessionContextSnapshot()

	cachedQueries := make([]string, 0, len(snap.CachedReflect))
	for q := range snap.CachedReflect {
		cachedQueries = append(cachedQueries, q)
	}
	recent := make([]string, 0, len(snap.RecentQueries))
	for _, q := range snap.RecentQueries {
		recent = append(recent, q.Text)
	}
	payload := journal.ThinkSessionContextPayload{
		TopicWeights:  snap.TopicWeights,
		RecentQueries: recent,
		CachedQueries: cachedQueries,
		SessionID:     t.sessionID,
	}
	entry, err := journal.NewThinkSessionContextEntry(payload)
	if err != nil {
		log.Printf("think: build journal entry: %v", err)
		return
	}
	classDir := filepath.Join(t.journalDir, "think")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		log.Printf("think: open journal writer: %v", err)
		return
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		log.Printf("think: append journal entry: %v", err)
	}
}

// SessionContext returns the current session's accumulated context.
// DEPRECATED: Use SessionContextSnapshot() to avoid race conditions.
// This method returns a pointer that can race with Think's updates.
func (t *Think) SessionContext() *cognition.SessionContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionCtx
}

// SessionContextSnapshot returns a deep copy of the session context.
// This is safe to read concurrently while Think is updating the original.
func (t *Think) SessionContextSnapshot() cognition.SessionContext {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.sessionCtx == nil {
		return cognition.SessionContext{}
	}

	// Deep copy all maps and slices
	snapshot := cognition.SessionContext{
		LastUpdated: t.sessionCtx.LastUpdated,
	}

	// Copy TopicWeights
	if t.sessionCtx.TopicWeights != nil {
		snapshot.TopicWeights = make(map[string]float64, len(t.sessionCtx.TopicWeights))
		for k, v := range t.sessionCtx.TopicWeights {
			snapshot.TopicWeights[k] = v
		}
	}

	// Copy RecentQueries
	if t.sessionCtx.RecentQueries != nil {
		snapshot.RecentQueries = make([]cognition.Query, len(t.sessionCtx.RecentQueries))
		copy(snapshot.RecentQueries, t.sessionCtx.RecentQueries)
	}

	// Copy RecentPrompts
	if t.sessionCtx.RecentPrompts != nil {
		snapshot.RecentPrompts = make([]string, len(t.sessionCtx.RecentPrompts))
		copy(snapshot.RecentPrompts, t.sessionCtx.RecentPrompts)
	}

	// Copy WarmCache (map of slices - need deep copy)
	if t.sessionCtx.WarmCache != nil {
		snapshot.WarmCache = make(map[string][]cognition.Result, len(t.sessionCtx.WarmCache))
		for k, v := range t.sessionCtx.WarmCache {
			resultsCopy := make([]cognition.Result, len(v))
			copy(resultsCopy, v)
			snapshot.WarmCache[k] = resultsCopy
		}
	}

	// Copy CachedReflect (map of slices - need deep copy)
	if t.sessionCtx.CachedReflect != nil {
		snapshot.CachedReflect = make(map[string][]cognition.Result, len(t.sessionCtx.CachedReflect))
		for k, v := range t.sessionCtx.CachedReflect {
			resultsCopy := make([]cognition.Result, len(v))
			copy(resultsCopy, v)
			snapshot.CachedReflect[k] = resultsCopy
		}
	}

	// Copy ResolvedContradictions
	if t.sessionCtx.ResolvedContradictions != nil {
		snapshot.ResolvedContradictions = make(map[string]string, len(t.sessionCtx.ResolvedContradictions))
		for k, v := range t.sessionCtx.ResolvedContradictions {
			snapshot.ResolvedContradictions[k] = v
		}
	}

	// Copy ExtractedNuances (map of slices - need deep copy)
	if t.sessionCtx.ExtractedNuances != nil {
		snapshot.ExtractedNuances = make(map[string][]cognition.Nuance, len(t.sessionCtx.ExtractedNuances))
		for k, v := range t.sessionCtx.ExtractedNuances {
			nuancesCopy := make([]cognition.Nuance, len(v))
			copy(nuancesCopy, v)
			snapshot.ExtractedNuances[k] = nuancesCopy
		}
	}

	// Copy ProcessedPatternIDs
	if t.sessionCtx.ProcessedPatternIDs != nil {
		snapshot.ProcessedPatternIDs = make(map[string]bool, len(t.sessionCtx.ProcessedPatternIDs))
		for k, v := range t.sessionCtx.ProcessedPatternIDs {
			snapshot.ProcessedPatternIDs[k] = v
		}
	}

	return snapshot
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

// RecordCacheHit records that a cache lookup succeeded.
// Called when CachedReflect or WarmCache returns results for a query.
func (t *Think) RecordCacheHit() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cacheHits++
}

// RecordCacheMiss records that a cache lookup failed.
// Called when neither CachedReflect nor WarmCache has results for a query.
func (t *Think) RecordCacheMiss() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cacheMisses++
}

// CacheHits returns the total number of cache hits this session.
func (t *Think) CacheHits() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cacheHits
}

// CacheMisses returns the total number of cache misses this session.
func (t *Think) CacheMisses() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cacheMisses
}

// CacheHitRate returns the cache hit rate as a value between 0 and 1.
// Returns 0 if no cache lookups have been performed.
func (t *Think) CacheHitRate() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := t.cacheHits + t.cacheMisses
	if total == 0 {
		return 0
	}
	return float64(t.cacheHits) / float64(total)
}

// IngestPrompt processes a user prompt for pattern learning.
// First prompt in session → synchronous processing (immediate context learning)
// Subsequent prompts → async processing, writes hint for next injection
func (t *Think) IngestPrompt(ctx context.Context, prompt string, sessionID string) {
	if prompt == "" {
		return
	}

	// Track prompt in session context
	t.mu.Lock()
	if t.sessionCtx.RecentPrompts == nil {
		t.sessionCtx.RecentPrompts = make([]string, 0)
	}
	t.sessionCtx.RecentPrompts = append(t.sessionCtx.RecentPrompts, prompt)
	// Keep only last 10 prompts
	if len(t.sessionCtx.RecentPrompts) > 10 {
		t.sessionCtx.RecentPrompts = t.sessionCtx.RecentPrompts[len(t.sessionCtx.RecentPrompts)-10:]
	}
	isFirst := len(t.sessionCtx.RecentPrompts) == 1
	t.mu.Unlock()

	if isFirst {
		// First prompt: sync processing for immediate context
		t.processPromptSync(ctx, prompt, sessionID)
	} else {
		// Subsequent prompts: async processing
		go t.processPromptAsync(ctx, prompt, sessionID)
	}
}

// processPromptSync processes a prompt synchronously (first prompt in session).
func (t *Think) processPromptSync(ctx context.Context, prompt string, sessionID string) {
	log.Printf("Think: processing first prompt for session %s (sync)", sessionID)

	// Extract topics from prompt
	topics := t.extractTopics(prompt)

	// Update topic weights
	t.mu.Lock()
	for _, topic := range topics {
		if weight, exists := t.sessionCtx.TopicWeights[topic]; exists {
			t.sessionCtx.TopicWeights[topic] = weight + 0.2
		} else {
			t.sessionCtx.TopicWeights[topic] = 0.5
		}
	}
	t.sessionCtx.LastUpdated = time.Now()
	t.mu.Unlock()

	log.Printf("Think: learned %d topics from first prompt", len(topics))
}

// processPromptAsync processes a prompt asynchronously (subsequent prompts).
func (t *Think) processPromptAsync(ctx context.Context, prompt string, sessionID string) {
	log.Printf("Think: processing prompt for session %s (async)", sessionID)

	// Extract and update topics
	topics := t.extractTopics(prompt)

	t.mu.Lock()
	for _, topic := range topics {
		if weight, exists := t.sessionCtx.TopicWeights[topic]; exists {
			t.sessionCtx.TopicWeights[topic] = weight + 0.1
		} else {
			t.sessionCtx.TopicWeights[topic] = 0.3
		}
	}
	t.sessionCtx.LastUpdated = time.Now()
	t.mu.Unlock()

	// Note: Hint file writing is handled in Phase 3
}

// extractTopics extracts meaningful topics from a prompt.
func (t *Think) extractTopics(prompt string) []string {
	return ExtractTerms(prompt)
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
		terms := ExtractTerms(q.Text)
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

// extractRecentNuances extracts implementation gotchas from high-importance patterns.
// Returns the number of operations performed.
func (t *Think) extractRecentNuances(ctx context.Context, budget int) int {
	// Check if LLM and storage are available
	t.mu.Lock()
	llmProvider := t.llm
	store := t.storage
	t.mu.Unlock()

	if llmProvider == nil || !llmProvider.IsAvailable() || store == nil {
		return 0
	}

	// Get high-importance patterns (importance >= 8 on 0-10 scale, which maps to 0.8 on 0-1 scale)
	insights, err := store.GetImportantInsights(8, 10)
	if err != nil {
		log.Printf("Think: failed to get important insights: %v", err)
		return 0
	}

	ops := 0
	for _, insight := range insights {
		if ops >= budget {
			break
		}

		// Create a unique ID for this pattern
		patternID := fmt.Sprintf("insight:%d", insight.ID)

		// Skip if already processed
		t.mu.Lock()
		if t.sessionCtx.ProcessedPatternIDs[patternID] {
			t.mu.Unlock()
			continue
		}
		t.mu.Unlock()

		// Extract nuances
		nuances, err := ExtractNuances(ctx, llmProvider, insight.Summary)
		if err != nil {
			log.Printf("Think: failed to extract nuances for %s: %v", patternID, err)
			ops++
			continue
		}

		// Store nuances if any were found
		t.mu.Lock()
		t.sessionCtx.ProcessedPatternIDs[patternID] = true
		if len(nuances) > 0 {
			// Convert internal Nuance to cognition.Nuance
			cogNuances := make([]cognition.Nuance, len(nuances))
			for i, n := range nuances {
				cogNuances[i] = cognition.Nuance{
					Detail: n.Detail,
					Why:    n.Why,
				}
			}
			t.sessionCtx.ExtractedNuances[patternID] = cogNuances
			log.Printf("Think: extracted %d nuances for pattern %s", len(nuances), patternID)
		}
		t.mu.Unlock()

		ops++
	}

	return ops
}
