// Package cognition implements the five cognitive modes for context retrieval.
//
// # Cognitive Architecture
//
// The architecture processes context through five modes, inspired by human cognition:
//
//	Reflex  - Fast mechanical retrieval (embeddings, tags, recency) <10ms
//	Reflect - LLM-based reranking and contradiction detection ~200ms+
//	Resolve - Decides injection timing (now, wait, queue)
//	Think   - Background processing during active work
//	Dream   - Deep exploration during idle periods
//
// # Retrieval Modes
//
// Two retrieval paths optimize for different scenarios:
//
//	Fast - Reflex → Resolve (Reflect runs async for next time)
//	Full - Reflex → Reflect → Resolve (used at session start)
//
// Fast mode minimizes latency during active work. Reflex returns immediately,
// Reflect runs in background and caches results for subsequent retrievals.
//
// Full mode is used at session start when accuracy matters more than speed.
// Runs the complete pipeline synchronously.
//
// # Background Modes: Think vs Dream
//
// Think and Dream are both triggered opportunistically by Retrieve calls,
// but serve different purposes and have inverse resource models:
//
//	┌────────────────────────────────────────────────────────────┐
//	│  THINK (during active work)                                │
//	│  "Let me process this while you're busy"                   │
//	│  • Runs when system is active (frequent Retrieve calls)    │
//	│  • Budget DECAYS with activity (less spare capacity)       │
//	│  • Quick, bounded operations using spare cycles            │
//	│  • Like humans: thinking while working exhausts resources  │
//	└────────────────────────────────────────────────────────────┘
//
//	┌────────────────────────────────────────────────────────────┐
//	│  DREAM (during idle periods)                               │
//	│  "Now that you're resting, let me explore"                 │
//	│  • Runs when system is idle (infrequent Retrieve calls)    │
//	│  • Budget GROWS with idle time (capped at MaxBudget)       │
//	│  • Samples from diverse sources via DreamSource interface  │
//	│  • Creates new embeddings, discovers connections           │
//	│  • Like humans: dreaming happens when resting              │
//	└────────────────────────────────────────────────────────────┘
//
// # Dream Sources
//
// Dream explores diverse content via registered DreamSources:
//
//	┌────────────────────────────────────────────────────────────┐
//	│  DreamSources (read)           │  Dream Outputs (write)    │
//	├────────────────────────────────┼───────────────────────────┤
//	│  • Project files               │  • New embeddings         │
//	│  • Cortex DB (events/insights) │  • New insights           │
//	│  • Claude Code session history │  • Entity relationships   │
//	│  • Git history                 │  • Proactive queue        │
//	└────────────────────────────────┴───────────────────────────┘
//
// Dream randomly samples from sources and may generate new embeddings,
// discover connections, extract insights, and queue important items
// for proactive injection via Resolve.
//
// # How Background Modes Influence Resolve
//
// Both Think and Dream enhance Resolve's decision-making:
//
//	┌────────────────────────────────────────────────────────────┐
//	│  THINK → Resolve                                           │
//	│                                                            │
//	│  SessionContext provides:                                  │
//	│  • CachedReflect: pre-computed reranking (Fast ≈ Full)     │
//	│  • TopicWeights: boost results matching session patterns   │
//	│  • WarmCache: pre-fetched results for likely queries       │
//	│  • ResolvedContradictions: conflicts already figured out   │
//	└────────────────────────────────────────────────────────────┘
//
//	┌────────────────────────────────────────────────────────────┐
//	│  DREAM → Resolve                                           │
//	│                                                            │
//	│  ProactiveQueue provides:                                  │
//	│  • Important discoveries to inject opportunistically       │
//	│  • Even when not directly relevant to current query        │
//	│  • "I found something you should know about"               │
//	└────────────────────────────────────────────────────────────┘
//
// This architecture lets mechanical modes (Reflex, Resolve) benefit from
// agentic processing (Think, Dream, Reflect) without blocking on LLM calls.
//
// Budget models are inverse:
//
//	| Mode  | Activity Level     | Budget                        |
//	|-------|--------------------|------------------------------ |
//	| Think | High (busy)        | Low (spare cycles only)       |
//	| Think | Low (winding down) | Higher                        |
//	| Dream | High (busy)        | Skip entirely                 |
//	| Dream | Low (idle)         | High (capped at MaxBudget)    |
//
// Both modes are bounded - Think by spare capacity, Dream by MaxBudget.
// Neither runs unbounded.
//
// # Trigger Model
//
// Retrieve determines activity level and triggers the appropriate background mode:
//
//	Retrieve() called
//	       │
//	   [main work: Reflex → (Reflect) → Resolve]
//	       │
//	   Check activity level (retrieve frequency, recency)
//	       │
//	       ├─ Active? → MaybeThink() in goroutine
//	       │
//	       └─ Idle?   → MaybeDream() in goroutine
//
// Activity level is determined by:
//   - Time since last Retrieve call
//   - Frequency of recent Retrieve calls
//   - System resource availability
package cognition

import (
	"context"
	"time"

	"github.com/dereksantos/cortex/pkg/events"
)

// Result represents a retrieved piece of context.
type Result struct {
	ID        string        // Unique identifier
	Content   string        // The actual context text
	Category  string        // decision, pattern, constraint, correction, etc.
	Score     float64       // Relevance score (0-1)
	Source    events.Source // Which AI tool captured this
	Timestamp time.Time     // When it was captured
	Tags      []string      // Associated tags
	Metadata  map[string]any
}

// Query represents a retrieval request.
type Query struct {
	Text       string    // Natural language query or event content
	Tags       []string  // Filter by tags
	Categories []string  // Filter by category (decision, pattern, etc.)
	Since      time.Time // Only results after this time
	Limit      int       // Max results (0 = default)
	Threshold  float64   // Min score threshold (0-1)
}

// Decision represents what to do with retrieved context.
type Decision int

const (
	Inject  Decision = iota // Inject context immediately
	Wait                    // Need more context before injecting
	Queue                   // Queue for next hook opportunity
	Discard                 // Not relevant enough to inject
)

func (d Decision) String() string {
	switch d {
	case Inject:
		return "inject"
	case Wait:
		return "wait"
	case Queue:
		return "queue"
	case Discard:
		return "discard"
	default:
		return "unknown"
	}
}

// ResolveResult contains the injection decision and payload.
type ResolveResult struct {
	Decision   Decision // What to do
	Results    []Result // Context to inject (when Decision == Inject)
	Formatted  string   // Pre-formatted for LLM consumption
	Confidence float64  // Confidence in this decision (0-1)
	Reason     string   // Why this decision was made
}

// RetrieveMode controls the retrieval path.
type RetrieveMode int

const (
	// Fast runs Reflex → Resolve, with Reflect async in background.
	// Use during active sessions where latency matters.
	Fast RetrieveMode = iota

	// Full runs Reflex → Reflect → Resolve synchronously.
	// Use at session start when accuracy matters more than speed.
	Full
)

func (m RetrieveMode) String() string {
	switch m {
	case Fast:
		return "fast"
	case Full:
		return "full"
	default:
		return "unknown"
	}
}

// Reflexer performs fast mechanical retrieval.
// Must complete in <10ms.
type Reflexer interface {
	// Reflex returns candidates via embeddings similarity, tag matching, and recency.
	// This is a purely mechanical operation with no LLM calls.
	Reflex(ctx context.Context, q Query) ([]Result, error)
}

// Reflector performs LLM-based reranking and analysis.
// Typically takes 200ms+.
type Reflector interface {
	// Reflect reranks candidates for relevance to the query.
	// Also detects contradictions between candidates and resolves them.
	// Returns candidates in relevance order with updated scores.
	Reflect(ctx context.Context, q Query, candidates []Result) ([]Result, error)
}

// Resolver decides injection timing and formats output.
type Resolver interface {
	// Resolve decides whether to inject now, wait, or queue.
	// Returns formatted context ready for LLM consumption.
	//
	// The results parameter can come from either Reflex (fast path) or
	// Reflect (full path) - Resolver is agnostic to the source.
	Resolve(ctx context.Context, q Query, results []Result) (*ResolveResult, error)
}

// SessionContext captures patterns from current session activity.
//
// Think updates this during active work; Resolve reads it to make
// faster, better-informed decisions. This is how agentic processing
// (Think) benefits mechanical retrieval (Reflex → Resolve).
type SessionContext struct {
	// TopicWeights boosts results matching recent activity patterns.
	// Keys are topic/tag names, values are weights (0-1).
	// e.g., {"authentication": 0.8, "database": 0.3}
	TopicWeights map[string]float64

	// RecentQueries tracks what's been asked for pattern detection.
	// Newer queries are at the end.
	RecentQueries []Query

	// RecentPrompts tracks raw user prompts for pattern learning.
	// Think uses these to understand what the user is working on.
	// Newer prompts are at the end.
	RecentPrompts []string

	// WarmCache holds pre-computed results for likely next queries.
	// Keys are query hashes or fingerprints.
	WarmCache map[string][]Result

	// CachedReflect holds Reflect results computed in background.
	// Resolve can use these instead of raw Reflex results in Fast mode.
	CachedReflect map[string][]Result

	// ResolvedContradictions maps conflicting result IDs to winners.
	// When Think spots contradictions, it resolves them proactively.
	// Key format: "id1:id2" (sorted), value: winning ID.
	ResolvedContradictions map[string]string

	// LastUpdated is when this context was last modified.
	LastUpdated time.Time
}

// ThinkConfig controls Think mode behavior.
//
// Think runs during active work periods, using spare cycles for background
// processing. Its budget is inversely proportional to activity level:
// busier system = less thinking capacity.
type ThinkConfig struct {
	// MaxBudget is the maximum operations when activity is low.
	// Budget decreases as activity increases.
	// Default: 5
	MaxBudget int

	// MinBudget is the floor for operations during high activity.
	// Default: 1
	MinBudget int

	// OperationTimeout bounds each individual operation.
	// Default: 200ms
	OperationTimeout time.Duration

	// ActivityWindow is the time window for measuring activity level.
	// More Retrieve calls in this window = higher activity.
	// Default: 1 minute
	ActivityWindow time.Duration

	// HighActivityThreshold is the Retrieve count in ActivityWindow
	// that constitutes "high activity" (minimum budget).
	// Default: 10
	HighActivityThreshold int

	// MinDisplayDuration is the minimum time to show Think status.
	// Ensures the mode is visible in status bar before returning to idle.
	// Default: 2 seconds
	MinDisplayDuration time.Duration
}

// DefaultThinkConfig returns sensible defaults for ThinkConfig.
func DefaultThinkConfig() ThinkConfig {
	return ThinkConfig{
		MaxBudget:             5,
		MinBudget:             1,
		OperationTimeout:      200 * time.Millisecond,
		ActivityWindow:        1 * time.Minute,
		HighActivityThreshold: 10,
		MinDisplayDuration:    2 * time.Second,
	}
}

// ThinkStatus indicates why MaybeThink did or didn't run.
type ThinkStatus int

const (
	// ThinkRan indicates the think session completed.
	ThinkRan ThinkStatus = iota

	// ThinkSkippedIdle indicates system is idle (Dream should run instead).
	ThinkSkippedIdle

	// ThinkSkippedRunning indicates another think session is in progress.
	ThinkSkippedRunning

	// ThinkSkippedResources indicates insufficient spare resources.
	ThinkSkippedResources
)

func (s ThinkStatus) String() string {
	switch s {
	case ThinkRan:
		return "ran"
	case ThinkSkippedIdle:
		return "skipped_idle"
	case ThinkSkippedRunning:
		return "skipped_running"
	case ThinkSkippedResources:
		return "skipped_resources"
	default:
		return "unknown"
	}
}

// ThinkResult contains the outcome of a MaybeThink call.
type ThinkResult struct {
	Status     ThinkStatus   // Why think did or didn't run
	Operations int           // Number of operations performed
	Duration   time.Duration // How long the session took
}

// Thinker performs background processing during active work.
//
// Think uses spare cycles while the user is actively working.
// Budget is inversely proportional to activity level - the busier
// the system, the less capacity for background thinking.
//
// Think's primary job is to make Fast mode nearly as good as Full mode:
//   - Pre-compute Reflect results for recent/likely queries
//   - Track session patterns (topic weights)
//   - Pre-resolve contradictions
//   - Warm the cache for predicted queries
//
// This is how agentic processing benefits mechanical retrieval.
type Thinker interface {
	// MaybeThink attempts background processing if spare capacity exists.
	//
	// Checks preconditions:
	//   - System is active (not idle - Dream handles idle)
	//   - No other think session running
	//   - Spare resources available
	//
	// Budget calculated inversely from activity level in ActivityWindow.
	//
	// During a think session:
	//   - Runs Reflect on recent Reflex results, caches output
	//   - Analyzes query patterns, updates TopicWeights
	//   - Detects contradictions in cached results, resolves them
	//   - Predicts likely queries, pre-fetches results
	MaybeThink(ctx context.Context) (*ThinkResult, error)

	// SessionContext returns the current session's accumulated context.
	// Resolve uses this to boost relevant results and make faster decisions.
	//
	// The context includes:
	//   - TopicWeights: boost scores for results matching session patterns
	//   - CachedReflect: pre-computed Reflect results for Fast mode
	//   - ResolvedContradictions: conflicts already figured out
	//   - WarmCache: pre-fetched results for likely queries
	SessionContext() *SessionContext
}

// DreamSource provides content for Dream to explore.
//
// Dream randomly samples from registered sources during idle periods.
// Sources can include project files, Cortex's own database, Claude Code
// session history, git history, etc.
type DreamSource interface {
	// Name identifies this source (e.g., "project", "cortex", "claude-history", "git")
	Name() string

	// Sample returns random items to explore.
	// n is the desired count; implementations may return fewer.
	// Sampling should be lightweight - content can be loaded lazily.
	Sample(ctx context.Context, n int) ([]DreamItem, error)
}

// DreamItem is something Dream can explore.
//
// Items are sampled from DreamSources and may trigger:
//   - New embedding generation (if not already indexed)
//   - Connection discovery (relationships to existing content)
//   - Insight extraction (patterns, decisions, constraints)
type DreamItem struct {
	ID       string         // Unique identifier within the source
	Source   string         // Which DreamSource this came from
	Content  string         // Text content to analyze
	Path     string         // File path, event ID, conversation ID, etc.
	Metadata map[string]any // Source-specific metadata
}

// DreamConfig controls Dream mode behavior.
//
// Dream runs during idle periods for deep exploration. Its budget grows
// with idle time (more rest = more capacity) but is capped at MaxBudget
// to prevent unbounded resource usage.
//
// Budget calculation:
//
//	idle_time = time since last Retrieve
//	growth_factor = min(1.0, idle_time / GrowthDuration)
//	budget = MinBudget + (MaxBudget - MinBudget) * growth_factor
//
// Example with MinBudget=2, MaxBudget=20, GrowthDuration=10min:
//
//	Just went idle:  2 operations
//	2.5 min idle:    ~6 operations
//	5 min idle:      ~11 operations
//	10+ min idle:    20 operations (capped)
type DreamConfig struct {
	// MaxBudget is the upper bound for operations, reached after GrowthDuration.
	// Dream budget grows toward this but never exceeds it.
	// Default: 20
	MaxBudget int

	// MinBudget is the starting budget when system first becomes idle.
	// Default: 2
	MinBudget int

	// GrowthDuration is how long until budget reaches MaxBudget.
	// Default: 10 minutes
	GrowthDuration time.Duration

	// OperationTimeout bounds each individual exploration operation.
	// Default: 1 second (longer than Think since we have more capacity)
	OperationTimeout time.Duration

	// IdleThreshold is the time since last Retrieve to be considered idle.
	// Default: 30 seconds
	IdleThreshold time.Duration

	// MinInterval is minimum time between dream sessions.
	// Default: 1 minute
	MinInterval time.Duration

	// MaxMemoryPct (0-100) above which Dream skips. 0 disables check.
	// Default: 80
	MaxMemoryPct float64

	// MaxCPUPct (0-100) above which Dream skips. 0 disables check.
	// Default: 50 (lower than Think since we want truly idle system)
	MaxCPUPct float64

	// MinDisplayDuration is the minimum time to show Dream status.
	// Ensures the mode is visible in status bar before returning to idle.
	// Default: 30 seconds
	MinDisplayDuration time.Duration
}

// DefaultDreamConfig returns sensible defaults for DreamConfig.
func DefaultDreamConfig() DreamConfig {
	return DreamConfig{
		MaxBudget:          20,
		MinBudget:          2,
		GrowthDuration:     10 * time.Minute,
		OperationTimeout:   1 * time.Second,
		IdleThreshold:      30 * time.Second,
		MinInterval:        1 * time.Minute,
		MaxMemoryPct:       80,
		MaxCPUPct:          50,
		MinDisplayDuration: 30 * time.Second,
	}
}

// DreamStatus indicates why MaybeDream did or didn't run.
type DreamStatus int

const (
	// DreamRan indicates the dream session completed.
	DreamRan DreamStatus = iota

	// DreamSkippedActive indicates system is active (Think should run instead).
	DreamSkippedActive

	// DreamSkippedRecent indicates a dream ran too recently (MinInterval).
	DreamSkippedRecent

	// DreamSkippedRunning indicates another dream session is in progress.
	DreamSkippedRunning

	// DreamSkippedMemory indicates memory usage exceeded MaxMemoryPct.
	DreamSkippedMemory

	// DreamSkippedCPU indicates CPU usage exceeded MaxCPUPct.
	DreamSkippedCPU
)

func (s DreamStatus) String() string {
	switch s {
	case DreamRan:
		return "ran"
	case DreamSkippedActive:
		return "skipped_active"
	case DreamSkippedRecent:
		return "skipped_recent"
	case DreamSkippedRunning:
		return "skipped_running"
	case DreamSkippedMemory:
		return "skipped_memory"
	case DreamSkippedCPU:
		return "skipped_cpu"
	default:
		return "unknown"
	}
}

// DreamResult contains the outcome of a MaybeDream call.
type DreamResult struct {
	Status         DreamStatus   // Why dream did or didn't run
	Operations     int           // Number of operations performed
	Duration       time.Duration // How long the session took
	Insights       int           // Number of new insights discovered
	SourcesCovered []string      // Which sources were sampled
}

// Dreamer explores diverse sources during idle periods.
//
// Dream performs deep exploration when the system is idle. It randomly
// samples from registered DreamSources (project files, Cortex DB,
// Claude Code history, git, etc.) and may:
//
//   - Generate new embeddings for unindexed content
//   - Discover connections between concepts
//   - Extract insights (patterns, decisions, constraints)
//   - Detect and resolve contradictions
//   - Queue items for proactive injection
//
// Budget grows with idle time (more rest = more capacity for exploration)
// but is capped at MaxBudget to prevent unbounded resource usage.
//
// Dream only runs when:
//   - System is idle (time since last Retrieve > IdleThreshold)
//   - MinInterval has passed since last dream
//   - No other dream session running
//   - System resources below thresholds
type Dreamer interface {
	// RegisterSource adds a source for Dream to explore.
	// Sources are sampled randomly during dream sessions.
	// Common sources: project files, cortex DB, claude history, git.
	RegisterSource(source DreamSource)

	// MaybeDream attempts exploration if the system is idle.
	//
	// Samples randomly from registered sources, with budget based on idle time:
	//   budget = MinBudget + (MaxBudget - MinBudget) * (idle_time / GrowthDuration)
	//
	// For each sampled item, Dream may:
	//   - Generate embeddings (if not indexed)
	//   - Find connections to existing content
	//   - Extract insights and store them
	//   - Queue important discoveries for proactive injection
	//
	// Returns immediately if preconditions fail (Status indicates why).
	MaybeDream(ctx context.Context) (*DreamResult, error)

	// Insights returns a channel of discoveries from dreaming.
	// The channel is buffered; old insights may be dropped if not consumed.
	// Insights are also persisted to storage for future retrieval.
	Insights() <-chan Result

	// ProactiveQueue returns items Dream has queued for injection.
	// Resolve checks this queue and may include these in injection decisions
	// even if not directly relevant to the current query.
	ProactiveQueue() []Result

	// ForceIdle forces the activity tracker to idle state.
	// Used for testing Dream mode without waiting for actual idle time.
	ForceIdle()

	// ResetForTesting resets Dream's internal state for testing.
	// Clears the lastDream timestamp so MinInterval check passes.
	ResetForTesting()
}

// DigestConfig controls Digest mode behavior.
//
// Digest is a second-pass operation that deduplicates and consolidates
// insights at query time. It runs after Dream completes and groups
// similar insights to reduce noise.
type DigestConfig struct {
	// MaxMerges is the maximum number of merge operations per session.
	// Default: 10
	MaxMerges int

	// SimilarityThreshold is the minimum similarity score (0-1) to consider
	// two insights as duplicates. Higher = stricter matching.
	// Default: 0.7
	SimilarityThreshold float64

	// RecencyBias gives preference to newer insights when merging.
	// When true, newer insights survive; when false, higher importance wins.
	// Default: true
	RecencyBias bool

	// MinDisplayDuration is the minimum time to show Digest status.
	// Default: 2 seconds
	MinDisplayDuration time.Duration
}

// DefaultDigestConfig returns sensible defaults for DigestConfig.
func DefaultDigestConfig() DigestConfig {
	return DigestConfig{
		MaxMerges:           10,
		SimilarityThreshold: 0.7,
		RecencyBias:         true,
		MinDisplayDuration:  2 * time.Second,
	}
}

// DigestStatus indicates why MaybeDigest did or didn't run.
type DigestStatus int

const (
	// DigestRan indicates the digest session completed.
	DigestRan DigestStatus = iota

	// DigestSkippedNoDream indicates Dream hasn't run recently.
	DigestSkippedNoDream

	// DigestSkippedRunning indicates another digest is in progress.
	DigestSkippedRunning

	// DigestSkippedNoInsights indicates no insights to digest.
	DigestSkippedNoInsights
)

func (s DigestStatus) String() string {
	switch s {
	case DigestRan:
		return "ran"
	case DigestSkippedNoDream:
		return "skipped_no_dream"
	case DigestSkippedRunning:
		return "skipped_running"
	case DigestSkippedNoInsights:
		return "skipped_no_insights"
	default:
		return "unknown"
	}
}

// DigestResult contains the outcome of a MaybeDigest call.
type DigestResult struct {
	Status   DigestStatus  // Why digest did or didn't run
	Groups   int           // Number of duplicate groups found
	Merged   int           // Number of insights consolidated
	Duration time.Duration // How long the session took
}

// DigestedInsight represents an insight with its duplicates grouped.
type DigestedInsight struct {
	// Representative is the primary insight (survivor of merge).
	Representative Result

	// Duplicates are similar insights grouped under the representative.
	// Empty if this insight has no duplicates.
	Duplicates []Result

	// Similarity score between representative and duplicates.
	Similarity float64
}

// Digester consolidates duplicate insights.
//
// Digest is a second-pass operation that runs after Dream. It reads
// insights from storage, groups similar ones, and returns a deduplicated
// view. This is a query-time operation - it doesn't modify stored data.
//
// The digest process:
//  1. Fetch recent insights from storage
//  2. Group by category (decision, pattern, etc.)
//  3. Within each category, compute pairwise similarity
//  4. Merge similar insights (similarity > threshold)
//  5. Return deduplicated results
//
// Recency bias: when merging, newer insights become the representative
// by default, preserving the most recent understanding.
type Digester interface {
	// NotifyDreamCompleted signals that Dream just finished.
	// Must be called before MaybeDigest will run.
	NotifyDreamCompleted()

	// MaybeDigest attempts to consolidate insights after Dream.
	//
	// Only runs if:
	//   - Dream completed recently (NotifyDreamCompleted was called)
	//   - No other digest in progress
	//   - Insights exist to process
	//
	// Returns immediately if preconditions fail (Status indicates why).
	MaybeDigest(ctx context.Context) (*DigestResult, error)

	// DigestInsights performs on-demand deduplication of given insights.
	// This is the core algorithm, usable independently of MaybeDigest.
	DigestInsights(ctx context.Context, insights []Result) ([]DigestedInsight, error)

	// GetDigestedInsights returns all active insights in deduplicated form.
	// Convenience method that fetches from storage and digests.
	GetDigestedInsights(ctx context.Context, limit int) ([]DigestedInsight, error)
}

// Cortex combines all cognitive modes into a unified retrieval interface.
//
// The six modes work together:
//
//	| Mode    | Type       | When              | Purpose                  |
//	|---------|------------|-------------------|--------------------------|
//	| Reflex  | Mechanical | Every retrieval   | Fast candidates          |
//	| Reflect | Agentic    | Sync or async     | Rerank, contradictions   |
//	| Resolve | Agentic    | After results     | Injection decision       |
//	| Think   | Background | Active periods    | Process while working    |
//	| Dream   | Background | Idle periods      | Deep exploration         |
//	| Digest  | Background | After Dream       | Consolidate duplicates   |
type Cortex interface {
	Reflexer
	Reflector
	Resolver
	Thinker
	Dreamer
	Digester

	// Retrieve performs context retrieval using the specified mode.
	//
	// Fast mode: Reflex → Resolve (Reflect runs async, cached for next time)
	// Full mode: Reflex → Reflect → Resolve (synchronous, higher accuracy)
	//
	// After retrieval, checks activity level and triggers background mode:
	//   - Active: MaybeThink() in goroutine
	//   - Idle:   MaybeDream() in goroutine → MaybeDigest() on completion
	Retrieve(ctx context.Context, q Query, mode RetrieveMode) (*ResolveResult, error)
}
