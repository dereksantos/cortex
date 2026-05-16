//go:build !windows

package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// GoLJudgeResult is one judge call's structured outcome.
type GoLJudgeResult struct {
	Pass    bool
	Verdict string // "PASS" or "FAIL: <reason>"
	Reason  string // raw judge response, useful for diagnosis
}

// golJudgePrompt is the standing prompt for the qualitative-correctness
// judge. The judge is asked one binary question per call so the response
// shape is trivial to parse — no JSON, no chain-of-thought required.
// Small judges (Haiku) are reliable on this shape.
const golJudgePrompt = `You will see %d generations of a program's output, which claims to implement Conway's Game of Life on a fixed-size grid. The cells are represented as "." (dead) and "#" (alive). Each generation is separated by a blank line.

Verify three things:
  (a) Each generation is a valid Conway successor of the previous (apply the rules: live cell with 2-3 live neighbors survives; dead cell with exactly 3 live neighbors becomes alive; otherwise the cell dies/stays dead).
  (b) Grid dimensions (rows × columns) are preserved across generations.
  (c) The first generation matches the initial configuration provided.

Initial configuration (input):
%s

Program output (first generation should match the input):
%s

Reply with ONLY "PASS" or "FAIL: <one-line reason>". Do not include any other text.`

// ScoreGoLJudge runs the built binary against a freeform initial
// configuration, then asks the judge LLM whether the output is a
// valid Conway's Game of Life evolution.
//
// freeformInputPath is the path to a .txt file containing the initial
// grid the binary should be fed on stdin. The binary is invoked with
// --generations <generations>; judge sees both input and output.
func ScoreGoLJudge(ctx context.Context, binaryPath, freeformInputPath, workdir string, generations int, judge llm.Provider) (GoLJudgeResult, error) {
	if judge == nil {
		return GoLJudgeResult{}, fmt.Errorf("judge provider is nil")
	}
	if !filepath.IsAbs(workdir) {
		return GoLJudgeResult{}, fmt.Errorf("workdir must be absolute, got %q", workdir)
	}

	in, err := readFileOrError(freeformInputPath)
	if err != nil {
		return GoLJudgeResult{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binaryPath, "--generations", fmt.Sprintf("%d", generations))
	cmd.Dir = workdir
	cmd.Stdin = bytes.NewReader([]byte(in))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return GoLJudgeResult{
			Pass:    false,
			Verdict: fmt.Sprintf("FAIL: binary exited with error: %v (output: %s)", err, truncateGoL(out.String(), 256)),
			Reason:  fmt.Sprintf("binary failed: %v", err),
		}, nil
	}

	prompt := fmt.Sprintf(golJudgePrompt, generations+1, in, out.String())

	judgeCtx, judgeCancel := context.WithTimeout(ctx, 60*time.Second)
	defer judgeCancel()
	resp, err := judge.Generate(judgeCtx, prompt)
	if err != nil {
		return GoLJudgeResult{}, fmt.Errorf("judge call: %w", err)
	}

	verdict := strings.TrimSpace(resp)
	pass := strings.HasPrefix(verdict, "PASS")
	return GoLJudgeResult{
		Pass:    pass,
		Verdict: verdict,
		Reason:  resp,
	}, nil
}

// readFileOrError is a thin wrapper that returns a friendly error
// when the freeform input is missing. Pulled out so the caller's
// error message references the user-supplied path.
func readFileOrError(path string) (string, error) {
	bb, err := exec.Command("/bin/cat", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read %s: %v (%s)", path, err, strings.TrimSpace(string(bb)))
	}
	return string(bb), nil
}
