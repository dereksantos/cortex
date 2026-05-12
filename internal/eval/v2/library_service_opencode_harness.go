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
	"regexp"
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

	// Phase 8 — set per-cell by the grid runner via SetCortexExtensionEnabled.
	// When true, runSession symlinks $CORTEX_OPENCODE_PLUGIN_SOURCE into
	// <workdir>/.opencode/plugins/cortex.ts before invoking opencode, and
	// asserts that $CORTEX_BINARY is set in the harness env so the loaded
	// plugin can shell out to a known-good cortex binary.
	//
	// When false, no install happens — the harness behaves exactly as it
	// did pre-Phase 8 for baseline / cortex (prefix) / frontier strategies.
	cortexExtensionEnabled bool
}

// EnvOpencodeCortexPluginSource names the env var that must hold an
// absolute path to the opencode-cortex plugin file
// (packages/opencode-cortex/plugins/cortex.ts) when the cortex_extension
// strategy is active. The grid runner sets this; the harness reads it
// at install time.
//
// Differs from pi-cortex (CORTEX_PI_EXTENSION_SOURCE) in that it points
// at a single .ts FILE — opencode auto-discovers plugins from
// .opencode/plugins/*.{ts,js} as flat files, not package subdirs.
//
// $CORTEX_PROJECT_ROOT is shared with the pi-dev harness (defined as
// EnvCortexProjectRoot in library_service_pidev_harness.go) so plugins
// can locate the project's .cortex/ directory regardless of opencode's
// per-session cwd.
const EnvOpencodeCortexPluginSource = "CORTEX_OPENCODE_PLUGIN_SOURCE"

// EnvOpencodeCortexExtension is the rollback gate. Per the pi-extension
// close report (docs/phase8-close-report.md "Rollback if regression"),
// the cortex_extension strategy ships default-OFF when the cell-level
// eval shows a pass-rate regression vs baseline. The opencode-cortex
// integration regressed -40pp on test/evals/coding × gpt-oss-20b:free
// (see docs/phase8-opencode-extension-vs-prefix.md), so the install
// is gated behind this env var even when the per-cell flag is true.
//
// Set to literal "1" to enable. Anything else (unset, "true", "yes",
// empty, …) keeps the gate closed — predictable bootstrapping is more
// important than convenience here.
//
// Reviewers/operators flip this on after seeding the cortex store with
// scenario-relevant context; default-off avoids the regression's blast
// radius on day one of merge.
const EnvOpencodeCortexExtension = "CORTEX_OPENCODE_EXTENSION"

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

// SetCortexExtensionEnabled toggles whether RunSession installs the
// opencode-cortex plugin into the cell's workdir before invoking
// opencode. The grid runner calls this per-cell based on
// cell.ContextStrategy — true for StrategyCortexExtension, false for
// everything else.
//
// Must be reset between cells so a baseline cell following a
// cortex_extension cell doesn't accidentally load the plugin.
func (h *OpenCodeHarness) SetCortexExtensionEnabled(enabled bool) {
	h.cortexExtensionEnabled = enabled
}

// CortexExtensionEnabled reports the current state of the plugin
// install flag. Mainly useful for tests.
func (h *OpenCodeHarness) CortexExtensionEnabled() bool {
	return h.cortexExtensionEnabled
}

// ensureOpencodeCortexPluginInstalled symlinks the opencode-cortex
// plugin file into <workdir>/.opencode/plugins/cortex.ts so opencode's
// auto-discovery (Glob.scan("{plugin,plugins}/*.{ts,js}")) picks it up
// for the upcoming run. Idempotent — removes any prior install in the
// dest path first. Symlink (rather than copy) is used so the package's
// node_modules tree doesn't have to be re-resolved per cell.
//
// Differs from pi-cortex's installer in that the source points at a
// single .ts FILE; opencode plugins are flat, not package subdirs.
//
// Errors when $CORTEX_OPENCODE_PLUGIN_SOURCE is unset, relative, or
// does not point at a regular file.
func ensureOpencodeCortexPluginInstalled(workdir string) error {
	source := os.Getenv(EnvOpencodeCortexPluginSource)
	if source == "" {
		return fmt.Errorf("$%s must be set for cortex_extension strategy (expected absolute path to packages/opencode-cortex/plugins/cortex.ts)", EnvOpencodeCortexPluginSource)
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("$%s must be absolute, got %q", EnvOpencodeCortexPluginSource, source)
	}
	if info, err := os.Stat(source); err != nil {
		return fmt.Errorf("%s=%s: %w", EnvOpencodeCortexPluginSource, source, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s=%s: not a regular file (opencode plugins are flat .ts files, not directories)", EnvOpencodeCortexPluginSource, source)
	}
	parent := filepath.Join(workdir, ".opencode", "plugins")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("mkdir .opencode/plugins: %w", err)
	}
	dest := filepath.Join(parent, "cortex.ts")
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear stale dest %s: %w", dest, err)
	}
	if err := os.Symlink(source, dest); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", dest, source, err)
	}
	return nil
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
	// Phase 8: install the opencode-cortex plugin into the cell's workdir
	// before invoking opencode. opencode's project-local auto-discovery
	// picks it up from .opencode/plugins/cortex.ts. Gated on BOTH:
	//   1) the per-cell flag the grid runner sets via
	//      SetCortexExtensionEnabled (true for cortex_extension strategy)
	//   2) the env rollback gate $CORTEX_OPENCODE_EXTENSION="1"
	//      (pi-extension precedent: docs/phase8-close-report.md;
	//      regression details in docs/phase8-opencode-extension-vs-prefix.md)
	//
	// Both must be true. If the per-cell flag is true but the env gate
	// is closed, the harness behaves as baseline (no install, normal
	// opencode run). This is how the regressing integration ships
	// safely: opt-in for operators who have seeded their cortex store.
	if h.cortexExtensionEnabled && os.Getenv(EnvOpencodeCortexExtension) == "1" {
		if err := ensureOpencodeCortexPluginInstalled(workdir); err != nil {
			return HarnessResult{ModelEcho: h.model, ProviderEcho: opencodeProviderFromModel(h.model)},
				fmt.Errorf("install cortex plugin: %w", err)
		}
		if os.Getenv("CORTEX_BINARY") == "" {
			return HarnessResult{ModelEcho: h.model, ProviderEcho: opencodeProviderFromModel(h.model)},
				fmt.Errorf("$CORTEX_BINARY must be set for cortex_extension strategy so the plugin can shell out to a known-good binary")
		}
		// Default $CORTEX_PROJECT_ROOT to the harness process's cwd if
		// the caller didn't set it. The plugin's tool.execute.after
		// hook reads this to fix the spawn cwd for `cortex capture`
		// (opencode's per-session cwd may not contain .cortex/). This
		// is a fallback; the grid runner should set it explicitly to
		// the directory holding .cortex/.
		if os.Getenv(EnvCortexProjectRoot) == "" {
			if cwd, err := os.Getwd(); err == nil {
				_ = os.Setenv(EnvCortexProjectRoot, cwd)
			}
		}
	}

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

	// Fallback: when the model replies without invoking any tools,
	// opencode emits step_start + text and exits *without* a closing
	// step_finish — so the live-stream parser sums zero tokens. The
	// session data is still in opencode's local DB and queryable via
	// `opencode export <sessionID>`. We backfill from there when the
	// stream gave us nothing. Best-effort: a failure leaves zeros.
	if res.TokensIn == 0 && res.TokensOut == 0 {
		if sid := extractOpencodeSessionID(stdout.String()); sid != "" {
			if fb, ferr := h.exportSessionStats(ctx, sid); ferr == nil {
				res.TokensIn = fb.TokensIn
				res.TokensOut = fb.TokensOut
				res.CostUSD = fb.CostUSD
				if res.AgentTurnsTotal == 0 {
					res.AgentTurnsTotal = fb.AgentTurnsTotal
				}
			}
		}
	}

	res.LatencyMs = elapsed
	res.ModelEcho = h.model
	res.ProviderEcho = opencodeProviderFromModel(h.model)
	return res, nil
}

// opencodeSessionIDRE matches the first `"sessionID":"ses_..."` in the
// NDJSON event stream. Every event carries this top-level field so we
// pick whichever appears first.
var opencodeSessionIDRE = regexp.MustCompile(`"sessionID":"(ses_[^"]+)"`)

// extractOpencodeSessionID returns the first session ID emitted in the
// event stream, or "" if none. Used by the export-fallback path when
// the stream lacks a step_finish event to read tokens from.
func extractOpencodeSessionID(stream string) string {
	m := opencodeSessionIDRE.FindStringSubmatch(stream)
	if m == nil {
		return ""
	}
	return m[1]
}

// exportSessionStats runs `opencode export <sessionID>` and parses the
// JSON envelope for per-assistant-message token/cost totals. Returns
// the summed HarnessResult (TokensIn/Out, CostUSD, AgentTurnsTotal
// derived from the number of assistant messages).
//
// Output shape (opencode 1.14.46):
//
//	Exporting session: ses_...
//	{
//	  "info": { ... },
//	  "messages": [
//	    {"info": {"role": "user", ...}},
//	    {"info": {"role": "assistant", "tokens": {...}, "cost": 0, ...}}
//	  ]
//	}
//
// The first stdout line is the human-readable "Exporting session: <id>"
// banner; we strip it before json.Unmarshal.
func (h *OpenCodeHarness) exportSessionStats(ctx context.Context, sessionID string) (HarnessResult, error) {
	cmd := exec.CommandContext(ctx, h.binary, "export", sessionID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return HarnessResult{}, fmt.Errorf("opencode export %s: %w (stderr: %s)",
			sessionID, err, strings.TrimSpace(stderr.String()))
	}
	return parseOpencodeExport(stdout.String())
}

// parseOpencodeExport handles the export-output parsing. Split out so a
// unit test can pin the shape without spawning a real opencode process.
func parseOpencodeExport(raw string) (HarnessResult, error) {
	// Drop the "Exporting session: ..." banner. Find the first '{' to
	// be tolerant of an empty banner or a different banner format in
	// future opencode versions.
	i := strings.Index(raw, "{")
	if i < 0 {
		return HarnessResult{}, fmt.Errorf("opencode export: no JSON envelope")
	}
	var env struct {
		Messages []struct {
			Info struct {
				Role   string `json:"role"`
				Tokens struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"tokens"`
				Cost *float64 `json:"cost"`
			} `json:"info"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw[i:]), &env); err != nil {
		return HarnessResult{}, fmt.Errorf("opencode export: parse envelope: %w", err)
	}
	var r HarnessResult
	for _, m := range env.Messages {
		if m.Info.Role != "assistant" {
			continue
		}
		r.AgentTurnsTotal++
		r.TokensIn += m.Info.Tokens.Input
		r.TokensOut += m.Info.Tokens.Output
		if m.Info.Cost != nil {
			r.CostUSD += *m.Info.Cost
		}
	}
	return r, nil
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
		Tool   string   `json:"tool"`   // present on tool_use
		Reason string   `json:"reason"` // present on step_finish
		Cost   *float64 `json:"cost"`   // pointer so missing != 0
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
