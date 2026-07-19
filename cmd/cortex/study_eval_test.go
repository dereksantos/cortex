package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

func TestCountGoalHits(t *testing.T) {
	text := "The Resolve loop dispatches tool calls to Execute."
	if got := countGoalHits(text, []string{"Resolve", "Execute", "missing"}); got != 2 {
		t.Errorf("countGoalHits = %d, want 2", got)
	}
	if got := countGoalHits(text, nil); got != 0 {
		t.Errorf("no wants → 0, got %d", got)
	}
	// Acceptance is all-present: a case passes only when every Want appears.
	want := []string{"Resolve", "Execute"}
	if countGoalHits(text, want) != len(want) {
		t.Error("both facts present should be a pass")
	}
}

func TestStudyProbePass(t *testing.T) {
	tests := []struct {
		name   string
		probe  StudyProbe
		digest string
		want   bool
	}{
		{
			name:   "all gold present (MinGold 0 → all)",
			probe:  StudyProbe{Gold: []string{"wrapServerError"}},
			digest: "The wrapServerError function prepends the endpoint name.",
			want:   true,
		},
		{
			name:   "needle missing → fail",
			probe:  StudyProbe{Gold: []string{"wrapServerError"}},
			digest: "Something about server errors, but not the function name.",
			want:   false,
		},
		{
			name:   "MinGold met by a subset",
			probe:  StudyProbe{Gold: []string{"maxRepeatedToolCalls", "nudge", "repeat"}, MinGold: 2},
			digest: "It tracks repeats and aborts; a nudge fires first.",
			want:   true,
		},
		{
			name:   "MinGold not met → fail",
			probe:  StudyProbe{Gold: []string{"Safe", "Risky", "Blocked", "classif"}, MinGold: 2},
			digest: "It classifies commands.", // only "classif" present (1 < 2)
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.probe.pass(tc.digest); got != tc.want {
				t.Errorf("pass(%q) = %v, want %v (need %d of %d gold)",
					tc.digest, got, tc.want, tc.probe.need(), len(tc.probe.Gold))
			}
		})
	}
}

// Every agentic request carries a finite output cap — the runaway backstop —
// stamped at the SINGLE cap site (requestFor) plus session init. The old
// second cap helper was deleted in the engine unification; requestFor's stamp
// is covered by TestRequestForSetsMaxTokens.
func TestAgentRequestOutputCapped(t *testing.T) {
	// The base coder request is bounded by default (before any config override).
	if got := (CortexArgs{}).Request().MaxTokens; got != codeMaxOutputTokens {
		t.Errorf("coder request MaxTokens = %d, want %d", got, codeMaxOutputTokens)
	}
	// requestFor never emits an unbounded request: a 0 falls back to the default.
	if got := requestFor(ModelSpec{}, "s", "g", nil, 0, llm.DialectTemplateKwargs).MaxTokens; got != defaultAgentMaxTokens {
		t.Errorf("unset cap → %d, want backstop %d", got, defaultAgentMaxTokens)
	}
	// ModelSpec.maxOut: a config override wins, else the role default.
	if got := (ModelSpec{MaxTokens: 4096}).maxOut(studyMaxOutputTokens); got != 4096 {
		t.Errorf("maxOut override = %d, want 4096", got)
	}
	if got := (ModelSpec{}).maxOut(studyMaxOutputTokens); got != studyMaxOutputTokens {
		t.Errorf("maxOut default = %d, want %d", got, studyMaxOutputTokens)
	}
}

// need() defaults to all of Gold when MinGold is unset, else MinGold.
func TestStudyProbeNeed(t *testing.T) {
	if n := (StudyProbe{Gold: []string{"a", "b", "c"}}).need(); n != 3 {
		t.Errorf("default need = %d, want 3 (all gold)", n)
	}
	if n := (StudyProbe{Gold: []string{"a", "b", "c"}, MinGold: 2}).need(); n != 2 {
		t.Errorf("MinGold need = %d, want 2", n)
	}
}

// fixtureStudyEvalRow is a fully-populated row shaped like a real
// runStudyEvalNav rep — used by the journal-emit tests below so they don't
// need a live fleet to exercise the row→journal seam (docs/study-subagent.md
// §5's deferred journal-sink wiring, completion-roadmap.md Track C item C3).
func fixtureStudyEvalRow() studyEvalRow {
	return studyEvalRow{
		Path:             "pkg/llm",
		Model:            "qwen3-coder-next",
		Rep:              1,
		LatencyMS:        4200,
		GoalHit:          1.0,
		GoldPresent:      1,
		GoldNeed:         1,
		StopReason:       "clean-finalize",
		FinalizeForced:   false,
		Iterations:       3,
		Outlines:         1,
		Greps:            2,
		Reads:            1,
		ToolErrs:         0,
		ReadBytes:        2048,
		Bounded:          true,
		InputTokens:      900,
		OutputTokens:     210,
		PeakOutputTokens: 210,
		MaxTokensClamped: false,
		Salvaged:         false,
		Thinking:         "off",
		ReasoningTokens:  64,
		GoalHitPer1k:     4.76,
		Pass:             true,
		DigestChars:      512,
		Note:             "needle: locate one symbol in a package",
	}
}

// TestStudyResultPayload_MapsRowFields is the pure row→payload mapping the
// design calls the preferred seam: the shared eval vocabulary rides the
// embedded EvalCellResultPayload (same json tags emitSessionMetrics writes),
// the study-only discriminators land in the typed extension block, no field
// is silently dropped.
func TestStudyResultPayload_MapsRowFields(t *testing.T) {
	row := fixtureStudyEvalRow()
	p := studyResultPayload(row, "run-1", "https://backend.example")

	// Shared vocabulary (EvalCellResultPayload).
	if p.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", p.RunID)
	}
	if p.ScenarioID != row.Path {
		t.Errorf("ScenarioID = %q, want %q (row.Path)", p.ScenarioID, row.Path)
	}
	if p.Harness != "study" {
		t.Errorf("Harness = %q, want study", p.Harness)
	}
	if p.Model != row.Model {
		t.Errorf("Model = %q, want %q", p.Model, row.Model)
	}
	if p.Backend != "https://backend.example" {
		t.Errorf("Backend = %q", p.Backend)
	}
	if p.TokensIn != row.InputTokens || p.TokensOut != row.OutputTokens {
		t.Errorf("tokens = %d/%d, want %d/%d", p.TokensIn, p.TokensOut, row.InputTokens, row.OutputTokens)
	}
	if p.LatencyMs != row.LatencyMS {
		t.Errorf("LatencyMs = %d, want %d", p.LatencyMs, row.LatencyMS)
	}
	if p.AgentTurnsTotal != row.Iterations {
		t.Errorf("AgentTurnsTotal = %d, want %d (row.Iterations)", p.AgentTurnsTotal, row.Iterations)
	}
	if !p.TaskSuccess {
		t.Errorf("TaskSuccess = false, want true (row.Pass)")
	}
	if p.Thinking != "off" || p.ReasoningTokens != 64 {
		t.Errorf("Thinking/ReasoningTokens = %q/%d, want off/64", p.Thinking, p.ReasoningTokens)
	}
	if p.Notes != row.Note {
		t.Errorf("Notes = %q, want %q (row.Note)", p.Notes, row.Note)
	}

	// Study-specific extension block — has no home in EvalCellResultPayload.
	if p.Rep != row.Rep {
		t.Errorf("Rep = %d, want %d", p.Rep, row.Rep)
	}
	if p.GoalHit != row.GoalHit {
		t.Errorf("GoalHit = %v, want %v", p.GoalHit, row.GoalHit)
	}
	if p.StopReason != row.StopReason {
		t.Errorf("StopReason = %q, want %q", p.StopReason, row.StopReason)
	}
	if p.Outlines != row.Outlines || p.Greps != row.Greps || p.Reads != row.Reads {
		t.Errorf("tool counts = %d/%d/%d, want %d/%d/%d",
			p.Outlines, p.Greps, p.Reads, row.Outlines, row.Greps, row.Reads)
	}
	if p.ReadBytes != row.ReadBytes || p.Bounded != row.Bounded {
		t.Errorf("ReadBytes/Bounded = %d/%v, want %d/%v", p.ReadBytes, p.Bounded, row.ReadBytes, row.Bounded)
	}
	if p.PeakOutputTokens != row.PeakOutputTokens || p.MaxTokensClamped != row.MaxTokensClamped {
		t.Errorf("peak/clamped = %d/%v, want %d/%v",
			p.PeakOutputTokens, p.MaxTokensClamped, row.PeakOutputTokens, row.MaxTokensClamped)
	}
}

// TestStudyResultPayload_CarriesError covers a failed rep: Error must land
// on the extension block, and TaskSuccess must reflect the (false) row.Pass.
func TestStudyResultPayload_CarriesError(t *testing.T) {
	row := fixtureStudyEvalRow()
	row.Pass = false
	row.Error = "context deadline exceeded"
	p := studyResultPayload(row, "run-1", "")
	if p.TaskSuccess {
		t.Error("TaskSuccess = true, want false for a failed rep")
	}
	if p.Error != "context deadline exceeded" {
		t.Errorf("Error = %q", p.Error)
	}
}

// TestEmitStudyResult_RoundTrip drives the actual write path (journal.NewWriter,
// same as emitSessionMetrics) against a temp class dir and reads the segment
// back, confirming the row lands with the expected shared + extension fields.
func TestEmitStudyResult_RoundTrip(t *testing.T) {
	classDir := filepath.Join(t.TempDir(), "journal", "study")
	row := fixtureStudyEvalRow()

	if err := emitStudyResult(classDir, row, "run-abc", "backend-x"); err != nil {
		t.Fatalf("emitStudyResult: %v", err)
	}

	r, err := journal.NewReader(classDir)
	if err != nil {
		t.Fatalf("journal.NewReader: %v", err)
	}
	defer r.Close()

	e, err := r.Next()
	if err != nil {
		t.Fatalf("r.Next: %v", err)
	}
	if e.Type != journal.TypeStudyResult {
		t.Errorf("entry type = %s, want %s", e.Type, journal.TypeStudyResult)
	}
	p, err := journal.ParseStudyResult(e)
	if err != nil {
		t.Fatalf("ParseStudyResult: %v", err)
	}
	if p.RunID != "run-abc" || p.ScenarioID != "pkg/llm" || p.Model != "qwen3-coder-next" {
		t.Errorf("run/scenario/model = %s/%s/%s", p.RunID, p.ScenarioID, p.Model)
	}
	if p.Backend != "backend-x" {
		t.Errorf("Backend = %q, want backend-x", p.Backend)
	}
	if p.GoalHit != 1.0 || p.StopReason != "clean-finalize" || !p.Bounded {
		t.Errorf("goal_hit/stop_reason/bounded = %v/%s/%v", p.GoalHit, p.StopReason, p.Bounded)
	}
	if p.Greps != 2 || p.Reads != 1 {
		t.Errorf("greps/reads = %d/%d, want 2/1", p.Greps, p.Reads)
	}

	// Only one entry — no stray writes.
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("r.Next after the one entry: err = %v, want io.EOF", err)
	}
}

// TestEmitStudyResult_AppendsAcrossReps confirms successive reps land as
// successive entries in offset order, each carrying its own Rep index — the
// shape a jq-over-the-journal consumer (Gate C) depends on.
func TestEmitStudyResult_AppendsAcrossReps(t *testing.T) {
	classDir := filepath.Join(t.TempDir(), "journal", "study")

	for rep := 0; rep < 2; rep++ {
		row := fixtureStudyEvalRow()
		row.Rep = rep
		if err := emitStudyResult(classDir, row, "run-multi", ""); err != nil {
			t.Fatalf("emitStudyResult rep %d: %v", rep, err)
		}
	}

	r, err := journal.NewReader(classDir)
	if err != nil {
		t.Fatalf("journal.NewReader: %v", err)
	}
	defer r.Close()

	for want := 0; want < 2; want++ {
		e, err := r.Next()
		if err != nil {
			t.Fatalf("r.Next rep %d: %v", want, err)
		}
		p, err := journal.ParseStudyResult(e)
		if err != nil {
			t.Fatalf("ParseStudyResult rep %d: %v", want, err)
		}
		if p.Rep != want {
			t.Errorf("entry %d: Rep = %d, want %d", want, p.Rep, want)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("r.Next after two entries: err = %v, want io.EOF", err)
	}
}

// TestEmitStudyResult_DoesNotWriteStdout guards the "stdout unchanged"
// requirement: the journal emitter must never itself print — the existing
// fmt.Println(string(b)) JSONL line drivers parse is the only stdout writer
// for a rep, and it happens independently of emitStudyResult.
func TestEmitStudyResult_DoesNotWriteStdout(t *testing.T) {
	classDir := filepath.Join(t.TempDir(), "journal", "study")
	row := fixtureStudyEvalRow()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	emitErr := emitStudyResult(classDir, row, "run-stdout", "")
	w.Close()
	os.Stdout = origStdout

	captured, _ := io.ReadAll(r)
	if emitErr != nil {
		t.Fatalf("emitStudyResult: %v", emitErr)
	}
	if len(captured) != 0 {
		t.Errorf("emitStudyResult wrote to stdout: %q", captured)
	}
}

// TestEmitStudyResult_WrapsWriterError: when the class dir can't be created
// (a regular file sits where the directory needs to go), emitStudyResult
// returns a wrapped, non-nil error instead of panicking or failing silently.
func TestEmitStudyResult_WrapsWriterError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	classDir := filepath.Join(blocker, "study") // blocker is a file, not a dir

	err := emitStudyResult(classDir, fixtureStudyEvalRow(), "run-err", "")
	if err == nil {
		t.Fatal("expected an error when the class dir can't be created")
	}
	if !strings.Contains(err.Error(), "study-eval:") {
		t.Errorf("error = %v, want it wrapped with a study-eval: prefix", err)
	}
}
