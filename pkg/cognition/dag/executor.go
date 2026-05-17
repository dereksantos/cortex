// Package dag — seed-and-grow executor.
//
// The executor walks a tree that grows from a seed under a decaying
// budget. Each node may spawn children; the executor schedules them
// in the pending set; when budget exhausts, in-flight finishes but
// no new spawns happen.
//
// V0 scope (per docs/dag-build-plan.md Stage 1):
// - Single-threaded; spawn-by-spawn FIFO walking
// - No parallelism (Stage 4 adds it)
// - Per-node telemetry rows written to a callback (Phase 1
//   cell_results.jsonl integration is the caller's responsibility)
// - Depth cap + budget exhaustion graceful degradation
package dag

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TraceEntry is one node's executed record in the post-hoc trace.
// One entry per node call. Includes parent pointer for tree
// reconstruction and the cost actually consumed.
type TraceEntry struct {
	NodeID         string
	ParentID       string
	QualifiedName  string
	OK             bool
	ErrorCode      string
	ErrorMessage   string
	CostConsumed   Cost
	BudgetAfter    Budget
	Out            map[string]any
	SpawnedChildren []string // node IDs of children spawned by this node
	WallStart      time.Time
	WallEnd        time.Time
}

// Trace is the executor's post-hoc artifact for one turn.
type Trace struct {
	TurnID         string
	SeedNodeIDs    []string
	InitialBudget  Budget
	FinalBudget    Budget
	Exhausted      bool
	ExhaustedAxis  string
	TotalExecuted  int
	SpawnRefusals  []SpawnRefusal
	Entries        []TraceEntry
}

// SpawnRefusal records a child that was NOT scheduled because of
// budget exhaustion or depth cap.
type SpawnRefusal struct {
	ParentID      string
	ChildQualName string
	ErrorCode     string // budget_exceeded | depth_exceeded
	ExhaustedAxis string // for budget_exceeded
}

// Executor walks a seed under a decaying budget.
type Executor struct {
	registry *Registry
	traceCB  TraceCallback // optional: called per node after execution
}

// TraceCallback is invoked after each node executes — callers wire
// this to cell_results.jsonl writes (or whatever telemetry sink they
// use).
type TraceCallback func(TraceEntry)

// NewExecutor constructs an executor backed by the given registry.
// traceCB is optional; nil means no per-node callback.
func NewExecutor(reg *Registry, traceCB TraceCallback) *Executor {
	return &Executor{registry: reg, traceCB: traceCB}
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

	// Pending set: FIFO queue for v0 (no parallelism).
	type pendingItem struct {
		spec    NodeSpec
		depth   int // tree depth of this node (0 for seed)
	}
	pending := make([]pendingItem, 0, len(seed)*2)

	// Seed nodes get auto-assigned IDs if absent + depth 0.
	for i, s := range seed {
		if s.ID == "" {
			s.ID = fmt.Sprintf("seed-%d", i)
		}
		pending = append(pending, pendingItem{spec: s, depth: 0})
		trace.SeedNodeIDs = append(trace.SeedNodeIDs, s.ID)
	}

	nextSpawnIdx := 0 // monotonic counter for auto-assigning child IDs

	for len(pending) > 0 {
		// Budget exhaustion: stop spawning, abandon remaining pending.
		if exh, axis := budget.Exhausted(); exh {
			trace.Exhausted = true
			trace.ExhaustedAxis = axis
			// Record refusals for each unscheduled pending item.
			for _, p := range pending {
				trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
					ChildQualName: p.spec.QualifiedName(),
					ErrorCode:     "budget_exceeded",
					ExhaustedAxis: axis,
				})
			}
			break
		}

		// Dequeue head.
		item := pending[0]
		pending = pending[1:]

		entry := TraceEntry{
			NodeID:        item.spec.ID,
			ParentID:      item.spec.Parent,
			QualifiedName: item.spec.QualifiedName(),
			WallStart:     time.Now(),
		}

		// Look up handler.
		spec, err := e.registry.Get(item.spec.QualifiedName())
		if errors.Is(err, ErrUnknownNode) {
			entry.OK = false
			entry.ErrorCode = "unknown_node"
			entry.ErrorMessage = err.Error()
			entry.WallEnd = time.Now()
			trace.Entries = append(trace.Entries, entry)
			if e.traceCB != nil {
				e.traceCB(entry)
			}
			continue
		} else if err != nil {
			return trace, fmt.Errorf("registry get %s: %w", item.spec.QualifiedName(), err)
		}

		// Merge per-call attrs from the spawn spec with registry defaults.
		// In v0, the spawn spec's Attrs takes precedence.
		invocation := spec
		invocation.ID = item.spec.ID
		invocation.Parent = item.spec.Parent
		invocation.Attrs = item.spec.Attrs

		// Invoke handler.
		result, herr := invocation.Handler(ctx, invocation.Attrs, budget)
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

		// Apply cost to budget regardless of handler error (cost was
		// already incurred).
		budget.Consume(result.CostConsumed)

		// Schedule spawned children — respecting depth cap and budget.
		if entry.OK {
			for _, childSpec := range result.Spawn {
				childDepth := item.depth + 1
				// Depth cap: initial.Depth names the *deepest allowed*
				// depth. A child at depth > initial.Depth is refused.
				if childDepth > initial.Depth {
					trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
						ParentID:      item.spec.ID,
						ChildQualName: childSpec.QualifiedName(),
						ErrorCode:     "depth_exceeded",
					})
					continue
				}
				// Pre-spawn budget check: if the child's registered cost
				// hint exceeds the remaining budget on any axis, refuse
				// before scheduling. Modulate model from dag-protocol.md.
				if childRegistered, getErr := e.registry.Get(childSpec.QualifiedName()); getErr == nil {
					if !budget.CanAfford(childRegistered.Cost) {
						axis := "latency_ms"
						if childRegistered.Cost.Tokens > budget.Tokens {
							axis = "tokens"
						}
						trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
							ParentID:      item.spec.ID,
							ChildQualName: childSpec.QualifiedName(),
							ErrorCode:     "budget_exceeded",
							ExhaustedAxis: axis,
						})
						continue
					}
				}
				// MaxFanout check (per the parent spec).
				if len(entry.SpawnedChildren) >= spec.MaxFanout {
					trace.SpawnRefusals = append(trace.SpawnRefusals, SpawnRefusal{
						ParentID:      item.spec.ID,
						ChildQualName: childSpec.QualifiedName(),
						ErrorCode:     "max_fanout_exceeded",
					})
					continue
				}
				// Auto-assign child ID if not set.
				if childSpec.ID == "" {
					nextSpawnIdx++
					childSpec.ID = fmt.Sprintf("n-%d", nextSpawnIdx)
				}
				childSpec.Parent = item.spec.ID
				pending = append(pending, pendingItem{spec: childSpec, depth: childDepth})
				entry.SpawnedChildren = append(entry.SpawnedChildren, childSpec.ID)
			}
		}

		entry.BudgetAfter = budget
		trace.Entries = append(trace.Entries, entry)
		trace.TotalExecuted++

		if e.traceCB != nil {
			e.traceCB(entry)
		}

		// Pre-spawn budget check for next iteration is handled at the
		// top of the loop (Exhausted check). Depth-cap is checked at
		// spawn-schedule time above.
	}

	trace.FinalBudget = budget
	return trace, nil
}
