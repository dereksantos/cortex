//go:build !windows

package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// RepoTestSpec describes a build-then-test contract for a workdir-hosted
// repo. Extracted from score_gol_frames so other benchmarks (Repo-Bench,
// future Go-test scenarios) can share the same build pipeline.
//
// SWE-bench scoring runs out-of-loop in Docker and does NOT use this
// type — see internal/eval/benchmarks/swebench/score.go.
type RepoTestSpec struct {
	// BuildCmd is the build invocation (e.g. ["go","build","-o","gol","."]).
	// Optional: empty BuildCmd skips the build step and reports BuildOK=true.
	BuildCmd []string

	// TestCmd is the test invocation (e.g. ["go","test","-v","./..."]).
	// Optional: empty TestCmd skips the test step; AllPassed mirrors BuildOK.
	TestCmd []string

	// ExpectedPass names the tests that must pass. When non-empty, any
	// ExpectedPass name that does not appear in the test output's PASS
	// lines is reported as Failed. When empty, AllPassed reflects the
	// test command's exit code alone.
	ExpectedPass []string

	// Timeout is applied per phase (build, then test). Defaults to
	// 60s if zero.
	Timeout time.Duration

	// Env overrides the default sandbox environment. Empty means use
	// the sandbox env (PATH/HOME/GOPATH/GOCACHE/GOMODCACHE rooted at
	// workdir). Used to extend PATH for non-Go runners.
	Env []string
}

// RepoTestResult is the structured outcome of one RunRepoTests call.
//
// Passed/Failed are test names parsed from the test command's stdout.
// AllPassed is the single source of truth for "this attempt scored
// successfully" and is the field benchmarks should branch on.
type RepoTestResult struct {
	BuildOK   bool
	BuildOut  string // truncated build output on failure
	Passed    []string
	Failed    []string
	AllPassed bool
}

// RunRepoTests runs BuildCmd (if set) then TestCmd (if set) in workdir
// with a sandboxed environment, returning a structured result.
//
// Both phases honor spec.Timeout (per-phase, not total). Build failure
// short-circuits the test phase: callers see BuildOK=false and skip
// further scoring.
//
// Test-name parsing supports the two formats Cortex evals actually
// emit today:
//   - Go test:  "--- PASS: TestX" / "--- FAIL: TestX"
//   - pytest -v: "test_name PASSED" / "test_name FAILED"
//
// Callers needing a different format can post-process by ignoring
// Passed/Failed and reading their own captured output.
func RunRepoTests(ctx context.Context, workdir string, spec RepoTestSpec) (RepoTestResult, error) {
	if !filepath.IsAbs(workdir) {
		return RepoTestResult{}, fmt.Errorf("workdir must be absolute, got %q", workdir)
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	env := spec.Env
	if len(env) == 0 {
		env = sandboxEnv(workdir)
	}

	res := RepoTestResult{BuildOK: true}

	if len(spec.BuildCmd) > 0 {
		buildCtx, cancel := context.WithTimeout(ctx, timeout)
		out, err := runCmd(buildCtx, workdir, env, spec.BuildCmd)
		cancel()
		if err != nil {
			res.BuildOK = false
			res.BuildOut = truncateRepoTestOut(out, 2048)
			return res, nil
		}
	}

	if len(spec.TestCmd) == 0 {
		res.AllPassed = res.BuildOK
		return res, nil
	}

	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, runErr := runCmd(testCtx, workdir, env, spec.TestCmd)

	passed, failed := parseRepoTestNames(out)
	res.Passed = passed
	res.Failed = failed

	if len(spec.ExpectedPass) > 0 {
		// Any expected name not in Passed is a failure even if the
		// test runner did not emit an explicit FAIL line for it.
		passSet := map[string]bool{}
		for _, n := range passed {
			passSet[n] = true
		}
		for _, want := range spec.ExpectedPass {
			if !passSet[want] && !containsRepoTestName(failed, want) {
				res.Failed = append(res.Failed, want)
			}
		}
		// AllPassed: every expected name in Passed AND nothing in Failed.
		allOK := true
		failSet := map[string]bool{}
		for _, n := range res.Failed {
			failSet[n] = true
		}
		for _, want := range spec.ExpectedPass {
			if !passSet[want] || failSet[want] {
				allOK = false
				break
			}
		}
		res.AllPassed = allOK
	} else {
		// No expectations: exit-code-based pass/fail. Failed list still
		// surfaces any explicit FAILs parsed from output.
		res.AllPassed = runErr == nil && len(res.Failed) == 0
	}

	return res, nil
}

// runCmd executes argv in workdir with env, returning combined output.
func runCmd(ctx context.Context, workdir string, env, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// sandboxEnv returns the workdir-rooted env used by Cortex's in-loop
// shell tool. Mirrors score_gol_frames' historical choices so the
// refactor is byte-identical for GoL.
func sandboxEnv(workdir string) []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/local/bin",
		"HOME=" + workdir,
		"GOPATH=" + filepath.Join(workdir, ".gopath"),
		"GOCACHE=" + filepath.Join(workdir, ".gocache"),
		"GOMODCACHE=" + filepath.Join(workdir, ".gomodcache"),
	}
}

var (
	repoTestGoPassRE  = regexp.MustCompile(`(?m)^\s*--- PASS:\s+(\S+)`)
	repoTestGoFailRE  = regexp.MustCompile(`(?m)^\s*--- FAIL:\s+(\S+)`)
	repoTestPyPassRE  = regexp.MustCompile(`(?m)^(\S+)\s+PASSED\b`)
	repoTestPyFailRE  = regexp.MustCompile(`(?m)^(\S+)\s+FAILED\b`)
	repoTestPyErrorRE = regexp.MustCompile(`(?m)^(\S+)\s+ERROR\b`)
)

// parseRepoTestNames extracts pass/fail test names from go-test and
// pytest verbose output. Names are deduped per category.
func parseRepoTestNames(out string) (passed, failed []string) {
	seenPass := map[string]bool{}
	seenFail := map[string]bool{}
	for _, m := range repoTestGoPassRE.FindAllStringSubmatch(out, -1) {
		if !seenPass[m[1]] {
			seenPass[m[1]] = true
			passed = append(passed, m[1])
		}
	}
	for _, m := range repoTestGoFailRE.FindAllStringSubmatch(out, -1) {
		if !seenFail[m[1]] {
			seenFail[m[1]] = true
			failed = append(failed, m[1])
		}
	}
	for _, m := range repoTestPyPassRE.FindAllStringSubmatch(out, -1) {
		if !seenPass[m[1]] {
			seenPass[m[1]] = true
			passed = append(passed, m[1])
		}
	}
	for _, re := range []*regexp.Regexp{repoTestPyFailRE, repoTestPyErrorRE} {
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			if !seenFail[m[1]] {
				seenFail[m[1]] = true
				failed = append(failed, m[1])
			}
		}
	}
	return passed, failed
}

func containsRepoTestName(list []string, needle string) bool {
	for _, s := range list {
		if s == needle {
			return true
		}
	}
	return false
}

func truncateRepoTestOut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
