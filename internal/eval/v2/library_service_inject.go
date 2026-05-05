package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Injector wraps Cortex (or no-op) interactions around the per-session
// loop for the library-service eval.
//
// The runner stays Cortex-ignorant: it calls Preamble before each session
// and Record after, and the injector decides whether to inject anything
// or capture anything. Baseline and frontier conditions get NoOpInjector;
// the cortex condition gets CortexInjector.
//
// See plans/03-cortex-injection.md for the contract.
type Injector interface {
	// Preamble returns markdown to prepend to the next session's prompt.
	// For S1 (sessionIdx == 0) the implementation MUST return "" — there
	// is nothing to mine from prior sessions yet.
	Preamble(ctx context.Context, sessionIdx int, workdir string) (string, error)

	// Record captures what the just-completed session produced. No-op for
	// baseline; for the cortex condition this feeds Cortex's pipeline so
	// that the next Preamble call has something to retrieve.
	Record(ctx context.Context, sessionIdx int, workdir string, result SessionResult) error
}

// NoOpInjector is the injector for baseline and frontier conditions.
type NoOpInjector struct{}

// Preamble is a no-op: returns "" and nil for every session index.
func (NoOpInjector) Preamble(_ context.Context, _ int, _ string) (string, error) {
	return "", nil
}

// Record is a no-op: drops the SessionResult on the floor.
func (NoOpInjector) Record(_ context.Context, _ int, _ string, _ SessionResult) error {
	return nil
}

// preambleQuery is the search query used to retrieve "what conventions
// are in use in this codebase" before sessions S2..S5.
//
// This is a hand-tuned starting hypothesis — not a fixed contract. If the
// retrieval results are weak, this is the first place to iterate. The
// query intentionally names the concrete conventions a Go HTTP service
// will need to be cohesive across resources (handlers, error handling,
// response shape, validation, tests) so the embeddings landing zone is
// closer to what S1 actually wrote than a vague "what was done last".
//
// Tier-2 dynamic mining (project_direction_small_model_amplifier.md)
// will eventually replace this with something that can extract per-file
// patterns. Until that exists, we use Cortex as-is.
const preambleQuery = "what conventions are in use in this Go HTTP service for handlers, error handling, response shape, validation, tests"

// preambleHeader is the markdown wrapper around retrieved Cortex output.
// The "Match them in your implementation" line is what makes this an
// instruction (not just background context) for the model.
const preambleHeader = "## Conventions established in prior sessions\n\nThe following patterns are in use in this codebase. Match them in your implementation:\n\n"

// CortexInjector invokes the `cortex` CLI to capture session output and
// retrieve preamble context for the next session. State is isolated per
// instance via HOME — every cortex CLI invocation is run with HOME and
// cwd set to the injector's stateDir, so cortex's hardcoded ~/.cortex/
// resolves to <stateDir>/.cortex/. Two CortexInjectors with different
// stateDirs do not see each other's events.
//
// Why HOME? Cortex reads ~/.cortex/ via os.UserHomeDir(), which on Unix
// honors $HOME. There is no first-class CORTEX_GLOBAL_DIR env var; HOME
// is the cleanest seam.
//
// What we capture (Record):
//   - One event per .go file changed in the session, type="code", with
//     file path + content as the body. We do NOT diff — Cortex stores
//     the whole file so subsequent search has full context to embed.
//
// What we retrieve (Preamble):
//   - A single `cortex search` against preambleQuery. The result is
//     wrapped in markdown and prepended verbatim to the next session's
//     prompt. ANSI/styling from the CLI output is left as-is; cortex
//     search currently emits plain text.
type CortexInjector struct {
	binary   string                // absolute path to a cortex binary
	stateDir string                // doubles as HOME and cwd; isolates all cortex state
	verbose  bool                  // if true, log preamble + capture summaries
	logf     func(string, ...any)  // verbose logger; defaults to fmt.Printf
	initOnce bool                  // tracks lazy `cortex init`
}

// CortexInjectorOption configures a CortexInjector.
type CortexInjectorOption func(*CortexInjector)

// WithVerbose enables per-call logging of preamble retrieval and capture.
// When enabled the injector logs:
//   - the preamble that was prepended to each session prompt
//   - what was captured (type, file count, byte count)
//   - the search query and the raw result before markdown-wrapping
//
// This is the mandatory observability surface called out in
// plans/03-cortex-injection-prompt.md — when results don't move the first
// question is always "what did Cortex actually inject?"
func WithVerbose(logf func(string, ...any)) CortexInjectorOption {
	return func(c *CortexInjector) {
		c.verbose = true
		if logf == nil {
			c.logf = func(format string, args ...any) {
				fmt.Printf(format, args...)
			}
			return
		}
		c.logf = logf
	}
}

// NewCortexInjector returns an injector that uses binary as the cortex
// CLI and stateDir as the isolated HOME for every cortex invocation.
// stateDir must be an absolute path to a directory the caller can write
// to; it is created if missing. binary must point at an existing file.
//
// The constructor performs `cortex init` lazily on the first Record call
// — many tests never need a populated state dir, and lazy init keeps
// constructor cost predictable.
func NewCortexInjector(binary, stateDir string, opts ...CortexInjectorOption) (*CortexInjector, error) {
	if binary == "" {
		return nil, errors.New("cortex binary path is empty")
	}
	if _, err := os.Stat(binary); err != nil {
		return nil, fmt.Errorf("cortex binary not found at %s: %w", binary, err)
	}
	if stateDir == "" {
		return nil, errors.New("cortex stateDir is empty")
	}
	if !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("cortex stateDir must be absolute: %s", stateDir)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create stateDir: %w", err)
	}
	c := &CortexInjector{
		binary:   binary,
		stateDir: stateDir,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Preamble retrieves prior-session conventions from the isolated cortex
// state dir and wraps them in markdown for prepending to the next
// session's prompt. Returns "" for sessionIdx==0 — Cortex has nothing
// stored yet so a search would be pure noise.
func (c *CortexInjector) Preamble(ctx context.Context, sessionIdx int, workdir string) (string, error) {
	if sessionIdx == 0 {
		if c.verbose {
			c.logf("cortex-inject S%d: skipping preamble (S1 has no prior sessions)\n", sessionIdx+1)
		}
		return "", nil
	}
	if !c.initOnce {
		// No Record has happened, so the state dir is empty. A search
		// would run against an empty store; skip rather than spawn a
		// pointless subprocess.
		if c.verbose {
			c.logf("cortex-inject S%d: skipping preamble (state dir not initialized; nothing recorded yet)\n", sessionIdx+1)
		}
		return "", nil
	}

	out, err := c.runCortex(ctx, nil, "search", "--limit=10", preambleQuery)
	if err != nil {
		return "", fmt.Errorf("cortex search: %w", err)
	}
	raw := strings.TrimSpace(out)
	if c.verbose {
		c.logf("cortex-inject S%d: search query=%q\n", sessionIdx+1, preambleQuery)
		c.logf("cortex-inject S%d: raw result (%d bytes):\n%s\n", sessionIdx+1, len(raw), raw)
	}
	if raw == "" {
		return "", nil
	}
	preamble := preambleHeader + raw + "\n"
	if c.verbose {
		c.logf("cortex-inject S%d: prepended preamble (%d bytes)\n", sessionIdx+1, len(preamble))
	}
	return preamble, nil
}

// Record captures every .go file listed in result.FilesChanged into the
// isolated cortex state dir as a separate event. Non-Go files (README,
// system-spec) are skipped on purpose — the eval scores Go convention
// adherence, so non-Go file noise would dilute retrieval relevance.
//
// Lazy `cortex init` on first call: many test setups never reach Record,
// and we don't want every constructor to pay init cost.
func (c *CortexInjector) Record(ctx context.Context, sessionIdx int, workdir string, result SessionResult) error {
	if !c.initOnce {
		if _, err := c.runCortex(ctx, nil, "init"); err != nil {
			return fmt.Errorf("cortex init: %w", err)
		}
		c.initOnce = true
	}

	files := goFiles(result.FilesChanged)
	if c.verbose {
		c.logf("cortex-inject S%d: recording %d Go file(s) (out of %d changed)\n",
			sessionIdx+1, len(files), len(result.FilesChanged))
	}

	totalBytes := 0
	for _, rel := range files {
		abs := filepath.Join(workdir, rel)
		body, err := os.ReadFile(abs)
		if err != nil {
			// A file in FilesChanged that no longer exists is unexpected
			// but not fatal — record what we can and keep going.
			if c.verbose {
				c.logf("cortex-inject S%d: skip %s: %v\n", sessionIdx+1, rel, err)
			}
			continue
		}
		content := fmt.Sprintf("session=%s file=%s\n%s", result.SessionID, rel, string(body))
		if _, err := c.runCortex(ctx, nil, "capture", "--type=code", "--content="+content); err != nil {
			return fmt.Errorf("cortex capture %s: %w", rel, err)
		}
		totalBytes += len(content)
	}

	// Run synchronous ingest so the next Preamble's search sees the
	// events without depending on a daemon. `cortex ingest` reads from
	// the project-local queue and writes to the global DB; both live in
	// stateDir so this is purely local.
	if _, err := c.runCortex(ctx, nil, "ingest"); err != nil {
		return fmt.Errorf("cortex ingest: %w", err)
	}

	if c.verbose {
		c.logf("cortex-inject S%d: recorded %d files, %d bytes total\n",
			sessionIdx+1, len(files), totalBytes)
	}
	return nil
}

// runCortex executes the cortex CLI with cwd=stateDir and HOME=stateDir.
// stdin is optional; pass nil to skip. Returns combined stdout — stderr
// is captured separately and surfaced in the error.
func (c *CortexInjector) runCortex(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Dir = c.stateDir
	cmd.Env = append(os.Environ(),
		"HOME="+c.stateDir,
		// CORTEX_TEST_HOME mirrors HOME for any future cortex code that
		// prefers an explicit signal over reading $HOME.
		"CORTEX_TEST_HOME="+c.stateDir,
	)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w (stderr: %s)",
			filepath.Base(c.binary), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// resolveCortexBinary returns the cortex binary path for the cortex
// condition. Resolution order:
//  1. $CORTEX_BINARY (absolute path, must exist)
//  2. PATH lookup for `cortex`
//
// Per plans/03-cortex-injection-prompt.md gotcha #1: do not rely on
// $PATH alone — developer machines may have stale installs. The env var
// override lets the eval pin a freshly-built binary.
func resolveCortexBinary() (string, error) {
	if env := os.Getenv("CORTEX_BINARY"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("CORTEX_BINARY must be absolute, got %q", env)
		}
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("CORTEX_BINARY=%s: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("cortex")
	if err != nil {
		return "", fmt.Errorf("cortex binary not found in PATH (set $CORTEX_BINARY to override)")
	}
	return path, nil
}

// newCortexStateDir mints a fresh tempdir for one cortex condition run.
// The directory is created empty; the injector lazily runs `cortex init`
// inside it on the first Record call.
func newCortexStateDir() (string, error) {
	return os.MkdirTemp("", "cortex-libsvc-state-*")
}

// goFiles filters a FilesChanged list down to .go entries, deduplicates,
// and returns them in sorted order so capture is deterministic across
// runs (helpful for tests; meaningless for retrieval).
func goFiles(files []string) []string {
	seen := make(map[string]struct{}, len(files))
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
