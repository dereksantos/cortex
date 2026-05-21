package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// mockExtract returns a single canned insight per call. Counts calls
// so tests can assert it was invoked the expected number of times.
type mockExtract struct {
	calls    int
	category string
	tag      string
}

func (m *mockExtract) Fn(ctx context.Context, content, source, langHint, fileRoleHint string) ([]ExtractedInsight, bool, error) {
	m.calls++
	return []ExtractedInsight{
		{
			Content:    "mock insight for " + langHint,
			Category:   m.category,
			Importance: 0.6,
			Tags:       []string{m.tag, langHint},
		},
	}, false, nil
}

// buildFixture creates a minimal project: a go.mod at root, two Go
// files, two doc files. Returns the project root.
func buildFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	must("go.mod", "module example.com/m\n")
	must("main.go", "package main\n\nfunc main() {}\n\n// a useful comment\n"+strings.Repeat("var _ = 1\n", 30))
	must("util/util.go", "package util\n\nfunc Helper() {}\n"+strings.Repeat("var x = 1\n", 30))
	must("README.md", "# Demo\n\nA tiny demo project.\n")
	must("CONTRIBUTING.md", "# Contributing\n\nFile a PR.\n")
	return root
}

func TestController_Run_HitsTarget(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")
	mockOverview := &mockExtract{category: "overview:source", tag: "overview"}
	mockInsight := &mockExtract{category: "pattern", tag: "insight"}

	c, err := NewController(ControllerConfig{
		Config: Config{
			ProjectRoot:    root,
			ContextDir:     cortexDir,
			TargetCoverage: 0.50,
			BudgetMax:      30,
			BatchSize:      2,
			WindowLines:    400,
			WindowOverlap:  40,
			ExtractOp:      ExtractOpAuto,
		},
		ExtractInsightFn:  mockInsight.Fn,
		ExtractOverviewFn: mockOverview.Fn,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := c.State()
	if st.CompletedAt == nil {
		t.Fatal("CompletedAt nil; expected completion")
	}
	if st.Halted == "" {
		t.Errorf("Halted empty")
	}
	if st.InsightsEmitted == 0 {
		t.Error("no insights emitted")
	}

	// Check the journal got dream entries.
	dreamDir := filepath.Join(cortexDir, "journal", "dream")
	r, err := journal.NewReader(dreamDir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	entries := 0
	for {
		e, err := r.Next()
		if err != nil {
			break
		}
		if e.Type == journal.TypeDreamInsight {
			entries++
		}
	}
	if entries == 0 {
		t.Error("no dream.insight entries in journal")
	}

	// State file persisted.
	if _, err := os.Stat(StatePath(cortexDir)); err != nil {
		t.Errorf("state file missing: %v", err)
	}

	// Auto routing should call overview for Go (source) and insight
	// for Markdown (prose).
	if mockOverview.calls == 0 {
		t.Errorf("overview never called (Go files should route to it)")
	}
	if mockInsight.calls == 0 {
		t.Errorf("insight never called (Markdown files should route to it)")
	}
}

func TestController_Run_DryRun(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")
	mock := &mockExtract{category: "pattern", tag: "test"}

	c, err := NewController(ControllerConfig{
		Config: Config{
			ProjectRoot:    root,
			ContextDir:     cortexDir,
			TargetCoverage: 0.30,
			BudgetMax:      10,
			BatchSize:      2,
			ExtractOp:      ExtractOpInsight,
			DryRun:         true,
		},
		ExtractInsightFn:  mock.Fn,
		ExtractOverviewFn: mock.Fn,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Dry-run skips journal writes but counts emitted insights for
	// reporting. Verify no actual journal entries are present.
	dreamDir := filepath.Join(cortexDir, "journal", "dream")
	r, err := journal.NewReader(dreamDir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	entries := 0
	for {
		_, err := r.Next()
		if err != nil {
			break
		}
		entries++
	}
	if entries != 0 {
		t.Errorf("dry-run produced %d journal entries (want 0)", entries)
	}
}

func TestController_Run_BudgetHalt(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")
	mock := &mockExtract{category: "pattern", tag: "test"}

	c, err := NewController(ControllerConfig{
		Config: Config{
			ProjectRoot:    root,
			ContextDir:     cortexDir,
			TargetCoverage: 0.99, // basically unreachable
			BudgetMax:      2,    // halt fast
			BatchSize:      1,
			ExtractOp:      ExtractOpAuto,
		},
		ExtractInsightFn:  mock.Fn,
		ExtractOverviewFn: mock.Fn,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := c.State()
	if st.Halted != "budget_loc" && st.Halted != "budget_files" {
		t.Errorf("Halted = %q, want budget_loc or budget_files", st.Halted)
	}
}

func TestController_Run_Resumes(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")
	mock := &mockExtract{category: "pattern", tag: "test"}

	mkController := func(target float64, budget int) (*BootstrapController, error) {
		return NewController(ControllerConfig{
			Config: Config{
				ProjectRoot:    root,
				ContextDir:     cortexDir,
				TargetCoverage: target,
				BudgetMax:      budget,
				BatchSize:      1,
				ExtractOp:      ExtractOpAuto,
			},
			ExtractInsightFn:  mock.Fn,
			ExtractOverviewFn: mock.Fn,
		})
	}

	// Pass 1: low budget, won't reach target.
	c1, err := mkController(0.99, 1)
	if err != nil {
		t.Fatalf("NewController 1: %v", err)
	}
	if err := c1.Run(context.Background()); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	covered1 := len(c1.State().CoveredChunkIDs)
	if covered1 == 0 {
		t.Fatal("pass 1 covered no chunks")
	}

	// Force CompletedAt back to nil so pass 2 is treated as resume.
	st, err := LoadState(StatePath(cortexDir))
	if err != nil || st == nil {
		t.Fatalf("LoadState: %v / nil=%v", err, st == nil)
	}
	st.CompletedAt = nil
	if err := SaveState(StatePath(cortexDir), st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Pass 2: resume, bigger budget. Should pick up where pass 1 left off.
	c2, err := mkController(0.99, 10)
	if err != nil {
		t.Fatalf("NewController 2: %v", err)
	}
	if err := c2.Run(context.Background()); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	covered2 := len(c2.State().CoveredChunkIDs)
	if covered2 <= covered1 {
		t.Errorf("resume did not add coverage: %d → %d", covered1, covered2)
	}
}

func TestController_Run_PidLockSkipsSecond(t *testing.T) {
	root := buildFixture(t)
	cortexDir := filepath.Join(root, ".cortex")
	mock := &mockExtract{category: "pattern", tag: "test"}

	// Acquire the lock externally so the controller can't take it.
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pid, ok, err := AcquirePIDLock(cortexDir)
	if err != nil || !ok {
		t.Fatalf("AcquirePIDLock: ok=%v err=%v", ok, err)
	}
	defer pid.Release()

	c, err := NewController(ControllerConfig{
		Config: Config{
			ProjectRoot:    root,
			ContextDir:     cortexDir,
			TargetCoverage: 0.30,
			BudgetMax:      10,
			BatchSize:      1,
			ExtractOp:      ExtractOpAuto,
		},
		ExtractInsightFn:  mock.Fn,
		ExtractOverviewFn: mock.Fn,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	// Run should detect existing lock and return nil with no journal
	// writes.
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run (locked): %v", err)
	}
	// No state file because we never started — the lock check ran
	// before the loop.
	if _, err := os.Stat(StatePath(cortexDir)); err == nil {
		t.Error("state file present after lock-skipped run; want absent")
	}
}

func TestShouldRunBootstrap(t *testing.T) {
	cortexDir := t.TempDir()

	if run, reason := ShouldRunBootstrap(cortexDir); !run || reason != "never_run" {
		t.Errorf("missing state: run=%v reason=%q (want true, never_run)", run, reason)
	}

	// Write an incomplete state.
	s := &BootstrapState{ProjectRoot: "/x", StateHash: "abc"}
	if err := SaveState(StatePath(cortexDir), s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if run, reason := ShouldRunBootstrap(cortexDir); !run || reason != "incomplete" {
		t.Errorf("incomplete state: run=%v reason=%q (want true, incomplete)", run, reason)
	}

	// Mark complete.
	now := mustNow()
	s.CompletedAt = &now
	if err := SaveState(StatePath(cortexDir), s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if run, reason := ShouldRunBootstrap(cortexDir); run || reason != "" {
		t.Errorf("complete state: run=%v reason=%q (want false, '')", run, reason)
	}
}

func mustNow() (t time.Time) {
	return time.Now().UTC()
}
