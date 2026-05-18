package dagnode

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Tests for the Stage 3 act-dispatch wiring in coding_turn.
//
// We exercise the dispatcher in isolation — building a fake
// harness.ToolRegistry with a stub tool, plus an act-op metadata
// registry, and observing the synthetic trace entries emitted. The
// full-loop integration is covered by the existing eval/v2 suite.

func newTestHarnessRegistry(name, returnOut string, returnErr error) (*harness.ToolRegistry, *stubTool) {
	reg := harness.NewToolRegistry()
	st := &stubTool{name: name, returnOut: returnOut, returnErr: returnErr}
	reg.Register(st)
	return reg, st
}

func TestNewActDispatcher_delegatesToHarnessRegistry(t *testing.T) {
	actReg := dag.NewRegistry()
	if err := RegisterActOpMetadata(actReg, "read_file",
		dag.AxisContract{Mutator: false}, dag.Cost{LatencyMS: 50}); err != nil {
		t.Fatalf("register: %v", err)
	}
	harnessReg, underlying := newTestHarnessRegistry("read_file", `{"content":"hello"}`, nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: actReg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", &spawned)

	out, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != `{"content":"hello"}` {
		t.Errorf("expected harness output forwarded, got %q", out)
	}
	if underlying.called != 1 {
		t.Errorf("expected 1 underlying call via harness registry, got %d", underlying.called)
	}
	if underlying.lastArgs != `{"path":"x"}` {
		t.Errorf("expected args forwarded, got %q", underlying.lastArgs)
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

func TestNewActDispatcher_missEmitsUnknownNodeTraceButStillDispatches(t *testing.T) {
	// Act registry has NO entry for the tool. Dispatcher should still
	// delegate to harness registry (agent keeps working) but emit an
	// unknown_node trace row so the operator sees the metadata gap.
	actReg := dag.NewRegistry()
	harnessReg, underlying := newTestHarnessRegistry("no_such_tool", `{"result":"ok"}`, nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: actReg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", &spawned)

	out, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "no_such_tool", Arguments: "{}"},
	})
	if err != nil {
		t.Fatalf("miss should still dispatch successfully: %v", err)
	}
	if out != `{"result":"ok"}` {
		t.Errorf("expected harness output even on metadata miss, got %q", out)
	}
	if underlying.called != 1 {
		t.Errorf("expected harness dispatch despite act-metadata miss, got %d calls", underlying.called)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(captured))
	}
	if captured[0].ErrorCode != "unknown_node" {
		t.Errorf("expected unknown_node on miss, got %s", captured[0].ErrorCode)
	}
}

func TestNewActDispatcher_normalizesToolName(t *testing.T) {
	actReg := dag.NewRegistry()
	_ = RegisterActOpMetadata(actReg, "read_file", dag.AxisContract{}, dag.Cost{LatencyMS: 50})
	harnessReg, underlying := newTestHarnessRegistry("read_file", "ok", nil)

	var spawned []string
	cfg := CodingTurnConfig{ActRegistry: actReg}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	_, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "read_file<|channel|>commentary", Arguments: "{}"},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if underlying.called != 1 {
		t.Errorf("expected 1 call after name normalization; got %d", underlying.called)
	}
}

func TestNewActDispatcher_multipleCallsAccumulateChildren(t *testing.T) {
	actReg := dag.NewRegistry()
	_ = RegisterActOpMetadata(actReg, "read_file", dag.AxisContract{}, dag.Cost{LatencyMS: 50})
	harnessReg, _ := newTestHarnessRegistry("read_file", "ok", nil)

	var spawned []string
	var captured []dag.TraceEntry
	cfg := CodingTurnConfig{
		ActRegistry: actReg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	for i := 0; i < 3; i++ {
		_, _ = dispatch(context.Background(), harnessReg, llm.ToolCall{
			Function: llm.ToolCallFunction{Name: "read_file", Arguments: "{}"},
		})
	}
	if len(spawned) != 3 {
		t.Errorf("expected 3 children, got %d", len(spawned))
	}
	if len(captured) != 3 {
		t.Errorf("expected 3 trace entries, got %d", len(captured))
	}
	seen := map[string]bool{}
	for _, e := range captured {
		if seen[e.NodeID] {
			t.Errorf("duplicate child ID: %s", e.NodeID)
		}
		seen[e.NodeID] = true
	}
}

func TestNewActDispatcher_emitsCostHintForCalibration(t *testing.T) {
	// The dispatcher surfaces the registered cost hint in Out so the
	// operator can compare against observed latency. This is the
	// calibration feedback channel for follow-up #2 of the Stage 3
	// eval-journal entry.
	actReg := dag.NewRegistry()
	_ = RegisterActOpMetadata(actReg, "read_file", dag.AxisContract{}, dag.Cost{LatencyMS: 75, Tokens: 0})
	harnessReg, _ := newTestHarnessRegistry("read_file", "ok", nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: actReg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
	}
	dispatch := NewActDispatcher(cfg, "p", &spawned)
	_, _ = dispatch(context.Background(), harnessReg, llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: "{}"},
	})
	if len(captured) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(captured))
	}
	if v, _ := captured[0].Out["cost_hint_ms"].(int); v != 75 {
		t.Errorf("expected cost_hint_ms=75 surfaced for calibration, got %v", captured[0].Out["cost_hint_ms"])
	}
}

// REGRESSION: principles 5 + 7 (Reproducible + Structured).
//
// Earlier-this-session bug: the --dag dispatcher built a parallel
// ToolRegistry and routed tool calls through it, so the harness's
// own registry never saw the write_file / run_shell calls. This
// silently zeroed HarnessResult.FilesChanged + ShellNonZeroExits
// when --dag was on. Same prompt → different reported field values
// depending on flag state. Violates principle 5 (Reproducible) and
// principle 7 (Structured) — CellResults carried corrupted fields.
//
// Fix verification: dispatcher MUST delegate to the harness's
// registry. write_file calls increment FilesWritten on the
// passed-in registry; run_shell calls increment ShellNonZeroExits.
func TestNewActDispatcher_preservesHarnessAccountingForFilesWritten(t *testing.T) {
	actReg := dag.NewRegistry()
	_ = RegisterActOpMetadata(actReg, "write_file",
		dag.AxisContract{Mutator: true}, dag.Cost{LatencyMS: 50})

	tempDir := t.TempDir()
	harnessReg := harness.NewToolRegistry()
	harnessReg.Register(harness.NewWriteFileTool(tempDir, harnessReg))

	var spawned []string
	cfg := CodingTurnConfig{ActRegistry: actReg}
	dispatch := NewActDispatcher(cfg, "p", &spawned)

	_, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      "write_file",
			Arguments: `{"path":"hello.txt","content":"hi"}`,
		},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	written := harnessReg.FilesWritten()
	if len(written) != 1 || written[0] != "hello.txt" {
		t.Errorf("dispatcher must preserve harness FilesWritten accounting; got %v", written)
	}
}

func TestNormalizeToolName(t *testing.T) {
	cases := map[string]string{
		"read_file":                      "read_file",
		"read_file<|channel|>commentary": "read_file",
		"  read_file  ":                  "  read_file",
		"foo<|x":                         "foo",
	}
	for in, want := range cases {
		got := normalizeToolName(in)
		if got != want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegisterDefaultActOpMetadata_registersAllFive(t *testing.T) {
	reg := dag.NewRegistry()
	n, err := RegisterDefaultActOpMetadata(reg)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 registrations (incl cortex_search), got %d", n)
	}
	for _, name := range []string{"act.read_file", "act.list_dir", "act.write_file", "act.run_shell", "act.cortex_search"} {
		spec, err := reg.Get(name)
		if err != nil {
			t.Errorf("expected %s registered: %v", name, err)
			continue
		}
		if spec.AxisContract == nil {
			t.Errorf("%s missing AxisContract", name)
		}
	}
}

// AdaptToolAsAct is still exported for the standalone-NodeSpec path
// (where an act op is invoked via the DAG executor with its own
// Handler rather than via the dispatcher's delegation). Smoke-test
// kept; the heavy tests live in act_ops_test.go.
func TestAdaptToolAsAct_stillWorksStandalone(t *testing.T) {
	stub := &stubTool{name: "read_file", returnOut: "ok"}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler: stub,
		Cost:    dag.Cost{LatencyMS: 50},
	})
	if !strings.HasPrefix(spec.Op, "read_file") {
		t.Errorf("expected op read_file, got %s", spec.Op)
	}
}
