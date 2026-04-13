package cognition

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// Resolve implements cognition.Resolver for injection decision making.
type Resolve struct {
	formatter *Formatter

	// Decision thresholds
	InjectThreshold  float64 // Min avg score to inject (default 0.5)
	QueueThreshold   float64 // Min avg score to queue (default 0.3)
	WaitThreshold    float64 // Min avg score to wait (default 0.2)
	MinResultsInject int     // Min results needed to inject (default 1)

	// SessionContext for boost - this is a snapshot (copy), safe from races
	sessionCtx *cognition.SessionContext

	// Proactive queue (set by Dream)
	proactiveQueue []cognition.Result

	// Thinker provides session context snapshots (safe from races)
	thinker *Think

	// ActivityLogger for logging decisions to activity.log
	activityLogger *ActivityLogger
}

// NewResolve creates a new Resolve instance with default thresholds.
func NewResolve() *Resolve {
	return &Resolve{
		formatter:        NewFormatter(),
		InjectThreshold:  0.5,
		QueueThreshold:   0.3,
		WaitThreshold:    0.2,
		MinResultsInject: 1,
	}
}

// SetSessionContext sets the session context for topic boosting.
// DEPRECATED: Use SetThinker instead to get race-safe snapshots.
func (r *Resolve) SetSessionContext(ctx *cognition.SessionContext) {
	r.sessionCtx = ctx
}

// SetThinker sets the Think instance for getting session context snapshots.
// This is the preferred way to provide session context - it's race-safe.
func (r *Resolve) SetThinker(thinker *Think) {
	r.thinker = thinker
}

// SetActivityLogger sets the activity logger for decision logging.
func (r *Resolve) SetActivityLogger(logger *ActivityLogger) {
	r.activityLogger = logger
}

// AddProactiveResult adds a result from Dream to the proactive queue.
func (r *Resolve) AddProactiveResult(result cognition.Result) {
	r.proactiveQueue = append(r.proactiveQueue, result)
}

// ClearProactiveQueue clears the proactive queue after injection.
func (r *Resolve) ClearProactiveQueue() {
	r.proactiveQueue = nil
}

// Resolve decides whether to inject, wait, queue, or discard results.
func (r *Resolve) Resolve(ctx context.Context, q cognition.Query, results []cognition.Result) (*cognition.ResolveResult, error) {
	// Get a race-safe snapshot of session context
	// Prefer thinker (provides fresh snapshot) over static sessionCtx
	var sessionCtx *cognition.SessionContext
	if r.thinker != nil {
		snapshot := r.thinker.SessionContextSnapshot()
		sessionCtx = &snapshot
	} else if r.sessionCtx != nil {
		sessionCtx = r.sessionCtx
	}

	// Merge with proactive results from Dream
	if len(r.proactiveQueue) > 0 {
		results = r.mergeProactive(results)
	}

	// Apply session context boost if available
	if sessionCtx != nil {
		results = r.applySessionBoostWithCtx(results, sessionCtx)
	}

	// Check for cached Reflect results
	if sessionCtx != nil && q.Text != "" {
		if cached, ok := sessionCtx.CachedReflect[q.Text]; ok && len(cached) > 0 {
			results = cached
		}
	}

	// Calculate aggregate scores
	avgScore, maxScore, count := r.calculateScores(results)

	// Make decision
	decision, confidence := r.makeDecision(avgScore, maxScore, count)

	// Format output if injecting
	var formatted string
	if decision == cognition.Inject {
		// Pass SessionContext to formatter for any enrichments (nuances, etc.)
		formatted = r.formatter.FormatForInjection(results, sessionCtx)
	}

	// Log decision to activity.log for eval analysis
	if r.activityLogger != nil {
		// Ignoring error - logging should not block decisions
		_ = r.activityLogger.LogDecision(decision.String(), confidence, q.Text, count)
	}

	return &cognition.ResolveResult{
		Decision:   decision,
		Results:    results,
		Formatted:  formatted,
		Confidence: confidence,
		Reason:     r.explainDecision(decision, avgScore, count),
	}, nil
}

// mergeProactive merges proactive results with main results.
// Proactive results are added but scored lower unless highly relevant.
func (r *Resolve) mergeProactive(results []cognition.Result) []cognition.Result {
	// Add proactive results with a slight penalty
	for _, pr := range r.proactiveQueue {
		pr.Score *= 0.8 // Slight penalty for not being directly queried
		if pr.Metadata == nil {
			pr.Metadata = make(map[string]any)
		}
		pr.Metadata["proactive"] = true
		results = append(results, pr)
	}

	// Clear queue after merge
	r.proactiveQueue = nil

	// Re-sort by score
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// applySessionBoostWithCtx boosts results that match session topic weights.
// Takes an explicit session context to avoid race conditions.
func (r *Resolve) applySessionBoostWithCtx(results []cognition.Result, sessionCtx *cognition.SessionContext) []cognition.Result {
	if sessionCtx == nil || len(sessionCtx.TopicWeights) == 0 {
		return results
	}

	for i := range results {
		// Check if result tags match any topic weights
		boost := 0.0
		for _, tag := range results[i].Tags {
			if weight, ok := sessionCtx.TopicWeights[tag]; ok {
				boost += weight * 0.1 // Up to 10% boost per matching topic
			}
		}

		// Also check content for topic keywords
		for topic, weight := range sessionCtx.TopicWeights {
			if containsIgnoreCase(results[i].Content, topic) {
				boost += weight * 0.05 // 5% boost for content match
			}
		}

		// Apply boost (capped at 20%)
		if boost > 0.2 {
			boost = 0.2
		}
		results[i].Score *= (1.0 + boost)
		if results[i].Score > 1.0 {
			results[i].Score = 1.0
		}
	}

	return results
}

// calculateScores computes aggregate statistics.
func (r *Resolve) calculateScores(results []cognition.Result) (avg, max float64, count int) {
	if len(results) == 0 {
		return 0, 0, 0
	}

	count = len(results)
	total := 0.0

	for _, result := range results {
		total += result.Score
		if result.Score > max {
			max = result.Score
		}
	}

	avg = total / float64(count)
	return
}

// makeDecision determines the injection action based on scores.
func (r *Resolve) makeDecision(avgScore, maxScore float64, count int) (cognition.Decision, float64) {
	// No results = discard
	if count == 0 {
		return cognition.Discard, 0.9
	}

	// High average score and enough results = inject
	if avgScore >= r.InjectThreshold && count >= r.MinResultsInject {
		return cognition.Inject, avgScore
	}

	// Very high max score even with low average = inject the top result
	if maxScore >= 0.8 {
		return cognition.Inject, maxScore
	}

	// Medium score = queue for later opportunity
	if avgScore >= r.QueueThreshold {
		return cognition.Queue, avgScore
	}

	// Low but not zero = wait for more context
	if avgScore >= r.WaitThreshold {
		return cognition.Wait, avgScore
	}

	// Too low = discard
	return cognition.Discard, 1.0 - avgScore
}

// explainDecision provides a human-readable reason for the decision.
func (r *Resolve) explainDecision(decision cognition.Decision, avgScore float64, count int) string {
	switch decision {
	case cognition.Inject:
		return fmt.Sprintf("Injecting %d results (avg score: %.2f)", count, avgScore)
	case cognition.Queue:
		return fmt.Sprintf("Queuing %d results for next opportunity (avg score: %.2f)", count, avgScore)
	case cognition.Wait:
		return fmt.Sprintf("Waiting for more context (avg score: %.2f)", avgScore)
	case cognition.Discard:
		if count == 0 {
			return "No relevant results found"
		}
		return fmt.Sprintf("Discarding %d low-relevance results (avg score: %.2f)", count, avgScore)
	default:
		return "Unknown decision"
	}
}
