package swebench

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Result is the structured outcome of one Docker-based scoring run.
//
// AllPassed is the field benchmarks branch on: it requires every
// FailToPass test to pass AND every PassToPass test to keep passing.
// A test that the agent's patch broke at the wrong layer (so it never
// even shows up in the pytest output) is treated as Failed; silence
// is not a win.
type Result struct {
	F2PPassed int
	F2PFailed int
	P2PPassed int
	P2PFailed int
	AllPassed bool

	Stdout string // truncated; full output is left in the workdir log
	Image  string // docker image actually used (post-prefix resolution)
}

// dockerLookPath is exec.LookPath wrapped behind a test-overridable
// var. Tests that exercise the "docker missing" branch swap this out
// rather than mutating PATH.
var dockerLookPath = exec.LookPath

// preflightDockerInfo is the actual `docker info` invocation, hoisted
// behind a var so tests can stub the daemon-down branch without
// mucking with the host's Docker socket.
var preflightDockerInfo = func(ctx context.Context) ([]byte, error) {
	// short timeout: the only thing this should ever do is fail-fast
	// when the daemon is down. A healthy daemon answers in <100ms.
	tCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(tCtx, "docker", "info").CombinedOutput()
}

// preflightImageInspect is the actual `docker image inspect IMAGE`
// invocation, hoisted behind a var so tests can stub the
// missing-image branch without pre-staging container images.
var preflightImageInspect = func(ctx context.Context, image string) ([]byte, error) {
	tCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(tCtx, "docker", "image", "inspect", image).CombinedOutput()
}

// preflightImagePull is the actual `docker pull IMAGE` invocation,
// kept variable so tests can stub the pull-fail branch deterministically.
// 2-minute timeout matches a normal SWE-bench image pull (~1 GB on a
// fast link).
var preflightImagePull = func(ctx context.Context, image string) ([]byte, error) {
	tCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return exec.CommandContext(tCtx, "docker", "pull", image).CombinedOutput()
}

// preflightImage verifies the scoring image for one representative
// instance can be inspected locally OR pulled from the registry.
// Called by SWEBench.Load after the first instance is known.
//
// Sequence: image inspect → on miss, docker pull → on pull failure,
// surface the registry error with the resolved image id so the
// operator can debug. Successful inspect short-circuits the pull
// (no network round-trip on warm laptops).
//
// We deliberately do NOT pre-pull every instance's image — Verified
// has 500 instances at ~1 GB each. The first-instance check is a
// pragmatic "if THIS pulls, the rest of the family probably will
// too." A future improvement is to pull lazily per (instance, attempt)
// inside RunSWEBenchTests; for now the one-image gate catches the
// "no SWE-bench images at all locally" failure mode that previously
// produced silent F2P=0/N rows.
func preflightImage(ctx context.Context, inst Instance, imagePrefix string) error {
	if imagePrefix == "" {
		imagePrefix = "swebench/sweb.eval.x86_64."
	}
	image := imageNameFor(imagePrefix, inst)
	if _, err := preflightImageInspect(ctx, image); err == nil {
		return nil
	}
	pullOut, pullErr := preflightImagePull(ctx, image)
	if pullErr == nil {
		return nil
	}
	tail := strings.TrimSpace(string(pullOut))
	if len(tail) > 512 {
		tail = "…" + tail[len(tail)-512:]
	}
	return fmt.Errorf(
		"swebench preflight: scoring image %q not local and pull failed.\n"+
			"  hint: SWE-bench Verified images live on Docker Hub under "+
			"swebench/sweb.eval.x86_64.<org>_1776_<repo>-<issue>. "+
			"Check the image exists (`docker search swebench`) and that "+
			"you can `docker pull` it manually before re-running.\n"+
			"  raw: %s\n"+
			"  err: %w",
		image, tail, pullErr,
	)
}

// preflight verifies SWE-bench's runtime pre-requisites and returns a
// clean, actionable error when any are missing. Called from
// SWEBench.Load before any instances are returned, so the operator
// finds out about a host-side problem BEFORE burning model spend on
// a doomed run.
//
// Today the only hard pre-req is a running Docker daemon (scoring
// runs inside the canonical sweb.eval.* images). Image availability
// is checked separately via preflightImage once the first instance
// is known. Without these gates, Docker-down OR image-missing runs
// silently produced "F2P=0/N" rows that looked like real model
// failures rather than infra error — that's how the 2026-05-17
// SWE-bench probes got mis-recorded as zero pass-rate.
func preflight(ctx context.Context) error {
	if _, err := dockerLookPath("docker"); err != nil {
		return fmt.Errorf(
			"swebench preflight: docker not on PATH. Install Docker " +
				"Desktop (macOS) or the docker CLI (Linux): " +
				"https://docs.docker.com/get-docker/",
		)
	}
	out, err := preflightDockerInfo(ctx)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Trim noisy multi-line client banner so the message is one
		// thing the operator can act on.
		if i := strings.Index(msg, "\nerror"); i > 0 {
			msg = msg[i+1:]
		}
		hint := "Cannot connect to the Docker daemon. " +
			"Start Docker Desktop (macOS: `open -a Docker`; Linux: " +
			"`sudo systemctl start docker`) and retry."
		if msg != "" {
			return fmt.Errorf("swebench preflight: %s\n  raw: %s", hint, msg)
		}
		return fmt.Errorf("swebench preflight: %s (docker info: %w)", hint, err)
	}
	return nil
}

// RunSWEBenchTests applies patchPath inside the prebuilt SWE-bench
// Docker image for the instance, runs pytest against FAIL_TO_PASS +
// PASS_TO_PASS, and returns a structured Result.
//
// Behavior:
//   - Docker missing → hard error with a clean "install docker:..." message.
//   - Empty patchPath → still runs the test set against the unmodified
//     base_commit (useful as a baseline-vs-cortex sanity check).
//   - timeout caps both the docker invocation and the inner pytest run.
//
// The function does NOT pull the image; Docker will fetch on demand.
// Callers building a CI pipeline can pre-pull with `docker pull` to
// keep wall-time per instance closer to the canonical scoring window.
func RunSWEBenchTests(ctx context.Context, inst Instance, patchPath, imagePrefix string, timeout time.Duration) (Result, error) {
	if _, err := dockerLookPath("docker"); err != nil {
		return Result{}, fmt.Errorf("docker not on PATH (install docker: https://docs.docker.com/get-docker/ or set --docker-image-prefix to a runtime that is on PATH): %w", err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	image := imageNameFor(imagePrefix, inst)

	tests := append([]string{}, inst.FailToPass...)
	tests = append(tests, inst.PassToPass...)
	if len(tests) == 0 {
		return Result{Image: image, AllPassed: true}, nil
	}

	// The canonical sweb.eval.* images ship with the repo at
	// /testbed and a pre-installed env. We:
	//  1) checkout the base commit (image baseline is usually
	//     already at it, but be defensive)
	//  2) apply patchPath if non-empty
	//  3) run pytest -v <tests>
	scriptParts := []string{
		"set -eu",
		"cd /testbed",
		"git checkout " + shellQuote(inst.BaseCommit) + " >/dev/null 2>&1 || true",
	}
	if patchPath != "" {
		scriptParts = append(scriptParts,
			"git apply --allow-empty /cortex.patch || patch -p1 < /cortex.patch",
		)
	}
	scriptParts = append(scriptParts,
		"python -m pytest -v -rN --no-header "+joinShell(tests),
	)
	script := strings.Join(scriptParts, " && ")

	dockerArgs := []string{
		"run", "--rm",
		"--network=none",
	}
	if patchPath != "" {
		dockerArgs = append(dockerArgs, "-v", patchPath+":/cortex.patch:ro")
	}
	dockerArgs = append(dockerArgs, image, "bash", "-lc", script)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "docker", dockerArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	res := scoreFromOutput(inst, buf.String())
	res.Image = image
	res.Stdout = truncate(buf.String(), 16*1024)
	// runErr is not surfaced unless we got zero parsable output —
	// pytest exits non-zero whenever any test fails, which is normal
	// for a failed-but-scored run.
	if runErr != nil && res.F2PPassed+res.F2PFailed+res.P2PPassed+res.P2PFailed == 0 {
		return res, fmt.Errorf("docker run %s: %w (output: %s)", image, runErr, res.Stdout)
	}
	return res, nil
}

// imageNameFor builds the canonical sweb.eval.* image tag from a
// prefix and instance. Format mirrors the upstream Docker Hub
// layout under hub.docker.com/u/swebench:
//
//	<prefix><org>_1776_<repo>-<issue>:latest
//
// e.g. "swebench/sweb.eval.x86_64.django_1776_django-10097:latest".
//
// The `_1776_` is a fixed separator used by SWE-bench's image
// publisher (corresponds to a specific upstream PR number; treat it
// as an opaque token). The instance_id format
// "<org>__<repo>-<issue>" maps directly: replace the double-underscore
// with `_1776_`. The earlier ":v<version>" tag scheme returned
// "repository does not exist" — Verified images are per-instance,
// not per-(repo, major version), tagged :latest.
func imageNameFor(prefix string, inst Instance) string {
	suffix := strings.Replace(inst.InstanceID, "__", "_1776_", 1)
	return prefix + suffix + ":latest"
}

// pytestLineRE matches the verbose pytest line format used by both the
// SWE-bench canonical script and a plain `pytest -v`. Two captures:
// (1) the full test id, (2) the status word.
var pytestLineRE = regexp.MustCompile(`(?m)^([^\s]+)\s+(PASSED|FAILED|ERROR)\b`)

// parsePytestOutput returns the lists of passed and failed test ids
// observed in raw pytest stdout. ERROR is treated as FAILED.
// Ordering is the on-screen order; duplicates dedupe to first
// occurrence.
func parsePytestOutput(out string) (passed, failed []string) {
	seenPass := map[string]bool{}
	seenFail := map[string]bool{}
	for _, m := range pytestLineRE.FindAllStringSubmatch(out, -1) {
		id, status := m[1], m[2]
		switch status {
		case "PASSED":
			if !seenPass[id] && !seenFail[id] {
				seenPass[id] = true
				passed = append(passed, id)
			}
		case "FAILED", "ERROR":
			if !seenFail[id] {
				seenFail[id] = true
				failed = append(failed, id)
			}
			// If we'd previously marked it passed (rare; flaky test
			// re-run), demote it to failed.
			if seenPass[id] {
				passed = removeStr(passed, id)
				delete(seenPass, id)
			}
		}
	}
	return passed, failed
}

// scoreFromOutput maps raw pytest output onto F2P/P2P counts for one
// instance. Missing tests (the agent's patch made them un-discoverable)
// count as failed.
func scoreFromOutput(inst Instance, out string) Result {
	passed, failed := parsePytestOutput(out)
	passSet := map[string]bool{}
	for _, n := range passed {
		passSet[n] = true
	}
	failSet := map[string]bool{}
	for _, n := range failed {
		failSet[n] = true
	}

	res := Result{}
	for _, name := range inst.FailToPass {
		if passSet[name] {
			res.F2PPassed++
		} else {
			res.F2PFailed++
		}
		_ = failSet // (kept for symmetry; pass takes priority via passSet)
	}
	for _, name := range inst.PassToPass {
		if passSet[name] {
			res.P2PPassed++
		} else {
			res.P2PFailed++
		}
	}
	res.AllPassed = res.F2PFailed == 0 && res.P2PFailed == 0 && (len(inst.FailToPass)+len(inst.PassToPass) > 0)
	return res
}

func removeStr(xs []string, target string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != target {
			out = append(out, x)
		}
	}
	return out
}

// shellQuote single-quotes s for bash -c. Sufficient for the commit
// hashes / file paths SWE-bench feeds us; not a general-purpose
// quoter.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// joinShell space-joins single-quoted test ids so pytest gets one arg
// per name even when names contain `::` or `[` style brackets.
func joinShell(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = shellQuote(x)
	}
	return strings.Join(out, " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
