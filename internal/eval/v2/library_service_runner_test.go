package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeHarness is a synchronous stand-in for ClaudeCLIHarness used by the
// runner tests. The onCall hook lets each test simulate model behavior:
// happy-path file writes, broken-build code, hard errors, ctx cancellation.
type fakeHarness struct {
	mu       sync.Mutex
	calls    []fakeCall
	onCall   func(ctx context.Context, prompt, workdir string, idx int) error
}

type fakeCall struct {
	prompt  string
	workdir string
}

func (h *fakeHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	h.mu.Lock()
	idx := len(h.calls)
	h.calls = append(h.calls, fakeCall{prompt: prompt, workdir: workdir})
	h.mu.Unlock()
	if h.onCall == nil {
		return nil
	}
	return h.onCall(ctx, prompt, workdir, idx)
}

// writeSessionFile writes a tiny but compilable Go file under
// internal/sessions/. Used as the default "harness produced something"
// behavior across the happy-path tests.
func writeSessionFile(workdir string, idx int) error {
	path := filepath.Join(workdir, "internal", "sessions", fmt.Sprintf("s%02d.go", idx+1))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "package sessions\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

func newRunnerEvaluator(t *testing.T) *LibraryServiceEvaluator {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	specDir := filepath.Join(root, "test", "evals", "library-service")
	seed := filepath.Join(root, "test", "evals", "projects", "library-service-seed")
	if _, err := os.Stat(filepath.Join(specDir, "sessions")); err != nil {
		t.Fatalf("specDir/sessions missing: %v", err)
	}
	if _, err := os.Stat(seed); err != nil {
		t.Fatalf("seed missing: %v", err)
	}
	return NewLibraryServiceEvaluator(specDir, seed)
}

// TestRunWithHarness_HappyPath drives all five session prompts through a
// fake harness that writes a compilable Go file each time. Every session
// should be recorded with BuildOK/TestsOK true and the new file showing in
// FilesChanged. Score must run cleanly against the resulting workdir.
func TestRunWithHarness_HappyPath(t *testing.T) {
	ev := newRunnerEvaluator(t)
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			return writeSessionFile(workdir, idx)
		},
	}

	run, err := ev.RunWithHarness(context.Background(), ConditionBaseline, "fake", fh)
	if err != nil {
		t.Fatalf("RunWithHarness: %v", err)
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	if run.WorkDir == "" {
		t.Fatal("WorkDir empty on success")
	}
	if _, err := os.Stat(run.WorkDir); err != nil {
		t.Fatalf("workdir gone before Cleanup: %v", err)
	}
	if got, want := len(run.SessionLog), 5; got != want {
		t.Fatalf("SessionLog len = %d, want %d", got, want)
	}
	if got, want := len(fh.calls), 5; got != want {
		t.Errorf("harness called %d times, want %d", got, want)
	}
	for i, sr := range run.SessionLog {
		if sr.SessionID == "" {
			t.Errorf("session %d: empty SessionID", i)
		}
		if !sr.BuildOK {
			t.Errorf("session %d (%s): BuildOK=false, want true", i, sr.SessionID)
		}
		if !sr.TestsOK {
			t.Errorf("session %d (%s): TestsOK=false, want true", i, sr.SessionID)
		}
		want := fmt.Sprintf("internal/sessions/s%02d.go", i+1)
		found := false
		for _, f := range sr.FilesChanged {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("session %d (%s): FilesChanged=%v, missing %q",
				i, sr.SessionID, sr.FilesChanged, want)
		}
	}
	// Score should populate the deferred RefactorDeltaPct sentinel even
	// though the dummy workdir lacks any real handlers — confirms Score()
	// was invoked.
	if run.Score.RefactorDeltaPct != -1 {
		t.Errorf("RefactorDeltaPct = %.3f, want -1", run.Score.RefactorDeltaPct)
	}
	if run.Condition != ConditionBaseline {
		t.Errorf("Condition = %q, want %q", run.Condition, ConditionBaseline)
	}
	if run.Model != "fake" {
		t.Errorf("Model = %q, want %q", run.Model, "fake")
	}
}

// TestRunWithHarness_BuildFailureRecorded confirms that a soft failure (a
// session leaving broken Go in the tree) gets captured in SessionResult and
// the loop continues through the remaining sessions. Per the plan:
// "Build fails or tests fail after a session → record … as false, continue".
func TestRunWithHarness_BuildFailureRecorded(t *testing.T) {
	ev := newRunnerEvaluator(t)
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			if idx == 2 {
				// Session 3 plants syntactically broken Go.
				path := filepath.Join(workdir, "internal", "broken", "broken.go")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				return os.WriteFile(path, []byte("package broken\nthis is not valid go\n"), 0o644)
			}
			return writeSessionFile(workdir, idx)
		},
	}

	run, err := ev.RunWithHarness(context.Background(), ConditionBaseline, "fake", fh)
	if err != nil {
		t.Fatalf("RunWithHarness: %v", err)
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	if got, want := len(run.SessionLog), 5; got != want {
		t.Fatalf("SessionLog len = %d, want %d (run must continue past soft failure)", got, want)
	}
	// Sessions 1 & 2 (indexes 0,1) build cleanly.
	for _, i := range []int{0, 1} {
		if !run.SessionLog[i].BuildOK {
			t.Errorf("session %d: BuildOK=false, want true (broken file not yet introduced)", i)
		}
	}
	// Session 3 introduces the break; sessions 4 & 5 still see it because
	// the runner does not auto-fix prior sessions' output.
	for _, i := range []int{2, 3, 4} {
		if run.SessionLog[i].BuildOK {
			t.Errorf("session %d: BuildOK=true, want false (broken.go persists)", i)
		}
	}
}

// TestRunWithHarness_HardErrorAborts confirms a harness error is treated as
// a hard error per the plan: the run aborts with a wrapped error and no
// further sessions are invoked. SessionResults captured before the failure
// remain in run.SessionLog so callers can inspect partial progress.
func TestRunWithHarness_HardErrorAborts(t *testing.T) {
	ev := newRunnerEvaluator(t)
	hardErr := errors.New("model unreachable")
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			if idx == 1 {
				return hardErr
			}
			return writeSessionFile(workdir, idx)
		},
	}

	run, err := ev.RunWithHarness(context.Background(), ConditionBaseline, "fake", fh)
	if err == nil {
		t.Fatal("expected hard error from harness, got nil")
	}
	if !errors.Is(err, hardErr) {
		t.Errorf("err = %v, want chain containing %v", err, hardErr)
	}
	if !strings.Contains(err.Error(), "session 02-") {
		t.Errorf("err = %q, want session id in wrap message", err.Error())
	}
	if run == nil {
		t.Fatal("run is nil; runner should return partial run alongside error")
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	// Session 01 succeeded; session 02's harness call failed without
	// running build/test. Per runOneSession's contract, the partial result
	// for session 2 is still appended (with BuildOK/TestsOK zero-value).
	if len(run.SessionLog) != 2 {
		t.Errorf("SessionLog len = %d, want 2 (session 1 ok + partial session 2)",
			len(run.SessionLog))
	}
	if got, want := len(fh.calls), 2; got != want {
		t.Errorf("harness called %d times, want %d (no further sessions after hard error)", got, want)
	}
	// Score should not run on hard error.
	if run.Score.RefactorDeltaPct == -1 {
		t.Error("Score appears to have run despite hard error")
	}
}

// TestRunWithHarness_ContextCancel confirms ctx cancellation propagates
// cleanly: the harness sees the cancelled ctx, the runner returns ctx.Err.
func TestRunWithHarness_ContextCancel(t *testing.T) {
	ev := newRunnerEvaluator(t)
	ctx, cancel := context.WithCancel(context.Background())
	fh := &fakeHarness{
		onCall: func(ctx context.Context, _, workdir string, idx int) error {
			if idx == 1 {
				cancel()
				return ctx.Err()
			}
			return writeSessionFile(workdir, idx)
		},
	}

	run, err := ev.RunWithHarness(ctx, ConditionBaseline, "fake", fh)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want chain containing context.Canceled", err)
	}
	if run == nil {
		t.Fatal("run is nil")
	}
	t.Cleanup(func() { _ = run.Cleanup() })
	if len(fh.calls) > 2 {
		t.Errorf("harness called %d times, want at most 2 (cancellation aborts after session 2)", len(fh.calls))
	}
}

// TestRun_NonBaselineNotImplemented locks in the Plan 02 contract that
// ConditionCortex / ConditionFrontier are deferred to Plan 03.
func TestRun_NonBaselineNotImplemented(t *testing.T) {
	ev := newRunnerEvaluator(t)
	for _, cond := range []LibraryServiceCondition{ConditionCortex, ConditionFrontier} {
		_, err := ev.Run(context.Background(), cond, "")
		if err == nil {
			t.Errorf("Run(%q) returned nil error, want not-implemented", cond)
			continue
		}
		if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("Run(%q) err = %v, want 'not implemented' message", cond, err)
		}
	}
}

func TestNewClaudeCLIHarness_BinaryMissing(t *testing.T) {
	_, err := NewClaudeCLIHarness("/nonexistent/claude-cli-binary-for-test", "")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestDiscoverSessionPrompts(t *testing.T) {
	dir := t.TempDir()
	files := []struct{ name, body string }{
		{"01-foo.md", "first"},
		{"03-baz.md", "third"},
		{"02-bar.md", "second"},
		{"README.md", "ignore"},
		{"notes.txt", "ignore"},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}

	got, err := discoverSessionPrompts(dir)
	if err != nil {
		t.Fatalf("discoverSessionPrompts: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d prompts, want 3 (NN- prefixed only)", len(got))
	}
	wantOrder := []string{"01-foo", "02-bar", "03-baz"}
	for i, p := range got {
		if p.id != wantOrder[i] {
			t.Errorf("prompts[%d].id = %q, want %q", i, p.id, wantOrder[i])
		}
		if p.body == "" {
			t.Errorf("prompts[%d].body is empty", i)
		}
	}
}

// TestSetupLibraryWorkdir_GitInit is a smoke test for the workdir helper:
// after setup, the workdir exists, contains the seed files alongside a
// resolved copy of system-spec.md (the seed README references it locally),
// and has a single "seed" commit so per-session diffs are well-defined.
func TestSetupLibraryWorkdir_GitInit(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	seed := filepath.Join(root, "test", "evals", "projects", "library-service-seed")
	specDir := filepath.Join(root, "test", "evals", "library-service")

	workdir, err := setupLibraryWorkdir(seed, specDir, ConditionBaseline)
	if err != nil {
		t.Fatalf("setupLibraryWorkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workdir) })

	if _, err := os.Stat(filepath.Join(workdir, "go.mod")); err != nil {
		t.Errorf("seed go.mod not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "system-spec.md")); err != nil {
		t.Errorf("system-spec.md not copied into workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".git")); err != nil {
		t.Errorf(".git not initialized: %v", err)
	}
	out, err := runGit(workdir, "log", "--oneline")
	if err != nil {
		t.Fatalf("git log: %v: %s", err, out)
	}
	if !strings.Contains(out, "seed") {
		t.Errorf("git log = %q, want seed commit", out)
	}
}
