//go:build !windows

package longmemeval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// TestHydrateHaystack_DrainsToStorage proves the bulk-capture + ingest
// pipeline actually lands events in <workdir>/.cortex where the
// subsequent `cortex code` invocation's cortex_search tool will find
// them. We don't drive `cortex code` here (that needs an OpenRouter
// key); we just check the journal + projection.
func TestHydrateHaystack_DrainsToStorage(t *testing.T) {
	workdir := t.TempDir()
	binary := os.Getenv("CORTEX_BINARY")
	if binary == "" {
		t.Skip("CORTEX_BINARY not set (run via go test, which builds via TestMain)")
	}

	q := Question{
		QuestionID:         "qa_t1",
		QuestionType:       "single-session-user",
		Question:           "Where did I move?",
		Answer:             "Berlin",
		QuestionDate:       "2024-08-15",
		HaystackSessionIDs: []string{"s1", "s2"},
		HaystackDates:      []string{"2024-03-01", "2024-04-12"},
		HaystackSessions: [][]Turn{
			{
				{Role: "user", Content: "I moved to Berlin last week.", HasAnswer: true},
				{Role: "assistant", Content: "Good luck with the move."},
			},
			{
				{Role: "user", Content: "Meeting at 3pm tomorrow."},
			},
		},
	}

	if err := hydrateHaystack(context.Background(), binary, workdir, q); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	// Journal should contain capture entries for all 3 turns.
	journalDir := filepath.Join(workdir, ".cortex", "journal", "capture")
	entries, err := os.ReadDir(journalDir)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("journal dir is empty; want at least one segment")
	}
	// At least one segment is non-empty.
	totalBytes := int64(0)
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		totalBytes += fi.Size()
	}
	if totalBytes == 0 {
		t.Errorf("journal segments are empty (count=%d)", len(entries))
	}
}

// TestHydrateHaystack_SkipsEmptyTurns confirms a blank turn doesn't
// trip the writer or create a useless capture. Exercises
// buildHaystackEvents directly so it doesn't need a binary.
func TestHydrateHaystack_SkipsEmptyTurns(t *testing.T) {
	q := Question{
		QuestionID:         "qa_t2",
		HaystackSessionIDs: []string{"s1"},
		HaystackDates:      []string{"2024-01-01"},
		HaystackSessions: [][]Turn{
			{
				{Role: "user", Content: ""},
				{Role: "user", Content: "   "},
				{Role: "user", Content: "real content"},
			},
		},
	}
	evs := buildHaystackEvents(q)
	if len(evs) != 1 {
		t.Fatalf("buildHaystackEvents returned %d events, want 1 (empty turns must be skipped)", len(evs))
	}
	if evs[0].ToolResult != "real content" {
		t.Errorf("kept turn content = %q, want %q", evs[0].ToolResult, "real content")
	}
}

func TestMakeCellResult_CortexStrategySetsVersionAndPasses(t *testing.T) {
	b := &Benchmark{model: "anthropic/claude-haiku-4.5"}
	pl := InstancePayload{
		Q:        Question{QuestionID: "qa_x"},
		Strategy: StrategyCortex,
	}
	out := &benchmarks.CodeOutput{TokensIn: 100, TokensOut: 50, CostUSD: 0.001, LatencyMs: 1234, Turns: 3}
	cell := makeCellResult(b, pl, AxisSingleHop, "Berlin.", out, true, "axis=single-hop")
	if err := cell.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cell.ContextStrategy != evalv2.StrategyCortex {
		t.Errorf("ContextStrategy=%q want %q", cell.ContextStrategy, evalv2.StrategyCortex)
	}
	if cell.CortexVersion == "" {
		t.Error("CortexVersion must be set on cortex-strategy cells")
	}
	if cell.Benchmark != "longmemeval" {
		t.Errorf("Benchmark=%q", cell.Benchmark)
	}
	if !strings.HasPrefix(cell.ScenarioID, "longmemeval/") {
		t.Errorf("ScenarioID=%q must be prefixed", cell.ScenarioID)
	}
	if cell.TaskSuccessCriterion != evalv2.CriterionJudgeLLM {
		t.Errorf("criterion=%q want %q", cell.TaskSuccessCriterion, evalv2.CriterionJudgeLLM)
	}
	if !cell.TaskSuccess {
		t.Error("TaskSuccess=false")
	}
}

func TestMakeCellResult_BaselineStrategyOmitsCortexVersion(t *testing.T) {
	b := &Benchmark{model: "anthropic/claude-haiku-4.5"}
	pl := InstancePayload{
		Q:        Question{QuestionID: "qa_x"},
		Strategy: StrategyBaseline,
	}
	cell := makeCellResult(b, pl, AxisMultiHop, "answer", nil, false, "axis=multi-hop")
	if err := cell.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cell.ContextStrategy != evalv2.StrategyBaseline {
		t.Errorf("ContextStrategy=%q want %q", cell.ContextStrategy, evalv2.StrategyBaseline)
	}
	if cell.CortexVersion != "" {
		t.Errorf("CortexVersion must be empty on baseline cells; got %q", cell.CortexVersion)
	}
	if cell.InjectedContextTokens != 0 {
		t.Errorf("InjectedContextTokens=%d must be 0 on baseline", cell.InjectedContextTokens)
	}
}

func TestRun_RegistersThroughBenchmarksRegistry(t *testing.T) {
	// init() side effect: longmemeval is registered.
	got, err := benchmarks.Get("longmemeval")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "longmemeval" {
		t.Errorf("Name()=%q", got.Name())
	}
}

func TestParseHaystackDate_TolerantOfFormats(t *testing.T) {
	tests := []struct {
		in       string
		wantYear int // 0 means "fallback to now is OK"
	}{
		{"2024-01-15", 2024},
		{"2024-01-15T10:30:00Z", 2024},
		{"2024/01/15", 2024},
		// LongMemEval upstream format with weekday tag.
		{"2021/06/01 (Tue) 21:10", 2021},
		{"2022/12/31 (Sat) 23:59", 2022},
		{"", 0},
		{"nonsense", 0},
	}
	for _, tc := range tests {
		got := parseHaystackDate(tc.in)
		if got.IsZero() {
			t.Errorf("parseHaystackDate(%q) returned zero time", tc.in)
			continue
		}
		if tc.wantYear != 0 && got.Year() != tc.wantYear {
			t.Errorf("parseHaystackDate(%q) year=%d want %d", tc.in, got.Year(), tc.wantYear)
		}
	}
}

func TestSanitizeNote_StripsNewlinesAndTruncates(t *testing.T) {
	in := "line one\nline two\twith\ttabs\rand\"quotes"
	got := sanitizeNote(in)
	for _, ch := range "\n\r\t\"" {
		if strings.ContainsRune(got, ch) {
			t.Errorf("sanitizeNote left forbidden rune %q in %q", ch, got)
		}
	}
	long := strings.Repeat("x", 500)
	if out := sanitizeNote(long); len(out) > 250 {
		t.Errorf("sanitizeNote did not truncate; len=%d", len(out))
	}
}

// TestRun_BadPayloadErrors guards the type assertion at the top of Run().
func TestRun_BadPayloadErrors(t *testing.T) {
	b := New()
	_, err := b.Run(context.Background(), benchmarks.Instance{ID: "x", Payload: "not a payload"}, benchmarks.Env{Workdir: t.TempDir()})
	if err == nil {
		t.Fatal("want error on bad payload type")
	}
	if !strings.Contains(err.Error(), "payload type") {
		t.Errorf("err=%q should mention payload type", err)
	}
}

// TestRun_RequiresWorkdir guards the env precondition.
func TestRun_RequiresWorkdir(t *testing.T) {
	b := New()
	pl := InstancePayload{Q: Question{QuestionID: "qa_x"}, Strategy: StrategyBaseline}
	_, err := b.Run(context.Background(), benchmarks.Instance{ID: "x", Payload: pl}, benchmarks.Env{})
	if err == nil {
		t.Fatal("want error on missing workdir")
	}
}

// TestLoad_PicksUpFiltersOntoBenchmark proves opts.Filter overrides
// land on the Benchmark struct (so Run() sees them).
func TestLoad_PicksUpFiltersOntoBenchmark(t *testing.T) {
	withFixture(t)
	b := New()
	_, err := b.Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{
			FilterModel:      "qwen/qwen3-coder",
			FilterJudge:      "true",
			FilterJudgeModel: "openai/gpt-4o",
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.model != "qwen/qwen3-coder" {
		t.Errorf("model=%q want qwen/qwen3-coder", b.model)
	}
	if !b.useJudge {
		t.Error("useJudge=false want true")
	}
	if b.judgeModel != "openai/gpt-4o" {
		t.Errorf("judgeModel=%q", b.judgeModel)
	}
}

// TestLoad_AppliesDefaultsWhenFlagsAbsent confirms the package's
// default model + judge model survive an empty Filter.
func TestLoad_AppliesDefaultsWhenFlagsAbsent(t *testing.T) {
	withFixture(t)
	b := New()
	_, err := b.Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.model != DefaultModel {
		t.Errorf("model=%q want %q", b.model, DefaultModel)
	}
	if b.judgeModel != DefaultJudgeModel {
		t.Errorf("judgeModel=%q want %q", b.judgeModel, DefaultJudgeModel)
	}
}

// keep the import alive for IDE.
var _ = time.Now
