//go:build !windows

package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeCortexScript is a minimal `cortex` CLI stand-in. It reads HOME from
// its environment and writes to / reads from $HOME/.cortex/events.log, so
// two invocations with different HOMEs see disjoint state — exactly the
// isolation contract CortexInjector relies on.
//
// Subcommands:
//   - init: ensure $HOME/.cortex/ exists, touch events.log, exit 0
//   - capture --type=X --content=Y: append "X|||Y" + newline + 0x1E to log
//   - ingest: exit 0 (no-op; the fake skips the queue → DB step)
//   - search ...: cat events.log to stdout, exit 0
//
// On any unknown subcommand the script exits 0 silently to keep tests
// permissive — we only assert the calls we care about.
const fakeCortexScript = `#!/bin/sh
set -e
mkdir -p "$HOME/.cortex"
LOG="$HOME/.cortex/events.log"
case "$1" in
  init)
    : > "$LOG"
    echo "init ok"
    ;;
  capture)
    shift
    TYPE=""
    CONTENT=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --type=*) TYPE="${1#--type=}" ;;
        --type) TYPE="$2"; shift ;;
        --content=*) CONTENT="${1#--content=}" ;;
        --content) CONTENT="$2"; shift ;;
      esac
      shift
    done
    printf '%s|||%s\n\036\n' "$TYPE" "$CONTENT" >> "$LOG"
    echo "captured ${TYPE}"
    ;;
  ingest)
    echo "ingest ok"
    ;;
  search)
    if [ -f "$LOG" ]; then
      cat "$LOG"
    fi
    ;;
  *)
    : # silent for any other subcommand
    ;;
esac
`

// errorFakeCortexScript exits non-zero on any subcommand, with a known
// stderr line we can assert on. Used to verify error propagation.
const errorFakeCortexScript = `#!/bin/sh
echo "fake error: subcommand=$1" >&2
exit 7
`

// installFakeCortex writes the given script body to <dir>/cortex and
// chmods it executable. Returns the absolute path. Skips on Windows
// because the shebang machinery here is Unix-only.
func installFakeCortex(t *testing.T, dir, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake cortex shell script is Unix-only")
	}
	path := filepath.Join(dir, "cortex")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake cortex: %v", err)
	}
	return path
}

// TestNoOpInjector confirms baseline+frontier injector is fully inert:
// no preamble, no record-side state, no errors.
func TestNoOpInjector(t *testing.T) {
	var inj Injector = NoOpInjector{}
	for idx := 0; idx < 5; idx++ {
		got, err := inj.Preamble(context.Background(), idx, "/tmp")
		if err != nil {
			t.Errorf("Preamble(%d): err = %v, want nil", idx, err)
		}
		if got != "" {
			t.Errorf("Preamble(%d) = %q, want empty", idx, got)
		}
		if err := inj.Record(context.Background(), idx, "/tmp", SessionResult{SessionID: "x"}); err != nil {
			t.Errorf("Record(%d): err = %v, want nil", idx, err)
		}
	}
}

func TestNewCortexInjector_BinaryMissing(t *testing.T) {
	stateDir := t.TempDir()
	_, err := NewCortexInjector("/path/does/not/exist/cortex", stateDir)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "cortex binary not found") {
		t.Errorf("err = %v, want 'cortex binary not found'", err)
	}
}

func TestNewCortexInjector_StateDirRelative(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)
	if _, err := NewCortexInjector(bin, "relative/path"); err == nil {
		t.Fatal("expected error for relative stateDir")
	}
}

func TestCortexInjector_PreambleS1IsEmpty(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)
	inj, err := NewCortexInjector(bin, t.TempDir())
	if err != nil {
		t.Fatalf("NewCortexInjector: %v", err)
	}
	got, err := inj.Preamble(context.Background(), 0, "/tmp")
	if err != nil {
		t.Fatalf("Preamble: %v", err)
	}
	if got != "" {
		t.Errorf("Preamble(S1) = %q, want empty", got)
	}
}

func TestCortexInjector_PreambleBeforeRecordIsEmpty(t *testing.T) {
	// Even after S1 (sessionIdx=1+), if no Record call has happened the
	// state dir is empty and a search would hit nothing — short-circuit.
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)
	inj, err := NewCortexInjector(bin, t.TempDir())
	if err != nil {
		t.Fatalf("NewCortexInjector: %v", err)
	}
	got, err := inj.Preamble(context.Background(), 1, "/tmp")
	if err != nil {
		t.Fatalf("Preamble: %v", err)
	}
	if got != "" {
		t.Errorf("Preamble(S2 pre-record) = %q, want empty", got)
	}
}

// TestCortexInjector_RecordThenPreamble walks the happy path: capture a
// file's content into the fake cortex, then retrieve a preamble that
// references it.
func TestCortexInjector_RecordThenPreamble(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)
	stateDir := t.TempDir()

	workdir := t.TempDir()
	relPath := "internal/handlers/books.go"
	full := filepath.Join(workdir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("package handlers\n// SENTINEL_BOOKS_HANDLER\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	// A non-Go file MUST be ignored by Record.
	mdPath := filepath.Join(workdir, "README.md")
	if err := os.WriteFile(mdPath, []byte("# readme\n"), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	inj, err := NewCortexInjector(bin, stateDir)
	if err != nil {
		t.Fatalf("NewCortexInjector: %v", err)
	}

	sr := SessionResult{
		SessionID:    "01-scaffold-and-books",
		FilesChanged: []string{relPath, "README.md"},
	}
	if err := inj.Record(context.Background(), 0, workdir, sr); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The fake cortex log under the isolated HOME should contain the
	// captured file content (and only the .go file, not README.md).
	log := readLog(t, stateDir)
	if !strings.Contains(log, "SENTINEL_BOOKS_HANDLER") {
		t.Errorf("log missing captured Go file content; log=%q", log)
	}
	if strings.Contains(log, "# readme") {
		t.Errorf("log contained README.md content (Record should skip non-.go); log=%q", log)
	}

	// Preamble for S2 should be non-empty and contain the markdown header
	// + the captured sentinel.
	preamble, err := inj.Preamble(context.Background(), 1, workdir)
	if err != nil {
		t.Fatalf("Preamble: %v", err)
	}
	if preamble == "" {
		t.Fatal("Preamble = empty after Record")
	}
	if !strings.Contains(preamble, "Conventions established in prior sessions") {
		t.Errorf("preamble missing markdown header; got=%q", preamble)
	}
	if !strings.Contains(preamble, "SENTINEL_BOOKS_HANDLER") {
		t.Errorf("preamble missing captured content; got=%q", preamble)
	}
}

// TestCortexInjector_StateIsolation is the isolation contract: two
// injectors with different stateDirs MUST NOT see each other's events.
// This is mandatory per plans/03-cortex-injection-prompt.md — without it
// baseline and cortex condition runs would corrupt each other.
func TestCortexInjector_StateIsolation(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)

	stateA := t.TempDir()
	stateB := t.TempDir()

	workdir := t.TempDir()
	pathA := filepath.Join(workdir, "a.go")
	pathB := filepath.Join(workdir, "b.go")
	if err := os.WriteFile(pathA, []byte("package x // SENTINEL_A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("package x // SENTINEL_B\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	injA, err := NewCortexInjector(bin, stateA)
	if err != nil {
		t.Fatalf("inj A: %v", err)
	}
	injB, err := NewCortexInjector(bin, stateB)
	if err != nil {
		t.Fatalf("inj B: %v", err)
	}

	if err := injA.Record(context.Background(), 0, workdir, SessionResult{
		SessionID: "01-A", FilesChanged: []string{"a.go"},
	}); err != nil {
		t.Fatalf("Record A: %v", err)
	}
	if err := injB.Record(context.Background(), 0, workdir, SessionResult{
		SessionID: "01-B", FilesChanged: []string{"b.go"},
	}); err != nil {
		t.Fatalf("Record B: %v", err)
	}

	preA, err := injA.Preamble(context.Background(), 1, workdir)
	if err != nil {
		t.Fatalf("Preamble A: %v", err)
	}
	preB, err := injB.Preamble(context.Background(), 1, workdir)
	if err != nil {
		t.Fatalf("Preamble B: %v", err)
	}

	if !strings.Contains(preA, "SENTINEL_A") {
		t.Errorf("inj A preamble missing its own sentinel; got=%q", preA)
	}
	if strings.Contains(preA, "SENTINEL_B") {
		t.Errorf("inj A preamble leaked B's sentinel — state NOT isolated; got=%q", preA)
	}
	if !strings.Contains(preB, "SENTINEL_B") {
		t.Errorf("inj B preamble missing its own sentinel; got=%q", preB)
	}
	if strings.Contains(preB, "SENTINEL_A") {
		t.Errorf("inj B preamble leaked A's sentinel — state NOT isolated; got=%q", preB)
	}
}

// TestCortexInjector_ErrorPropagation: if the cortex binary fails, the
// error MUST surface (not be swallowed) so the runner can fail fast.
func TestCortexInjector_ErrorPropagation(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, errorFakeCortexScript)
	inj, err := NewCortexInjector(bin, t.TempDir())
	if err != nil {
		t.Fatalf("NewCortexInjector: %v", err)
	}

	workdir := t.TempDir()
	path := filepath.Join(workdir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = inj.Record(context.Background(), 0, workdir, SessionResult{
		SessionID: "01", FilesChanged: []string{"x.go"},
	})
	if err == nil {
		t.Fatal("Record returned nil with failing cortex; want error")
	}
	if !strings.Contains(err.Error(), "fake error") {
		t.Errorf("err = %v, want stderr ('fake error') in chain", err)
	}
}

// TestCortexInjector_VerboseLogging confirms verbose mode emits both the
// search query/result and the capture summary. This is the observability
// surface that makes "what did Cortex actually inject?" answerable.
func TestCortexInjector_VerboseLogging(t *testing.T) {
	binDir := t.TempDir()
	bin := installFakeCortex(t, binDir, fakeCortexScript)
	stateDir := t.TempDir()

	workdir := t.TempDir()
	rel := "h.go"
	if err := os.WriteFile(filepath.Join(workdir, rel), []byte("package h // V_SENTINEL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logf := func(format string, args ...any) {
		fmt.Fprintf(&buf, format, args...)
	}
	inj, err := NewCortexInjector(bin, stateDir, WithVerbose(logf))
	if err != nil {
		t.Fatalf("NewCortexInjector: %v", err)
	}

	if err := inj.Record(context.Background(), 0, workdir, SessionResult{
		SessionID: "01", FilesChanged: []string{rel},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := inj.Preamble(context.Background(), 1, workdir); err != nil {
		t.Fatalf("Preamble: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"recording 1 Go file",
		"search query=",
		"raw result",
		"V_SENTINEL", // raw result echoes captured content
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose log missing %q; full log=%q", want, got)
		}
	}
}

// TestGoFiles_FiltersAndDedupes locks in the helper's contract: only .go
// files, dedup, sorted output.
func TestGoFiles_FiltersAndDedupes(t *testing.T) {
	in := []string{
		"b.go",
		"a.go",
		"README.md",
		"a.go", // dupe
		"c.txt",
		"sub/d.go",
	}
	got := goFiles(in)
	want := []string{"a.go", "b.go", "sub/d.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

// TestRunWithInjector_PreambleReachesHarness is the runner-level
// integration: confirms the runner prepends the injector's preamble to
// the prompt that reaches the harness for sessions S2..S5, and leaves
// S1's prompt untouched.
func TestRunWithInjector_PreambleReachesHarness(t *testing.T) {
	ev := newRunnerEvaluator(t)
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			return writeSessionFile(workdir, idx)
		},
	}
	inj := &recordingInjector{
		preamble: "## Conventions established in prior sessions\nINJECTED_SENTINEL\n",
	}

	run, err := ev.RunWithInjector(context.Background(), ConditionCortex, "fake", fh, inj)
	if err != nil {
		t.Fatalf("RunWithInjector: %v", err)
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	if got := len(fh.calls); got != 5 {
		t.Fatalf("harness calls = %d, want 5", got)
	}
	// S1 prompt MUST NOT carry a preamble — there's nothing to mine
	// before S1.
	if strings.Contains(fh.calls[0].prompt, "INJECTED_SENTINEL") {
		t.Errorf("S1 prompt contained preamble; want untouched. prompt=%q", fh.calls[0].prompt)
	}
	// S2..S5 prompts MUST carry the preamble.
	for i := 1; i < 5; i++ {
		if !strings.Contains(fh.calls[i].prompt, "INJECTED_SENTINEL") {
			t.Errorf("S%d prompt missing preamble; got=%q", i+1, fh.calls[i].prompt)
		}
	}
	// Record was called once per session (5 total).
	if got := len(inj.recorded); got != 5 {
		t.Errorf("Record called %d times, want 5", got)
	}
}

// TestRunWithInjector_PreambleErrorAborts: a Preamble error MUST stop
// the run before the harness is invoked (we don't want to spend a
// session on a half-built prompt).
func TestRunWithInjector_PreambleErrorAborts(t *testing.T) {
	ev := newRunnerEvaluator(t)
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			return writeSessionFile(workdir, idx)
		},
	}
	bang := errors.New("preamble exploded")
	inj := &recordingInjector{preambleErrAt: 1, preambleErr: bang}

	run, err := ev.RunWithInjector(context.Background(), ConditionCortex, "fake", fh, inj)
	if err == nil {
		t.Fatal("expected preamble error, got nil")
	}
	if !errors.Is(err, bang) {
		t.Errorf("err = %v, want chain containing %v", err, bang)
	}
	if run == nil {
		t.Fatal("run nil")
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	// S1 ran; S2's preamble exploded; harness MUST NOT have been called
	// for S2.
	if len(fh.calls) != 1 {
		t.Errorf("harness calls = %d, want 1 (only S1 before preamble error)", len(fh.calls))
	}
}

// TestRunWithInjector_RecordErrorAborts: a Record error MUST stop the
// run after that session (nothing else to retrieve once recording is
// broken).
func TestRunWithInjector_RecordErrorAborts(t *testing.T) {
	ev := newRunnerEvaluator(t)
	fh := &fakeHarness{
		onCall: func(_ context.Context, _, workdir string, idx int) error {
			return writeSessionFile(workdir, idx)
		},
	}
	bang := errors.New("record exploded")
	inj := &recordingInjector{recordErrAt: 0, recordErr: bang}

	run, err := ev.RunWithInjector(context.Background(), ConditionCortex, "fake", fh, inj)
	if err == nil {
		t.Fatal("expected record error, got nil")
	}
	if !errors.Is(err, bang) {
		t.Errorf("err = %v, want chain containing %v", err, bang)
	}
	if run == nil {
		t.Fatal("run nil")
	}
	t.Cleanup(func() { _ = run.Cleanup() })

	// S1 harness call happened; S1 record exploded; loop did not move
	// to S2.
	if len(fh.calls) != 1 {
		t.Errorf("harness calls = %d, want 1", len(fh.calls))
	}
}

// recordingInjector is a programmable Injector for runner-level tests:
// every Preamble returns a fixed string (or error at preambleErrAt),
// every Record records its arguments (or errors at recordErrAt).
type recordingInjector struct {
	mu            sync.Mutex
	preamble      string
	preambleErrAt int
	preambleErr   error
	recordErrAt   int
	recordErr     error
	preambles     []int
	recorded      []SessionResult
}

func (r *recordingInjector) Preamble(_ context.Context, idx int, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preambles = append(r.preambles, idx)
	if r.preambleErr != nil && idx == r.preambleErrAt {
		return "", r.preambleErr
	}
	if idx == 0 {
		return "", nil
	}
	return r.preamble, nil
}

func (r *recordingInjector) Record(_ context.Context, idx int, _ string, sr SessionResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorded = append(r.recorded, sr)
	if r.recordErr != nil && idx == r.recordErrAt {
		return r.recordErr
	}
	return nil
}

// readLog reads the fake cortex events.log under stateDir/.cortex/.
func readLog(t *testing.T, stateDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(stateDir, ".cortex", "events.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read events.log: %v", err)
	}
	return string(body)
}

// TestCompareRuns_BaselineCortex confirms the report contains all five
// metrics, both rows, and a non-zero headline lift derived from
// cortex.ShapeSimilarity − baseline.ShapeSimilarity.
func TestCompareRuns_BaselineCortex(t *testing.T) {
	base := &LibraryServiceRun{
		Condition: ConditionBaseline, Model: "qwen2.5",
		Score: LibraryServiceScore{
			ShapeSimilarity: 0.40, NamingAdherence: 0.50,
			SmellDensity: 1.20, TestParity: 0.55, EndToEndPassRate: 0.60,
		},
	}
	cor := &LibraryServiceRun{
		Condition: ConditionCortex, Model: "qwen2.5",
		Score: LibraryServiceScore{
			ShapeSimilarity: 0.85, NamingAdherence: 0.90,
			SmellDensity: 0.45, TestParity: 0.92, EndToEndPassRate: 1.00,
		},
	}

	out := CompareRuns(base, cor, nil)
	for _, want := range []string{
		"Shape similarity",
		"Naming adherence",
		"Smell density",
		"Test parity",
		"End-to-end pass rate",
		"Headline shape-similarity lift",
		"+0.450", // cortex 0.85 - baseline 0.40
		"baseline:",
		"cortex:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- report ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "frontier:") {
		t.Errorf("report contained frontier line when frontier=nil: %q", out)
	}
}

func TestCompareRuns_WithFrontier(t *testing.T) {
	base := &LibraryServiceRun{Condition: ConditionBaseline, Model: "small"}
	cor := &LibraryServiceRun{Condition: ConditionCortex, Model: "small"}
	front := &LibraryServiceRun{
		Condition: ConditionFrontier, Model: "sonnet",
		Score:     LibraryServiceScore{ShapeSimilarity: 0.95},
	}
	out := CompareRuns(base, cor, front)
	if !strings.Contains(out, "frontier:") {
		t.Errorf("report missing frontier row: %s", out)
	}
	if !strings.Contains(out, "Frontier") {
		t.Errorf("report missing Frontier column: %s", out)
	}
}

func TestCompareRuns_NilGuards(t *testing.T) {
	out := CompareRuns(nil, nil, nil)
	if !strings.Contains(out, "required") {
		t.Errorf("CompareRuns(nil,nil,nil) = %q, want guard message", out)
	}
}
