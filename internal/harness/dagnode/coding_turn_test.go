package dagnode

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Tests for the Stage 3 act-dispatch wiring in coding_turn.
//
// We exercise the dispatcher in isolation — building a fake act
// registry + observing the synthetic trace entries emitted — rather
// than spinning up a full CortexHarness. The full-loop integration
// is covered by `cortex run --type=turn` smoke testing and the
// existing eval/v2 suite.

func TestNewActDispatcher_routesToRegistry(t *testing.T) {
	reg := dag.NewRegistry()
	stub := &stubTool{name: "read_file", returnOut: `{"content":"hello"}`}
	if err := reg.Register(AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: false},
		Cost:     dag.Cost{LatencyMS: 50},
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: reg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", &spawned)

	out, err := dispatch(context.Background(), llm.ToolCall{
		ID: "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != stub.returnOut {
		t.Errorf("expected output %q, got %q", stub.returnOut, out)
	}
	if stub.called != 1 {
		t.Errorf("expected 1 underlying call, got %d", stub.called)
	}
	if stub.lastArgs != `{"path":"x"}` {
		t.Errorf("expected args forwarded, got %q", stub.lastArgs)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(captured))
	}
	e := captured[0]
	if e.QualifiedName != "act.read_file" {
		t.Errorf("expected qname act.read_file, got %s", e.QualifiedName)
	}
	if e.ParentID != "coding_turn_id" {
		t.Errorf("expected parent=coding_turn_id, got %s", e.ParentID)
	}
	if !e.OK {
		t.Errorf("expected OK, got error: %s", e.ErrorMessage)
	}
	if len(spawned) != 1 || spawned[0] != e.NodeID {
		t.Errorf("expected spawnedChildren to contain %s, got %v", e.NodeID, spawned)
	}
}

func TestNewActDispatcher_missEmitsTraceAndReturnsError(t *testing.T) {
	// Registry has NO act ops. Dispatcher should emit a miss trace entry
	// and return a structured error message the LLM can read.
	reg := dag.NewRegistry()

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: reg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", &spawned)

	out, err := dispatch(context.Background(), llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "no_such_tool", Arguments: "{}"},
	})
	if err != nil {
		t.Fatalf("dispatch should not error on miss (LLM gets the error in output): %v", err)
	}
	if !strings.Contains(out, "no act op registered") {
		t.Errorf("expected error output, got: %s", out)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 trace entry on miss, got %d", len(captured))
	}
	if captured[0].ErrorCode != "unknown_node" {
		t.Errorf("expected ErrorCode=unknown_node, got %s", captured[0].ErrorCode)
	}
	if !strings.HasPrefix(captured[0].QualifiedName, "act.") {
		t.Errorf("miss qname should be act.<requested>, got %s", captured[0].QualifiedName)
	}
}

func TestNewActDispatcher_normalizesToolName(t *testing.T) {
	// gpt-oss-20b style: tool name has chat-template artifacts.
	reg := dag.NewRegistry()
	stub := &stubTool{name: "read_file", returnOut: "ok"}
	if err := reg.Register(AdaptToolAsAct(ActOpConfig{
		Handler: stub,
		Cost:    dag.Cost{LatencyMS: 50},
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	var spawned []string
	cfg := CodingTurnConfig{ActRegistry: reg}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	out, err := dispatch(context.Background(), llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "read_file<|channel|>commentary", Arguments: "{}"},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != "ok" {
		t.Errorf("expected normalized lookup to hit read_file; got %q", out)
	}
	if stub.called != 1 {
		t.Errorf("expected 1 call after name normalization; got %d", stub.called)
	}
}

func TestNewActDispatcher_destructiveOpsAutoConfirmed(t *testing.T) {
	// run_shell has RequiresConfirmation=true. The dispatcher forces
	// confirm=true (user opted into destructive ops by running cortex
	// code). Verify the gate doesn't block.
	reg := dag.NewRegistry()
	stub := &stubTool{name: "run_shell", returnOut: "ok"}
	if err := reg.Register(AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: true, RequiresConfirmation: true},
		Cost:     dag.Cost{LatencyMS: 100},
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	var spawned []string
	cfg := CodingTurnConfig{ActRegistry: reg}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	out, err := dispatch(context.Background(), llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "run_shell", Arguments: `{"cmd":"ls"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != "ok" {
		t.Errorf("expected dispatcher to auto-confirm; got %q", out)
	}
	if stub.called != 1 {
		t.Errorf("destructive op should have dispatched once; got %d", stub.called)
	}
}

func TestNewActDispatcher_multipleCallsAccumulateChildren(t *testing.T) {
	reg := dag.NewRegistry()
	stub := &stubTool{name: "read_file", returnOut: "ok"}
	_ = reg.Register(AdaptToolAsAct(ActOpConfig{Handler: stub, Cost: dag.Cost{LatencyMS: 50}}))

	var spawned []string
	var captured []dag.TraceEntry
	cfg := CodingTurnConfig{
		ActRegistry: reg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	for i := 0; i < 3; i++ {
		_, _ = dispatch(context.Background(), llm.ToolCall{
			Function: llm.ToolCallFunction{Name: "read_file", Arguments: "{}"},
		})
	}
	if len(spawned) != 3 {
		t.Errorf("expected 3 children accumulated, got %d", len(spawned))
	}
	if len(captured) != 3 {
		t.Errorf("expected 3 trace entries, got %d", len(captured))
	}
	// Each entry should have a unique ID.
	seen := map[string]bool{}
	for _, e := range captured {
		if seen[e.NodeID] {
			t.Errorf("duplicate child ID: %s", e.NodeID)
		}
		seen[e.NodeID] = true
	}
}

func TestNormalizeToolName(t *testing.T) {
	cases := map[string]string{
		"read_file":                       "read_file",
		"read_file<|channel|>commentary":  "read_file",
		"  read_file  ":                   "  read_file",
		"foo<|x":                          "foo",
	}
	for in, want := range cases {
		got := normalizeToolName(in)
		if got != want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}
