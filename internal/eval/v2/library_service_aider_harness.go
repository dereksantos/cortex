//go:build !windows

package eval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// SetModel changes the model used for subsequent RunSession calls.
// The grid runner type-asserts on this method to re-point one harness
// instance across multiple model cells without constructing a new
// AiderHarness per cell.
func (h *AiderHarness) SetModel(model string) {
	h.model = model
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
	_, err := h.runSession(ctx, prompt, workdir)
	return err
}

// RunSessionWithResult is the ResultfulHarness extension. Same lifecycle
// as RunSession; on success it parses Aider's summary lines (tokens,
// cost, "Applied edit to ...") into a HarnessResult.
//
// On error the returned HarnessResult is best-effort (LatencyMs +
// ModelEcho only); callers should not rely on its other fields.
func (h *AiderHarness) RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	return h.runSession(ctx, prompt, workdir)
}

// runSession is the shared implementation. Captures stdout (Aider's
// summary lines live there) and parses on success.
func (h *AiderHarness) runSession(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
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

	// Auto-add source files in the workdir to Aider's chat context.
	// Without this, Aider's repo map doesn't reliably include them
	// under --message mode and the model responds with "please add
	// these files to the chat" instead of editing — a real failure
	// mode discovered with claude-haiku-4.5 on 2026-05-10. Globbing
	// .go files covers the current coding-scenario library; extend
	// the suffix list when other languages land.
	for _, path := range discoverChatFiles(workdir) {
		args = append(args, "--file", path)
	}

	cmd := exec.Command(h.binary, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Aider/litellm reads the canonical OPENROUTER_API_KEY. Our project
	// env exports the underscore form OPEN_ROUTER_API_KEY. Re-export so
	// litellm can authenticate when the model is openrouter/* — only
	// when the alias isn't already set, to avoid overriding user intent.
	if k := os.Getenv("OPEN_ROUTER_API_KEY"); k != "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY="+k)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return HarnessResult{ModelEcho: h.model}, fmt.Errorf("start aider: %w", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	var runErr error
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
		runErr = ctx.Err()
	case err := <-waitErr:
		if err != nil {
			runErr = fmt.Errorf("aider exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	}

	elapsed := time.Since(start).Milliseconds()
	if runErr != nil {
		return HarnessResult{LatencyMs: elapsed, ModelEcho: h.model, ProviderEcho: aiderProviderFromModel(h.model)}, runErr
	}

	res := parseAiderOutput(stdout.String())
	res.LatencyMs = elapsed
	res.ModelEcho = h.model
	res.ProviderEcho = aiderProviderFromModel(h.model)
	res.AgentTurnsTotal = 1 // single --message invocation
	return res, nil
}

// discoverChatFiles walks workdir for source files Aider should add to
// its chat context via --file. Returns paths relative to workdir
// (Aider treats them as project-relative). Limits to common source
// extensions to avoid pulling in build artifacts; extend when adding
// non-Go scenarios.
//
// Sorted for stable test assertions and so Aider's chat order is
// deterministic across cells.
func discoverChatFiles(workdir string) []string {
	var paths []string
	skipDirs := map[string]bool{
		".git": true, "vendor": true, "node_modules": true,
		"testdata": true, ".cortex": true, "dist": true, "build": true,
	}
	_ = filepath.Walk(workdir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort; skip unreadable
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".py", ".ts", ".js", ".rs", ".java":
			rel, err := filepath.Rel(workdir, p)
			if err != nil {
				rel = p
			}
			paths = append(paths, rel)
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

// aiderProviderFromModel pulls the provider segment from Aider's
// "<provider>/<model>" convention. Returns "" if absent.
func aiderProviderFromModel(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[:i]
	}
	return ""
}

// Aider 0.86.2 emits these summary lines after a --message run:
//
//	Tokens: 1,234 sent, 567 received.
//	Cost: $0.0012 message, $0.0024 session.
//
// Variant: numbers may carry a "k"/"K" or "m"/"M" suffix
// (e.g. "1.2k sent"); the cost line uses "message" or "request" for the
// per-call total. Edited file paths appear as
//
//	Applied edit to <path>
//
// We deliberately do NOT fall back to OpenRouter's /api/v1/generation
// for cost — the 2026-05-10 probe showed that endpoint returns 404 for
// up to several seconds after a successful completion. Aider's parsed
// cost (via litellm) is authoritative for the data we need.
var (
	aiderTokensRE = regexp.MustCompile(`Tokens:\s+([\d.,kKmM]+)\s+sent,\s+([\d.,kKmM]+)\s+received`)
	aiderCostRE   = regexp.MustCompile(`Cost:\s+\$([\d.]+)\s+(?:message|request|send)`)
	aiderEditRE   = regexp.MustCompile(`(?m)^Applied edit to (.+)$`)
)

func parseAiderOutput(s string) HarnessResult {
	var r HarnessResult
	if m := aiderTokensRE.FindStringSubmatch(s); m != nil {
		r.TokensIn = parseAiderNumber(m[1])
		r.TokensOut = parseAiderNumber(m[2])
	}
	if m := aiderCostRE.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			r.CostUSD = v
		}
	}
	for _, m := range aiderEditRE.FindAllStringSubmatch(s, -1) {
		path := strings.TrimSpace(m[1])
		if path != "" {
			r.FilesChanged = append(r.FilesChanged, path)
		}
	}
	return r
}

// parseAiderNumber accepts plain ints, comma-grouped ints, or decimal
// values with k/K/m/M suffixes. Returns 0 on parse failure.
func parseAiderNumber(s string) int {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	mul := 1.0
	switch s[len(s)-1] {
	case 'k', 'K':
		mul = 1000
		s = s[:len(s)-1]
	case 'm', 'M':
		mul = 1_000_000
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * mul)
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
