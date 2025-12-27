package cognition

import (
	"sync"
	"time"
)

// ActivityTracker monitors system activity to determine Think vs Dream budget.
// High activity = more Think (spare cycles), low activity = more Dream (idle exploration).
type ActivityTracker struct {
	mu sync.Mutex

	// Retrieve timestamps for activity calculation
	retrieveTimes []time.Time

	// Config
	activityWindow        time.Duration // Window for measuring activity (default 1 min)
	idleThreshold         time.Duration // Time since last retrieve to be "idle" (default 30s)
	highActivityThreshold int           // Retrieve count that = high activity (default 10)
}

// NewActivityTracker creates a new tracker with default settings.
func NewActivityTracker() *ActivityTracker {
	return &ActivityTracker{
		activityWindow:        1 * time.Minute,
		idleThreshold:         30 * time.Second,
		highActivityThreshold: 10,
	}
}

// RecordRetrieve records a Retrieve call timestamp.
func (a *ActivityTracker) RecordRetrieve() {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	a.retrieveTimes = append(a.retrieveTimes, now)

	// Prune old timestamps (keep only those in activity window)
	cutoff := now.Add(-a.activityWindow)
	pruned := make([]time.Time, 0, len(a.retrieveTimes))
	for _, t := range a.retrieveTimes {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	a.retrieveTimes = pruned
}

// IsIdle returns true if the system is idle (time since last retrieve > idleThreshold).
func (a *ActivityTracker) IsIdle() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.retrieveTimes) == 0 {
		return true // No activity at all = idle
	}

	lastRetrieve := a.retrieveTimes[len(a.retrieveTimes)-1]
	return time.Since(lastRetrieve) > a.idleThreshold
}

// ActivityLevel returns the current activity level (0.0 = idle, 1.0 = very active).
func (a *ActivityTracker) ActivityLevel() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.retrieveTimes) == 0 {
		return 0.0
	}

	// Count retrieves in window
	count := len(a.retrieveTimes)

	// Normalize to 0-1 based on threshold
	level := float64(count) / float64(a.highActivityThreshold)
	if level > 1.0 {
		level = 1.0
	}

	return level
}

// TimeSinceLastRetrieve returns how long since the last Retrieve call.
func (a *ActivityTracker) TimeSinceLastRetrieve() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.retrieveTimes) == 0 {
		return time.Hour * 24 // No retrieves = very long time
	}

	lastRetrieve := a.retrieveTimes[len(a.retrieveTimes)-1]
	return time.Since(lastRetrieve)
}

// ThinkBudget calculates the Think budget based on activity level.
// Higher activity = lower budget (less spare cycles).
func (a *ActivityTracker) ThinkBudget(minBudget, maxBudget int) int {
	level := a.ActivityLevel()

	// Inverse relationship: high activity = low budget
	// budget = maxBudget - (level * (maxBudget - minBudget))
	budgetRange := maxBudget - minBudget
	budget := maxBudget - int(level*float64(budgetRange))

	if budget < minBudget {
		budget = minBudget
	}

	return budget
}

// DreamBudget calculates the Dream budget based on idle time.
// Longer idle = higher budget (more capacity for exploration).
func (a *ActivityTracker) DreamBudget(minBudget, maxBudget int, growthDuration time.Duration) int {
	idleTime := a.TimeSinceLastRetrieve()

	// Growth factor: increases with idle time, caps at 1.0
	growthFactor := float64(idleTime) / float64(growthDuration)
	if growthFactor > 1.0 {
		growthFactor = 1.0
	}

	// budget = minBudget + (growthFactor * (maxBudget - minBudget))
	budgetRange := maxBudget - minBudget
	budget := minBudget + int(growthFactor*float64(budgetRange))

	if budget > maxBudget {
		budget = maxBudget
	}

	return budget
}

// RetrieveCount returns the number of retrieves in the activity window.
func (a *ActivityTracker) RetrieveCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.retrieveTimes)
}
