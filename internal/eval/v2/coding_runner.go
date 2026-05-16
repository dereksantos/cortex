//go:build !windows

package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// CodingRunResult is the per-scenario summary the CLI reports back to
// the operator. Internally each attempt also lands as a CellResult in
// SQLite + JSONL + journal, so this struct is for human-readable
// reporting only.
type CodingRunResult struct {
	Scenario     *CodingScenario
	Attempts     []CodingAttemptResult
	Passed       bool // true if any attempt achieved TaskSuccess
	WinningRunID string
	LoopRoot     string // absolute path of the persistent per-eval root
}

// CodingAttemptResult is one attempt's outcome.
type CodingAttemptResult struct {
	Attempt       int
	RunID         string
	SessionID     string
	Workdir       string
	HarnessResult HarnessResult
	Frames        FrameDiffResult
	Judge         GoLJudgeResult
	TaskSuccess   bool
	CellRunID     string
	Err           error
}

// RunCodingScenario executes a CodingScenario end-to-end. It branches
// on scenario.Mode:
//
//   - single: one attempt, fresh workdir with empty .cortex/
//   - multi-session: up to MaxTries attempts, all sharing a persistent
//     loopRoot/.cortex; failures captured between attempts, idle window
//     between attempts (dreamDrainStub).
//
// harness drives the agent loop. judge (optional) scores qualitative
// correctness; pass nil to skip the judge half of success.
//
// One CellResult lands per attempt. The caller must have constructed
// persister via NewPersister so the journal+SQLite+JSONL fan-out is
// wired correctly.
func RunCodingScenario(ctx context.Context, scenario *CodingScenario, harness *CortexHarness, judge llm.Provider, persister *Persister, verbose bool) (*CodingRunResult, error) {
	if scenario == nil {
		return nil, fmt.Errorf("scenario is nil")
	}
	if harness == nil {
		return nil, fmt.Errorf("harness is nil")
	}
	if persister == nil {
		return nil, fmt.Errorf("persister is nil")
	}

	loopRoot, err := setupCodingLoopRoot(scenario)
	if err != nil {
		return nil, fmt.Errorf("setup loop root: %w", err)
	}
	if verbose {
		fmt.Printf("[coding-runner] loop root: %s\n", loopRoot)
	}

	res := &CodingRunResult{Scenario: scenario, LoopRoot: loopRoot}

	maxTries := scenario.MaxTries
	if scenario.Mode == "single" {
		maxTries = 1
	}

	runIDBase := newLoopRunIDBase()
	for attempt := 1; attempt <= maxTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}

		workdir, err := setupAttemptWorkdir(loopRoot, scenario, attempt)
		if err != nil {
			return res, fmt.Errorf("attempt %d: setup workdir: %w", attempt, err)
		}
		sessionID := fmt.Sprintf("%s-attempt-%d", runIDBase, attempt)
		if verbose {
			fmt.Printf("[coding-runner] attempt %d/%d: workdir=%s session=%s\n", attempt, maxTries, workdir, sessionID)
		}

		ar := runOneCodingAttempt(ctx, scenario, harness, judge, persister, workdir, sessionID, attempt, verbose)
		res.Attempts = append(res.Attempts, ar)
		if ar.Err != nil {
			if verbose {
				fmt.Printf("[coding-runner] attempt %d: error: %v\n", attempt, ar.Err)
			}
			// Errors are recorded on the CellResult; don't abort the
			// loop unless ctx is done.
		}
		if ar.TaskSuccess {
			res.Passed = true
			res.WinningRunID = ar.RunID
			if verbose {
				fmt.Printf("[coding-runner] attempt %d: PASS\n", attempt)
			}
			break
		}
		if verbose {
			fmt.Printf("[coding-runner] attempt %d: FAIL (build=%v frames-passed=%d frames-failed=%d judge=%v)\n",
				attempt, ar.Frames.BuildOK, ar.Frames.Passed, ar.Frames.Failed, ar.Judge.Pass)
		}

		// Multi-session: capture failures and idle-wait so Dream can
		// run (iteration 1: idle wait is a stub; captures are real).
		if scenario.Mode == "multi-session" && attempt < maxTries {
			storeDir := filepath.Join(loopRoot, ".cortex")
			if err := captureAttemptFailures(storeDir, attempt, ar.Frames, ar.Judge); err != nil && verbose {
				fmt.Printf("[coding-runner] capture failures: %v\n", err)
			}
			if err := dreamDrainStub(ctx, storeDir, scenario.DreamIdleSeconds); err != nil {
				return res, fmt.Errorf("dream drain: %w", err)
			}
		}
	}

	return res, nil
}

// runOneCodingAttempt executes one attempt: invoke the harness, run
// frame-diff + judge scoring, build a CellResult, persist it.
func runOneCodingAttempt(ctx context.Context, scenario *CodingScenario, h *CortexHarness, judge llm.Provider, persister *Persister, workdir, sessionID string, attempt int, verbose bool) CodingAttemptResult {
	ar := CodingAttemptResult{
		Attempt:   attempt,
		SessionID: sessionID,
		Workdir:   workdir,
	}
	runID := newCellRunID()
	ar.RunID = runID
	ar.CellRunID = runID

	hr, runErr := h.RunSessionWithResult(ctx, scenario.Prompt, workdir)
	ar.HarnessResult = hr
	loopResult := h.LastLoopResult()

	if runErr != nil {
		ar.Err = runErr
		// Even on harness error, write a CellResult so the failure is
		// captured in the SQLite + journal pipeline.
	}

	// Frame diff: builds and runs the binary, diffs against goldens.
	frames, frameErr := ScoreGoLFrames(ctx, workdir, scenario.FixturesDir, scenario.Generations)
	if frameErr != nil && verbose {
		fmt.Printf("[coding-runner] frame scorer error: %v\n", frameErr)
	}
	ar.Frames = frames

	// Judge: only runs if frames built OK (no binary -> no judge) and
	// a judge provider was supplied.
	if judge != nil && frames.BuildOK && scenario.FreeformInput != "" {
		j, jerr := ScoreGoLJudge(ctx, frames.BinaryPath, scenario.FreeformInput, workdir, scenario.Generations, judge)
		if jerr != nil && verbose {
			fmt.Printf("[coding-runner] judge error: %v\n", jerr)
		}
		ar.Judge = j
	} else if judge == nil {
		// No judge configured -> treat as inert "no opinion": frames
		// alone determine success.
		ar.Judge = GoLJudgeResult{Pass: true, Verdict: "PASS (judge not configured)"}
	} else {
		ar.Judge = GoLJudgeResult{Pass: false, Verdict: "FAIL: build did not produce a binary"}
	}

	ar.TaskSuccess = frames.BuildOK && frames.AllPassed && ar.Judge.Pass

	cell := &CellResult{
		SchemaVersion:         CellResultSchemaVersion,
		RunID:                 runID,
		Timestamp:             time.Now().UTC().Format(time.RFC3339Nano),
		ScenarioID:            scenario.ID,
		SessionID:             sessionID,
		Harness:               HarnessCortex,
		Provider:              ProviderOpenRouter,
		Model:                 hr.ModelEcho,
		ContextStrategy:       StrategyCortex, // cortex_search is always available; even an empty store is the "cortex" strategy
		CortexVersion:         CortexVersion,
		Temperature:           0, // OpenRouter default; harness doesn't override yet
		TokensIn:              hr.TokensIn,
		TokensOut:             hr.TokensOut,
		InjectedContextTokens: loopResult.InjectedContextTokens,
		CostUSD:               hr.CostUSD,
		LatencyMs:             hr.LatencyMs,
		AgentTurnsTotal:       hr.AgentTurnsTotal,
		CorrectionTurns:       loopResult.ShellNonZeroExits,
		TestsPassed:           frames.Passed,
		TestsFailed:           frames.Failed,
		TaskSuccess:           ar.TaskSuccess,
		TaskSuccessCriterion:  CriterionMixed,
		Notes:                 buildCellNotes(loopResult.Reason, ar.Frames, ar.Judge, runErr),
	}
	if err := persister.PersistCell(ctx, cell); err != nil {
		if ar.Err == nil {
			ar.Err = fmt.Errorf("persist: %w", err)
		}
		if verbose {
			fmt.Printf("[coding-runner] persist error: %v\n", err)
		}
	}
	return ar
}

// setupCodingLoopRoot creates a fresh tempdir for the eval loop, with
// a name that includes the scenario id for grep-ability. The
// directory is NOT pre-populated with .cortex — that's lazy-created
// by the first cortex_search call OR by captureAttemptFailures.
func setupCodingLoopRoot(s *CodingScenario) (string, error) {
	pattern := fmt.Sprintf("cortex-coding-%s-*", sanitize(s.ID))
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	return dir, nil
}

// setupAttemptWorkdir creates loopRoot/attempt-N, copies the seed
// into it, and runs git init for clean per-attempt diffs. The
// per-eval .cortex/ stays at loopRoot/.cortex (NOT copied per
// attempt); the harness's cortex_search tool reads from
// <workdir>/.cortex which we set up as a symlink to the shared root.
//
// Why a symlink: the cortex_search tool is constructed with workdir
// as its store root. Without sharing, attempt N+1 would not see
// attempt N's captures. The symlink keeps the contract clean (the
// tool still gets a workdir-relative .cortex) without requiring the
// tool to know about cross-attempt sharing.
func setupAttemptWorkdir(loopRoot string, s *CodingScenario, attempt int) (string, error) {
	workdir := filepath.Join(loopRoot, fmt.Sprintf("attempt-%d", attempt))
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	if err := copyDir(s.SeedDir, workdir); err != nil {
		return "", fmt.Errorf("copy seed: %w", err)
	}

	// Shared .cortex/ via symlink — captureAttemptFailures and the
	// cortex_search tool both target <workdir>/.cortex, but in
	// multi-session mode they all point at loopRoot/.cortex via this
	// symlink so context accumulates.
	sharedStore := filepath.Join(loopRoot, ".cortex")
	if err := os.MkdirAll(sharedStore, 0o755); err != nil {
		return "", fmt.Errorf("mkdir shared store: %w", err)
	}
	storeSymlink := filepath.Join(workdir, ".cortex")
	if err := os.Symlink(sharedStore, storeSymlink); err != nil && !errorsIsExist(err) {
		return "", fmt.Errorf("symlink shared store: %w", err)
	}

	if err := initGitRepo(workdir); err != nil {
		return "", fmt.Errorf("git init: %w", err)
	}
	return workdir, nil
}

// errorsIsExist reports whether err indicates the target already
// exists. Used to ignore EEXIST on the shared-store Symlink call
// when a prior attempt already created it.
func errorsIsExist(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return os.IsExist(pathErr) || pathErr.Err == fs.ErrExist
	}
	return os.IsExist(err)
}

// buildCellNotes summarizes the loop's terminal state, frame results,
// and judge verdict in a compact form suitable for the CellResult
// Notes field. Stays under 256 chars to keep SQLite rows narrow.
func buildCellNotes(reason interface{}, frames FrameDiffResult, judge GoLJudgeResult, runErr error) string {
	var parts []string
	if reason != nil {
		parts = append(parts, fmt.Sprintf("loop:%v", reason))
	}
	if !frames.BuildOK {
		parts = append(parts, "build:fail")
	} else if !frames.AllPassed {
		parts = append(parts, fmt.Sprintf("frames:%d/%d", frames.Passed, frames.Passed+frames.Failed))
	} else {
		parts = append(parts, "frames:all")
	}
	if judge.Verdict != "" {
		v := judge.Verdict
		if len(v) > 80 {
			v = v[:80] + "…"
		}
		parts = append(parts, "judge:"+v)
	}
	if runErr != nil {
		e := runErr.Error()
		if len(e) > 80 {
			e = e[:80] + "…"
		}
		parts = append(parts, "err:"+e)
	}
	out := strings.Join(parts, " | ")
	if len(out) > 256 {
		out = out[:256]
	}
	return out
}

// newLoopRunIDBase returns a short, sortable id used as the prefix
// for per-attempt SessionIDs in the same loop.
func newLoopRunIDBase() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	var b [3]byte
	_, _ = rand.Read(b[:])
	return ts + "-" + hex.EncodeToString(b[:])
}

// newCellRunID returns a unique RunID for a single CellResult. ULID
// would be ideal; for iteration 1, time-prefixed + nonce is enough.
func newCellRunID() string {
	ts := time.Now().UTC().Format("20060102T150405.000Z")
	var b [4]byte
	_, _ = rand.Read(b[:])
	return ts + "-" + hex.EncodeToString(b[:])
}

// sanitize replaces non-alphanumeric chars with hyphens so a
// scenario id is safe to embed in a tempdir name.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// (no _ to keep the package importable; strconv is used in coding_scenario.go)
var _ = strconv.Atoi
