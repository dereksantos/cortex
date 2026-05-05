package fractal

import (
	"sync"
	"time"
)

const (
	// MaxFractalDepth caps the depth of zoom-in recursion so we don't
	// spelunk forever inside one file.
	MaxFractalDepth = 3
	// DefaultQueueCapacity is the upper bound on pending follow-ups.
	DefaultQueueCapacity = 64
	// DefaultExpireAfter drops follow-ups that are not drained within
	// this many cycles' worth of time.
	DefaultExpireAfter = 5 * time.Minute
)

// FollowUp is a region queued for analysis in a future cycle.
type FollowUp struct {
	Region       Region
	ParentItemID string
	Depth        int
	Source       string         // source name (e.g. "project")
	Meta         map[string]any // pass-through for source-specific fields
	EnqueuedAt   time.Time
}

// FollowUpQueue is a bounded ring of zoom-in candidates produced by
// Dream when an item yields a high-signal insight.
type FollowUpQueue struct {
	mu          sync.Mutex
	cap         int
	expireAfter time.Duration
	items       []FollowUp
}

// NewFollowUpQueue constructs a queue. capacity <= 0 uses the default;
// expireAfter <= 0 uses the default.
func NewFollowUpQueue(capacity int, expireAfter time.Duration) *FollowUpQueue {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	if expireAfter <= 0 {
		expireAfter = DefaultExpireAfter
	}
	return &FollowUpQueue{
		cap:         capacity,
		expireAfter: expireAfter,
	}
}

// Enqueue adds a follow-up. Items at or above MaxFractalDepth are
// dropped silently. When the queue is full the oldest entry is evicted.
func (q *FollowUpQueue) Enqueue(f FollowUp) {
	if f.Depth > MaxFractalDepth {
		return
	}
	if f.EnqueuedAt.IsZero() {
		f.EnqueuedAt = time.Now()
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	q.items = append(q.items, f)
	for len(q.items) > q.cap {
		q.items = q.items[1:]
	}
}

// Drain returns up to `max` follow-ups, removing them from the queue.
// Expired entries are discarded. Lower-depth items come first so the
// fractal walk fans out before it dives.
func (q *FollowUpQueue) Drain(max int) []FollowUp {
	if max <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	alive := q.items[:0]
	for _, it := range q.items {
		if now.Sub(it.EnqueuedAt) >= q.expireAfter {
			continue
		}
		alive = append(alive, it)
	}
	q.items = alive

	// Stable sort: depth asc, then enqueue time asc.
	sortFollowUps(q.items)

	n := max
	if n > len(q.items) {
		n = len(q.items)
	}
	out := make([]FollowUp, n)
	copy(out, q.items[:n])
	q.items = q.items[n:]
	return out
}

// Len reports the current queue size (for tests and metrics).
func (q *FollowUpQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func sortFollowUps(items []FollowUp) {
	// Insertion sort — n is bounded by capacity and tests need stability.
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && less(items[j], items[j-1]) {
			items[j], items[j-1] = items[j-1], items[j]
			j--
		}
	}
}

func less(a, b FollowUp) bool {
	if a.Depth != b.Depth {
		return a.Depth < b.Depth
	}
	return a.EnqueuedAt.Before(b.EnqueuedAt)
}
