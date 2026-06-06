package codebase

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunOptions configures one fixture run. The runner shells out to the
// cortex binary (CortexBinary) with --prompt + --workdir + --json +
// --auto-retry + --keep-on-fail, then collects telemetry from the
// resulting session JSONL and dag_traces.jsonl.
//
// Treating cortex as a black box keeps the eval honest: any future
// REPL refactor that changes how the chain wires together is exercised
// by the suite without code edits, exactly the property we want from a
// "dial-and-measure" rig.
type RunOptions struct {
	// CortexBinary is the path to the cortex CLI. Defaults to "cortex"
	// (PATH lookup).
	CortexBinary string

	// Model overrides the REPL default. Empty means whatever cortex
	// picks (CORTEX_REPL_MODEL → .cortex/config.json → compile-time
	// default).
	Model string

	// Workdir is the absolute project root the prompt runs against.
	// Required; the runner does not derive it from Fixture.Project
	// because the fixture-path resolver (ResolveFixturePath) lives one
	// layer up and may need caller-supplied roots.
	Workdir string

	// Timeout caps the cortex invocation. 0 means no extra timeout
	// beyond cortex's own per-attempt budget.
	Timeout time.Duration

	// MaxTurns / MaxCostUSD / MaxCumulativeTokens are per-attempt
	// budget overrides forwarded to cortex. Zero leaves the binary's
	// defaults in place.
	MaxTurns            int
	MaxCostUSD          float64
	MaxCumulativeTokens int

	// ExtraArgs is forwarded verbatim after the runner's required
	// flags. Used by slices 4/5 to thread --baseline / language hints.
	ExtraArgs []string

	// Env lets callers seed CORTEX_REPL_MODEL etc. without mutating the
	// parent process. Merged on top of the inherited environment.
	Env map[string]string

	// Judge, when set, invokes the slice-3 LLM judge on Q-class
	// fixtures that carry a JudgeRubric. R-class and B-class are
	// mechanical-only by design (the doc names this); the runner
	// honors that even when Judge.Provider is set.
	Judge JudgeOptions
}

// RunResult bundles everything Extract needs plus diagnostics for the
// dashboard. AnswerText is the assistant's final message on the
// fixture's prompt; TraceRows are the dag_traces.jsonl rows the cortex
// invocation appended, selected by turn_id delta (the rows whose
// turn_id did not exist before the run).
type RunResult struct {
	AnswerText string
	TraceRows  []TraceRow

	SessionPath string
	WorkdirUsed string

	// CortexExitErr is non-nil when the cortex invocation exited with
	// non-zero. The runner still returns the result — partial telemetry
	// is useful for "what went wrong" debugging.
	CortexExitErr error

	// Invalid marks a HARNESS failure (not a quality failure): the cell
	// could not be fairly scored because the subprocess was killed /
	// timed out or produced no answer at all. INVALID cells are excluded
	// from pass-rate denominators so a fleet stall can't masquerade as a
	// regression. InvalidReason carries the human-readable cause.
	Invalid       bool
	InvalidReason string

	Stderr string

	// Judge is the slice-3 LLM-judge verdict. nil when the fixture
	// didn't request judging (R/B-class, or Q-class with no rubric) or
	// when no judge provider was wired into RunOptions.
	Judge *JudgeResult
}

// Run executes one Fixture and returns the captured artifacts +
// extracted metrics + bounds.
func Run(ctx context.Context, fx *Fixture, opts RunOptions) (*RunResult, Metrics, []Bound, error) {
	if fx == nil {
		return nil, Metrics{}, nil, errors.New("Run: nil fixture")
	}
	if opts.Workdir == "" {
		return nil, Metrics{}, nil, errors.New("Run: opts.Workdir is required")
	}
	abs, err := filepath.Abs(opts.Workdir)
	if err != nil {
		return nil, Metrics{}, nil, fmt.Errorf("abs workdir: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, Metrics{}, nil, fmt.Errorf("workdir: %w", err)
	}

	bin := opts.CortexBinary
	if bin == "" {
		bin = "cortex"
	}

	// Turn-id delta: snapshot the turn_ids already present in
	// dag_traces.jsonl, then after the run keep only rows whose turn_id
	// is new. A single cortex one-shot invocation stamps every node it
	// runs — the out-of-executor sense.classify_intent / sense.estimate_scope
	// AND the executor's nodes — with one turn_id, so this captures
	// exactly this cell's rows.
	//
	// This replaces a byte-offset tail that proved unsafe at full-suite
	// scale: the offset window could land mid-turn (after the early
	// estimate_scope row but before the agent-loop rows), silently
	// dropping the scope row and zeroing budget_tokens for every cell —
	// while hop/read counts still populated, masking the bug. Keying on
	// turn_id and reading the fully-flushed file once, after the
	// subprocess exits, is immune to that and to flush-ordering races.
	tracesPath := filepath.Join(abs, ".cortex", "db", "dag_traces.jsonl")
	priorTurns := snapshotTurnIDs(tracesPath)

	args := []string{
		"--prompt", fx.Prompt,
		"--workdir", abs,
		"--json",
		"--auto-retry",
		"--keep-on-fail",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", itoa(opts.MaxTurns))
	}
	if opts.MaxCostUSD > 0 {
		args = append(args, "--max-cost-usd", ftoa(opts.MaxCostUSD))
	}
	if opts.MaxCumulativeTokens > 0 {
		args = append(args, "--max-cumulative-tokens", itoa(opts.MaxCumulativeTokens))
	}
	args = append(args, opts.ExtraArgs...)

	runCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, bin, args...)
	// Run cortex with cwd=workdir so its dag_traces.jsonl writer (which
	// defaults to ".cortex/db/dag_traces.jsonl" relative to cwd) writes
	// inside the fixture's project. Otherwise traces leak into whichever
	// directory the runner happens to be invoked from — and the tail-
	// by-offset above reads from the wrong file, dropping every row.
	cmd.Dir = abs
	cmd.Env = mergeEnv(os.Environ(), opts.Env)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	res := &RunResult{WorkdirUsed: abs, Stderr: stderrBuf.String()}
	if runErr := cmd.Run(); runErr != nil {
		res.CortexExitErr = fmt.Errorf("cortex exit: %w (stderr=%s)", runErr, truncate(stderrBuf.String(), 800))
	}
	res.Stderr = stderrBuf.String()

	if sessionPath := parseSessionPath(stdoutBuf.String()); sessionPath != "" {
		res.SessionPath = sessionPath
		if txt, err := readFinalText(sessionPath); err == nil {
			res.AnswerText = txt
		}
	}

	rows, err := readNewTurnRows(tracesPath, priorTurns)
	if err != nil && res.CortexExitErr == nil {
		// Trace-read failure shouldn't shadow a successful cortex run —
		// surface as a soft error to the caller.
		res.CortexExitErr = fmt.Errorf("read dag_traces.jsonl new-turn rows: %w", err)
	}
	res.TraceRows = rows

	// Quarantine harness failures BEFORE scoring. A killed/timed-out
	// subprocess or an empty answer isn't a quality signal — scoring it
	// as FAIL is what let a fleet stall read as a 6/44 regression.
	res.Invalid, res.InvalidReason = classifyInvalid(res)

	m := Extract(res.AnswerText, res.TraceRows, fx)
	bounds := Evaluate(m, fx.Expected)

	if ShouldJudge(fx) && opts.Judge.Provider != nil {
		// Code-grounded judge: feed the fixture's must_cite_paths to the
		// judge so it can verify symbol claims instead of pattern-
		// matching. Workdir defaults to the fixture's resolved workdir
		// when the caller didn't specify one.
		judgeOpts := opts.Judge
		if judgeOpts.Workdir == "" {
			judgeOpts.Workdir = abs
		}
		if judgeOpts.MaxGroundingBytes == 0 {
			judgeOpts.MaxGroundingBytes = DefaultMaxGroundingBytes
		}
		jr, jerr := JudgeWithFixture(ctx, fx.Prompt, res.AnswerText, fx.JudgeRubric, fx, judgeOpts)
		if jerr != nil && res.CortexExitErr == nil {
			res.CortexExitErr = fmt.Errorf("judge: %w", jerr)
		}
		res.Judge = jr
		if jr != nil {
			bounds = append(bounds, Bound{
				Name: "judge_pass",
				Pass: jr.Pass,
				Want: "judge_pass=true",
				Got:  truncate(jr.Reason, 200),
			})
		}
	}
	return res, m, bounds, nil
}

// classifyInvalid decides whether a run is a HARNESS failure rather than
// a scoreable quality outcome. Two unambiguous signals:
//
//   - the subprocess was killed or timed out (context deadline cancels
//     runCtx → the child gets SIGKILL → "signal: killed"); the turn
//     never finished, so its bounds are meaningless.
//   - no answer was produced at all (empty AnswerText) — the synthesis
//     turn emitted nothing to score.
//
// A non-empty wrong answer, a "NEED_MORE:" that never converged, or an
// honest "I don't know" are all REAL quality outcomes and stay
// scoreable (FAIL/PASS) — only genuine harness failures are quarantined.
func classifyInvalid(res *RunResult) (bool, string) {
	if res.CortexExitErr != nil {
		e := res.CortexExitErr.Error()
		for _, sig := range []string{"signal: killed", "killed", "deadline", "context canceled", "context deadline"} {
			if strings.Contains(e, sig) {
				return true, "subprocess killed/timed out: " + truncate(e, 160)
			}
		}
	}
	if strings.TrimSpace(res.AnswerText) == "" {
		return true, "no answer produced (empty synthesis turn)"
	}
	return false, ""
}

// parseSessionPath extracts session_path from the one-shot JSON
// envelope cortex emits. The envelope is wrapped by the cliout Emitter
// (ok/fail), so we look at `data.session_path` or, as a fallback, the
// top-level field.
func parseSessionPath(stdout string) string {
	// Walk lines from the end — the envelope is the last JSON line.
	for _, ln := range reverseLines(stdout) {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "{") {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(ln), &v); err != nil {
			continue
		}
		if s, ok := v["session_path"].(string); ok && s != "" {
			return s
		}
		if data, ok := v["data"].(map[string]any); ok {
			if s, ok := data["session_path"].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// readFinalText scans the one-shot session JSONL and returns the
// assistant's final answer text. Headless one-shot writes exactly one
// turn row; if multiple appear (defensively), the last accepted-or-
// final one wins.
func readFinalText(sessionPath string) (string, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lastFinal string
	for scanner.Scan() {
		var row struct {
			FinalText          string `json:"final_text"`
			RetryFinalText     string `json:"retry_final_text"`
			UserRetryFinalText string `json:"user_retry_final_text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		// Prefer the latest retry text — it represents the agent's
		// final converged answer when verifier rounds happened.
		switch {
		case row.UserRetryFinalText != "":
			lastFinal = row.UserRetryFinalText
		case row.RetryFinalText != "":
			lastFinal = row.RetryFinalText
		case row.FinalText != "":
			lastFinal = row.FinalText
		}
	}
	if err := scanner.Err(); err != nil {
		return lastFinal, err
	}
	return lastFinal, nil
}

// snapshotTurnIDs returns the set of turn_ids already present in
// dag_traces.jsonl before a run. A missing file yields an empty set, so
// every row the run appends counts as new. The decode is turn_id-only
// to keep the pre-run scan cheap.
func snapshotTurnIDs(path string) map[string]struct{} {
	seen := map[string]struct{}{}
	f, err := os.Open(path)
	if err != nil {
		return seen
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var row struct {
			TurnID string `json:"turn_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row.TurnID != "" {
			seen[row.TurnID] = struct{}{}
		}
	}
	return seen
}

// readNewTurnRows reads dag_traces.jsonl and returns every row whose
// turn_id was NOT present before the run (prior). Reading the fully
// flushed file once, after the subprocess has exited, and keying on
// turn_id makes row→cell association robust to write-buffer flush
// ordering and to multiple cells sharing one project's trace file. Rows
// with no turn_id are dropped — they can't be attributed to this cell.
// A missing file returns an empty slice without error.
func readNewTurnRows(path string, prior map[string]struct{}) ([]TraceRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var rows []TraceRow
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var row TraceRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row.TurnID == "" {
			continue
		}
		if _, ok := prior[row.TurnID]; ok {
			continue
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return rows, err
	}
	return rows, nil
}

func mergeEnv(parent []string, override map[string]string) []string {
	if len(override) == 0 {
		return parent
	}
	have := map[string]string{}
	for _, kv := range parent {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		have[kv[:i]] = kv[i+1:]
	}
	for k, v := range override {
		have[k] = v
	}
	out := make([]string, 0, len(have))
	for k, v := range have {
		out = append(out, k+"="+v)
	}
	return out
}

func reverseLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[len(lines)-1-i] = ln
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
