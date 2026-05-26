// Package dag — node registry.
//
// The registry is the single source of truth for what nodes (cortex
// function × op) exist. Adding a Cortex capability is registering a
// node: declare its function, op, inputs/outputs, cost hint, and a
// handler conforming to the Handler signature.
//
// Per docs/dag-protocol.md "Node registry".
package dag

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// CortexFunction is one of the 10 typed roles a node can fill, per
// docs/integration-roadmap.md "Cortex functions (architecture)".
type CortexFunction string

const (
	FuncSense     CortexFunction = "sense"
	FuncAttend    CortexFunction = "attend"
	FuncRepresent CortexFunction = "represent"
	FuncRemember  CortexFunction = "remember"
	FuncModel     CortexFunction = "model"
	FuncValue     CortexFunction = "value"
	FuncDecide    CortexFunction = "decide"
	FuncAct       CortexFunction = "act"
	FuncMaintain  CortexFunction = "maintain"
	FuncModulate  CortexFunction = "modulate"
)

// IsValid reports whether f names a known cortex function.
func (f CortexFunction) IsValid() bool {
	switch f {
	case FuncSense, FuncAttend, FuncRepresent, FuncRemember, FuncModel,
		FuncValue, FuncDecide, FuncAct, FuncMaintain, FuncModulate:
		return true
	}
	return false
}

// NodeResult is what a Handler returns from a single invocation.
// Per docs/dag-protocol.md "Handler signature".
type NodeResult struct {
	// Out is the named outputs downstream consumers may reference via
	// $<node_id>.<out_name>.
	Out map[string]any

	// Spawn is the list of children to schedule after this node
	// completes. May be empty (leaf nodes).
	Spawn []NodeSpec

	// CostConsumed is the actual cost this node call consumed. The
	// executor subtracts this from the running turn budget.
	CostConsumed Cost
}

// Handler is the function signature every registered op must
// satisfy. It runs the op's work and reports outputs + any children
// to spawn + actual cost.
//
// budget is the *remaining* turn budget when this handler is invoked
// — handlers may self-modulate (e.g., skip LLM call when LatencyMS
// is low) and should never spend more than what's offered.
type Handler func(ctx context.Context, in map[string]any, budget Budget) (NodeResult, error)

// AxisContract names the 6-axis guarantees an act-typed node must
// honor. Per docs/tool-surface.md.
type AxisContract struct {
	Mutator              bool // axis 2: read-only vs mutator
	RequiresConfirmation bool // axis 5: destructive ops require confirm=true attr
}

// NodeSpec is both the registration record AND a scheduled
// invocation — fields are populated at registration time, then
// merged with per-call attrs at schedule time. The executor builds a
// NodeSpec for each pending invocation by combining the registry
// entry with the spawning node's spawn-spec.
type NodeSpec struct {
	// Identity (set at registration).
	Function    CortexFunction
	Op          string
	Description string

	// Schema (set at registration).
	Inputs  []ParamSpec
	Outputs []ParamSpec

	// ToolSchemaJSON is the underlying tool's JSON Schema (only set for
	// act-typed nodes built by AdaptToolAsAct). It is the OpenAI function-
	// calling parameters object — what specialist tool-call models (xLAM,
	// hermes-pro, etc.) are trained to consume. The wrapper's own Inputs
	// describe the DAG-level handler contract (`args` string + `confirm`
	// bool); ToolSchemaJSON describes the REAL parameter shape the LLM
	// must populate. The catalog renderer in decide.tool_call surfaces
	// this so the specialist sees actual field names and types instead of
	// the wrapper's plumbing.
	ToolSchemaJSON json.RawMessage

	// Contracts (set at registration; only meaningful for FuncAct).
	AxisContract *AxisContract

	// Cost hint (set at registration; used by executor for pre-spawn
	// budget check).
	Cost Cost

	// MaxFanout caps how many children a single invocation may spawn
	// (executor enforces). Defaults to 10 if zero.
	MaxFanout int

	// Exposable marks ops that a steering layer (e.g. decide.next) may
	// surface to an LLM as composable building blocks. Defaults to
	// false — only ops the steering LLM should know about should set
	// this. Used by Registry.Exposable() and the ops/catalog formatter
	// to filter out internal/stub/dispatcher-only ops.
	Exposable bool

	// Requires is the ordered capability preference chain the picker
	// walks at spawn time to pick the model for this node. Tags come
	// from pkg/llm.Cap* constants, including `:specialist` variants.
	// Empty means "any model — use the session default."
	//
	// The chain expresses preference, not requirement: the picker tries
	// the first cap, falls back to the next on no match, and ultimately
	// to the session default when the chain exhausts. Per
	// docs/per-node-routing-plan.md. Registration-time — not persisted
	// in nodeSpecPersist; reconstituted from the registry by qualified
	// name on replay, alongside Cost / Inputs / Outputs / Handler.
	Requires []string

	// Handler (set at registration).
	Handler Handler

	// Per-invocation (populated by executor at schedule time).
	ID     string         // unique within the turn tree
	Parent string         // parent node id; empty for seed nodes
	Attrs  map[string]any // call-time options

	// Salience (per-invocation; set by the spawning parent when it
	// wants to cap how many tokens this child may deposit into turn
	// state). Nil means "no contract" — pre-salience-budgets behavior.
	// See docs/salience-budgets.md for the protocol.
	Salience *SalienceContract
}

// SalienceContract is the per-spawn agreement between a parent and its
// child: "your output may not exceed MaxOutputTokens; if you must
// compress to fit, preserve content relevant to Intent." When the
// child's Out exceeds the cap, the executor synthesizes a
// reflect.compress node to bring it under budget before depositing into
// turn state. See docs/salience-budgets.md.
type SalienceContract struct {
	// MaxOutputTokens caps tokens deposited into turn state. Zero
	// means "no cap" — the contract was attached but not enforced
	// (Phase 1 default for many spawns until decide.next learns to
	// allocate budgets).
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`

	// Intent is the short string used as the compressor's salience
	// prompt: what should be preserved for the upstream consumer.
	// Empty means "preserve most salient content for general use."
	Intent string `json:"intent,omitempty"`

	// ChunkOnOversize switches the oversize-handling strategy from
	// LLM-mediated attend.compress (default) to deterministic line-
	// based chunking. When true and the output exceeds MaxOutputTokens,
	// the executor splits the content by line boundary into N pieces
	// each ≤ MaxOutputTokens, joins them into a single deposit with
	// "[chunk i/N, lines a-b]" headers, and emits a synthetic
	// attend.chunk trace entry instead of attend.compress.
	//
	// Use for tools (act.read_file, act.run_shell) where the calling
	// model can act on raw labeled content and an LLM-summarized version
	// would lose information. Leave false for nodes where salience
	// extraction is genuinely valuable (e.g. distilling search results).
	ChunkOnOversize bool `json:"chunk_on_oversize,omitempty"`

	// MaxEmittedTokens caps the TOTAL tokens emitted across all chunks
	// when ChunkOnOversize is true. Zero falls back to the legacy
	// MaxEmittedChunks=8 chunk-count cap (preserves pre-2026-05-25
	// behavior for callers that haven't been migrated). When > 0, the
	// chunker walks chunks accumulating their per-chunk token cost and
	// truncates as soon as adding the next would exceed this budget.
	//
	// Sized by Budget.EmittedTokensCap() at spawn time — 4K for code
	// intent, 16K for review/recall/meta, capped at 30% of the model's
	// context window. The old chunk-count cap was sized for older
	// per-chunk caps (1K-8K); at today's 500-token per-chunk cap, 8
	// chunks meant ~22% of repl.go was emitted before truncation, and
	// the calling model had no way to ask for the rest. Token budget
	// makes the cap independent of the per-chunk size.
	MaxEmittedTokens int `json:"max_emitted_tokens,omitempty"`
}

// ParamSpec declares one input or output parameter for a node.
type ParamSpec struct {
	Name     string
	Type     string // free-form for v0 (string / int / []Result / etc)
	Required bool
}

// QualifiedName returns "<function>.<op>" — the canonical lookup key.
func (n NodeSpec) QualifiedName() string {
	return fmt.Sprintf("%s.%s", n.Function, n.Op)
}

// nodeSpecPersist is the on-disk projection of a NodeSpec — only the
// identity-shaped fields that callers populate at spawn time. Handler
// and registration-time metadata (Cost, Inputs, Outputs, MaxFanout,
// AxisContract, Description) are reconstituted from the registry by
// qualified name on replay. Used by JSON marshalling so DeferredSpawn
// records can roundtrip through the file-backed queue.
type nodeSpecPersist struct {
	Function CortexFunction    `json:"function"`
	Op       string            `json:"op"`
	ID       string            `json:"id,omitempty"`
	Parent   string            `json:"parent,omitempty"`
	Attrs    map[string]any    `json:"attrs,omitempty"`
	Salience *SalienceContract `json:"salience,omitempty"`
}

// MarshalJSON projects NodeSpec down to its persistable identity for
// the DeferredSpawn queue. Non-persistable fields (Handler, cost
// hints, schema) are dropped — the executor looks them up on replay.
// Salience is identity-shaped (it was set by a parent at spawn time,
// not at registration) so it rides along.
func (n NodeSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeSpecPersist{
		Function: n.Function,
		Op:       n.Op,
		ID:       n.ID,
		Parent:   n.Parent,
		Attrs:    n.Attrs,
		Salience: n.Salience,
	})
}

// UnmarshalJSON populates a NodeSpec from its persistable identity.
// The Handler + registration-time fields stay zero; callers (the
// executor) look them up by QualifiedName before invocation.
func (n *NodeSpec) UnmarshalJSON(data []byte) error {
	var p nodeSpecPersist
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	n.Function = p.Function
	n.Op = p.Op
	n.ID = p.ID
	n.Parent = p.Parent
	n.Attrs = p.Attrs
	n.Salience = p.Salience
	return nil
}

// Registry is the in-memory map of registered nodes. Construct one
// per process via NewRegistry; register ops at init() time from the
// owning package.
type Registry struct {
	mu    sync.RWMutex
	specs map[string]NodeSpec // key: <function>.<op>
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		specs: make(map[string]NodeSpec),
	}
}

// Register adds a node to the registry. Replaces any prior entry with
// the same qualified name (last registration wins — useful for tests
// that swap real handlers for mocks).
func (r *Registry) Register(spec NodeSpec) error {
	if !spec.Function.IsValid() {
		return fmt.Errorf("Register: unknown function %q", spec.Function)
	}
	if spec.Op == "" {
		return fmt.Errorf("Register: empty op for function %s", spec.Function)
	}
	if spec.Handler == nil {
		return fmt.Errorf("Register: nil handler for %s", spec.QualifiedName())
	}
	if spec.MaxFanout == 0 {
		spec.MaxFanout = 10
	}
	if spec.Function == FuncAct && spec.AxisContract == nil {
		return fmt.Errorf("Register: act-typed node %s missing AxisContract", spec.QualifiedName())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs[spec.QualifiedName()] = spec
	return nil
}

// Get returns the registered spec for the given qualified name.
// Returns ErrUnknownNode if not registered.
func (r *Registry) Get(qualifiedName string) (NodeSpec, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[qualifiedName]
	if !ok {
		return NodeSpec{}, fmt.Errorf("%w: %s", ErrUnknownNode, qualifiedName)
	}
	return spec, nil
}

// All returns all registered specs sorted by qualified name.
// Used by tools.json generation and the planner's available-ops summary.
func (r *Registry) All() []NodeSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeSpec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	return out
}

// Exposable returns the subset of registered specs marked
// Exposable=true. Used by the steering layer (decide.next) to build a
// catalog the LLM sees as composable building blocks — filters out
// stubs, dispatcher-only metadata ops, and internal helpers.
func (r *Registry) Exposable() []NodeSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeSpec, 0, len(r.specs))
	for _, s := range r.specs {
		if s.Exposable {
			out = append(out, s)
		}
	}
	return out
}

// Count returns the number of registered nodes.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.specs)
}

// ErrUnknownNode is returned by Get when the qualified name isn't
// registered. Callers can errors.Is(err, ErrUnknownNode) to detect.
var ErrUnknownNode = fmt.Errorf("unknown node")
