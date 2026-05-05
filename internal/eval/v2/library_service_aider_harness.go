package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// AiderHarness drives a single library-service session via the `aider` CLI.
// It exists so the eval can run against a local Ollama-served model
// (the small-model amplifier thesis target), since the Claude CLI is
// hardwired to Anthropic models.
//
// Prerequisites (the harness does NOT install or start either):
//   - `aider` CLI on PATH or pointed at via $AIDER_BINARY
//   - For Ollama models: `ollama serve` running on 127.0.0.1:11434 and the
//     target model pulled, e.g. `ollama pull qwen2.5-coder:1.5b`. Aider
//     surfaces "ollama unreachable" / "model not found" errors directly,
//     and this harness propagates them via stderr.
//
// Aider config files (`.aider.conf.yml`) are picked up from cwd, $HOME, and
// parent dirs. The harness does NOT isolate $HOME. If a personal config
// injects unwanted flags during eval runs, mirror the HOME-isolation
// pattern from CortexInjector as a follow-up.
type AiderHarness struct {
	binary string // path to aider executable
	model  string // e.g., "ollama/qwen2.5-coder:1.5b"
}

// NewAiderHarness resolves the aider binary (PATH lookup if binary is
// empty, $AIDER_BINARY override otherwise) and verifies it exists. A
// missing binary is a hard error — the runner cannot proceed without it.
//
// model may be any string aider accepts via --model. The Ollama convention
// is "ollama/<model>:<tag>" (e.g. "ollama/qwen2.5-coder:1.5b"); other
// backends work but Ollama is the thesis target.
func NewAiderHarness(binary, model string) (*AiderHarness, error) {
	resolved, err := resolveAiderBinary(binary)
	if err != nil {
		return nil, err
	}
	return &AiderHarness{binary: resolved, model: model}, nil
}

// RunSession invokes aider non-interactively against workdir with prompt as
// the single message. Aider exits after the message completes (no REPL).
//
// Sessions for small Ollama models are expected to take 15–45 minutes; we
// honor ctx for cancellation but do not impose our own timeout. On cancel
// the subprocess group gets SIGTERM with a 2s grace period before SIGKILL
// — same lifecycle as ClaudeCLIHarness.
//
// Flag rationale (verified against aider 0.86.2 — re-check `aider --help`
// before bumping major versions; flag names occasionally shift):
//   - --message: pass the prompt and exit on completion (per its own help:
//     "process reply then exit (disables chat mode)")
//   - --yes-always: skip confirmation prompts; without it Aider blocks on
//     shell tool calls until ctx cancel
//   - --no-auto-commits: the runner already commits per session; letting
//     Aider commit too duplicates entries in the per-session diff
//   - --no-dirty-commits: Aider's dirty-commits default is True, which
//     would commit any pre-existing working-tree state on launch. The
//     runner controls the commit cadence, so disable this too.
//   - --no-stream / --no-pretty: deterministic, parse-friendly output
//   - --no-show-model-warnings: suppress noise about non-frontier models
//     (the thesis is *about* small models — the warning is irrelevant)
//   - --no-check-update: no network call to PyPI on every launch
//   - --analytics-disable: eval runs MUST NOT phone home with code samples
func (h *AiderHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	args := []string{
		"--message", prompt,
		"--yes-always",
		"--no-auto-commits",
		"--no-dirty-commits",
		"--no-stream",
		"--no-pretty",
		"--no-show-model-warnings",
		"--no-check-update",
		"--analytics-disable",
	}
	if h.model != "" {
		args = append(args, "--model", h.model)
	}

	cmd := exec.Command(h.binary, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start aider: %w", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-waitErr:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-waitErr
			}
		}
		return ctx.Err()
	case err := <-waitErr:
		if err != nil {
			return fmt.Errorf("aider exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// resolveAiderBinary returns the aider binary path. Resolution order:
//  1. explicit `binary` argument (must exist)
//  2. $AIDER_BINARY env var (must be absolute and exist)
//  3. PATH lookup for `aider`
//
// Mirrors resolveCortexBinary's contract so the eval driver can pin a
// specific Aider install without it being on PATH.
func resolveAiderBinary(binary string) (string, error) {
	if binary != "" {
		if _, err := os.Stat(binary); err != nil {
			return "", fmt.Errorf("aider binary not found: %s: %w", binary, err)
		}
		return binary, nil
	}
	if env := os.Getenv("AIDER_BINARY"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("AIDER_BINARY must be absolute, got %q", env)
		}
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("AIDER_BINARY=%s: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("aider")
	if err != nil {
		return "", fmt.Errorf("aider binary not found in PATH (set $AIDER_BINARY to override)")
	}
	return path, nil
}
