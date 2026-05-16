// Package commands — `cortex code` ad-hoc coding command.
//
// Drives the same agent loop that the eval framework uses
// (internal/harness via internal/eval/v2.CortexHarness), but bound to
// a user-specified workdir and a single freeform prompt. No scenario
// file, no scoring, no CellResult persistence — pure interactive use.
//
// Safety: --workdir is REQUIRED. Defaulting to cwd would let a typo
// turn this into a rewrite-my-real-project command. The user must
// opt in to a specific directory. Use --init to bootstrap a fresh
// tempdir if you just want to play.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness"
)

func init() {
	Register(&CodeCommand{})
}

// CodeCommand is the ad-hoc interactive coding entry point.
type CodeCommand struct{}

// Name returns the command name.
func (c *CodeCommand) Name() string { return "code" }

// Description returns the command description.
func (c *CodeCommand) Description() string {
	return "Run the Cortex coding harness against a workdir (requires --workdir and --model)"
}

// Execute parses flags and runs one session.
func (c *CodeCommand) Execute(ctx *Context) error {
	model := ""
	workdir := ""
	initFresh := false
	maxTurns := 0
	maxCostStr := ""
	maxCumulativeTokens := 0
	maxOutputTokens := 0
	apiURL := ""
	verbose := false
	quiet := false

	args := ctx.Args
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "-w", "--workdir":
			if i+1 < len(args) {
				workdir = args[i+1]
				i++
			}
		case "--init":
			initFresh = true
		case "--max-turns":
			if i+1 < len(args) {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--max-turns: %w", err)
				}
				maxTurns = n
				i++
			}
		case "--max-cost":
			if i+1 < len(args) {
				maxCostStr = args[i+1]
				i++
			}
		case "--max-tokens", "--max-cumulative-tokens":
			if i+1 < len(args) {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--max-cumulative-tokens: %w", err)
				}
				maxCumulativeTokens = n
				i++
			}
		case "--max-output", "--max-output-tokens":
			if i+1 < len(args) {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					return fmt.Errorf("--max-output: %w", err)
				}
				maxOutputTokens = n
				i++
			}
		case "--api-url", "--local":
			if args[i] == "--local" {
				// Convenience: --local is shorthand for the standard
				// Ollama OpenAI-compatible endpoint.
				apiURL = "http://localhost:11434/v1/chat/completions"
			} else if i+1 < len(args) {
				apiURL = args[i+1]
				i++
			}
		case "-v", "--verbose":
			verbose = true
		case "-q", "--quiet":
			quiet = true
		case "-h", "--help":
			printCodeHelp()
			return nil
		default:
			// Anything that doesn't look like a flag becomes the
			// prompt. We accept either a single positional arg
			// (typically quoted) or multiple words joined with
			// spaces.
		}
	}

	// Collect positional args as the prompt. Done separately so a
	// `cortex code --model X -- "do the thing"` invocation works,
	// and so flags can appear before or after the prompt.
	var promptParts []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch a {
		case "-m", "--model", "-w", "--workdir", "--max-turns", "--max-cost",
			"--max-tokens", "--max-cumulative-tokens",
			"--max-output", "--max-output-tokens",
			"--api-url":
			skipNext = true
			continue
		case "--init", "-v", "--verbose", "-q", "--quiet", "-h", "--help", "--local":
			continue
		case "--":
			continue
		}
		promptParts = append(promptParts, a)
	}
	prompt := strings.TrimSpace(strings.Join(promptParts, " "))

	if prompt == "" {
		printCodeHelp()
		return fmt.Errorf("missing prompt")
	}
	if model == "" {
		return fmt.Errorf("--model is required (e.g. anthropic/claude-haiku-4.5, qwen/qwen3-coder, openai/gpt-oss-20b:free)")
	}
	if workdir == "" {
		return fmt.Errorf("--workdir is required (use --init to create a fresh tempdir)")
	}

	resolvedWorkdir, err := resolveCodeWorkdir(workdir, initFresh)
	if err != nil {
		return err
	}

	maxCost := 0.0
	if maxCostStr != "" {
		v, err := strconv.ParseFloat(maxCostStr, 64)
		if err != nil {
			return fmt.Errorf("--max-cost: %w", err)
		}
		maxCost = v
	}

	h, err := evalv2.NewCortexHarness(model)
	if err != nil {
		return fmt.Errorf("init harness: %w", err)
	}
	if maxTurns > 0 {
		h.SetMaxTurns(maxTurns)
	}
	if maxCumulativeTokens > 0 || maxCost > 0 {
		b := harness.Budget{MaxCumulativeTokens: maxCumulativeTokens, MaxCostUSD: maxCost}
		h.SetBudget(b)
	}
	if maxOutputTokens > 0 {
		h.SetMaxOutputTokens(maxOutputTokens)
	}
	if apiURL != "" {
		h.SetAPIURL(apiURL)
	}
	if !quiet {
		h.SetNotify(makeCodeNotifier(verbose))
	}

	fmt.Printf("[cortex code] workdir: %s\n", resolvedWorkdir)
	fmt.Printf("[cortex code] model:   %s\n", model)
	if apiURL != "" {
		fmt.Printf("[cortex code] api-url: %s\n", apiURL)
	}
	fmt.Println("[cortex code] (Ctrl-C to stop; transcript at <workdir>/.cortex/journal/coding/)")
	fmt.Println()

	hr, err := h.RunSessionWithResult(context.Background(), prompt, resolvedWorkdir)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	loopRes := h.LastLoopResult()
	fmt.Println()
	fmt.Printf("[cortex code] turns=%d tokens=%d/%d cost=$%.4f latency=%dms reason=%s\n",
		hr.AgentTurnsTotal, hr.TokensIn, hr.TokensOut, hr.CostUSD, hr.LatencyMs, loopRes.Reason)
	if len(hr.FilesChanged) > 0 {
		fmt.Printf("[cortex code] files written: %s\n", strings.Join(hr.FilesChanged, ", "))
	}
	if loopRes.Final != "" {
		fmt.Println()
		fmt.Println("--- final ---")
		fmt.Println(loopRes.Final)
	}
	return nil
}

// resolveCodeWorkdir returns the absolute workdir path. If initFresh
// is set, it creates a fresh tempdir using --workdir as a name hint
// (sanitized). Otherwise the provided path must exist.
func resolveCodeWorkdir(workdir string, initFresh bool) (string, error) {
	if initFresh {
		pattern := fmt.Sprintf("cortex-code-%s-*", sanitizeCodeName(filepath.Base(workdir)))
		dir, err := os.MkdirTemp("", pattern)
		if err != nil {
			return "", fmt.Errorf("mkdtemp: %w", err)
		}
		// Also lay down a minimal go.mod so `go build` works without
		// the model having to scaffold it. Users running --init are
		// implicitly opting into "give me a Go playground".
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module workdir\n\ngo 1.22\n"), 0o644); err != nil {
			return "", fmt.Errorf("write go.mod: %w", err)
		}
		return dir, nil
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("abs path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workdir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %s is not a directory", abs)
	}
	return abs, nil
}

// makeCodeNotifier returns a callback that prints one human-readable
// line per loop event to stdout. verbose=true includes argument /
// result excerpts; false stays concise (one line per turn + tool
// call).
func makeCodeNotifier(verbose bool) func(string, any) {
	return func(kind string, payload any) {
		switch kind {
		case "coding.session_start":
			p := mapOf(payload)
			fmt.Printf("→ starting session (max_turns=%v max_cumulative_tokens=%v max_cost=$%.2f num_tools=%v)\n",
				p["max_turns"], p["max_cumulative_tokens"], asFloat(p["max_cost"]), p["num_tools"])
		case "coding.turn":
			p := mapOf(payload)
			fmt.Printf("\n— turn %v — finish=%v tokens=%v/%v cum=%v/%v cost=$%.4f calls=%v\n",
				p["turn"], p["finish_reason"],
				p["tokens_in"], p["tokens_out"],
				p["cumulative_in"], p["cumulative_out"],
				asFloat(p["cumulative_usd"]),
				p["tool_calls"])
		case "coding.tool_call":
			p := mapOf(payload)
			argsStr, _ := p["args"].(string)
			if !verbose && len(argsStr) > 120 {
				argsStr = argsStr[:120] + "…"
			}
			fmt.Printf("  → %v(%s)\n", p["name"], argsStr)
		case "coding.tool_result":
			p := mapOf(payload)
			if verbose {
				fmt.Printf("    (result: %v chars)\n", p["output_chars"])
			}
		case "coding.final":
			p := mapOf(payload)
			fmt.Printf("\n✓ model done at turn %v (%v chars of final content)\n",
				p["turn"], p["content"].(string)[:minInt(80, len(p["content"].(string)))])
		case "coding.turn_limit":
			fmt.Printf("\n⚠ turn limit hit\n")
		case "coding.budget_exceeded":
			p := mapOf(payload)
			fmt.Printf("\n⚠ budget exceeded (cumulative_tokens=%v/%v cost=$%.4f/$%.2f)\n",
				p["cumulative_tokens"], p["cap_tokens"], asFloat(p["cost_usd"]), asFloat(p["cap_cost"]))
		case "coding.error":
			p := mapOf(payload)
			fmt.Printf("\n⚠ provider error on turn %v: %v\n", p["turn"], p["error"])
		}
	}
}

// mapOf normalizes payload (which is `any`) into a map. The Loop
// passes map[string]any; if some future caller passes a struct we'd
// need json round-trip, but for now this is safe.
func mapOf(payload any) map[string]any {
	if m, ok := payload.(map[string]any); ok {
		return m
	}
	// Defensive: serialize → deserialize so unknown shapes still
	// produce something printable.
	bb, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(bb, &m)
	return m
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sanitizeCodeName mirrors the eval-side sanitizer for tempdir names.
func sanitizeCodeName(s string) string {
	if s == "" {
		return "session"
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func printCodeHelp() {
	fmt.Println(`Usage: cortex code [flags] <prompt>

Runs the Cortex coding harness (file ops + shell + cortex_search) against a
workdir using a single OpenRouter model. Streams progress to stdout.

Required:
  -m, --model NAME      OpenRouter model id (e.g. anthropic/claude-haiku-4.5,
                        qwen/qwen3-coder, openai/gpt-oss-20b:free)
  -w, --workdir DIR     Working directory the agent owns. Use --init to
                        create a fresh tempdir.

Optional:
  --init                          Treat --workdir as a *name hint* and create a fresh
                                  tempdir with a minimal go.mod. Use for ad-hoc plays.
  --max-turns N                   Cap iterations (default 25).
  --max-cumulative-tokens N       Cap sum of input+output tokens across all turns
                                  (default 300000). Aliased as --max-tokens for
                                  back-compat.
  --max-output N                  Cap per-turn output tokens (overrides the
                                  model-id-based default; e.g. 16000 for
                                  Claude, 4000 for gpt-oss-20b).
  --max-cost USD                  Cap cumulative cost (default 0.20).
  -v, --verbose                   Print tool arguments and result sizes.
  -q, --quiet                     No live stream; only final summary.

API key: read from macOS Keychain entry "cortex-openrouter" first, falling
back to OPEN_ROUTER_API_KEY env var.

Examples:
  cortex code --init -w gol -m anthropic/claude-haiku-4.5 \
    "implement Conway's Game of Life as a Go CLI"

  cortex code -w ./myproject -m qwen/qwen3-coder \
    "add a /healthz handler to internal/http/server.go"`)
}
