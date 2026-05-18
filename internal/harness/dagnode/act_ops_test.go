package dagnode

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/llm"
)

// stubTool is a minimal ToolHandler for unit tests.
type stubTool struct {
	name      string
	desc      string
	called    int
	lastArgs  string
	returnOut string
	returnErr error
}

func (s *stubTool) Name() string { return s.name }
func (s *stubTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        s.name,
			Description: s.desc,
		},
	}
}
func (s *stubTool) Call(_ context.Context, args string) (string, error) {
	s.called++
	s.lastArgs = args
	return s.returnOut, s.returnErr
}

func TestAdaptToolAsAct_readOpDispatches(t *testing.T) {
	stub := &stubTool{name: "read_file", desc: "Read a file", returnOut: `{"content":"hello"}`}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: false, RequiresConfirmation: false},
		Cost:     dag.Cost{LatencyMS: 50, Tokens: 0},
	})
	res, err := spec.Handler(context.Background(), map[string]any{"args": `{"path":"x"}`}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if stub.called != 1 {
		t.Errorf("expected 1 call, got %d", stub.called)
	}
	if stub.lastArgs != `{"path":"x"}` {
		t.Errorf("expected args forwarded verbatim, got %q", stub.lastArgs)
	}
	if out, _ := res.Out["output"].(string); out != stub.returnOut {
		t.Errorf("expected output forwarded, got %q", out)
	}
}

func TestAdaptToolAsAct_axis5BlocksDestructiveWithoutConfirm(t *testing.T) {
	stub := &stubTool{name: "run_shell", desc: "Run a shell command"}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: true, RequiresConfirmation: true},
		Cost:     dag.Cost{LatencyMS: 100, Tokens: 0},
	})
	_, err := spec.Handler(context.Background(), map[string]any{"args": `{"cmd":"ls"}`}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected axis-5 error without confirm=true")
	}
	if !strings.Contains(err.Error(), "axis-5") {
		t.Errorf("error should mention axis-5; got: %v", err)
	}
	if stub.called != 0 {
		t.Errorf("tool should NOT have been called; got %d calls", stub.called)
	}
}

func TestAdaptToolAsAct_axis5PassesWithConfirm(t *testing.T) {
	stub := &stubTool{name: "run_shell", desc: "Run a shell command", returnOut: "ok"}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: true, RequiresConfirmation: true},
		Cost:     dag.Cost{LatencyMS: 100, Tokens: 0},
	})
	res, err := spec.Handler(context.Background(), map[string]any{
		"args":    `{"cmd":"ls"}`,
		"confirm": true,
	}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("expected confirm=true to allow dispatch: %v", err)
	}
	if stub.called != 1 {
		t.Errorf("expected 1 call after confirm; got %d", stub.called)
	}
	if out, _ := res.Out["output"].(string); out != "ok" {
		t.Errorf("expected output forwarded, got %q", out)
	}
}

func TestAdaptToolAsAct_emptyArgsDefaultsToEmptyObject(t *testing.T) {
	// The harness convention: missing args == `{}` so the tool can
	// still parse JSON. The wrapper preserves this.
	stub := &stubTool{name: "list_dir", returnOut: "[]"}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{Mutator: false, RequiresConfirmation: false},
		Cost:     dag.Cost{LatencyMS: 50, Tokens: 0},
	})
	_, err := spec.Handler(context.Background(), map[string]any{}, dag.DefaultTurnBudget())
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if stub.lastArgs != "{}" {
		t.Errorf("expected empty args to default to '{}'; got %q", stub.lastArgs)
	}
}

func TestAdaptToolAsAct_toolErrorPropagates(t *testing.T) {
	stub := &stubTool{name: "read_file", returnOut: `{"error":"not found"}`, returnErr: errToolFail("not found")}
	spec := AdaptToolAsAct(ActOpConfig{
		Handler:  stub,
		Contract: dag.AxisContract{},
		Cost:     dag.Cost{LatencyMS: 50, Tokens: 0},
	})
	res, err := spec.Handler(context.Background(), map[string]any{"args": "{}"}, dag.DefaultTurnBudget())
	if err == nil {
		t.Fatal("expected tool error to propagate")
	}
	// Output still forwarded so the agent loop can see the structured error.
	if out, _ := res.Out["output"].(string); out == "" {
		t.Errorf("expected output forwarded even on error; got empty")
	}
	if te, _ := res.Out["tool_error"].(string); te == "" {
		t.Errorf("expected tool_error populated")
	}
}

type errToolFail string

func (e errToolFail) Error() string { return string(e) }

func TestRegisterActOps_registersAll(t *testing.T) {
	reg := dag.NewRegistry()
	stubs := []ActOpConfig{
		{Handler: &stubTool{name: "read_file"}, Cost: dag.Cost{LatencyMS: 50}},
		{Handler: &stubTool{name: "write_file"}, Contract: dag.AxisContract{Mutator: true}, Cost: dag.Cost{LatencyMS: 50}},
		{Handler: &stubTool{name: "run_shell"}, Contract: dag.AxisContract{Mutator: true, RequiresConfirmation: true}, Cost: dag.Cost{LatencyMS: 30000}},
	}
	n, err := RegisterActOps(reg, stubs)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 registered, got %d", n)
	}
	for _, name := range []string{"act.read_file", "act.write_file", "act.run_shell"} {
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

func TestDefaultActOpContracts_runShellRequiresConfirm(t *testing.T) {
	contracts := DefaultActOpContracts()
	if c, ok := contracts["run_shell"]; !ok || !c.RequiresConfirmation {
		t.Error("run_shell should require confirmation by default (axis-5)")
	}
	if c, ok := contracts["read_file"]; !ok || c.Mutator {
		t.Error("read_file should not be mutator")
	}
	if c, ok := contracts["write_file"]; !ok || !c.Mutator {
		t.Error("write_file should be mutator")
	}
}
