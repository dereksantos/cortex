package cognition

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Cortex combines all cognitive modes into a unified retrieval interface.
type Cortex struct {
	// Modes
	reflex  *Reflex
	reflect *Reflect
	resolve *Resolve
	think   *Think
	dream   *Dream
	digest  *Digest

	// Event routing
	router *Router

	// Session tracking for watch view
	sessionTracker *SessionTracker

	// Shared state
	activity *ActivityTracker

	// Metrics
	metricsWriter *BackgroundMetricsWriter

	// Graceful shutdown
	wg sync.WaitGroup
}

// New creates a new Cortex instance with all cognitive modes.
// embedder is optional - if provided, enables semantic search in Reflex.
func New(store *storage.Storage, provider llm.Provider, embedder llm.Embedder, cfg *config.Config) (*Cortex, error) {
	if store == nil {
		return nil, fmt.Errorf("storage is required")
	}

	// Create shared activity tracker
	activity := NewActivityTracker()

	// Create modes
	reflex := NewReflex(store, embedder) // embedder can be nil, falls back to text search
	reflect := NewReflect(provider)      // provider can be nil, Reflect will degrade gracefully
	think := NewThink(reflex, reflect, activity)
	dream := NewDream(store, provider, activity)
	digest := NewDigest(store)
	resolve := NewResolve()

	// Connect resolve to think for session context (race-safe via snapshots)
	resolve.SetThinker(think)

	// Create event router
	router := NewRouter(reflex, think, dream)

	// Create session tracker for watch view (if config provided)
	var sessionTracker *SessionTracker
	var metricsWriter *BackgroundMetricsWriter
	if cfg != nil {
		sessionTracker = NewSessionTracker(store, cfg.ContextDir)
		router.SetSessionTracker(sessionTracker)
		metricsWriter = NewBackgroundMetricsWriter(cfg.ContextDir)
	}

	return &Cortex{
		reflex:         reflex,
		reflect:        reflect,
		resolve:        resolve,
		think:          think,
		dream:          dream,
		digest:         digest,
		router:         router,
		sessionTracker: sessionTracker,
		metricsWriter:  metricsWriter,
		activity:       activity,
	}, nil
}

// Retrieve performs context retrieval using the specified mode.
//
// Fast mode: Reflex → Resolve (Reflect runs async, cached for next time)
// Full mode: Reflex → Reflect → Resolve (synchronous, higher accuracy)
func (c *Cortex) Retrieve(ctx context.Context, q cognition.Query, mode cognition.RetrieveMode) (*cognition.ResolveResult, error) {
	// Record activity
	c.activity.RecordRetrieve()
	c.think.RecordQuery(q)

	// Step 1: Reflex (always runs, must be <10ms)
	candidates, err := c.reflex.Reflex(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("reflex failed: %w", err)
	}

	// Step 2: Reflect (depends on mode)
	if mode == cognition.Full {
		// Synchronous Reflect for Full mode
		reflected, err := c.reflect.Reflect(ctx, q, candidates)
		if err == nil {
			candidates = reflected
			// Cache for future Fast mode
			c.think.CacheReflectResult(q.Text, candidates)
		}
	} else {
		// Fast mode: use cached Reflect if available
		sessionCtx := c.think.SessionContext()
		if cached, ok := sessionCtx.CachedReflect[q.Text]; ok && len(cached) > 0 {
			candidates = cached
		} else {
			// Run Reflect async for next time
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				reflected, err := c.reflect.Reflect(ctx, q, candidates)
				if err == nil {
					c.think.CacheReflectResult(q.Text, reflected)
				}
			}()
		}
	}

	// Merge proactive results from Dream
	proactive := c.dream.ProactiveQueue()
	if len(proactive) > 0 {
		for _, pr := range proactive {
			c.resolve.AddProactiveResult(pr)
		}
		c.dream.ClearProactiveQueue()
	}

	// Step 3: Resolve
	result, err := c.resolve.Resolve(ctx, q, candidates)
	if err != nil {
		return nil, fmt.Errorf("resolve failed: %w", err)
	}

	// Step 4: Trigger background mode (non-blocking)
	if c.activity.IsIdle() {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			result, _ := c.dream.MaybeDream(ctx)
			// Trigger Digest after Dream completes
			if result != nil && result.Status == cognition.DreamRan {
				c.digest.NotifyDreamCompleted()
				c.digest.MaybeDigest(ctx)
			}
			// Update metrics for watch command
			c.updateBackgroundMetrics()
		}()
	} else {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.think.MaybeThink(ctx)
			// Update metrics for watch command
			c.updateBackgroundMetrics()
		}()
	}

	return result, nil
}

// Ingest routes an event to the appropriate cognitive mode for processing.
//
// Event routing logic:
//   - user_prompt → Think.IngestPrompt() for pattern learning
//   - tool_use → stored for Reflex indexing
//   - stop → Dream.QueueTranscript() for deeper analysis
//
// This is the main entry point for event-driven cognition.
func (c *Cortex) Ingest(ctx context.Context, event *events.Event) *RouteResult {
	if c.router == nil {
		return &RouteResult{Routed: false}
	}
	return c.router.Route(ctx, event)
}

// IngestBatch processes multiple events through the cognition pipeline.
func (c *Cortex) IngestBatch(ctx context.Context, evts []*events.Event) int {
	routed := 0
	for _, event := range evts {
		result := c.Ingest(ctx, event)
		if result != nil && result.Routed {
			routed++
		}
	}
	return routed
}

// Reflex performs fast mechanical retrieval.
func (c *Cortex) Reflex(ctx context.Context, q cognition.Query) ([]cognition.Result, error) {
	return c.reflex.Reflex(ctx, q)
}

// Reflect performs LLM-based reranking.
func (c *Cortex) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	return c.reflect.Reflect(ctx, q, candidates)
}

// Resolve decides injection timing.
func (c *Cortex) Resolve(ctx context.Context, q cognition.Query, results []cognition.Result) (*cognition.ResolveResult, error) {
	return c.resolve.Resolve(ctx, q, results)
}

// MaybeThink attempts background processing.
func (c *Cortex) MaybeThink(ctx context.Context) (*cognition.ThinkResult, error) {
	return c.think.MaybeThink(ctx)
}

// SessionContext returns the current session context.
func (c *Cortex) SessionContext() *cognition.SessionContext {
	return c.think.SessionContext()
}

// RegisterSource adds a DreamSource.
func (c *Cortex) RegisterSource(source cognition.DreamSource) {
	c.dream.RegisterSource(source)
}

// MaybeDream attempts idle-time exploration.
func (c *Cortex) MaybeDream(ctx context.Context) (*cognition.DreamResult, error) {
	return c.dream.MaybeDream(ctx)
}

// Insights returns the Dream insights channel.
func (c *Cortex) Insights() <-chan cognition.Result {
	return c.dream.Insights()
}

// ProactiveQueue returns items queued for proactive injection.
func (c *Cortex) ProactiveQueue() []cognition.Result {
	return c.dream.ProactiveQueue()
}

// ForceIdle forces the activity tracker to idle state.
// Used for testing Dream mode without waiting for actual idle time.
func (c *Cortex) ForceIdle() {
	c.activity.ForceIdle()
}

// ResetForTesting resets Dream's internal state for testing.
// Clears the lastDream timestamp so MinInterval check passes.
func (c *Cortex) ResetForTesting() {
	c.dream.ResetForTesting()
}

// SetStateWriter sets the state writer for all cognitive modes.
func (c *Cortex) SetStateWriter(sw *StateWriter) {
	c.think.SetStateWriter(sw)
	c.dream.SetStateWriter(sw)
	c.digest.SetStateWriter(sw)
}

// NotifyDreamCompleted signals that Dream just finished.
func (c *Cortex) NotifyDreamCompleted() {
	c.digest.NotifyDreamCompleted()
}

// MaybeDigest attempts to consolidate insights after Dream.
func (c *Cortex) MaybeDigest(ctx context.Context) (*cognition.DigestResult, error) {
	return c.digest.MaybeDigest(ctx)
}

// DigestInsights performs on-demand deduplication of given insights.
func (c *Cortex) DigestInsights(ctx context.Context, insights []cognition.Result) ([]cognition.DigestedInsight, error) {
	return c.digest.DigestInsights(ctx, insights)
}

// GetDigestedInsights returns all active insights in deduplicated form.
func (c *Cortex) GetDigestedInsights(ctx context.Context, limit int) ([]cognition.DigestedInsight, error) {
	return c.digest.GetDigestedInsights(ctx, limit)
}

// SessionTracker returns the session tracker for watch view.
func (c *Cortex) SessionTracker() *SessionTracker {
	return c.sessionTracker
}

// updateBackgroundMetrics writes current background processing state for the watch command.
func (c *Cortex) updateBackgroundMetrics() {
	if c.metricsWriter == nil {
		return
	}

	// Get Think session context for cache stats
	ctx := c.think.SessionContext()
	cacheHits := 0
	cacheMisses := 0
	if ctx != nil {
		cacheHits = len(ctx.CachedReflect)
		// Estimate misses as queries without cached results
		cacheMisses = len(ctx.RecentQueries) - cacheHits
		if cacheMisses < 0 {
			cacheMisses = 0
		}
	}

	// Calculate cache hit rate
	var cacheHitRate float64
	total := cacheHits + cacheMisses
	if total > 0 {
		cacheHitRate = float64(cacheHits) / float64(total)
	}

	// Get activity state
	activityLevel := c.activity.ActivityLevel()
	idleSeconds := int(c.activity.TimeSinceLastRetrieve().Seconds())

	// Get Dream queue depth
	dreamQueueDepth := len(c.dream.ProactiveQueue())

	// Get budgets using default config values
	// Think: MaxBudget=5, MinBudget=1
	// Dream: MaxBudget=20, MinBudget=2, GrowthDuration=10min
	thinkBudget := c.activity.ThinkBudget(1, 5)
	dreamBudget := c.activity.DreamBudget(2, 20, 10*time.Minute)

	// Get session insight count (approximation from Dream)
	insightsSession := 0
	// Could track this more precisely if needed

	metrics := &BackgroundMetrics{
		ThinkBudget:     thinkBudget,
		ThinkMaxBudget:  5, // Default max budget
		DreamQueueDepth: dreamQueueDepth,
		DreamBudget:     dreamBudget,
		DreamMaxBudget:  20, // Default max budget
		ActivityLevel:   activityLevel,
		IdleSeconds:     idleSeconds,
		CacheHitRate:    cacheHitRate,
		CacheHits:       cacheHits,
		CacheMisses:     cacheMisses,
		InsightsSession: insightsSession,
	}

	c.metricsWriter.WriteMetrics(metrics)
}

// Wait blocks until all background goroutines complete.
func (c *Cortex) Wait() {
	c.wg.Wait()
}

// Shutdown gracefully stops background processing.
// Call this before application exit. The provided context can be used
// to set a timeout for waiting on goroutines to complete.
func (c *Cortex) Shutdown(ctx context.Context) error {
	// Signal goroutines to stop via context cancellation (handled by caller)
	// Wait for completion with timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
