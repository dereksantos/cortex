// Package dag — NodeSpec.Requires registration + persistence shape.
//
// Pinned here because per-node routing (docs/per-node-routing-plan.md)
// relies on two invariants: (1) Requires set at registration time
// survives Register/Get, and (2) Requires is NOT in the persistable
// projection — replay reconstitutes it from the registry by qualified
// name, alongside Handler / Cost / Inputs / Outputs.

package dag

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func noopHandler(_ context.Context, _ map[string]any, _ Budget) (NodeResult, error) {
	return NodeResult{}, nil
}

func TestRegistryPreservesRequires(t *testing.T) {
	reg := NewRegistry()
	spec := NodeSpec{
		Function: FuncDecide,
		Op:       "tool_call",
		Handler:  noopHandler,
		Requires: []string{"tool-calling:specialist", "tool-calling"},
	}
	if err := reg.Register(spec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := reg.Get(spec.QualifiedName())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []string{"tool-calling:specialist", "tool-calling"}
	if !reflect.DeepEqual(got.Requires, want) {
		t.Errorf("Requires: got %v want %v", got.Requires, want)
	}
}

// TestRequiresNotInPersistProjection pins that Requires lives at
// registration time and does NOT round-trip through MarshalJSON. The
// DeferredSpawn queue holds only identity-shaped fields; the executor
// looks up Requires (and Cost / Inputs / Handler) from the registry
// when replaying a spawn.
//
// If this invariant breaks, replays would carry stale Requires snapshots
// from when the spawn was queued — defeating the point of declaring
// capability needs at the node-type level.
func TestRequiresNotInPersistProjection(t *testing.T) {
	spec := NodeSpec{
		Function: FuncDecide,
		Op:       "tool_call",
		ID:       "node-1",
		Parent:   "seed",
		Attrs:    map[string]any{"model": "explicit-override"},
		Requires: []string{"tool-calling:specialist"},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Persisted JSON must not mention Requires.
	if got := string(data); contains(got, "requires") || contains(got, "Requires") {
		t.Errorf("persist projection should omit Requires; got %s", got)
	}
	// Round-trip leaves Requires zero — caller (executor) reconstitutes
	// it via registry.Get(QualifiedName).
	var back NodeSpec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Requires != nil {
		t.Errorf("Unmarshal should leave Requires nil; got %v", back.Requires)
	}
	// Identity-shaped fields DO round-trip.
	if back.Function != FuncDecide || back.Op != "tool_call" || back.ID != "node-1" || back.Parent != "seed" {
		t.Errorf("identity fields lost: %+v", back)
	}
	if v, _ := back.Attrs["model"].(string); v != "explicit-override" {
		t.Errorf("Attrs lost: %+v", back.Attrs)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
