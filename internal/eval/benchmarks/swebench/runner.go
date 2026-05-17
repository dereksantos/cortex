package swebench

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
)

// runnerPayload is what SWEBench.Load stores in benchmarks.Instance.Payload.
// Carries the rich Instance plus the strategy selected for this row, so
// SWEBench.Run can run the right (instance, strategy) pair without
// re-parsing flags.
type runnerPayload struct {
	Inst     Instance
	Strategy string
}

// defaultMaxRetries is the REPL auto-retry budget used when --max-retries
// isn't set on the CLI. Picked so a typical Verified instance has at
// least 2–3 chances to converge on a passing patch given verifier
// feedback, while keeping cost bounded (each retry runs both a fresh
// agent attempt AND a docker verifier pass).
const defaultMaxRetries = 3

// swebenchSystemPrompt is the system prompt the REPL uses for
// SWE-bench attempts. Differences vs the REPL's default seed
// (cmd/cortex/commands/repl.go defaultREPLSystemPrompt):
//
//   - Language/repo-neutral: SWE-bench Verified instances span django,
//     sympy, astropy, scikit-learn, etc. — Python, not Go. The
//     default's "You are a Go programmer" framing actively misleads.
//   - Mentions list_dir + cortex_search: the OpenRouter (non-Ollama)
//     path enables a 5-tool registry, but the default seed only
//     mentions 3, so the agent under-uses the explorer tools on
//     unfamiliar codebases.
//   - Explicitly anti-skip: the prior probe's agent ran 5 tool calls
//     reading code, then exited without write_file. The default's
//     "respond with a short summary and NO further tool calls" line
//     encouraged that. SWE-bench instead emphasizes "produce a source
//     edit, then wait for verifier feedback."
//   - Verifier-loop awareness: tells the agent it'll see the test
//     failure as retry context on subsequent attempts.
const swebenchSystemPrompt = `You are a software engineer fixing a bug in an open-source codebase.
You are working inside a workdir that contains a clone of the repository.

Available tools:
  - list_dir(path): list the contents of a directory
  - read_file(path): read a file
  - write_file(path, content): create or replace a file
  - run_shell(command, args): run shell commands available in your sandbox
  - cortex_search(query): semantic search over project context (may be empty for new repos)

Workflow:
  1. Use list_dir + read_file to locate the source file(s) implicated by
     the bug report.
  2. Use write_file to apply your fix. Edit source code only — do NOT
     modify test files or fixtures.
  3. A test verifier runs after each of your attempts. If it fails, you
     will see the failing test output as 'PREVIOUS ATTEMPT FAILED' in
     your next turn — use it to refine the fix.

Rules:
  - Paths are relative to the workdir.
  - Never write under .git or .cortex.
  - Make the smallest change that makes the failing tests pass.
  - You MUST produce at least one write_file call per attempt — reading
    alone is not progress. If you cannot find the relevant file, list_dir
    deeper and search broader before giving up.
`

// buildAgentPrompt returns the prompt the REPL feeds the agent. It
// pairs the upstream problem_statement (always the bulk of the
// signal) with the explicit list of FAIL_TO_PASS test names the
// verifier will check. Without the test list the agent has to infer
// what "good" looks like from the issue description alone — and the
// prior single-shot baseline showed it can't, even on Sonnet-4.5.
//
// The test names are NOT coaching (eval-principles #2 — coaching is
// telling the model HOW to use tools; this is telling the model WHAT
// success looks like, which is the same information the canonical
// scoring uses). SWE-Agent and Aider's harnesses do the equivalent.
func buildAgentPrompt(inst Instance) string {
	if len(inst.FailToPass) == 0 {
		return inst.ProblemStatement
	}
	var b strings.Builder
	b.WriteString(inst.ProblemStatement)
	b.WriteString("\n\n---\n\nYour patch must make these tests pass (they currently fail):\n")
	for _, t := range inst.FailToPass {
		b.WriteString("  - ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	b.WriteString("\nThe verifier runs these tests after each attempt and reports the result back. Do not modify the test files themselves; fix the source code under test.")
	return b.String()
}

// buildVerifierCommand returns the shell command the REPL runs after
// each agent attempt to decide pass/fail. The command:
//
//  1. Snapshots the agent's current diff from repoDir.
//  2. Pre-flights the diff: if empty, exits 1 with a clear message
//     ("NO_PATCH: agent did not edit any files") so the agent sees an
//     actionable error rather than git's terse "No valid patches" on
//     the next retry's PREVIOUS ATTEMPT FAILED context.
//  3. Spins up the canonical SWE-bench scoring image for the instance.
//  4. Writes the F2P test list into the container via stdin (avoids
//     blowing up the shell command line for instances with hundreds
//     of F2P entries; the prior version's command line was ~50 KB).
//  5. Inside the image, applies the agent's diff and runs pytest with
//     output truncated to the last 4 KB so the retry context fits in
//     the model's input cap.
//
// Per-attempt docker overhead is ~15–30s — that's the cost of using
// the same scoring stack as final evaluation, in exchange for
// guaranteed parity between "the verifier says pass" and "final
// scoring says pass."
//
// Image id mirrors score.go's derivation: <imagePrefix><repo with /
// → __>:v<version>.
func buildVerifierCommand(inst Instance, imagePrefix, repoDir string, timeout time.Duration) string {
	image := imageIDFor(inst, imagePrefix)
	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 60 {
		timeoutSec = 60
	}
	tmpPatch := filepath.Join(repoDir, ".cortex-attempt.patch")
	tmpTests := filepath.Join(repoDir, ".cortex-f2p-tests.txt")

	// Tests list as one-per-line, mounted into the container at
	// /tmp/cortex-f2p-tests.txt — keeps the shell command line short.
	testsList := strings.Join(inst.FailToPass, "\n")
	if len(inst.FailToPass) == 0 {
		// No F2P → final scoring will catch any real failure; treat
		// verifier as trivially passing once a patch exists.
		testsList = ""
	}

	// Inner script that runs inside the docker container. Note the
	// `tail -c 4096`: pytest output for django can be megabytes;
	// retry context goes back into the agent prompt so we keep it
	// bounded.
	inner := "set -e; cd /testbed && " +
		"git apply --whitespace=nowarn /tmp/cortex-attempt.patch 2>&1 && " +
		"if [ -s /tmp/cortex-f2p-tests.txt ]; then " +
		"  xargs -a /tmp/cortex-f2p-tests.txt python -m pytest --no-header -q 2>&1 | tail -c 4096; " +
		"else echo 'no F2P tests configured; verifier trivially passes'; fi"

	// Outer script: snapshot the diff, write tests list, run docker.
	// We avoid `set -e` on the OUTER script because we want to keep
	// running past the NO_PATCH branch and emit the marker.
	return fmt.Sprintf(
		"cd %s && git add -A && git diff --no-color HEAD > %s && "+
			"if [ ! -s %s ]; then "+
			"  echo 'NO_PATCH: agent did not edit any files. You MUST call write_file to modify source code; reading alone does not fix the bug.'; "+
			"  exit 1; "+
			"fi && "+
			"printf '%%s' %s > %s && "+
			"docker run --rm --network=none --stop-timeout=%d "+
			"-v %s:/tmp/cortex-attempt.patch:ro "+
			"-v %s:/tmp/cortex-f2p-tests.txt:ro "+
			"%s bash -lc %s",
		shellEscape(repoDir),
		shellEscape(tmpPatch),
		shellEscape(tmpPatch),
		shellEscape(testsList),
		shellEscape(tmpTests),
		timeoutSec,
		shellEscape(tmpPatch),
		shellEscape(tmpTests),
		shellEscape(image),
		shellEscape(inner),
	)
}

// shellEscape wraps s in single-quotes, escaping any embedded single
// quotes via the standard '\'' trick. Suitable for `sh -c "<...>"`
// substitution into the verifier command above.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// imageIDFor mirrors score.go's logic for deriving the canonical
// SWE-bench scoring image id from an instance: <prefix><repo with /
// → __>:v<version>. Lives here so the verifier and the final scorer
// can both reference the same image without circular imports.
func imageIDFor(inst Instance, imagePrefix string) string {
	if imagePrefix == "" {
		imagePrefix = "swebench/sweb.eval.x86_64."
	}
	repoSlug := strings.ReplaceAll(inst.Repo, "/", "__")
	return fmt.Sprintf("%s%s:v%s", imagePrefix, repoSlug, inst.Version)
}

// runInstance executes one (instance, strategy) pair end-to-end:
// clone → harness run → diff → docker score → CellResult.
//
// Returns the CellResult ready for the dispatcher's PersistCell call.
// runErr (returned non-nil) means the instance was not scoreable
// (clone failure, harness setup failure); the CellResult will still
// be populated with whatever was captured so the failure lands in
// SQLite/JSONL.
func runInstance(ctx context.Context, p runnerPayload, cfg SWEBenchConfig, env benchInfo) (*evalv2.CellResult, error) {
	inst := p.Inst
	strategy := p.Strategy

	workdir := env.Workdir
	repoDir := filepath.Join(workdir, "repo")
	if err := cloneRepoAt(ctx, inst.Repo, inst.BaseCommit, repoDir, cfg.GitCacheDir); err != nil {
		return failedCell(inst, strategy, cfg, env, "clone: "+err.Error()), nil
	}

	binary, err := benchmarks.ResolveCortexBinary()
	if err != nil {
		return failedCell(inst, strategy, cfg, env, "resolve cortex binary: "+err.Error()), nil
	}

	// Build the verifier shell command that the REPL runs after each
	// agent attempt. Mirrors RunSWEBenchTests's docker invocation in
	// score.go — same image, same patch-then-pytest sequence — but
	// returns exit 0 iff every F2P test passes, so the REPL can use
	// it as a verify-and-retry signal. Agent gets pytest output as
	// the retry context, which is the principled equivalent of
	// SWE-Agent's "test failure → next turn" loop.
	verifierCmd := buildVerifierCommand(inst, cfg.DockerImagePrefix, repoDir, cfg.InstanceTimeout)

	maxRetries := cfg.MaxRetries
	if maxRetries < 1 {
		maxRetries = defaultMaxRetries
	}

	// Augment the agent's prompt with the F2P test names so the
	// agent knows which tests must pass. Without this, the agent has
	// to infer targets from the problem statement alone, which is
	// what kept the prior single-shot baseline at 0/3 even on Sonnet.
	prompt := buildAgentPrompt(inst)

	// Write the SWE-bench-flavored system prompt to a temp file so we
	// can pass it via the REPL's --system-prompt flag. The REPL's
	// auto-seeded default declares the agent a "Go programmer" using
	// `go build` — actively misleading on a Django repo, and a major
	// reason the prior probe got zero source edits in 5 agent turns.
	sysPromptPath := filepath.Join(workdir, "swebench-system-prompt.md")
	if err := os.WriteFile(sysPromptPath, []byte(swebenchSystemPrompt), 0o644); err != nil {
		return failedCell(inst, strategy, cfg, env, "write system prompt: "+err.Error()), nil
	}

	start := time.Now()
	// Drive the REPL (the unified agent surface, same one humans use
	// and that GoL eval drives in-process) via headless flags. The
	// REPL's verify-and-retry loop runs the verifier above after each
	// attempt; on fail it feeds the verifier output back into the
	// agent's next prompt as retry context. Up to maxRetries
	// attempts, then the loop exits with the last verify result.
	headlessOut, runErr := benchmarks.RunREPLHeadless(ctx, binary, benchmarks.REPLHeadlessOpts{
		Workdir:      repoDir,
		Model:        cfg.Model,
		Prompt:       prompt,
		Verifier:     verifierCmd,
		MaxRetries:   maxRetries,
		SystemPrompt: sysPromptPath,
	})
	elapsed := time.Since(start).Milliseconds()
	if runErr != nil && env.Verbose {
		fmt.Fprintf(os.Stderr, "[swebench %s] cortex repl headless err: %v\n", inst.InstanceID, runErr)
	}
	// A subprocess failure still produces a patch attempt if the agent
	// got that far before crashing; if not, extractPatch below writes an
	// empty file and the scorer marks the instance failed.
	//
	// We don't have token/cost accounting from the headless REPL
	// summary today (JSON shape is minimal); use zero placeholders.
	// The session.jsonl at headlessOut.SessionPath has the per-attempt
	// detail for downstream analysis.
	out := &benchmarks.CodeOutput{}
	if headlessOut != nil && env.Verbose {
		fmt.Fprintf(os.Stderr, "[swebench %s] session: %s\n", inst.InstanceID, headlessOut.SessionPath)
	}

	patchPath := filepath.Join(workdir, "cortex.patch")
	if err := extractPatch(repoDir, patchPath); err != nil {
		return failedCell(inst, strategy, cfg, env, "extract patch: "+err.Error()), nil
	}

	result, scoreErr := RunSWEBenchTests(ctx, inst, patchPath, cfg.DockerImagePrefix, cfg.InstanceTimeout)
	if scoreErr != nil {
		return failedCell(inst, strategy, cfg, env, "score: "+scoreErr.Error()), nil
	}

	cell := &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                newRunID(),
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		Benchmark:            "swebench",
		ScenarioID:           "swebench/" + inst.InstanceID,
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderOpenRouter,
		Model:                cfg.Model,
		ContextStrategy:      strategyToContext(strategy),
		CortexVersion:        evalv2.CortexVersion,
		TokensIn:             out.TokensIn,
		TokensOut:            out.TokensOut,
		CostUSD:              out.CostUSD,
		LatencyMs:            elapsed,
		AgentTurnsTotal:      out.Turns,
		TestsPassed:          result.F2PPassed + result.P2PPassed,
		TestsFailed:          result.F2PFailed + result.P2PFailed,
		TaskSuccess:          result.AllPassed,
		TaskSuccessCriterion: evalv2.CriterionTestsPassAll,
		Notes: fmt.Sprintf("F2P=%d/%d P2P=%d/%d image=%s",
			result.F2PPassed, len(inst.FailToPass),
			result.P2PPassed, len(inst.PassToPass),
			result.Image),
	}
	return cell, nil
}

// failedCell builds a CellResult for an instance that didn't reach the
// scoring stage. TaskSuccess=false. Notes captures what went wrong so
// SQLite/JSONL consumers can group failures by cause.
func failedCell(inst Instance, strategy string, cfg SWEBenchConfig, env benchInfo, reason string) *evalv2.CellResult {
	_ = env
	return &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                newRunID(),
		Timestamp:            time.Now().UTC().Format(time.RFC3339Nano),
		Benchmark:            "swebench",
		ScenarioID:           "swebench/" + inst.InstanceID,
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderOpenRouter,
		Model:                cfg.Model,
		ContextStrategy:      strategyToContext(strategy),
		CortexVersion:        evalv2.CortexVersion,
		TaskSuccess:          false,
		TaskSuccessCriterion: evalv2.CriterionTestsPassAll,
		Notes:                "ERROR: " + reason,
	}
}

// strategyToContext maps a swebench strategy ("baseline"|"cortex")
// onto the canonical CellResult.ContextStrategy enum.
func strategyToContext(s string) string {
	switch s {
	case "baseline":
		return evalv2.StrategyBaseline
	default:
		return evalv2.StrategyCortex
	}
}

// cloneRepoAt does `git clone https://github.com/<repo>.git <dest>` then
// `git -C <dest> checkout <baseCommit>`. When gitCacheDir is non-empty,
// uses `--reference` to share git objects across instances of the same
// repo (huge wall-time win for django/sympy).
func cloneRepoAt(ctx context.Context, repo, baseCommit, dest, gitCacheDir string) error {
	url := "https://github.com/" + repo + ".git"
	args := []string{"clone", "--quiet"}
	if gitCacheDir != "" {
		mirror, err := ensureMirror(ctx, repo, gitCacheDir)
		if err == nil {
			args = append(args, "--reference-if-able", mirror)
		}
	}
	args = append(args, url, dest)
	if err := runGitNoOutput(ctx, "", args...); err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	if baseCommit != "" {
		if err := runGitNoOutput(ctx, dest, "checkout", "--quiet", baseCommit); err != nil {
			return fmt.Errorf("git checkout %s: %w", baseCommit, err)
		}
	}
	return nil
}

// ensureMirror keeps a bare mirror at <gitCacheDir>/<repo>.git for use
// with `git clone --reference-if-able`. Errors are surfaced so callers
// can fall back to a non-cached clone; the mirror is an optimization,
// not a hard requirement.
func ensureMirror(ctx context.Context, repo, gitCacheDir string) (string, error) {
	mirror := filepath.Join(gitCacheDir, repo+".git")
	if _, err := os.Stat(mirror); err == nil {
		// Best-effort fetch; ignore error so a stale mirror still works.
		_ = runGitNoOutput(ctx, mirror, "fetch", "--quiet", "--prune")
		return mirror, nil
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o755); err != nil {
		return "", err
	}
	url := "https://github.com/" + repo + ".git"
	if err := runGitNoOutput(ctx, "", "clone", "--mirror", "--quiet", url, mirror); err != nil {
		return "", err
	}
	return mirror, nil
}

// extractPatch runs `git -C repoDir diff --no-color` and writes the
// output to patchPath. An empty diff is valid (the agent may not have
// produced any changes) — the empty file lands in patchPath so the
// scorer can still mount it.
func extractPatch(repoDir, patchPath string) error {
	cmd := exec.Command("git", "diff", "--no-color")
	cmd.Dir = repoDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git diff: %w", err)
	}
	return os.WriteFile(patchPath, buf.Bytes(), 0o644)
}

// runGitNoOutput runs git with args in dir, capturing stderr only when
// the command fails. Quiet by default so per-instance logs don't drown
// in clone progress.
func runGitNoOutput(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// newRunID returns a short, sortable, uniquely-suffixed run id.
func newRunID() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	var b [4]byte
	_, _ = rand.Read(b[:])
	return ts + "-" + hex.EncodeToString(b[:])
}

// benchInfo is the small slice of benchmarks.Env this package needs.
// Defined locally to keep runInstance signature free of the heavy
// Persister field we don't touch.
type benchInfo struct {
	Workdir string
	Verbose bool
}
