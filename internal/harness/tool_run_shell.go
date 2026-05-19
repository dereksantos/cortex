package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// runShellTool runs a command string through `bash -c` with the
// REPL's workdir as cwd. Iter-7 (2026-05-19) rewrite: dropped the
// binary allowlist + metacharacter rejection in favour of real shell
// semantics. The model can now pipe, redirect, glob, and call any
// binary on the user's PATH — the trust model is "the user opted in
// by running cortex; the agent has the keys to the workdir."
//
// The previous allowlist was Go-pinned hardcoding masquerading as a
// security boundary. Real defenses (timeout, output cap, axis-5
// confirm in DAG mode) still apply.
type runShellTool struct {
	workdir  string
	registry *ToolRegistry
	policy   ShellPolicy
}

// runShellTimeout bounds each subprocess. 30s catches infinite loops
// while leaving room for a real build/test pass on small projects.
const runShellTimeout = 30 * time.Second

// runShellOutputCap caps combined stdout+stderr returned to the
// model. The remainder is truncated with a sentinel; if the model
// needs more it can re-run with `head`, `tail`, or `grep`.
const runShellOutputCap = 8 * 1024

type runShellArgs struct {
	Command string `json:"command"`
}

// NewRunShellTool constructs the tool. workdir must be absolute.
//
// A ShellPolicy is loaded eagerly from <workdir>/.cortex/shell-policy.json
// (falling back to $HOME/.cortex/shell-policy.json, then empty). An
// empty policy permits everything — that's the default. The policy is
// captured at construction; reload by re-creating the tool (a REPL
// /shell-policy reload command can do this in a later slice).
func NewRunShellTool(workdir string, reg *ToolRegistry) ToolHandler {
	return &runShellTool{
		workdir:  workdir,
		registry: reg,
		policy:   LoadShellPolicy(workdir),
	}
}

// NewRunShellToolWithPolicy lets callers supply an explicit policy
// (tests, benchmark runners with synthetic policies). Skips the
// config-file lookup.
func NewRunShellToolWithPolicy(workdir string, reg *ToolRegistry, policy ShellPolicy) ToolHandler {
	return &runShellTool{
		workdir:  workdir,
		registry: reg,
		policy:   policy,
	}
}

func (t *runShellTool) Name() string { return "run_shell" }

func (t *runShellTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name: t.Name(),
			Description: "Run a shell command via `bash -c` with the workdir as cwd. Full shell " +
				"semantics: pipes, redirects, glob expansion, env vars all work. Use this to build, " +
				"test, run, or inspect anything the project needs. 30s timeout; output capped at 8 KiB. " +
				"Pick the right command for the project's stack (go build, npm test, cargo run, pytest, etc.).",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Full shell command, e.g. \"npm test\" or \"go build ./...\" or \"grep -r foo src | head\"."}
				},
				"required": ["command"]
			}`),
		},
	}
}

func (t *runShellTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args runShellArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}
	if args.Command == "" {
		return errorJSON(errors.New("run_shell: command must not be empty")), nil
	}

	// User-controlled allow/deny guardrail. Empty policy permits
	// everything; non-empty Deny rejects matches; non-empty Allow
	// requires a match.
	if err := t.policy.Check(args.Command); err != nil {
		return errorJSON(err), nil
	}

	subCtx, cancel := context.WithTimeout(ctx, runShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(subCtx, "bash", "-c", args.Command)
	cmd.Dir = t.workdir
	// Inherit the user's environment — npm needs HOME, virtualenvs
	// rely on PATH, etc. The earlier minimal-env approach was tuned
	// for the Go-only iteration and broke every other stack.
	cmd.Env = os.Environ()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if t.registry != nil {
		t.registry.noteShellExit(exitCode)
	}

	bb := out.Bytes()
	truncated := false
	if len(bb) > runShellOutputCap {
		bb = bb[:runShellOutputCap]
		truncated = true
	}

	body, _ := json.Marshal(struct {
		Command   string `json:"command"`
		ExitCode  int    `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		ElapsedMs int64  `json:"elapsed_ms"`
		Output    string `json:"output"`
		Error     string `json:"error,omitempty"`
	}{
		Command:   args.Command,
		ExitCode:  exitCode,
		Truncated: truncated,
		ElapsedMs: elapsed.Milliseconds(),
		Output:    string(bb),
		Error:     runErrorString(runErr, subCtx),
	})
	return string(body), nil
}

// runErrorString stringifies the run error, distinguishing context
// timeout from a simple non-zero exit (which is normal and reported
// via exit_code).
func runErrorString(err error, ctx context.Context) string {
	if err == nil {
		return ""
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("timed out after %s", runShellTimeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ""
	}
	return err.Error()
}
