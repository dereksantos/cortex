// Package dag — per-turn shared state.
//
// The executor maintains a turn-scoped KV map that any handler can
// read via PriorOut / PriorOutByName. After each node completes, its
// NodeResult.Out is deposited into the state under the node's id (and
// reverse-indexed by qualified name). Downstream handlers — especially
// "synthesize" nodes that need to compose an answer from prior tool
// calls — can fetch those outputs without the parent having to thread
// them through Attrs at spawn time.
//
// Concurrency: in parallel mode, multiple handlers may read the state
// concurrently. The map is mutex-guarded. Writes happen after a batch
// joins (serialized in the executor), so reads in the next batch see
// a consistent view of all prior batches' outputs.
package dag

import (
	"context"
	"sync"
)

// turnState is the per-turn shared output map. Construct one per
// Executor.Run; bind to context via withTurnState; read via
// PriorOut / PriorOutByName.
type turnState struct {
	mu sync.RWMutex
	// outs is keyed by node id. Each entry is the NodeResult.Out from
	// that node's most recent execution. Re-executions (if any) over-
	// write the entry — node ids are turn-scoped, so collisions only
	// happen via parent.SpawnedChildren reuse.
	outs map[string]map[string]any
	// names tracks the ordered list of node ids per qualified name so
	// PriorOutByName can return "most recent" deterministically.
	names map[string][]string
	// types tracks qualified name per node id (cheap reverse lookup
	// for diagnostic helpers).
	types map[string]string
	// order is the global deposit order — node ids in the sequence
	// they completed. Used by AllPriorOutputs to surface a turn-wide
	// chronological history (synthesis handlers fold over this).
	order []string
}

func newTurnState() *turnState {
	return &turnState{
		outs:  make(map[string]map[string]any),
		names: make(map[string][]string),
		types: make(map[string]string),
	}
}

// deposit records a completed node's Out into the turn state. nodeID
// is required; qname (e.g., "act.read_file") may be empty.
func (s *turnState) deposit(nodeID, qname string, out map[string]any) {
	if s == nil || nodeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, existed := s.outs[nodeID]; !existed {
		s.order = append(s.order, nodeID)
	}
	s.outs[nodeID] = out
	if qname != "" {
		s.names[qname] = append(s.names[qname], nodeID)
		s.types[nodeID] = qname
	}
}

func (s *turnState) get(nodeID string) (map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out, ok := s.outs[nodeID]
	return out, ok
}

func (s *turnState) lastByName(qname string) (string, map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.names[qname]
	if len(ids) == 0 {
		return "", nil, false
	}
	id := ids[len(ids)-1]
	return id, s.outs[id], true
}

// allByName returns every output recorded for qname in execution
// order. Used by synthesis nodes that want to fold over multiple
// tool calls of the same type (e.g., three act.read_file invocations).
func (s *turnState) allByName(qname string) []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.names[qname]
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if v, ok := s.outs[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

// PriorOutputRecord is one entry in the turn state — a node id, its
// qualified name, and the Out map. Returned by AllPriorOutputs in
// deposit order. Useful for synthesis handlers that want to fold over
// every prior step (e.g., to build a context block for the model).
type PriorOutputRecord struct {
	NodeID        string
	QualifiedName string
	Out           map[string]any
}

// all returns the deposit-order list of every recorded output. Read-
// locked snapshot — the returned slice references the underlying
// Out maps directly, so callers must not mutate them.
func (s *turnState) all() []PriorOutputRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PriorOutputRecord, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, PriorOutputRecord{
			NodeID:        id,
			QualifiedName: s.types[id],
			Out:           s.outs[id],
		})
	}
	return out
}

// AllPriorOutputs returns every recorded NodeResult.Out from this
// turn in deposit order. Returns nil when no turn state is attached.
// Used by handlers (e.g., decide.coding_turn) that want to fold the
// full prior history into their prompt context.
func AllPriorOutputs(ctx context.Context) []PriorOutputRecord {
	s := turnStateFromContext(ctx)
	if s == nil {
		return nil
	}
	return s.all()
}

// turnStateContextKey carries the turnState through ctx.WithValue.
// Private — handlers should use the exported PriorOut helpers, not
// fish the state out of context directly.
type turnStateContextKey struct{}

// withTurnState returns a derived context that carries s. Callers
// (the executor) attach this at Run-entry; handlers consume it via
// PriorOut / PriorOutByName.
func withTurnState(ctx context.Context, s *turnState) context.Context {
	return context.WithValue(ctx, turnStateContextKey{}, s)
}

// turnStateFromContext is the internal accessor. Returns nil when no
// turn state is attached — callers should tolerate that path (e.g.,
// when a handler runs outside the executor, like in a unit test).
func turnStateFromContext(ctx context.Context) *turnState {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(turnStateContextKey{}).(*turnState)
	return v
}

// PriorOut returns the NodeResult.Out for the named prior node in
// this turn, or nil if the id hasn't run (yet) or if no turn state
// is attached. nodeID should match the id the parent set when
// spawning, or the auto-assigned "n-N" id the executor produced.
func PriorOut(ctx context.Context, nodeID string) map[string]any {
	s := turnStateFromContext(ctx)
	if s == nil {
		return nil
	}
	out, _ := s.get(nodeID)
	return out
}

// PriorOutByName returns the most recent Out from any node whose
// qualified name matches qname (e.g., "act.read_file"). Returns nil
// when no matching node has completed in this turn. Handy for
// "synthesize from the last tool call" patterns where the caller
// doesn't know the spawned node id.
func PriorOutByName(ctx context.Context, qname string) map[string]any {
	s := turnStateFromContext(ctx)
	if s == nil {
		return nil
	}
	_, out, _ := s.lastByName(qname)
	return out
}

// PriorOutsByName returns every Out recorded for qname this turn in
// execution order. Used by synthesis handlers that need to fold over
// multiple invocations of the same op.
func PriorOutsByName(ctx context.Context, qname string) []map[string]any {
	s := turnStateFromContext(ctx)
	if s == nil {
		return nil
	}
	return s.allByName(qname)
}

// WithTestTurnState attaches a turn state to ctx pre-seeded with the
// given (nodeID, qualifiedName, out) records — exposed so tests in
// sibling packages (e.g. ops) can exercise turn-state-reading
// handlers without spinning a real executor. Records are deposited
// in order; if you need a specific "latest" assertion (PriorOutByName
// / LatestAccumulatorSnapshot) put the desired record last.
//
// Not meant for production use — handlers reach turn state via the
// executor's natural attachment.
func WithTestTurnState(ctx context.Context, records []TestDeposit) context.Context {
	s := newTurnState()
	for _, r := range records {
		s.deposit(r.NodeID, r.QualifiedName, r.Out)
	}
	return withTurnState(ctx, s)
}

// TestDeposit is one seeded entry for WithTestTurnState.
type TestDeposit struct {
	NodeID        string
	QualifiedName string
	Out           map[string]any
}

// LatestAccumulatorSnapshot returns the most recent attend.accumulate
// snapshot deposited in turn state, or "" when no accumulator has
// run this turn yet.
//
// This is the bridge that lets later nodes (decide.next, the final
// decide.coding_turn) read the bounded working memory the
// accumulator chain has been building. Returns the snapshot string
// only; callers who want the token count or fallback flag can fetch
// the full Out map via PriorOutByName("attend.accumulate").
func LatestAccumulatorSnapshot(ctx context.Context) string {
	out := PriorOutByName(ctx, "attend.accumulate")
	if out == nil {
		return ""
	}
	snap, _ := out["snapshot"].(string)
	return snap
}
