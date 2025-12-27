package cognition

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
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

	// Shared state
	activity *ActivityTracker
}

// New creates a new Cortex instance with all cognitive modes.
func New(store *storage.Storage, provider llm.Provider, cfg *config.Config) (*Cortex, error) {
	if store == nil {
		return nil, fmt.Errorf("storage is required")
	}

	// Create shared activity tracker
	activity := NewActivityTracker()

	// Create modes
	reflex := NewReflex(store)
	reflect := NewReflect(provider) // provider can be nil, Reflect will degrade gracefully
	think := NewThink(reflex, reflect, activity)
	dream := NewDream(store, provider, activity)
	resolve := NewResolve()

	// Connect resolve to think for session context
	resolve.SetSessionContext(think.SessionContext())

	return &Cortex{
		reflex:   reflex,
		reflect:  reflect,
		resolve:  resolve,
		think:    think,
		dream:    dream,
		activity: activity,
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
			go func() {
				reflected, err := c.reflect.Reflect(context.Background(), q, candidates)
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
		go c.dream.MaybeDream(context.Background())
	} else {
		go c.think.MaybeThink(context.Background())
	}

	return result, nil
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

// SetStateWriter sets the state writer for all cognitive modes.
func (c *Cortex) SetStateWriter(sw *StateWriter) {
	c.think.SetStateWriter(sw)
	c.dream.SetStateWriter(sw)
}
