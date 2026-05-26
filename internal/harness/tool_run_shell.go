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
				"Pick the right command for the project's stack (go build, npm test, cargo run, pytest, etc.). " +
				"For search/enumeration: ALWAYS exclude Cortex-internal dirs which contain stale per-turn " +
				"workdir copies — pass `--exclude-dir=.cortex --exclude-dir=.context` to grep, " +
				"`-not -path '*/.cortex/*' -not -path '*/.context/*'` to find. The tool also auto-filters " +
				"these paths from output and returns a `hint` field when it had to.",
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

	// Strip Cortex-internal path lines (session snapshots, journals,
	// daemon logs) from the raw output before any caps or truncation.
	// Cortex copies the workdir into .cortex/sessions/<ts>/snapshots/
	// each turn, plus writes journals + daemon state, and recursive
	// find/grep commands the model runs surface those as if they were
	// source — they're not, they're stale duplicates that pollute the
	// result list (especially when the model pipes through `head -N`
	// and the snapshot copies crowd out the real answer). list_dir
	// already hides .cortex; this matches that behavior for shell
	// output.
	rawOut := out.Bytes()
	bb := stripCortexSnapshotLines(rawOut)
	filteredLines := 0
	if len(bb) != len(rawOut) {
		filteredLines = bytes.Count(rawOut, []byte("\n")) - bytes.Count(bb, []byte("\n"))
	}
	truncated := false
	if len(bb) > runShellOutputCap {
		bb = bb[:runShellOutputCap]
		truncated = true
	}

	hint := ""
	if filteredLines > 0 {
		// Surface a concrete, actionable nudge — small models won't
		// otherwise know that the harness silently dropped lines and
		// will misread an empty/sparse result as "no matches in the
		// project." The flags are correct for both grep and find.
		hint = fmt.Sprintf("filtered %d line(s) pointing inside Cortex-internal directories (.cortex/, .context/) — those are stale per-turn workdir copies + cortex state, not project source. "+
			"If results look incomplete, re-run with: grep --exclude-dir=.cortex --exclude-dir=.context  or  find ... -not -path '*/.cortex/*' -not -path '*/.context/*'.", filteredLines)
	}

	body, _ := json.Marshal(struct {
		Command   string `json:"command"`
		ExitCode  int    `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		ElapsedMs int64  `json:"elapsed_ms"`
		Output    string `json:"output"`
		Hint      string `json:"hint,omitempty"`
		Error     string `json:"error,omitempty"`
	}{
		Command:   args.Command,
		ExitCode:  exitCode,
		Truncated: truncated,
		ElapsedMs: elapsed.Milliseconds(),
		Output:    string(bb),
		Hint:      hint,
		Error:     runErrorString(runErr, subCtx),
	})
	return string(body), nil
}

// cortexInternalNeedles is the path-substring deny list for shell
// output filtering. These are directories Cortex itself writes to
// inside a project workdir — not source code, not user files. When
// the model runs find/grep recursively, lines pointing here are pure
// noise that crowds out real answers (especially under `head -N`).
//
// Keep this list aligned with list_dir's skippedNames (.git, .cortex,
// .context, vendor, node_modules etc. for the latter two), though
// vendor/node_modules are intentionally LEFT IN shell output — the
// user may legitimately want to grep dependencies. Only Cortex-owned
// state is filtered.
var cortexInternalNeedles = [][]byte{
	[]byte("/.cortex/sessions/"), // per-turn workdir copies
	[]byte("/.cortex/db/"),       // dag traces + cell_results
	[]byte("/.cortex/journal/"),  // append-only journals
	[]byte("/.cortex/data/"),     // observations / insights / etc.
	[]byte("/.context/"),         // legacy cortex daemon state
}

// stripCortexSnapshotLines removes lines whose path component points
// inside Cortex-internal directories — see cortexInternalNeedles.
// Returns input unchanged when no such lines are present, so commands
// that don't enumerate paths (e.g. `go build`) pay no cost.
//
// Matching is conservative: a line is dropped only when it contains
// one of the canonical substrings (e.g. "/.cortex/sessions/") or its
// "./..." or bare-prefix form. The vanishingly unlikely case where a
// real source path coincides is acceptable; reading inside .cortex/
// or .context/ requires explicit absolute paths anyway.
func stripCortexSnapshotLines(b []byte) []byte {
	hit := false
	for _, n := range cortexInternalNeedles {
		if bytes.Contains(b, n) {
			hit = true
			break
		}
	}
	if !hit {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	out := lines[:0]
nextLine:
	for _, ln := range lines {
		for _, n := range cortexInternalNeedles {
			if bytes.Contains(ln, n) {
				continue nextLine
			}
			// Also catch bare-prefix forms: ".cortex/sessions/foo" or
			// "./.cortex/sessions/foo". Strip the leading "./" once for
			// the trimmed check.
			trimmed := bytes.TrimLeft(ln, " \t")
			trimmed = bytes.TrimPrefix(trimmed, []byte("./"))
			bare := n[1:] // drop leading "/"
			if bytes.HasPrefix(trimmed, bare) {
				continue nextLine
			}
		}
		out = append(out, ln)
	}
	return bytes.Join(out, []byte("\n"))
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
