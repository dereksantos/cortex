//go:build !windows

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// PiDevHarness drives a single library-service session via the `pi`
// CLI (pi.dev, https://pi.dev). Built as the third harness for the
// Phase 7 cross-harness ablation — together with AiderHarness and
// OpenCodeHarness, three independent agents on the same scenario
// disambiguate cortex-side effects from harness-shape effects.
//
// Prerequisites (the harness does NOT install pi):
//   - `pi` CLI on PATH or pointed at via $PI_BINARY
//   - For OpenRouter models: OPEN_ROUTER_API_KEY (project's underscore
//     form) is re-exported as OPENROUTER_API_KEY in the child env.
//     pi 0.74.0 reads the canonical OPENROUTER_API_KEY directly and
//     ships built-in `openrouter` provider support — no per-host
//     models.json is required.
//
// Event-stream contract: see docs/pidev-events.md. NDJSON, one event
// per line, token+cost rollup on message_end events where
// message.role == "assistant"; file edits on tool_execution_end with
// toolName ∈ {edit, write} and isError == false.
type PiDevHarness struct {
	binary string // path to pi executable
	model  string // e.g., "openrouter/openai/gpt-oss-20b:free"

	// Phase 8 — set per-cell by the grid runner via SetCortexExtensionEnabled.
	// When true, runSession symlinks $CORTEX_PI_EXTENSION_SOURCE into
	// <workdir>/.pi/extensions/cortex/ before invoking pi, and asserts
	// that $CORTEX_BINARY is set in the harness env so the loaded
	// extension can shell out to a known-good cortex binary.
	//
	// When false, no extension install happens — the harness behaves
	// exactly as it did pre-Phase 8 for baseline / cortex (prefix) /
	// frontier strategies.
	cortexExtensionEnabled bool
}

// EnvPiCortexExtensionSource names the env var that must hold an
// absolute path to packages/pi-cortex/ (the extension package root)
// when the cortex_extension strategy is active. The grid runner sets
// this; the harness reads it at install time.
const EnvPiCortexExtensionSource = "CORTEX_PI_EXTENSION_SOURCE"

// EnvCortexProjectRoot tells the spawned `cortex capture` child
// (fired by the extension's tool_result hook) where the project's
// `.cortex/` directory lives. Pi's cwd is the cell's temp workdir
// which has no `.cortex/`, so `cortex capture`'s findProjectRoot
// walk would otherwise fail silently. The harness sets this to the
// directory holding `.cortex/` (typically the cortex repo root).
const EnvCortexProjectRoot = "CORTEX_PROJECT_ROOT"

// SetModel changes the model used for subsequent RunSession calls.
// The grid runner type-asserts on this method to swap models on the
// same harness instance.
func (h *PiDevHarness) SetModel(model string) {
	h.model = model
}

// Model returns the currently configured model string.
func (h *PiDevHarness) Model() string {
	return h.model
}

// SetCortexExtensionEnabled toggles whether RunSession installs the
// pi-cortex extension into the cell's workdir before invoking pi.
// The grid runner calls this per-cell based on cell.ContextStrategy
// — true for StrategyCortexExtension, false for everything else.
//
// Must be reset between cells so a baseline cell following a
// cortex_extension cell doesn't accidentally load the extension.
func (h *PiDevHarness) SetCortexExtensionEnabled(enabled bool) {
	h.cortexExtensionEnabled = enabled
}

// CortexExtensionEnabled reports the current state of the extension
// install flag. Mainly useful for tests.
func (h *PiDevHarness) CortexExtensionEnabled() bool {
	return h.cortexExtensionEnabled
}

// ensurePiCortexExtensionInstalled symlinks the pi-cortex package
// into <workdir>/.pi/extensions/cortex so pi's auto-discovery picks
// it up for the upcoming run. Idempotent — removes any prior
// install in the dest path first. Symlink (rather than copy) is
// used because the package tree is many MB once node_modules is
// included, and the workdir is short-lived.
//
// Errors when $CORTEX_PI_EXTENSION_SOURCE is unset or invalid.
func ensurePiCortexExtensionInstalled(workdir string) error {
	source := os.Getenv(EnvPiCortexExtensionSource)
	if source == "" {
		return fmt.Errorf("$%s must be set for cortex_extension strategy (expected absolute path to packages/pi-cortex/)", EnvPiCortexExtensionSource)
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("$%s must be absolute, got %q", EnvPiCortexExtensionSource, source)
	}
	if info, err := os.Stat(source); err != nil {
		return fmt.Errorf("%s=%s: %w", EnvPiCortexExtensionSource, source, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%s=%s: not a directory", EnvPiCortexExtensionSource, source)
	}
	parent := filepath.Join(workdir, ".pi", "extensions")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("mkdir .pi/extensions: %w", err)
	}
	dest := filepath.Join(parent, "cortex")
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear stale dest %s: %w", dest, err)
	}
	if err := os.Symlink(source, dest); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", dest, source, err)
	}
	return nil
}

// NewPiDevHarness resolves the pi binary (PATH lookup if binary is
// empty, $PI_BINARY override otherwise) and verifies it exists.
//
// model uses the project-wide "<provider>/<model>" convention
// (e.g. "openrouter/openai/gpt-oss-20b:free"); the harness splits it
// into pi's --provider/--model flags at invocation time.
func NewPiDevHarness(binary, model string) (*PiDevHarness, error) {
	resolved, err := resolvePiBinary(binary)
	if err != nil {
		return nil, err
	}
	return &PiDevHarness{binary: resolved, model: model}, nil
}

// RunSession invokes pi non-interactively against workdir with prompt
// as the single message. pi exits when the agent loop terminates
// (no REPL).
//
// Cancellation: honors ctx via SIGTERM to the process group with a 2s
// grace window before SIGKILL — same lifecycle as Aider/OpenCode
// harnesses.
func (h *PiDevHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	_, err := h.runSession(ctx, prompt, workdir)
	return err
}

// RunSessionWithResult is the ResultfulHarness extension. Same
// lifecycle as RunSession; on success it parses the NDJSON event
// stream into a HarnessResult (per docs/pidev-events.md aggregation
// rules).
//
// On error the returned HarnessResult is best-effort (LatencyMs +
// ModelEcho + ProviderEcho only).
func (h *PiDevHarness) RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	return h.runSession(ctx, prompt, workdir)
}

// runSession is the shared implementation.
//
//	pi --mode json --provider <provider> --model <model-without-provider>
//	   -p "<prompt>"
//
// cmd.Dir is set to workdir; pi resolves all relative tool-call paths
// against that, so no --add/--file flag is needed (verified by probe).
func (h *PiDevHarness) runSession(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	provider, modelSlug := splitPiModel(h.model)

	// Phase 8: install the cortex extension into the cell's workdir
	// before invoking pi. Pi's project-local auto-discovery picks it
	// up from .pi/extensions/cortex/. This is gated on the per-cell
	// flag the grid runner sets via SetCortexExtensionEnabled.
	if h.cortexExtensionEnabled {
		if err := ensurePiCortexExtensionInstalled(workdir); err != nil {
			return HarnessResult{ModelEcho: h.model, ProviderEcho: provider},
				fmt.Errorf("install cortex extension: %w", err)
		}
		if os.Getenv("CORTEX_BINARY") == "" {
			return HarnessResult{ModelEcho: h.model, ProviderEcho: provider},
				fmt.Errorf("$CORTEX_BINARY must be set for cortex_extension strategy so the extension can shell out to a known-good binary")
		}
		// Default $CORTEX_PROJECT_ROOT to the harness process's cwd
		// if the caller didn't set it. The extension's tool_result
		// hook reads this to fix the spawn cwd for `cortex capture`
		// (pi's cwd is the cell's temp workdir, which has no
		// .cortex/). This is a fallback; the grid runner should set
		// it explicitly to the directory holding .cortex/.
		if os.Getenv(EnvCortexProjectRoot) == "" {
			if cwd, err := os.Getwd(); err == nil {
				_ = os.Setenv(EnvCortexProjectRoot, cwd)
			}
		}
	}

	args := []string{"--mode", "json"}
	if provider != "" {
		args = append(args, "--provider", provider)
	}
	if modelSlug != "" {
		args = append(args, "--model", modelSlug)
	}
	args = append(args, "-p", prompt)

	cmd := exec.Command(h.binary, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// pi reads OPENROUTER_API_KEY from env. The project env exports
	// OPEN_ROUTER_API_KEY (underscore form). Re-export only when the
	// canonical name isn't already set. CORTEX_BINARY is inherited
	// directly from os.Environ() (no remapping needed).
	if k := os.Getenv("OPEN_ROUTER_API_KEY"); k != "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY="+k)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return HarnessResult{ModelEcho: h.model, ProviderEcho: provider},
			fmt.Errorf("start pi: %w", err)
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
			runErr = fmt.Errorf("pi exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	}

	elapsed := time.Since(start).Milliseconds()
	if runErr != nil {
		return HarnessResult{
			LatencyMs:    elapsed,
			ModelEcho:    h.model,
			ProviderEcho: provider,
		}, runErr
	}

	res := parsePidevStream(stdout.String())
	res.LatencyMs = elapsed
	res.ModelEcho = h.model
	res.ProviderEcho = provider
	return res, nil
}

// splitPiModel converts a "provider/model" combined string into the
// two args pi expects. For "openrouter/openai/gpt-oss-20b:free" this
// returns ("openrouter", "openai/gpt-oss-20b:free"). A string without
// a slash is returned as ("", model) — pi will use its default
// provider (currently `google`); the caller is expected to pass a
// fully qualified provider/model pair, but we don't fail on shorthand.
func splitPiModel(combined string) (provider, model string) {
	if combined == "" {
		return "", ""
	}
	i := strings.Index(combined, "/")
	if i <= 0 {
		return "", combined
	}
	return combined[:i], combined[i+1:]
}

// pidevEvent is the partial schema we extract from each NDJSON line.
// json.Unmarshal silently ignores unknown fields, so unknown event
// types and extra payload keys pass through with zero values.
type pidevEvent struct {
	Type     string `json:"type"`
	ToolName string `json:"toolName"`
	IsError  bool   `json:"isError"`
	Args     struct {
		Path string `json:"path"`
	} `json:"args"`
	Message struct {
		Role  string `json:"role"`
		Usage struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Cost   struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

// parsePidevStream walks NDJSON stdout and produces a HarnessResult.
//
// Aggregation rules (see docs/pidev-events.md):
//   - TokensIn   = Σ message.usage.input        over message_end events with role == "assistant"
//   - TokensOut  = Σ message.usage.output       over same
//   - CostUSD    = Σ message.usage.cost.total   over same
//   - AgentTurnsTotal = count of turn_start events
//   - FilesChanged   = unique args.path from tool_execution_end events
//     where toolName ∈ {edit, write} and isError == false
//
// We use message_end exclusively (not turn_end) — both events carry
// the same usage block for assistant turns, so summing both would
// double-count.
//
// Malformed lines (non-JSON, missing type) are skipped silently.
func parsePidevStream(s string) HarnessResult {
	var r HarnessResult
	seen := map[string]bool{}

	for _, raw := range strings.Split(s, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var ev pidevEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "turn_start":
			r.AgentTurnsTotal++
		case "message_end":
			if ev.Message.Role != "assistant" {
				continue
			}
			r.TokensIn += ev.Message.Usage.Input
			r.TokensOut += ev.Message.Usage.Output
			r.CostUSD += ev.Message.Usage.Cost.Total
		case "tool_execution_end":
			if ev.IsError {
				continue
			}
			switch ev.ToolName {
			case "edit", "write":
				p := strings.TrimSpace(ev.Args.Path)
				if p == "" || seen[p] {
					continue
				}
				seen[p] = true
				r.FilesChanged = append(r.FilesChanged, p)
			}
		}
	}
	return r
}

// resolvePiBinary returns the pi binary path. Resolution:
//  1. explicit `binary` argument (must exist)
//  2. $PI_BINARY env var (must be absolute and exist)
//  3. PATH lookup for `pi`
//
// Mirrors the contract of resolveAiderBinary / resolveOpencodeBinary.
func resolvePiBinary(binary string) (string, error) {
	if binary != "" {
		if _, err := os.Stat(binary); err != nil {
			return "", fmt.Errorf("pi binary not found: %s: %w", binary, err)
		}
		return binary, nil
	}
	if env := os.Getenv("PI_BINARY"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("PI_BINARY must be absolute, got %q", env)
		}
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("PI_BINARY=%s: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("pi")
	if err != nil {
		return "", fmt.Errorf("pi binary not found in PATH (set $PI_BINARY to override)")
	}
	return path, nil
}

// Compile-time interface guards.
var (
	_ Harness          = (*PiDevHarness)(nil)
	_ ResultfulHarness = (*PiDevHarness)(nil)
)
