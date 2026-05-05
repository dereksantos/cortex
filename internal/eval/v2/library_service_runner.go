package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// runSessions copies the seed into a fresh workdir, drives all session
// prompts via h sequentially (consulting inj before/after each), and
// Scores the final state. See library_service.go for the public
// entrypoints.
//
// Plan 03 added the injector seam: before each session, inj.Preamble is
// prepended to the session prompt; after each session, inj.Record is
// invoked with the SessionResult so Cortex (or whatever inj wraps) can
// learn from what was just produced.
func (e *LibraryServiceEvaluator) runSessions(ctx context.Context, cond LibraryServiceCondition, model string, h Harness, inj Injector) (*LibraryServiceRun, error) {
	if h == nil {
		return nil, errors.New("harness is nil")
	}
	if inj == nil {
		inj = NoOpInjector{}
	}
	if e.seedProject == "" {
		return nil, errors.New("evaluator missing seedProject")
	}
	if e.specDir == "" {
		return nil, errors.New("evaluator missing specDir")
	}

	prompts, err := discoverSessionPrompts(filepath.Join(e.specDir, "sessions"))
	if err != nil {
		return nil, fmt.Errorf("discover sessions: %w", err)
	}
	if len(prompts) == 0 {
		return nil, fmt.Errorf("no session prompts under %s", filepath.Join(e.specDir, "sessions"))
	}

	workdir, err := setupLibraryWorkdir(e.seedProject, e.specDir, cond)
	if err != nil {
		return nil, fmt.Errorf("setup workdir: %w", err)
	}

	run := &LibraryServiceRun{
		Condition: cond,
		Model:     model,
		WorkDir:   workdir,
	}

	for idx, p := range prompts {
		if err := ctx.Err(); err != nil {
			return run, err
		}

		preamble, perr := inj.Preamble(ctx, idx, workdir)
		if perr != nil {
			return run, fmt.Errorf("session %s: preamble: %w", p.id, perr)
		}
		wrapped := p
		if preamble != "" {
			wrapped.body = preamble + "\n" + p.body
			if e.verbose {
				fmt.Printf("S%s: prepended preamble (%d bytes)\n--- preamble ---\n%s--- /preamble ---\n",
					p.id, len(preamble), preamble)
			}
		}

		sr, runErr := e.runOneSession(ctx, h, workdir, wrapped)
		// Always record what we managed to capture before the error so
		// callers can see how far the run got.
		if sr.SessionID != "" {
			run.SessionLog = append(run.SessionLog, sr)
		}
		if runErr != nil {
			return run, fmt.Errorf("session %s: %w", p.id, runErr)
		}

		if rerr := inj.Record(ctx, idx, workdir, sr); rerr != nil {
			return run, fmt.Errorf("session %s: record: %w", p.id, rerr)
		}
	}

	score, err := e.Score(ctx, workdir)
	if err != nil {
		return run, fmt.Errorf("score: %w", err)
	}
	run.Score = score
	return run, nil
}

// runOneSession is the per-session block: invoke harness, snapshot what
// changed, run build + tests, commit. Hard errors from the harness propagate;
// build/test failures are recorded into SessionResult and the loop continues.
func (e *LibraryServiceEvaluator) runOneSession(ctx context.Context, h Harness, workdir string, p sessionPrompt) (SessionResult, error) {
	sr := SessionResult{SessionID: p.id}

	if e.verbose {
		fmt.Printf("S%s: started\n", p.id)
	}

	start := time.Now()
	err := h.RunSession(ctx, p.body, workdir)
	sr.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		return sr, err
	}

	files, ferr := stagedChangedFiles(workdir)
	if ferr != nil {
		return sr, fmt.Errorf("stage changes: %w", ferr)
	}
	sr.FilesChanged = files

	sr.BuildOK = goBuildOK(ctx, workdir)
	sr.TestsOK = goTestsOK(ctx, workdir)

	if err := commitWorkdir(workdir, "session "+p.id); err != nil {
		return sr, fmt.Errorf("commit: %w", err)
	}

	if e.verbose {
		fmt.Printf("S%s: %dms, %d files changed, build=%s tests=%s\n",
			p.id, sr.DurationMs, len(sr.FilesChanged),
			okString(sr.BuildOK), okString(sr.TestsOK))
	}
	return sr, nil
}

func okString(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

// sessionPrompt is a single session file under specDir/sessions/.
type sessionPrompt struct {
	id   string // e.g. "01-scaffold-and-books"
	body string
}

// discoverSessionPrompts returns prompts in lexical (i.e. NN-) order.
func discoverSessionPrompts(dir string) ([]sessionPrompt, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []sessionPrompt
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		// Filenames must lead with a 2-digit index (01-, 02-, …) so we
		// don't pick up README.md or similar accidentally.
		if len(name) < 3 || !isASCIIDigit(name[0]) || !isASCIIDigit(name[1]) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, sessionPrompt{
			id:   strings.TrimSuffix(name, ".md"),
			body: string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// setupLibraryWorkdir copies seedDir into a fresh tempdir, drops in
// system-spec.md from specDir so the seed README's reference resolves, and
// `git init`s the result with a single "seed" commit so per-session diffs
// are trivial.
//
// The spec copy is part of workdir setup (not a separate step) because the
// seed is meaningless without it — the model would look for the spec the
// README mentions, fail to find it, and burn a model run guessing.
func setupLibraryWorkdir(seedDir, specDir string, cond LibraryServiceCondition) (string, error) {
	if _, err := os.Stat(seedDir); err != nil {
		return "", fmt.Errorf("seed dir: %w", err)
	}
	specPath := filepath.Join(specDir, "system-spec.md")
	if _, err := os.Stat(specPath); err != nil {
		return "", fmt.Errorf("system-spec.md: %w", err)
	}
	ts := time.Now().UTC().Format("20060102T150405")
	pattern := fmt.Sprintf("cortex-libsvc-%s-%s-*", cond, ts)
	workdir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("mkdtemp: %w", err)
	}
	if err := copyDir(seedDir, workdir); err != nil {
		_ = os.RemoveAll(workdir)
		return "", fmt.Errorf("copy seed: %w", err)
	}
	if err := copyFile(specPath, filepath.Join(workdir, "system-spec.md"), 0o644); err != nil {
		_ = os.RemoveAll(workdir)
		return "", fmt.Errorf("copy system-spec.md: %w", err)
	}
	if err := initGitRepo(workdir); err != nil {
		_ = os.RemoveAll(workdir)
		return "", fmt.Errorf("git init: %w", err)
	}
	return workdir, nil
}

// initGitRepo runs init/add/commit in workdir. We pass user.email/name via
// `-c` so the runner doesn't depend on the operator's global git identity.
func initGitRepo(workdir string) error {
	steps := [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{
			"-c", "user.email=eval@cortex.local",
			"-c", "user.name=cortex-eval",
			"-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", "seed",
		},
	}
	for _, args := range steps {
		out, err := runGit(workdir, args...)
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(out))
		}
	}
	return nil
}

// stagedChangedFiles stages every working-tree change (including new files)
// and returns the staged file list. Stage-then-diff catches untracked files
// — `git diff --name-only HEAD` on its own would miss them.
func stagedChangedFiles(workdir string) ([]string, error) {
	if out, err := runGit(workdir, "add", "-A"); err != nil {
		return nil, fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(out))
	}
	out, err := runGit(workdir, "diff", "--cached", "--name-only")
	if err != nil {
		return nil, fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(out))
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// commitWorkdir wraps a `git commit --allow-empty` so sessions that produced
// no edits still leave a marker on the history.
func commitWorkdir(workdir, msg string) error {
	args := []string{
		"-c", "user.email=eval@cortex.local",
		"-c", "user.name=cortex-eval",
		"-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", msg,
	}
	out, err := runGit(workdir, args...)
	if err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func runGit(workdir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// goBuildOK / goTestsOK return whether the workdir compiles / tests pass.
// Output is discarded — the runner records boolean outcomes per the rubric;
// failures show up as soft signals in SessionResult.
func goBuildOK(ctx context.Context, workdir string) bool {
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = workdir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func goTestsOK(ctx context.Context, workdir string) bool {
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = workdir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// copyDir replicates src into dst, preserving file mode bits. Skips any
// pre-existing .git directory in src so the runner controls the seed history.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		// Skip symlinks for simplicity — the seed is plain files.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ClaudeCLIHarness drives a session via the `claude` CLI. It mirrors the
// invocation pattern in pkg/llm.ClaudeCLI but runs against the live workdir
// (instead of pkg/llm's hook-isolated workspace) so the agent can read the
// repo it's editing.
type ClaudeCLIHarness struct {
	binary string
	model  string
}

// NewClaudeCLIHarness resolves the claude binary (PATH lookup if binary is
// empty) and verifies it exists. A missing binary is a hard error — the
// runner cannot proceed without it.
func NewClaudeCLIHarness(binary, model string) (*ClaudeCLIHarness, error) {
	if binary == "" {
		path, err := exec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("claude binary not found in PATH")
		}
		binary = path
	}
	if _, err := os.Stat(binary); err != nil {
		return nil, fmt.Errorf("claude binary not found: %s: %w", binary, err)
	}
	return &ClaudeCLIHarness{binary: binary, model: model}, nil
}

// RunSession invokes `claude -p <prompt>` with cwd=workdir. Sessions are
// expected to take 5–15 minutes for small models; we honor ctx for
// cancellation but do not impose our own timeout. On cancel, the subprocess
// group gets SIGTERM with a 2s grace period before SIGKILL.
func (h *ClaudeCLIHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	// --bare strips hooks, auto-memory, plugin sync, CLAUDE.md auto-discovery,
	// and other per-machine state. Critical for eval validity: without it the
	// user's installed Cortex hooks fire on every session, contaminating both
	// conditions with passive Cortex capture and making "baseline" not really
	// a baseline. With --bare, the cortex condition's lift over baseline is
	// strictly attributable to the explicit preamble injected by CortexInjector.
	//
	// HOWEVER: --bare disables OAuth/keychain auth — it requires
	// ANTHROPIC_API_KEY (or apiKeyHelper via --settings) to authenticate.
	// On Max-subscription auth (no API key), --bare causes immediate exit 1.
	// We auto-detect: --bare is included only when ANTHROPIC_API_KEY is in
	// the env. Operators who want the cleanest comparison should provision
	// API credits; operators on Max accept the hook-active methodology.
	//
	// --permission-mode=bypassPermissions is required: in -p (print) mode
	// without it, Edit/Write/Bash tool calls get auto-denied because there's
	// no human to confirm them. The eval workdir is a fresh tempdir copy of
	// the seed, so bypassing permissions is safe — the model can only
	// affect the workdir.
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--permission-mode", "bypassPermissions",
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		args = append(args, "--bare")
	}
	if h.model != "" {
		args = append(args, "--model", h.model)
	}

	cmd := exec.Command(h.binary, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-waitErr:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-waitErr
			}
		}
		return ctx.Err()
	case err := <-waitErr:
		if err != nil {
			return fmt.Errorf("claude exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}
