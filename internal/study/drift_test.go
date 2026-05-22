package study

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// driftMock is a minimal ExtractFunc that lets us count calls per
// invocation of Run — we use this to assert "second run extracted only
// the changed file, not all of them."
type driftMock struct {
	calls       int
	tag         string
	bySourceTag map[string]int // source string from controller → call count
}

func (m *driftMock) Fn(ctx context.Context, content, source, langHint, fileRoleHint string) ([]ExtractedInsight, bool, error) {
	m.calls++
	if m.bySourceTag == nil {
		m.bySourceTag = map[string]int{}
	}
	m.bySourceTag[source]++
	return []ExtractedInsight{{
		Content:    "mock for " + langHint,
		Category:   "pattern",
		Importance: 0.6,
		Tags:       []string{m.tag, langHint},
	}}, false, nil
}

func mkDriftController(t *testing.T, root string, mock *driftMock, target float64, budget int) *Controller {
	t.Helper()
	cortexDir := filepath.Join(root, ".cortex")
	c, err := NewController(ControllerConfig{
		Config: Config{
			ProjectRoot:    root,
			ContextDir:     cortexDir,
			TargetCoverage: target,
			BudgetMax:      budget,
			BatchSize:      4,
			WindowLines:    400,
			WindowOverlap:  40,
			ExtractOp:      ExtractOpAuto,
		},
		ExtractInsightFn:  mock.Fn,
		ExtractOverviewFn: mock.Fn,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return c
}

// TestController_Drift_EditedFileBecomesUncovered: edit a file after a
// completed run; the next run sees its chunks as uncovered (other
// files keep their coverage), and the mock extract gets called only
// for that file.
func TestController_Drift_EditedFileBecomesUncovered(t *testing.T) {
	root := buildFixture(t)
	mock1 := &driftMock{tag: "pass1"}

	// Pass 1: low target to make sure we cover at least one chunk.
	c1 := mkDriftController(t, root, mock1, 0.99, 100)
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	covered1 := countCoveredChunks(c1.State().CoveredFiles)
	if covered1 == 0 {
		t.Fatal("pass 1: no chunks covered")
	}
	if _, ok := c1.State().CoveredFiles["main.go"]; !ok {
		t.Fatalf("pass 1: main.go not in CoveredFiles")
	}
	mainHash1 := c1.State().CoveredFiles["main.go"].ContentHash

	// Sleep past mtime resolution to be safe, then edit main.go.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n// edited\n"+strings.Repeat("var _ = 2\n", 30)), 0o644); err != nil {
		t.Fatalf("rewrite main.go: %v", err)
	}

	// Pass 2: drift-aware resume. main.go's hash changed; its chunk(s)
	// must be re-extracted. Other files keep their coverage.
	mock2 := &driftMock{tag: "pass2"}
	c2 := mkDriftController(t, root, mock2, 0.99, 100)
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	st2 := c2.State()
	if fc, ok := st2.CoveredFiles["main.go"]; !ok {
		t.Fatalf("pass 2: main.go missing after drift re-cover")
	} else if fc.ContentHash == mainHash1 {
		t.Errorf("pass 2: main.go ContentHash unchanged (%s); should reflect edit", fc.ContentHash)
	}

	// At least one extract call should target main.go in pass 2.
	mainCalls := 0
	for src, n := range mock2.bySourceTag {
		if strings.Contains(src, "main.go") {
			mainCalls += n
		}
	}
	if mainCalls == 0 {
		t.Errorf("pass 2: no extract calls hit main.go; got %v", mock2.bySourceTag)
	}
}

// TestController_Drift_UntouchedFilesStayCovered: pass 2 with no
// project changes triggers the no_drift short-circuit and zero LLM
// calls.
func TestController_Drift_NoOpWhenStable(t *testing.T) {
	root := buildFixture(t)
	mock1 := &driftMock{tag: "pass1"}

	// Cover the whole project in pass 1.
	c1 := mkDriftController(t, root, mock1, 0.50, 100)
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if c1.State().Halted != "target" {
		t.Fatalf("pass 1: expected halted=target, got %q", c1.State().Halted)
	}

	// Pass 2: no edits → no_drift short-circuit, zero LLM calls.
	mock2 := &driftMock{tag: "pass2"}
	c2 := mkDriftController(t, root, mock2, 0.50, 100)
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if c2.State().Halted != "no_drift" {
		t.Errorf("pass 2 Halted=%q (want no_drift)", c2.State().Halted)
	}
	if mock2.calls != 0 {
		t.Errorf("pass 2: mock called %d times (want 0)", mock2.calls)
	}
}

// TestController_Drift_NewFileTriggersWork: add a new file after a
// completed run; the next run picks it up.
func TestController_Drift_NewFileTriggersWork(t *testing.T) {
	root := buildFixture(t)
	mock1 := &driftMock{tag: "pass1"}

	c1 := mkDriftController(t, root, mock1, 0.50, 100)
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}

	// Add a new file.
	newRel := "added/new.go"
	full := filepath.Join(root, newRel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full,
		[]byte("package added\n\nfunc New() {}\n"+strings.Repeat("var z = 1\n", 30)), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	mock2 := &driftMock{tag: "pass2"}
	c2 := mkDriftController(t, root, mock2, 0.99, 100)
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	st2 := c2.State()
	if _, ok := st2.CoveredFiles[newRel]; !ok {
		t.Errorf("pass 2: new file %s not covered (CoveredFiles=%v)", newRel, keysOf(st2.CoveredFiles))
	}
	// At least one extract call should mention the new file.
	hit := 0
	for src := range mock2.bySourceTag {
		if strings.Contains(src, newRel) {
			hit++
		}
	}
	if hit == 0 {
		t.Errorf("pass 2: no extract calls hit %s; got %v", newRel, mock2.bySourceTag)
	}
}

// TestController_Drift_DeletedFileDropsFromCoverage: delete a file
// after a completed run; the next run removes it from CoveredFiles and
// the denominator shrinks.
func TestController_Drift_DeletedFileDropsFromCoverage(t *testing.T) {
	root := buildFixture(t)
	mock1 := &driftMock{tag: "pass1"}

	c1 := mkDriftController(t, root, mock1, 0.99, 100)
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if _, ok := c1.State().CoveredFiles["CONTRIBUTING.md"]; !ok {
		t.Fatalf("pass 1: CONTRIBUTING.md expected in covered set")
	}

	if err := os.Remove(filepath.Join(root, "CONTRIBUTING.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	mock2 := &driftMock{tag: "pass2"}
	c2 := mkDriftController(t, root, mock2, 0.99, 100)
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if _, ok := c2.State().CoveredFiles["CONTRIBUTING.md"]; ok {
		t.Errorf("pass 2: deleted file CONTRIBUTING.md still in CoveredFiles")
	}
}

// TestController_V1Migration adopts legacy CoveredChunkIDs into the
// per-file CoveredFiles map without losing coverage.
func TestController_V1Migration(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")

	// Run once to populate state, then fabricate a v1-shaped state file:
	// drop CoveredFiles, populate CoveredChunkIDs from the existing map.
	mock1 := &driftMock{tag: "v1seed"}
	c1 := mkDriftController(t, root, mock1, 0.50, 100)
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	st1 := c1.State()
	flat := []string{}
	for _, fc := range st1.CoveredFiles {
		flat = append(flat, fc.ChunkIDs...)
	}
	if len(flat) == 0 {
		t.Fatal("pass 1 produced no chunks")
	}

	// Synthesize a v1 state. SaveState wipes CoveredChunkIDs on the v2
	// write path, so we marshal directly to mimic what a v1 binary
	// would have left on disk.
	v1 := *st1
	v1.Version = 1
	v1.CoveredFiles = nil
	v1.CoveredChunkIDs = flat
	b, err := json.MarshalIndent(&v1, "", "  ")
	if err != nil {
		t.Fatalf("marshal v1: %v", err)
	}
	if err := os.WriteFile(StatePath(cortexDir), b, 0o644); err != nil {
		t.Fatalf("write v1 state: %v", err)
	}

	// On the next run, controller should auto-migrate without any
	// edits — and short-circuit no_drift because the migration restored
	// the same effective coverage at target.
	mock2 := &driftMock{tag: "v2"}
	c2 := mkDriftController(t, root, mock2, 0.50, 100)
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	st2 := c2.State()
	if len(st2.CoveredFiles) == 0 {
		t.Fatal("post-migration CoveredFiles empty")
	}
	if st2.Halted != "no_drift" {
		t.Errorf("post-migration Halted=%q (want no_drift — migration should reconstruct coverage at target)", st2.Halted)
	}
}

func keysOf(m map[string]FileCoverage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
