// Package ops — act.verify.
//
// Runs a shell command in the project workdir and reports pass/fail
// + the last lines of combined output. Used by the project-type DAG
// (commands/run.go runProjectDAG) to gate sub-task completion: a
// coding_turn finishes, act.verify runs the verify_cmd, on fail the
// chain spawns a re-attempt coding_turn with the failure output in
// context.
//
// Bounded by a 60-second context deadline so a verify command that
// hangs (rare but possible — `go test ./...` on a busted package
// loop) can't strand the executor.
package ops

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// VerifyConfig is the registration shape. Mechanical op — no
// dependencies.
type VerifyConfig struct {
	// Timeout for the verify command. 0 = 60s default. Verify shell
	// commands are typically `go build` / `go test` style which
	// finish well under a minute; if a project's verify takes longer,
	// raise this explicitly.
	Timeout time.Duration
}

// VerifySpec returns the NodeSpec for act.verify.
func VerifySpec(cfg VerifyConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncAct,
		Op:          "verify",
		Description: "run a shell verify command; emit pass/fail + tail of output",
		AxisContract: &dag.AxisContract{
			Mutator:              false, // verify reads + spawns subprocesses; doesn't mutate the workdir itself
			RequiresConfirmation: false,
		},
		Inputs: []dag.ParamSpec{
			{Name: "cmd", Type: "string", Required: true},
			{Name: "workdir", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "pass", Type: "bool"},
			{Name: "exit_code", Type: "int"},
			{Name: "output_tail", Type: "string"},
		},
		Cost:    dag.Cost{LatencyMS: 5000, Tokens: 0},
		Handler: NewVerifyHandler(cfg),
	}
}

// NewVerifyHandler returns the dag.Handler.
func NewVerifyHandler(cfg VerifyConfig) dag.Handler {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
		started := time.Now()
		cmdStr := readString(in, "cmd")
		if cmdStr == "" {
			return dag.NodeResult{
				Out:          map[string]any{"pass": true, "exit_code": 0, "output_tail": "(no verify command)"},
				CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
			}, nil
		}
		workdir := readString(in, "workdir")

		deadline, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(deadline, "bash", "-c", cmdStr)
		if workdir != "" {
			cmd.Dir = workdir
		}
		out, runErr := cmd.CombinedOutput()
		latency := int(time.Since(started).Milliseconds())

		exitCode := 0
		if runErr != nil {
			if ee, ok := runErr.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				// Context deadline / spawn failure / etc. — surface as
				// non-zero exit so the chain treats it as a fail.
				exitCode = -1
			}
		}
		pass := runErr == nil

		tail := strings.TrimSpace(string(out))
		// Tail to ~600 bytes so we don't blow up the trace row.
		if len(tail) > 600 {
			tail = "..." + tail[len(tail)-600:]
		}
		var errStr string
		if runErr != nil && !pass {
			errStr = fmt.Sprintf("%v", runErr)
		}

		result := dag.NodeResult{
			Out: map[string]any{
				"pass":        pass,
				"exit_code":   exitCode,
				"output_tail": tail,
				"cmd":         cmdStr,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: 0},
		}
		if errStr != "" {
			result.Out["error"] = errStr
		}
		return result, nil
	}
}
