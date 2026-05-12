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
}

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
	// canonical name isn't already set.
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
