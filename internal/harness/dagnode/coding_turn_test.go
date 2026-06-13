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

// TestIsSynthMode validates the synth-mode recognizer that gates both
// the synthesizer prompt directive AND the SynthDefaultModel routing
// fallback. The two branches must agree on what counts as synth-mode
// or the model picked for synth turns drifts from the directive
// applied to them.
func TestIsSynthMode(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{"true bool", map[string]any{"synthesize": true}, true},
		{"false bool", map[string]any{"synthesize": false}, false},
		{"numeric 1.0 (LLM JSON round-trip)", map[string]any{"synthesize": 1.0}, true},
		{"numeric 0.0", map[string]any{"synthesize": 0.0}, false},
		{"missing", map[string]any{}, false},
		{"string is not coerced", map[string]any{"synthesize": "true"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSynthMode(c.in); got != c.want {
				t.Errorf("isSynthMode(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestSynthDefaultModelPrecedence locks the synth-mode precedence
// rule: in synthesize mode, SynthDefaultModel WINS over an upstream
// attrs.model when it returns non-empty. Non-synth mode preserves the
// legacy precedence (in[model] wins). The closure is only consulted
// in synth mode.
//
// Rationale: gpt-5.4 (decide.next's planner) often picks the coder
// for audit synth turns, which produced hallucinated answers on
// review-class Q3 baseline runs. The REPL knows the classified intent
// and can route deterministically to a reasoner — that's the lift
// Path B is meant to capture. Letting in[model] win would surrender
// that decision back to the LLM.
func TestSynthDefaultModelPrecedence(t *testing.T) {
	called := 0
	cfg := CodingTurnConfig{
		Model:   "", // stub-mode default
		Workdir: t.TempDir(),
		SynthDefaultModel: func() string {
			called++
			return "" // empty -> defer to in[model]/cfg.Model
		},
	}
	h := NewCodingTurnHandler(cfg)

	// Case 1: synth + empty in[model] + SynthDefaultModel returns "" =>
	// stub-mode. Closure consulted exactly once.
	res, err := h(context.Background(), map[string]any{
		"prompt":     "x",
		"synthesize": true,
	}, dag.Budget{})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if stub, _ := res.Out["stub"].(bool); !stub {
		t.Errorf("expected stub-mode; got %+v", res.Out)
	}
	if called != 1 {
		t.Errorf("closure called %d times in synth mode, want 1", called)
	}

	// Case 2: non-synth + empty in[model] => closure NOT consulted.
	called = 0
	res2, _ := h(context.Background(), map[string]any{
		"prompt":     "x",
		"synthesize": false,
	}, dag.Budget{})
	if stub, _ := res2.Out["stub"].(bool); !stub {
		t.Errorf("expected stub mode; got %+v", res2.Out)
	}
	if called != 0 {
		t.Errorf("closure should NOT fire on non-synth; called=%d", called)
	}

	// Case 3: synth + in[model] set + SynthDefaultModel returns "" =>
	// fall through to in[model]. Closure consulted, returns empty,
	// in[model] takes over.
	called = 0
	_, _ = h(context.Background(), map[string]any{
		"prompt":     "x",
		"synthesize": true,
		"model":      "explicit/winner",
	}, dag.Budget{})
	if called != 1 {
		t.Errorf("closure should be consulted in synth mode; called=%d", called)
	}

	// Case 4 (Path B inversion): synth + in[model] AND
	// SynthDefaultModel returns non-empty => closure's pick WINS.
	// We verify by confirming the closure was still consulted; the
	// observable model resolution beyond that requires a harness mock.
	called = 0
	cfg2 := CodingTurnConfig{
		Model:   "",
		Workdir: t.TempDir(),
		SynthDefaultModel: func() string {
			called++
			return "winner/from-closure"
		},
	}
	h2 := NewCodingTurnHandler(cfg2)
	_, _ = h2(context.Background(), map[string]any{
		"prompt":     "x",
		"synthesize": true,
		"model":      "loser/from-in-model",
	}, dag.Budget{})
	if called != 1 {
		t.Errorf("closure should be consulted; called=%d", called)
	}
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
	if n != 7 {
		t.Errorf("expected 7 registrations (5 act.* + attend.compress/compact), got %d", n)
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
	for _, attendOp := range []string{"attend.compress", "attend.compact"} {
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

// TestFinalizeSynthFinal pins the no-empty-terminal invariant: in synth
// mode, a synthesizer whose only output is a NEED_MORE: line must never
// yield an empty Final — otherwise a hop that can't schedule (budget
// refused / hop cap) surfaces an empty answer and INVALIDates the eval
// cell (the q2-cross-file-cortex failure).
func TestFinalizeSynthFinal(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantFinal   string
		wantRaw     string
		wantNonEmpt bool
	}{
		{
			name:      "no marker passes through, no raw",
			raw:       "Here is the answer, with a citation.",
			wantFinal: "Here is the answer, with a citation.",
			wantRaw:   "",
		},
		{
			name:      "partial answer + marker keeps the partial",
			raw:       "Found the producer at line 42.\nNEED_MORE: read consumer.go",
			wantFinal: "Found the producer at line 42.",
			wantRaw:   "Found the producer at line 42.\nNEED_MORE: read consumer.go",
		},
		{
			name:        "marker-only falls back to non-empty with the action",
			raw:         "NEED_MORE: shell: grep -rn estimate_scope .",
			wantRaw:     "NEED_MORE: shell: grep -rn estimate_scope .",
			wantNonEmpt: true,
		},
		{
			name:        "marker-only with empty action still non-empty",
			raw:         "NEED_MORE:",
			wantRaw:     "NEED_MORE:",
			wantNonEmpt: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			final, raw := finalizeSynthFinal(tc.raw)
			if strings.TrimSpace(final) == "" {
				t.Errorf("final is empty; the no-empty-terminal invariant is violated for %q", tc.raw)
			}
			if tc.wantFinal != "" && final != tc.wantFinal {
				t.Errorf("final:\n  got=%q\n want=%q", final, tc.wantFinal)
			}
			if tc.wantNonEmpt {
				// Fallback must name the action when one was present.
				if action, ok := parseNeedMore(tc.raw); ok && !strings.Contains(final, action) {
					t.Errorf("fallback %q does not surface the needed action %q", final, action)
				}
			}
			if raw != tc.wantRaw {
				t.Errorf("rawBeforeStrip:\n  got=%q\n want=%q", raw, tc.wantRaw)
			}
		})
	}
}
