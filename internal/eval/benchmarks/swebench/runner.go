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

	start := time.Now()
	out, runErr := benchmarks.RunCode(ctx, binary, benchmarks.CodeOpts{
		Workdir:  repoDir,
		Model:    cfg.Model,
		Prompt:   inst.ProblemStatement,
		NoSearch: strategy == "baseline",
	})
	elapsed := time.Since(start).Milliseconds()
	if runErr != nil && env.Verbose {
		fmt.Fprintf(os.Stderr, "[swebench %s] cortex code err: %v\n", inst.InstanceID, runErr)
	}
	// A subprocess failure still produces a patch attempt if the agent
	// got that far before crashing; if not, extractPatch below writes an
	// empty file and the scorer marks the instance failed.
	if out == nil {
		out = &benchmarks.CodeOutput{}
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
