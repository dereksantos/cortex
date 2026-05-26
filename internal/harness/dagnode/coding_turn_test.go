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
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "", &spawned)

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
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "", &spawned)

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
	dispatch := NewActDispatcher(cfg, "p", "", &spawned)

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
	dispatch := NewActDispatcher(cfg, "p", "", &spawned)

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
	dispatch := NewActDispatcher(cfg, "p", "", &spawned)
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
	dispatch := NewActDispatcher(cfg, "p", "", &spawned)

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

// TestNewActDispatcher_SalienceChunksOversizedReadOutput pins the
// Phase-B behavior: when act.read_file output exceeds
// ToolOutputSalienceCap, the dispatcher splits it deterministically by
// line boundary, joins with "[chunk i/N, lines a-b]" headers, and
// emits a synthetic attend.chunk trace entry — NOT attend.compress.
// The calling model sees the raw bytes with location headers and can
// re-fetch specific ranges if needed.
func TestNewActDispatcher_SalienceChunksOversizedReadOutput(t *testing.T) {
	actReg := dag.NewRegistry()
	if _, err := RegisterDefaultActOpMetadata(actReg); err != nil {
		t.Fatalf("register default metadata: %v", err)
	}
	// 100 lines of 40-char content = ~1K tokens total. Cap=40 forces
	// ~25 chunks; the dispatcher truncates beyond MaxEmittedChunks (8).
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("abcd", 10) + "\n"
	}
	bigOut := strings.Join(lines, "")
	harnessReg, _ := newTestHarnessRegistry("read_file", bigOut, nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry:           actReg,
		TraceCB:               func(e dag.TraceEntry) { captured = append(captured, e) },
		ToolOutputSalienceCap: 40,
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "find TODOs", &spawned)

	out, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "[chunk 1/") {
		t.Errorf("chunked output should carry location header; got %q", out[:min(200, len(out))])
	}
	if !strings.Contains(out, "[truncated") {
		t.Errorf("expected truncation marker when chunks > MaxEmittedChunks; got %q", out[:min(400, len(out))])
	}

	if len(captured) != 2 {
		t.Fatalf("expected 2 trace entries (act.read_file + attend.chunk), got %d", len(captured))
	}
	act, chunk := captured[0], captured[1]
	if act.QualifiedName != "act.read_file" {
		t.Fatalf("first row should be act.read_file, got %s", act.QualifiedName)
	}
	if act.Salience == nil || act.Salience.MaxOutputTokens != 40 || act.Salience.Intent != "find TODOs" {
		t.Errorf("act row missing/wrong SalienceContract: %+v", act.Salience)
	}
	if chunk.QualifiedName != "attend.chunk" || chunk.ParentID != act.NodeID {
		t.Errorf("chunk row shape wrong: qname=%s parent=%s want attend.chunk parent=%s",
			chunk.QualifiedName, chunk.ParentID, act.NodeID)
	}
	if !chunk.OK {
		t.Errorf("chunk should be OK, got: %s", chunk.ErrorMessage)
	}
	if chunk.Salience == nil || !chunk.Salience.ChunkOnOversize {
		t.Errorf("chunk row should carry ChunkOnOversize=true salience contract")
	}
	if len(act.SpawnedChildren) == 0 || act.SpawnedChildren[0] != chunk.NodeID {
		t.Errorf("act.SpawnedChildren should include chunk %s, got %v",
			chunk.NodeID, act.SpawnedChildren)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestNewActDispatcher_TokenBudgetEmitsMoreChunks pins Piece 1 of the
// chunker fix: with cfg.ToolOutputEmittedTokens > 0, the dispatcher
// uses a token-budget cap instead of the legacy 8-chunk cap. A budget
// generous enough to cover the whole file should emit ALL chunks and
// drop the "[truncated …]" marker — closing the loop pathology where
// the model re-reads the same 22% slice every turn.
func TestNewActDispatcher_TokenBudgetEmitsMoreChunks(t *testing.T) {
	actReg := dag.NewRegistry()
	if _, err := RegisterDefaultActOpMetadata(actReg); err != nil {
		t.Fatalf("register default metadata: %v", err)
	}
	// 100 lines × 40 chars ≈ 1K tokens. Per-chunk cap 40 forces ~25
	// chunks. With ToolOutputEmittedTokens=4000 (review/recall budget),
	// every chunk should emit.
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("abcd", 10) + "\n"
	}
	bigOut := strings.Join(lines, "")
	harnessReg, _ := newTestHarnessRegistry("read_file", bigOut, nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry:             actReg,
		TraceCB:                 func(e dag.TraceEntry) { captured = append(captured, e) },
		ToolOutputSalienceCap:   40,
		ToolOutputEmittedTokens: 4000,
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "explain this file", &spawned)

	out, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(out, "[truncated") {
		t.Errorf("token budget 4000 ≫ content (~1K tokens) — no truncation marker expected; got %q",
			out[max(0, len(out)-200):])
	}
	if len(captured) != 2 {
		t.Fatalf("expected 2 trace entries (act.read_file + attend.chunk), got %d", len(captured))
	}
	chunk := captured[1]
	if chunk.Salience == nil || chunk.Salience.MaxEmittedTokens != 4000 {
		t.Errorf("chunk trace should carry MaxEmittedTokens=4000; got %+v", chunk.Salience)
	}
	emitted, _ := chunk.Out["emitted"].(int)
	total, _ := chunk.Out["chunks"].(int)
	if emitted != total {
		t.Errorf("emitted=%d != total=%d — budget should have covered everything", emitted, total)
	}
	if emitted <= dag.MaxEmittedChunks {
		t.Errorf("emitted=%d should exceed legacy MaxEmittedChunks=%d to prove the token-budget path fired", emitted, dag.MaxEmittedChunks)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestNewActDispatcher_SalienceSkippedUnderCap pins the under-cap
// passthrough: tool outputs below ToolOutputSalienceCap stay unchanged
// and no synthetic compress trace fires. Guards against the dispatcher
// burning cycles on already-tiny outputs.
func TestNewActDispatcher_SalienceSkippedUnderCap(t *testing.T) {
	actReg := dag.NewRegistry()
	if _, err := RegisterDefaultActOpMetadata(actReg); err != nil {
		t.Fatalf("register: %v", err)
	}
	harnessReg, _ := newTestHarnessRegistry("read_file", "hello", nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry:           actReg,
		TraceCB:               func(e dag.TraceEntry) { captured = append(captured, e) },
		ToolOutputSalienceCap: 100,
	}
	dispatch := NewActDispatcher(cfg, "p", "any intent", &spawned)

	out, err := dispatch(context.Background(), harnessReg, llm.ToolCall{
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out != "hello" {
		t.Errorf("under-cap output should pass through unchanged, got %q", out)
	}
	if len(captured) != 1 {
		t.Errorf("expected 1 trace entry (no compress), got %d", len(captured))
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
	if n != 8 {
		t.Errorf("expected 8 registrations (5 act.* + attend.compress/accumulate/compact), got %d", n)
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
	for _, attendOp := range []string{"attend.compress", "attend.accumulate", "attend.compact"} {
		if _, err := reg.Get(attendOp); err != nil {
			t.Errorf("%s should be registered alongside act.* metadata: %v", attendOp, err)
		}
	}
	if _, err := reg.Get("attend.compress"); err != nil {
		t.Errorf("attend.compress should be registered alongside act.* metadata: %v", err)
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

// scriptedLLM is a minimal llm.Provider that returns a fixed string —
// used to drive attend.accumulate without a real network call.
type scriptedLLM struct {
	out string
}

func (s *scriptedLLM) Name() string      { return "scripted" }
func (s *scriptedLLM) IsAvailable() bool { return true }
func (s *scriptedLLM) Generate(_ context.Context, _ string) (string, error) {
	return s.out, nil
}
func (s *scriptedLLM) GenerateWithSystem(_ context.Context, _, _ string) (string, error) {
	return s.out, nil
}
func (s *scriptedLLM) GenerateWithStats(_ context.Context, _ string) (string, llm.GenerationStats, error) {
	return s.out, llm.GenerationStats{InputTokens: 20, OutputTokens: 8}, nil
}

// TestNewActDispatcher_AccumulatorFoldsToolOutput pins the
// bounded-context wiring: when AccumulatorProvider + MaxTokens are
// set on CodingTurnConfig, each tool output gets folded through
// attend.accumulate and the resulting snapshot is visible via
// dag.LatestAccumulatorSnapshot for any later node in the turn.
func TestNewActDispatcher_AccumulatorFoldsToolOutput(t *testing.T) {
	actReg := dag.NewRegistry()
	if _, err := RegisterDefaultActOpMetadataWithCompressor(actReg, &scriptedLLM{out: "compressed snippet"}); err != nil {
		t.Fatalf("register default metadata: %v", err)
	}
	harnessReg, _ := newTestHarnessRegistry("read_file", "tool result body", nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry:          actReg,
		TraceCB:              func(e dag.TraceEntry) { captured = append(captured, e) },
		AccumulatorProvider:  &scriptedLLM{out: "fact A; fact B; tool result body"},
		AccumulatorMaxTokens: 200,
		AccumulatorIntent:    "code",
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "find the bug", &spawned)

	// Attach turn state so the accumulator deposit lands somewhere
	// readable. Without this WithTestTurnState the deposit silently
	// no-ops — pinning that path too via the next test.
	ctx := dag.WithTestTurnState(context.Background(), nil)

	if _, err := dispatch(ctx, harnessReg, llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if snap := dag.LatestAccumulatorSnapshot(ctx); snap == "" {
		t.Errorf("expected accumulator snapshot deposited; got empty")
	}

	// Trace order: act.read_file followed by attend.accumulate.
	if len(captured) < 2 {
		t.Fatalf("expected at least 2 trace entries (act + accumulate), got %d", len(captured))
	}
	act := captured[0]
	accumulate := captured[len(captured)-1]
	if act.QualifiedName != "act.read_file" {
		t.Errorf("first row should be act.read_file, got %s", act.QualifiedName)
	}
	if accumulate.QualifiedName != "attend.accumulate" {
		t.Errorf("last row should be attend.accumulate, got %s", accumulate.QualifiedName)
	}
	if accumulate.ParentID != act.NodeID {
		t.Errorf("accumulate.ParentID should chain to act row %q, got %q", act.NodeID, accumulate.ParentID)
	}
	// act.SpawnedChildren must surface the accumulator deposit ID.
	found := false
	for _, c := range act.SpawnedChildren {
		if c == accumulate.NodeID {
			found = true
		}
	}
	if !found {
		t.Errorf("act.SpawnedChildren should include accumulate %s, got %v", accumulate.NodeID, act.SpawnedChildren)
	}
}

// TestNewActDispatcher_AccumulatorSkippedWhenUnconfigured pins the
// pre-bounded-context behavior: with no AccumulatorProvider set, the
// dispatcher runs unchanged — no extra trace rows, no deposit.
func TestNewActDispatcher_AccumulatorSkippedWhenUnconfigured(t *testing.T) {
	actReg := dag.NewRegistry()
	if _, err := RegisterDefaultActOpMetadata(actReg); err != nil {
		t.Fatalf("register: %v", err)
	}
	harnessReg, _ := newTestHarnessRegistry("read_file", "ok", nil)

	var captured []dag.TraceEntry
	var spawned []string
	cfg := CodingTurnConfig{
		ActRegistry: actReg,
		TraceCB:     func(e dag.TraceEntry) { captured = append(captured, e) },
		// AccumulatorProvider intentionally left nil.
	}
	dispatch := NewActDispatcher(cfg, "coding_turn_id", "intent", &spawned)
	ctx := dag.WithTestTurnState(context.Background(), nil)

	if _, err := dispatch(ctx, harnessReg, llm.ToolCall{
		ID:       "call_1",
		Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"x"}`},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if snap := dag.LatestAccumulatorSnapshot(ctx); snap != "" {
		t.Errorf("expected no accumulator snapshot when unconfigured; got %q", snap)
	}
	for _, e := range captured {
		if e.QualifiedName == "attend.accumulate" {
			t.Errorf("no attend.accumulate trace should fire when unconfigured; got %+v", e)
		}
	}
}

func TestStripNeedMoreLine(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantStr   string
		wantFound bool
	}{
		{
			name:      "no marker leaves input untouched",
			in:        "Final answer with no marker.\nAll content preserved.",
			wantStr:   "Final answer with no marker.\nAll content preserved.",
			wantFound: false,
		},
		{
			name:      "marker on its own line is removed cleanly",
			in:        "Partial answer.\n\nNEED_MORE: read foo.go\n",
			wantStr:   "Partial answer.",
			wantFound: true,
		},
		{
			name:      "marker as only content yields empty result",
			in:        "NEED_MORE: read foo.go",
			wantStr:   "",
			wantFound: true,
		},
		{
			name:      "marker without trailing newline strips through EOF",
			in:        "Content here.\nNEED_MORE: read foo.go",
			wantStr:   "Content here.",
			wantFound: true,
		},
		{
			name:      "marker with surrounding whitespace",
			in:        "  body\n   \n  NEED_MORE: read x  \nignored after",
			wantStr:   "  body",
			wantFound: true,
		},
		{
			name:      "marker mid-content keeps prefix only",
			in:        "Step 1: did X.\nStep 2: did Y.\nNEED_MORE: read Z.go\nthis trailing content is also dropped",
			wantStr:   "Step 1: did X.\nStep 2: did Y.",
			wantFound: true,
		},
		{
			name:      "empty input returns empty",
			in:        "",
			wantStr:   "",
			wantFound: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found := stripNeedMoreLine(tc.in)
			if got != tc.wantStr {
				t.Errorf("stripped:\n  got=%q\n want=%q", got, tc.wantStr)
			}
			if found != tc.wantFound {
				t.Errorf("found: got=%v want=%v", found, tc.wantFound)
			}
		})
	}
}
