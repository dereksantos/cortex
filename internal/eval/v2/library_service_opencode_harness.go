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

// OpenCodeHarness drives a single library-service session via the
// `opencode` CLI (https://opencode.ai). It exists alongside AiderHarness
// so the eval can run the same scenario through a different agent
// surface — the cross-harness ablation (Phase 7) needs at least two
// independent harnesses to disambiguate "Cortex helped" from "this
// particular CLI's prompt shape works well".
//
// Prerequisites (the harness does NOT install opencode):
//   - `opencode` CLI on PATH or pointed at via $OPENCODE_BINARY
//   - For OpenRouter models: OPEN_ROUTER_API_KEY (project's underscore
//     form) is re-exported as OPENROUTER_API_KEY for the child env.
//     The opencode SDK does NOT auto-detect OPEN_ROUTER_API_KEY.
//
// Event-stream contract: see docs/opencode-tiers.md for the schema we
// parse. NDJSON, one event per line, per-step token/cost rollup on
// `step_finish` events that must be summed.
type OpenCodeHarness struct {
	binary string // path to opencode executable
	model  string // e.g., "openrouter/openai/gpt-oss-20b:free"
}

// SetModel changes the model used for subsequent RunSession calls.
// The grid runner type-asserts on this method to re-point one harness
// instance across multiple model cells without constructing a new
// OpenCodeHarness per cell.
func (h *OpenCodeHarness) SetModel(model string) {
	h.model = model
}

// Model returns the currently configured model string.
func (h *OpenCodeHarness) Model() string {
	return h.model
}

// NewOpenCodeHarness resolves the opencode binary (PATH lookup if binary
// is empty, $OPENCODE_BINARY override otherwise) and verifies it exists.
// A missing binary is a hard error.
//
// model may be any string opencode accepts via --model. The convention
// is "<provider>/<model>" (e.g. "openrouter/openai/gpt-oss-20b:free").
func NewOpenCodeHarness(binary, model string) (*OpenCodeHarness, error) {
	resolved, err := resolveOpencodeBinary(binary)
	if err != nil {
		return nil, err
	}
	return &OpenCodeHarness{binary: resolved, model: model}, nil
}

// RunSession invokes opencode non-interactively against workdir with
// prompt as the single message. opencode's `run` subcommand exits when
// the model stops emitting tool calls (no REPL).
//
// Cancellation: honors ctx via SIGTERM to the process group with a 2s
// grace window before SIGKILL — same lifecycle as AiderHarness.
func (h *OpenCodeHarness) RunSession(ctx context.Context, prompt, workdir string) error {
	_, err := h.runSession(ctx, prompt, workdir)
	return err
}

// RunSessionWithResult is the ResultfulHarness extension. Same lifecycle
// as RunSession; on success it parses the NDJSON event stream into a
// HarnessResult (tokens summed across step_finish events, cost summed,
// files_changed collected from edit/write tool_use events).
//
// On error the returned HarnessResult is best-effort
// (LatencyMs + ModelEcho + ProviderEcho only).
func (h *OpenCodeHarness) RunSessionWithResult(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	return h.runSession(ctx, prompt, workdir)
}

// runSession is the shared implementation. Per docs/opencode-tiers.md:
//   - `--dir <workdir>` is sufficient to expose workdir files to the
//     model; no per-file flag is needed (contrast with AiderHarness'
//     --file globbing).
//   - `--format json` gives NDJSON events on stdout.
func (h *OpenCodeHarness) runSession(ctx context.Context, prompt, workdir string) (HarnessResult, error) {
	args := []string{
		"run",
		"--format", "json",
		"--dir", workdir,
	}
	if h.model != "" {
		args = append(args, "--model", h.model)
	}
	args = append(args, prompt)

	cmd := exec.Command(h.binary, args...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// opencode's OpenRouter integration reads OPENROUTER_API_KEY. Our
	// project env exports the underscore form OPEN_ROUTER_API_KEY.
	// Re-export only when the canonical name isn't already set.
	if k := os.Getenv("OPEN_ROUTER_API_KEY"); k != "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY="+k)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return HarnessResult{ModelEcho: h.model, ProviderEcho: opencodeProviderFromModel(h.model)},
			fmt.Errorf("start opencode: %w", err)
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
			runErr = fmt.Errorf("opencode exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
	}

	elapsed := time.Since(start).Milliseconds()
	if runErr != nil {
		return HarnessResult{
			LatencyMs:    elapsed,
			ModelEcho:    h.model,
			ProviderEcho: opencodeProviderFromModel(h.model),
		}, runErr
	}

	res := parseOpencodeStream(stdout.String())
	res.LatencyMs = elapsed
	res.ModelEcho = h.model
	res.ProviderEcho = opencodeProviderFromModel(h.model)
	return res, nil
}

// opencodeProviderFromModel pulls the provider segment from opencode's
// "<provider>/<model>" convention. Returns "" if absent. For models
// like "openrouter/openai/gpt-oss-20b:free" this returns "openrouter"
// (the routing layer), not the underlying provider.
func opencodeProviderFromModel(model string) string {
	if i := strings.Index(model, "/"); i > 0 {
		return model[:i]
	}
	return ""
}

// opencodeEvent is the partial schema we extract from each NDJSON line.
// We use json.Unmarshal into a strongly-typed struct rather than walking
// a map[string]any so unknown fields are silently ignored. Unknown event
// `type` values are also non-fatal — they pass through with default-zero
// fields and contribute nothing to the result.
type opencodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Tool   string `json:"tool"`   // present on tool_use
		Reason string `json:"reason"` // present on step_finish
		Cost   *float64 `json:"cost"` // pointer so missing != 0
		Tokens struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
		} `json:"tokens"`
		State struct {
			Status string `json:"status"`
			Input  struct {
				FilePath string `json:"filePath"`
			} `json:"input"`
		} `json:"state"`
	} `json:"part"`
}

// parseOpencodeStream walks NDJSON stdout and produces a HarnessResult.
//
// Aggregation rules (see docs/opencode-tiers.md):
//   - TokensIn  = Σ part.tokens.input  over step_finish events
//   - TokensOut = Σ part.tokens.output over step_finish events
//   - CostUSD   = Σ part.cost          over step_finish events
//   - AgentTurnsTotal = count of step_start events (closer to "model
//     turns" than step_finish, which can be missing on the final step)
//   - FilesChanged = unique part.state.input.filePath from tool_use
//     events where tool ∈ {edit, write} and state.status == "completed".
//     Tool == "invalid" is excluded (model hallucinated tool name).
//
// Malformed lines (non-JSON, missing type) are skipped silently — the
// stream may have a non-JSON banner / trailer in edge cases and we
// don't want to fail the whole session on a single bad line.
func parseOpencodeStream(s string) HarnessResult {
	var r HarnessResult
	seen := map[string]bool{}

	for _, raw := range strings.Split(s, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var ev opencodeEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "step_start":
			r.AgentTurnsTotal++
		case "step_finish":
			r.TokensIn += ev.Part.Tokens.Input
			r.TokensOut += ev.Part.Tokens.Output
			if ev.Part.Cost != nil {
				r.CostUSD += *ev.Part.Cost
			}
		case "tool_use":
			if ev.Part.State.Status != "completed" {
				continue
			}
			switch ev.Part.Tool {
			case "edit", "write":
				p := strings.TrimSpace(ev.Part.State.Input.FilePath)
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

// resolveOpencodeBinary returns the opencode binary path. Resolution:
//  1. explicit `binary` argument (must exist)
//  2. $OPENCODE_BINARY env var (must be absolute and exist)
//  3. PATH lookup for `opencode`
//
// Mirrors resolveAiderBinary's contract so the eval driver can pin a
// specific install without it being on PATH.
func resolveOpencodeBinary(binary string) (string, error) {
	if binary != "" {
		if _, err := os.Stat(binary); err != nil {
			return "", fmt.Errorf("opencode binary not found: %s: %w", binary, err)
		}
		return binary, nil
	}
	if env := os.Getenv("OPENCODE_BINARY"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("OPENCODE_BINARY must be absolute, got %q", env)
		}
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("OPENCODE_BINARY=%s: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("opencode")
	if err != nil {
		return "", fmt.Errorf("opencode binary not found in PATH (set $OPENCODE_BINARY to override)")
	}
	return path, nil
}

// Compile-time interface guards. If OpenCodeHarness ever stops
// satisfying either contract, the build breaks here rather than at the
// grid runner's type assertion.
var (
	_ Harness          = (*OpenCodeHarness)(nil)
	_ ResultfulHarness = (*OpenCodeHarness)(nil)
)
