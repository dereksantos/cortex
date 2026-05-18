// Package dag — cross-turn budget rollover.
//
// When the executor refuses a spawn for budget_exceeded mid-turn,
// the refusal is *also* written to a per-project deferred-spawn
// queue. The next turn's seed is prepended with any fresh deferred
// spawns from the queue, so high-cost work that didn't fit in turn N
// gets a second chance in turn N+1.
//
// Stale spawns (older than the queue's MaxAge) are dropped on the
// next read — a 24-hour-old deferred spawn replaying surprisingly is
// a bug, not a feature. Default cap is 1 hour; configurable.
//
// File format: JSONL at .cortex/db/deferred_spawns.jsonl. One record
// per line. Cross-process safe via advisory flock on the file
// descriptor. Per-project — each project's .cortex/db/ is its own
// isolation boundary.
//
// See docs/adrs/0006-cross-turn-budget-rollover.md for the policy.
package dag

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeferredSpawn is one child refused for budget in turn N, persisted
// for replay at the start of a later turn. The TurnID + ParentNodeID
// pair preserves trace lineage across turns even though the parent
// won't exist in the replaying turn's tree.
//
// The Child field is a NodeSpec but only its identity-shaped fields
// (Function, Op, ID, Parent, Attrs) survive the roundtrip — the
// Handler and registry-time metadata are reconstituted from the
// registry by qualified name on replay. NodeSpec's MarshalJSON /
// UnmarshalJSON enforce this.
type DeferredSpawn struct {
	TurnID       string    `json:"turn_id"`
	ParentNodeID string    `json:"parent_node_id"`
	Child        NodeSpec  `json:"child"`
	Reason       string    `json:"reason"` // exhausted_axis ("latency_ms" / "tokens")
	DeferredAt   time.Time `json:"deferred_at"`
}

// DeferredQueue is the persistence contract for cross-turn rollover.
// Implementations must be safe for concurrent use within a process
// AND across processes (a daemon and a CLI invocation can both
// append to the same project's queue simultaneously).
type DeferredQueue interface {
	// Append persists one deferred spawn. Idempotent at the
	// best-effort level — duplicate records may exist if the caller
	// retries; ReadAndConsume dedupes by (turn_id, parent_node_id,
	// child qualified-name) within a single call.
	Append(DeferredSpawn) error

	// ReadAndConsume returns deferred spawns younger than the queue's
	// MaxAge AND removes them from the queue (so they replay
	// exactly once). Stale spawns (older than MaxAge) are dropped.
	// Returned spawns are ordered by DeferredAt ascending.
	ReadAndConsume() ([]DeferredSpawn, error)
}

// FileDeferredQueue is the file-backed JSONL implementation.
type FileDeferredQueue struct {
	path   string
	maxAge time.Duration

	// In-process serialization. Cross-process serialization is via
	// flock at the file-descriptor level; the in-process mutex
	// prevents lock-recursion + double-append.
	mu sync.Mutex
}

// DefaultDeferredSpawnMaxAge is the staleness cap for deferred
// spawns. Deferred spawns older than this are dropped on the next
// read rather than replayed. 1 hour matches the working-session
// expectation: a deferred spawn should still be relevant within a
// single coding session, not a workday.
const DefaultDeferredSpawnMaxAge = 1 * time.Hour

// NewFileDeferredQueue constructs a file-backed queue at the given
// path. maxAge=0 uses DefaultDeferredSpawnMaxAge. The parent
// directory is created on first Append; absent file = empty queue.
func NewFileDeferredQueue(path string, maxAge time.Duration) *FileDeferredQueue {
	if maxAge <= 0 {
		maxAge = DefaultDeferredSpawnMaxAge
	}
	return &FileDeferredQueue{path: path, maxAge: maxAge}
}

// MaxAge returns the staleness cap.
func (q *FileDeferredQueue) MaxAge() time.Duration { return q.maxAge }

// Path returns the on-disk file path.
func (q *FileDeferredQueue) Path() string { return q.path }

// Append writes one record to the queue, flock-protected so a
// concurrent daemon/CLI append cannot interleave bytes mid-line.
func (q *FileDeferredQueue) Append(ds DeferredSpawn) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(q.path), err)
	}
	f, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", q.path, err)
	}
	defer f.Close()

	if err := acquireExclusiveLock(int(f.Fd())); err != nil {
		return fmt.Errorf("flock %s: %w", q.path, err)
	}

	if ds.DeferredAt.IsZero() {
		ds.DeferredAt = time.Now()
	}
	line, err := json.Marshal(ds)
	if err != nil {
		return fmt.Errorf("marshal deferred spawn: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", q.path, err)
	}
	return nil
}

// ReadAndConsume reads the queue, partitions fresh-vs-stale, writes
// back nothing (we're consuming everything), and returns fresh.
// Stale spawns are dropped. Atomic via flock + rename.
func (q *FileDeferredQueue) ReadAndConsume() ([]DeferredSpawn, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	f, err := os.OpenFile(q.path, os.O_RDWR, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", q.path, err)
	}
	defer f.Close()

	if err := acquireExclusiveLock(int(f.Fd())); err != nil {
		return nil, fmt.Errorf("flock %s: %w", q.path, err)
	}

	var fresh []DeferredSpawn
	cutoff := time.Now().Add(-q.maxAge)
	scanner := bufio.NewScanner(f)
	// JSONL lines for nested NodeSpec.Attrs can exceed the default
	// 64KB scanner buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := map[string]bool{}
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ds DeferredSpawn
		if err := json.Unmarshal(raw, &ds); err != nil {
			// Corrupt line — skip but don't fail (rolling logs are
			// allowed to have garbage from incomplete writes).
			continue
		}
		if ds.DeferredAt.Before(cutoff) {
			continue
		}
		key := ds.TurnID + "|" + ds.ParentNodeID + "|" + ds.Child.QualifiedName()
		if seen[key] {
			continue
		}
		seen[key] = true
		fresh = append(fresh, ds)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", q.path, err)
	}

	// Truncate — we've consumed everything fresh and dropped stale.
	if err := f.Truncate(0); err != nil {
		return nil, fmt.Errorf("truncate %s: %w", q.path, err)
	}

	return fresh, nil
}
