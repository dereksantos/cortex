// Package dag — executor + router integration.
//
// Pins that when a Router is wired on the Executor:
//   - The router runs per spawn (before the handler).
//   - Budget.Provider seen by the handler matches the router's return.
//   - TraceEntry.PickedModel + PickedReason carry the router's labels.
//   - No router (legacy default) leaves Provider nil and trace fields empty.

package dag

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeRouter returns the same canned (provider, id, reason) for every
// spec and records the spec.QualifiedName values it sees.
type fakeRouter struct {
	provider llm.Provider
	modelID  string
	reason   string
	saw      []string
}

func (r *fakeRouter) Resolve(_ context.Context, spec NodeSpec) (llm.Provider, string, string) {
	r.saw = append(r.saw, spec.QualifiedName())
	return r.provider, r.modelID, r.reason
}

// observedProviderHandler captures the Budget.Provider it saw, so
// tests can assert the executor populated it before calling the
// handler.
func observedProviderHandler(observed *llm.Provider) Handler {
	return func(_ context.Context, _ map[string]any, b Budget) (NodeResult, error) {
		*observed = b.Provider
		return NodeResult{Out: map[string]any{}, CostConsumed: Cost{LatencyMS: 1, Tokens: 1}}, nil
	}
}

func TestExecutor_RouterPopulatesBudgetAndTrace(t *testing.T) {
	provider := llm.NewMockProvider(0)
	router := &fakeRouter{provider: provider, modelID: "xlam-1.5b", reason: "requires:xlam-1.5b"}

	var observed llm.Provider
	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide,
		Op:       "tool_call",
		Cost:     Cost{LatencyMS: 1, Tokens: 1},
		Handler:  observedProviderHandler(&observed),
		Requires: []string{llm.CapToolCallingSpecialist, llm.CapToolCalling},
	})

	ex := NewExecutor(reg, nil)
	ex.SetRouter(router)
	seed := []NodeSpec{{Function: FuncDecide, Op: "tool_call", ID: "n1"}}
	trace, err := ex.Run(context.Background(), "test-router", seed, Budget{LatencyMS: 100, Tokens: 50, Depth: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if observed != provider {
		t.Errorf("handler should see Budget.Provider from router, got %v", observed)
	}
	if len(router.saw) != 1 || router.saw[0] != "decide.tool_call" {
		t.Errorf("router should be called once for decide.tool_call, saw %v", router.saw)
	}
	if len(trace.Entries) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(trace.Entries))
	}
	if got := trace.Entries[0].PickedModel; got != "xlam-1.5b" {
		t.Errorf("PickedModel: got %q want xlam-1.5b", got)
	}
	if got := trace.Entries[0].PickedReason; got != "requires:xlam-1.5b" {
		t.Errorf("PickedReason: got %q want requires:xlam-1.5b", got)
	}
}

func TestExecutor_NoRouter_LeavesBudgetAndTraceClean(t *testing.T) {
	// Legacy path: no router wired. Handler must see nil Budget.Provider
	// (so it falls back to its own cfg.Provider), and trace fields stay
	// empty (no PickedModel/PickedReason noise on pre-routing handlers).
	var observed llm.Provider = llm.NewMockProvider(0) // pre-set, to detect zero
	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide,
		Op:       "tool_call",
		Cost:     Cost{LatencyMS: 1, Tokens: 1},
		Handler:  observedProviderHandler(&observed),
	})

	ex := NewExecutor(reg, nil) // no SetRouter
	seed := []NodeSpec{{Function: FuncDecide, Op: "tool_call", ID: "n1"}}
	trace, err := ex.Run(context.Background(), "test-no-router", seed, Budget{LatencyMS: 100, Tokens: 50, Depth: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if observed != nil {
		t.Errorf("no router → Budget.Provider should be nil, got %v", observed)
	}
	if len(trace.Entries) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(trace.Entries))
	}
	if got := trace.Entries[0].PickedModel; got != "" {
		t.Errorf("PickedModel: got %q want empty", got)
	}
	if got := trace.Entries[0].PickedReason; got != "" {
		t.Errorf("PickedReason: got %q want empty", got)
	}
}

func TestExecutor_RouterReturnsNilProvider_LeavesBudgetNilButTraceReason(t *testing.T) {
	// Router resolved to "no-match" (nothing wired in deps). The
	// executor still records the reason on the trace — operators can
	// see "no-match" as a signal to install a specialist — but doesn't
	// overwrite Budget.Provider with nil (which would be redundant).
	router := &fakeRouter{provider: nil, modelID: "", reason: "no-match"}

	var observed llm.Provider
	reg := NewRegistry()
	mustRegister(t, reg, NodeSpec{
		Function: FuncDecide,
		Op:       "tool_call",
		Cost:     Cost{LatencyMS: 1, Tokens: 1},
		Handler:  observedProviderHandler(&observed),
		Requires: []string{llm.CapToolCallingSpecialist},
	})

	ex := NewExecutor(reg, nil)
	ex.SetRouter(router)
	seed := []NodeSpec{{Function: FuncDecide, Op: "tool_call", ID: "n1"}}
	trace, err := ex.Run(context.Background(), "test-no-match", seed, Budget{LatencyMS: 100, Tokens: 50, Depth: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if observed != nil {
		t.Errorf("handler should see nil Provider on no-match, got %v", observed)
	}
	if got := trace.Entries[0].PickedReason; got != "no-match" {
		t.Errorf("PickedReason should record no-match for operator visibility, got %q", got)
	}
}
