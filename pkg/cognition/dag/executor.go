// Package dag — seed-and-grow executor.
//
// The executor walks a tree that grows from a seed under a decaying
// budget. Each node may spawn children; the executor schedules them
// in the pending set; when budget exhausts, in-flight finishes but
// no new spawns happen.
//
// Stage 4 introduces batch-parallel execution: each tick drains the
// pending set into a batch and runs the batch concurrently. Budget
// state is mutex-guarded; cost application after each handler return
// is serialized. Sequential mode remains opt-in via SetSequential —
// the mechanic-4 budget-exhaustion test relies on the in-flight-only
// semantics that sequential walking guarantees.
package dag

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// TraceEntry is one node's executed record in the post-hoc trace.
// One entry per node call. Includes parent pointer for tree
// reconstruction and the cost actually consumed.
type TraceEntry struct {
	NodeID          string
	ParentID        string
	QualifiedName   string
	OK              bool
	ErrorCode       string
	ErrorMessage    string
	CostConsumed    Cost
	BudgetAfter     Budget
	Out             map[string]any
	SpawnedChildren []string // node IDs of children spawned by this node
	WallStart       time.Time
	WallEnd         time.Time
}

// Trace is the executor's post-hoc artifact for one turn.
type Trace struct {
	TurnID        string
	SeedNodeIDs   []string
	InitialBudget Budget
	FinalBudget   Budget
	Exhausted     bool
	ExhaustedAxis string
	TotalExecuted int
	SpawnRefusals []SpawnRefusal
	Entries       []TraceEntry
}

// SpawnRefusal records a child that was NOT scheduled because of
// budget exhaustion or depth cap.
type SpawnRefusal struct {
	ParentID      string
	ChildQualName string
	ErrorCode     string // budget_exceeded | depth_exceeded | max_fanout_exceeded
	ExhaustedAxis string // for budget_exceeded
}

// Executor walks a seed under a decaying budget.
type Executor struct {
	registry   *Registry
	traceCB    TraceCallback
	sequential bool          // when true, run nodes one at a time (Stage 1-3 behavior)
	deferred   DeferredQueue // optional; when set, budget_exceeded refusals are queued for rollover and prior fresh deferrals are prepended to the seed
}

// TraceCallback is invoked after each node executes — callers wire
// this to cell_results.jsonl writes (or whatever telemetry sink they
// use).
type TraceCallback func(TraceEntry)

// nodeIDContextKey is the context key the executor uses to surface
// the currently-executing node's ID to its handler. Handlers that
// emit synthetic child trace entries (e.g., Stage 3 coding_turn
// fabricating act.* rows from intercepted tool calls) need this to
// set the correct ParentID. Read via NodeIDFromContext.
type nodeIDContextKey struct{}

// NodeIDFromContext returns the ID of the currently-executing node,
// or "" if ctx wasn't produced by Executor.Run. Handlers that don't
// care about their own ID can ignore this.
func NodeIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(nodeIDContextKey{}).(string)
	return v
}

// NewExecutor constructs an executor backed by the given registry.
// Defaults to batch-parallel execution; tests that need deterministic
// budget-exhaustion semantics call SetSequential(true). traceCB is
// optional; nil means no per-node callback.
func NewExecutor(reg *Registry, traceCB TraceCallback) *Executor {
	return &Executor{registry: reg, traceCB: traceCB, sequential: false}
}

// SetSequential toggles batch-parallel vs single-threaded walking.
// Sequential mode preserves the Stage 1-3 budget semantics where a
// node that exhausts the budget mid-batch prevents its peers from
// running.
func (e *Executor) SetSequential(seq bool) {
	e.sequential = seq
}

// Sequential reports whether the executor will walk one node at a
// time. Exposed for tests + diagnostics.
func (e *Executor) Sequential() bool { return e.sequential }

// SetDeferredQueue wires a persistence layer for cross-turn budget
// rollover. When set:
//   - budget_exceeded spawn refusals are also appended to the queue
//   - on the next Run, fresh deferred spawns from the queue are
//     prepended to the seed (with their original ParentNodeID
//     preserved for cross-turn trace continuity)
//
// Pass nil to disable rollover. Tests typically leave this unset.
func (e *Executor) SetDeferredQueue(q DeferredQueue) {
	e.deferred = q
}

// pendingItem is one node waiting to execute.
type pendingItem struct {
	spec  NodeSpec
	depth int // tree depth of this node (0 for seed)
}

// Run walks the seed under the given budget and returns the trace.
// turnID is the identifier the executor uses for cross-referencing
// trace entries to the journal.
func (e *Executor) Run(ctx context.Context, turnID string, seed []NodeSpec, initial Budget) (*Trace, error) {
	if len(seed) == 0 {
		return nil, fmt.Errorf("Run: empty seed")
	}

	trace := &Trace{
		TurnID:        turnID,
		InitialBudget: initial,
	}
	budget := initial

	pending := make([]pendingItem, 0, len(seed)*2)

	// Cross-turn rollover: drain fresh deferred spawns from prior
	// turns and prepend them to the pending set before the caller's
	// seed. Failures here are non-fatal — a missing queue file just
	// means no prior deferrals, and a corrupt one is skipped (the
	// queue's reader tolerates corrupt lines).
	if e.deferred != nil {
		deferred, err := e.deferred.ReadAndConsume()
		if err == nil {
			for _, ds := range deferred {
				spec := ds.Child
				if spec.Parent == "" {
					spec.Parent = ds.ParentNodeID
				}
				if spec.ID == "" {
					spec.ID = fmt.Sprintf("deferred-%d", len(pending))
				}
				pending = append(pending, pendingItem{spec: spec, depth: 0})
				trace.SeedNodeIDs = append(trace.SeedNodeIDs, spec.ID)
			}
		}
	}

	for i, s := range seed {
		if s.ID == "" {
			s.ID = fmt.Sprintf("seed-%d", i)
		}
		pending = append(pending, pendingItem{spec: s, depth: 0})
		trace.SeedNodeIDs = append(trace.SeedNodeIDs, s.ID)
	}

	// Monotonic counter for auto-assigning child IDs. Mutex-guarded
	// because parallel handler callbacks (Stage 3 act-op dispatcher)
	// may race against spawn-scheduling; safer to centralize.
	var spawnIDMu sync.Mutex
	nextSpawnIdx := 0
	nextChildID := func() string {
		spawnIDMu.Lock()
		defer spawnIDMu.Unlock()
		nextSpawnIdx++
		return fmt.Sprintf("n-%d", nextSpawnIdx)
	}

	if e.sequential {
		return e.runSequential(ctx, trace, &budget, pending, initial, nextChildID)
	}
	return e.runParallel(ctx, trace, &budget, pending, initial, nextChildID)
}

// runSequential preserves the Stage 1-3 FIFO single-threaded walk.
// Mechanic-1..4 expectations are pinned to this path; mechanic-5
// (tree-shape variation) is mode-agnostic.
func (e *Executor) runSequential(
	ctx context.Context,
	trace *Trace,
	budget *Budget,
	pending []pendingItem,
	initial Budget,
	nextChildID func() string,
) (*Trace, error) {
	for len(pending) > 0 {
		if exh, axis := budget.Exhausted(); exh {
			trace.Exhausted = true
			trace.ExhaustedAxis = axis
			for _, p := range pending {
				trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
					ChildQualName: p.spec.QualifiedName(),
					ErrorCode:     "budget_exceeded",
					ExhaustedAxis: axis,
				})
			}
			break
		}

		item := pending[0]
		pending = pending[1:]

		entry, result, spec, ok := e.invokeOne(ctx, item, *budget)
		if !ok {
			trace.Entries = append(trace.Entries, entry)
			if e.traceCB != nil {
				e.traceCB(entry)
			}
			continue
		}

		budget.Consume(result.CostConsumed)
		entry.BudgetAfter = *budget

		if entry.OK {
			children, refusals := e.scheduleChildren(trace.TurnID, item, spec, result.Spawn, *budget, initial, nextChildID)
			entry.SpawnedChildren = childIDs(children)
			pending = append(pending, children...)
			trace.SpawnRefusals = append(trace.SpawnRefusals, refusals...)
		}

		trace.Entries = append(trace.Entries, entry)
		trace.TotalExecuted++
		if e.traceCB != nil {
			e.traceCB(entry)
		}
	}

	trace.FinalBudget = *budget
	return trace, nil
}

// runParallel walks the seed by draining the current pending set into
// a batch each iteration. Handlers in a batch run concurrently;
// budget application + spawn scheduling happen after the batch joins.
// Trace entries are sorted by WallStart so the JSONL projection is
// time-ordered (per dag-protocol.md "Trace ordering").
func (e *Executor) runParallel(
	ctx context.Context,
	trace *Trace,
	budget *Budget,
	pending []pendingItem,
	initial Budget,
	nextChildID func() string,
) (*Trace, error) {
	type batchResult struct {
		item    pendingItem
		entry   TraceEntry
		result  NodeResult
		spec    NodeSpec
		ok      bool
		unknown bool
	}

	for len(pending) > 0 {
		if exh, axis := budget.Exhausted(); exh {
			trace.Exhausted = true
			trace.ExhaustedAxis = axis
			for _, p := range pending {
				trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
					ChildQualName: p.spec.QualifiedName(),
					ErrorCode:     "budget_exceeded",
					ExhaustedAxis: axis,
				})
			}
			break
		}

		batch := pending
		pending = pending[:0]

		results := make([]batchResult, len(batch))
		var wg sync.WaitGroup
		// Snapshot the budget for handlers — they see the same
		// pre-batch budget. Real consumption applies after the join.
		snapshot := *budget
		for i, item := range batch {
			wg.Add(1)
			go func(i int, item pendingItem) {
				defer wg.Done()
				entry, result, spec, ok := e.invokeOne(ctx, item, snapshot)
				results[i] = batchResult{
					item:    item,
					entry:   entry,
					result:  result,
					spec:    spec,
					ok:      ok,
					unknown: !ok,
				}
			}(i, item)
		}
		wg.Wait()

		// Serialize trace + budget mutations in WallStart order so the
		// JSONL projection reads time-forward regardless of goroutine
		// scheduling. Stable sort preserves seed-order ties.
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].entry.WallStart.Before(results[j].entry.WallStart)
		})

		for _, r := range results {
			if r.unknown {
				trace.Entries = append(trace.Entries, r.entry)
				if e.traceCB != nil {
					e.traceCB(r.entry)
				}
				continue
			}
			budget.Consume(r.result.CostConsumed)
			entry := r.entry
			entry.BudgetAfter = *budget

			if entry.OK {
				children, refusals := e.scheduleChildren(trace.TurnID, r.item, r.spec, r.result.Spawn, *budget, initial, nextChildID)
				entry.SpawnedChildren = childIDs(children)
				pending = append(pending, children...)
				trace.SpawnRefusals = append(trace.SpawnRefusals, refusals...)
			}

			trace.Entries = append(trace.Entries, entry)
			trace.TotalExecuted++
			if e.traceCB != nil {
				e.traceCB(entry)
			}
		}
	}

	trace.FinalBudget = *budget
	return trace, nil
}

// invokeOne looks up the handler, runs it, builds the trace entry,
// and reports the registered spec. ok=false means the handler couldn't
// be looked up (entry has ErrorCode set, caller should append and
// move on); ok=true means handler ran (may still have entry.OK=false
// if the handler errored).
func (e *Executor) invokeOne(ctx context.Context, item pendingItem, budgetSnapshot Budget) (TraceEntry, NodeResult, NodeSpec, bool) {
	entry := TraceEntry{
		NodeID:        item.spec.ID,
		ParentID:      item.spec.Parent,
		QualifiedName: item.spec.QualifiedName(),
		WallStart:     time.Now(),
	}

	spec, err := e.registry.Get(item.spec.QualifiedName())
	if errors.Is(err, ErrUnknownNode) {
		entry.OK = false
		entry.ErrorCode = "unknown_node"
		entry.ErrorMessage = err.Error()
		entry.WallEnd = time.Now()
		return entry, NodeResult{}, NodeSpec{}, false
	} else if err != nil {
		// Non-ErrUnknownNode registry error — treat as handler_error
		// shape so the trace still emits a row.
		entry.OK = false
		entry.ErrorCode = "registry_error"
		entry.ErrorMessage = err.Error()
		entry.WallEnd = time.Now()
		return entry, NodeResult{}, NodeSpec{}, false
	}

	invocation := spec
	invocation.ID = item.spec.ID
	invocation.Parent = item.spec.Parent
	invocation.Attrs = item.spec.Attrs

	ctxForHandler := context.WithValue(ctx, nodeIDContextKey{}, invocation.ID)
	result, herr := invocation.Handler(ctxForHandler, invocation.Attrs, budgetSnapshot)
	entry.WallEnd = time.Now()
	entry.CostConsumed = result.CostConsumed
	entry.Out = result.Out

	if herr != nil {
		entry.OK = false
		entry.ErrorCode = "handler_error"
		entry.ErrorMessage = herr.Error()
	} else {
		entry.OK = true
	}

	return entry, result, spec, true
}

// scheduleChildren applies depth, per-op MaxFanout, and budget pre-check
// to the parent's spawned children. Returns the children to enqueue
// (with parent + auto-IDs filled in) and any spawn refusals. When a
// DeferredQueue is wired on the executor, budget_exceeded refusals
// are also appended to the queue for cross-turn rollover.
func (e *Executor) scheduleChildren(
	turnID string,
	parent pendingItem,
	parentSpec NodeSpec,
	spawn []NodeSpec,
	currentBudget Budget,
	initial Budget,
	nextChildID func() string,
) ([]pendingItem, []SpawnRefusal) {
	var scheduled []pendingItem
	var refusals []SpawnRefusal
	fanoutCount := 0
	for _, childSpec := range spawn {
		childDepth := parent.depth + 1
		if childDepth > initial.Depth {
			refusals = append(refusals, SpawnRefusal{
				ParentID:      parent.spec.ID,
				ChildQualName: childSpec.QualifiedName(),
				ErrorCode:     "depth_exceeded",
			})
			continue
		}
		if childRegistered, getErr := e.registry.Get(childSpec.QualifiedName()); getErr == nil {
			if !currentBudget.CanAfford(childRegistered.Cost) {
				axis := "latency_ms"
				if childRegistered.Cost.Tokens > currentBudget.Tokens {
					axis = "tokens"
				}
				refusals = append(refusals, SpawnRefusal{
					ParentID:      parent.spec.ID,
					ChildQualName: childSpec.QualifiedName(),
					ErrorCode:     "budget_exceeded",
					ExhaustedAxis: axis,
				})
				if e.deferred != nil {
					// Best-effort: a queue write failure must not
					// derail the turn (the in-progress trace is more
					// valuable than the rollover record).
					_ = e.deferred.Append(DeferredSpawn{
						TurnID:       turnID,
						ParentNodeID: parent.spec.ID,
						Child:        childSpec,
						Reason:       axis,
					})
				}
				continue
			}
		}
		if fanoutCount >= parentSpec.MaxFanout {
			refusals = append(refusals, SpawnRefusal{
				ParentID:      parent.spec.ID,
				ChildQualName: childSpec.QualifiedName(),
				ErrorCode:     "max_fanout_exceeded",
			})
			continue
		}
		if childSpec.ID == "" {
			childSpec.ID = nextChildID()
		}
		childSpec.Parent = parent.spec.ID
		scheduled = append(scheduled, pendingItem{spec: childSpec, depth: childDepth})
		fanoutCount++
	}
	return scheduled, refusals
}

// childIDs extracts node IDs from a slice of pending items for the
// SpawnedChildren trace field.
func childIDs(items []pendingItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.spec.ID
	}
	return out
}
