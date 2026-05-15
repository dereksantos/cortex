package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// runShellTool executes one command from the allowlist with a strict
// argv (no shell interpretation), cwd=workdir, explicit non-network
// environment, and a 30s wall-clock timeout. Combined output is
// capped at 8 KiB so a runaway test suite can't fill the context.
type runShellTool struct {
	workdir  string
	registry *ToolRegistry
}

// runShellTimeout bounds each subprocess. Go builds with no cache on
// the first call can take ~15s for a small program; 30s leaves
// headroom while still catching infinite loops.
const runShellTimeout = 30 * time.Second

// runShellOutputCap is the maximum bytes of combined stdout+stderr
// returned to the model in a single tool result. The remainder is
// truncated with a sentinel suffix; if the model needs more it can
// re-run with a filter (e.g. `go test -run TestX`).
const runShellOutputCap = 8 * 1024

type runShellArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// NewRunShellTool constructs the tool. workdir must be absolute.
func NewRunShellTool(workdir string, reg *ToolRegistry) ToolHandler {
	return &runShellTool{workdir: workdir, registry: reg}
}

func (t *runShellTool) Name() string { return "run_shell" }

func (t *runShellTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name: t.Name(),
			Description: "Run an allowlisted command (no shell, no interpolation). Allowed: " +
				"go, gofmt, ls, cat, head, tail, wc, diff, grep, test. " +
				"30s timeout; output capped at 8 KiB. Use 'go build', 'go test', 'go run' to iterate.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Command name (must be on the allowlist)."},
					"args":    {"type": "array", "items": {"type": "string"}, "description": "Arguments. No shell metacharacters. No absolute paths."}
				},
				"required": ["command", "args"]
			}`),
		},
	}
}

func (t *runShellTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args runShellArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}

	// Tolerate the common LLM mistake of passing `"command": "go test"`
	// instead of `command=go, args=[test, ...]`. We split on whitespace
	// and shift the trailing tokens onto args. The allowlist still
	// gates the first token, so this only relaxes the surface, not the
	// security boundary.
	if strings.ContainsAny(args.Command, " \t") {
		fields := strings.Fields(args.Command)
		args.Command = fields[0]
		args.Args = append(append([]string{}, fields[1:]...), args.Args...)
	}

	absCmd, err := resolveShellCommand(args.Command)
	if err != nil {
		return errorJSON(err), nil
	}
	for _, a := range args.Args {
		if err := validateShellArg(a); err != nil {
			return errorJSON(err), nil
		}
	}

	// Explicit env (no os.Environ): keeps things deterministic and
	// blocks tool calls from inheriting an HTTP_PROXY or similar.
	// The Go cache + module cache point into the workdir so test
	// runs don't poison the operator's GOPATH.
	env := []string{
		"PATH=/usr/bin:/bin:/usr/local/bin",
		"HOME=" + t.workdir,
		"GOPATH=" + filepath.Join(t.workdir, ".gopath"),
		"GOCACHE=" + filepath.Join(t.workdir, ".gocache"),
		"GOMODCACHE=" + filepath.Join(t.workdir, ".gomodcache"),
	}

	subCtx, cancel := context.WithTimeout(ctx, runShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(subCtx, absCmd, args.Args...)
	cmd.Dir = t.workdir
	cmd.Env = env

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
			// Non-exit error (context deadline, fork failure, etc).
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

	// Hand-build the JSON to keep multi-line output legible: we want
	// the model to see actual newlines, not "\n" escape sequences.
	// json.Marshal would emit the escaped form, which is harder for
	// the model to reason about line-by-line. The risk is that the
	// model's JSON parser sees a multi-line string field — most
	// providers handle this fine; the alternative is worse.
	body, _ := json.Marshal(struct {
		Command   string `json:"command"`
		Args      []any  `json:"args"`
		ExitCode  int    `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		ElapsedMs int64  `json:"elapsed_ms"`
		Output    string `json:"output"`
		Error     string `json:"error,omitempty"`
	}{
		Command:   args.Command,
		Args:      argsToAny(args.Args),
		ExitCode:  exitCode,
		Truncated: truncated,
		ElapsedMs: elapsed.Milliseconds(),
		Output:    string(bb),
		Error:     runErrorString(runErr, subCtx),
	})
	return string(body), nil
}

func argsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// runErrorString stringifies the run error, distinguishing context
// timeout from a simple non-zero exit (which is normal and not
// reported separately — the exit_code field carries it).
func runErrorString(err error, ctx context.Context) string {
	if err == nil {
		return ""
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("timed out after %s", runShellTimeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// exit_code carries this; redundant string would just
		// confuse the model.
		return ""
	}
	return err.Error()
}
