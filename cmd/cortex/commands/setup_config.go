package commands

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	projreg "github.com/dereksantos/cortex/pkg/registry"
)

// SetupCommand writes a model config — ~/.cortex/config.json (user) by default,
// or ./.cortex/config.json (project) with --project — by detecting the
// environment and mapping a backend choice to a deterministic config block.
// Mechanical by design: env vars + TCP probes only, no keychain, no network
// auth magic. The dev's own LLM authors the hard choices (see `cortex setup
// --prompt`, forthcoming); this tool just lands a correct, valid config.
type SetupCommand struct{}

func init() { Register(&SetupCommand{}) }

// Name returns the command name.
func (c *SetupCommand) Name() string { return "setup" }

// Description returns the command description.
func (c *SetupCommand) Description() string {
	return "Configure Cortex model bindings (user-level by default; --project to scope to one repo)"
}

// DescribeFlags surfaces setup's flag set into tools.json.
func (c *SetupCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.Bool("free", false, "Use OpenRouter free-tier models (no cost; prompts leave your machine)")
	fs.Bool("project", false, "Write project config (./.cortex/config.json) instead of user config")
	fs.String("backend", "", "Backend to write: free | openrouter | local (skips the prompt)")
	fs.String("code", "", "Pin the code (agent) model")
	fs.String("study", "", "Pin the study model")
	fs.Bool("yes", false, "Non-interactive: take detected defaults, overwrite without asking")
}

const (
	orEndpoint      = "https://openrouter.ai/api/v1"
	litellmEndpoint = "http://127.0.0.1:4000"
	ollamaEndpoint  = "http://127.0.0.1:11434"
	litellmAddr     = "127.0.0.1:4000"
	ollamaAddr      = "127.0.0.1:11434"
	orKeyEnv        = "OPENROUTER_API_KEY"
	anthropicKeyEnv = "ANTHROPIC_API_KEY"

	// Free, tool-capable defaults — see `cortex setup --prompt` / docs for the
	// live list. code needs tool-calling; study wants long context.
	freeCodeModel  = "qwen/qwen3-coder:free"
	freeStudyModel = "openai/gpt-oss-20b:free"
)

// cfgFile is the JSON the loop reads. It mirrors cmd/loop's Config (a separate
// package, so duplicated here as a serialization contract, not an import).
type cfgFile struct {
	Backend cfgBackend          `json:"backend"`
	Models  map[string]cfgModel `json:"models,omitempty"`
}

type cfgBackend struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	KeyEnv   string `json:"key_env,omitempty"`
}

type cfgModel struct {
	Model string `json:"model"`
}

// detected is what the environment probe found. Booleans only — key VALUES are
// never read, just presence.
type detected struct {
	ollama        bool
	litellm       bool
	openRouterKey bool
	anthropicKey  bool
}

// detectEnv probes the conventional local ports and scans for provider keys in
// the environment. probe is injected so the detection is testable without real
// sockets.
func detectEnv(probe func(addr string) bool) detected {
	return detected{
		ollama:        probe(ollamaAddr),
		litellm:       probe(litellmAddr),
		openRouterKey: strings.TrimSpace(os.Getenv(orKeyEnv)) != "",
		anthropicKey:  strings.TrimSpace(os.Getenv(anthropicKeyEnv)) != "",
	}
}

// probeTCP reports whether something is listening at addr within a short
// timeout. A refused/timed-out dial means "not running", never an error.
func probeTCP(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// freeConfig is the zero-cost OpenRouter bootstrap.
func freeConfig() cfgFile {
	return cfgFile{
		Backend: cfgBackend{Type: "openrouter", Endpoint: orEndpoint, KeyEnv: orKeyEnv},
		Models:  map[string]cfgModel{"code": {freeCodeModel}, "study": {freeStudyModel}},
	}
}

// openRouterConfig points at OpenRouter with the dev's own key. Models are
// pinned only when supplied — an empty Models map is valid (the dev can edit).
func openRouterConfig(code, study string) cfgFile {
	return cfgFile{
		Backend: cfgBackend{Type: "openrouter", Endpoint: orEndpoint, KeyEnv: orKeyEnv},
		Models:  pinned(code, study),
	}
}

// litellmConfig points at a local LiteLLM proxy. Models are optional: LiteLLM
// serves /model/info, so the loop discovers them when unpinned.
func litellmConfig(code, study string) cfgFile {
	return cfgFile{
		Backend: cfgBackend{Type: "litellm", Endpoint: litellmEndpoint},
		Models:  pinned(code, study),
	}
}

// ollamaConfig points at a local Ollama server. Ollama has no discovery
// endpoint, so models must be pinned (auto-filled from /api/tags when possible).
func ollamaConfig(code, study string) cfgFile {
	return cfgFile{
		Backend: cfgBackend{Type: "ollama", Endpoint: ollamaEndpoint},
		Models:  pinned(code, study),
	}
}

// pinned builds the per-role model map, omitting roles with no model so the
// result is a clean, minimal config.
func pinned(code, study string) map[string]cfgModel {
	m := map[string]cfgModel{}
	if code != "" {
		m["code"] = cfgModel{code}
	}
	if study != "" {
		m["study"] = cfgModel{study}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// pickOllamaModels chooses sensible code/study bindings from installed Ollama
// tags: a coder-tagged model for the agent, an instruct/general model for
// study, falling back to the first tag. Deterministic given the tag order.
func pickOllamaModels(tags []string) (code, study string) {
	for _, t := range tags {
		if code == "" && strings.Contains(strings.ToLower(t), "cod") {
			code = t
		}
		lt := strings.ToLower(t)
		if study == "" && (strings.Contains(lt, "instruct") || strings.Contains(lt, "qwen")) && !strings.Contains(lt, "cod") {
			study = t
		}
	}
	if code == "" && len(tags) > 0 {
		code = tags[0]
	}
	if study == "" {
		study = code
	}
	return code, study
}

// ollamaTags lists installed Ollama models via /api/tags. get is injected for
// testing. Returns nil (not an error) when Ollama is unreachable.
func ollamaTags(endpoint string, get func(url string) ([]byte, error)) []string {
	body, err := get(strings.TrimRight(endpoint, "/") + "/api/tags")
	if err != nil {
		return nil
	}
	var r struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	names := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		names = append(names, m.Name)
	}
	return names
}

// httpGet fetches a URL with a short timeout — the production ollamaTags fetcher.
func httpGet(url string) ([]byte, error) {
	cl := &http.Client{Timeout: 2 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// writeTo writes the config as pretty JSON, creating the parent dir.
func (f cfgFile) writeTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// userCfgPath mirrors the loop's resolution: CORTEX_HOME overrides, else
// ~/.cortex/config.json — so setup writes exactly where the loop reads.
func userCfgPath() string {
	if h := os.Getenv("CORTEX_HOME"); h != "" {
		return filepath.Join(h, "config.json")
	}
	return filepath.Join(projreg.GlobalDir(), "config.json")
}

// projectCfgPath is ./.cortex/config.json under the working directory.
func projectCfgPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, ".cortex", "config.json"), nil
}

// chooseBackend prints the available backends (gated by what was detected) and
// reads a numbered selection. Returns the chosen backend key.
func chooseBackend(det detected, in *bufio.Reader, out io.Writer) (string, error) {
	type opt struct{ key, label string }
	opts := []opt{{"free", "OpenRouter free tier — zero cost (prompts leave your machine)"}}
	if det.litellm {
		opts = append(opts, opt{"local-litellm", "Local LiteLLM (:4000) — private, recommended"})
	}
	if det.ollama {
		opts = append(opts, opt{"local-ollama", "Local Ollama (:11434) — private, recommended"})
	}
	opts = append(opts, opt{"openrouter", "OpenRouter with your own key"})

	fmt.Fprintln(out, "Select a backend:")
	for i, o := range opts {
		fmt.Fprintf(out, "  %d) %s\n", i+1, o.label)
	}
	if det.anthropicKey {
		fmt.Fprintln(out, "  note: ANTHROPIC_API_KEY found — usable via OpenRouter anthropic/* models or a local LiteLLM proxy")
	}
	fmt.Fprint(out, "> ")

	line, _ := in.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(opts) {
		return "", fmt.Errorf("invalid selection %q", strings.TrimSpace(line))
	}
	return opts[n-1].key, nil
}

// buildConfig maps a backend key + flag-supplied models to a config. For
// local-ollama with no pinned models it auto-fills from /api/tags via getTags.
func buildConfig(backend, code, study string, getTags func() []string) (cfgFile, error) {
	switch backend {
	case "free":
		return freeConfig(), nil
	case "openrouter":
		return openRouterConfig(code, study), nil
	case "local-litellm", "local":
		return litellmConfig(code, study), nil
	case "local-ollama":
		if code == "" && study == "" {
			c, s := pickOllamaModels(getTags())
			code, study = c, s
		}
		return ollamaConfig(code, study), nil
	default:
		return cfgFile{}, fmt.Errorf("unknown backend %q (want: free | openrouter | local)", backend)
	}
}

// Execute runs the setup command.
func (c *SetupCommand) Execute(ctx *Context) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	free := fs.Bool("free", false, "")
	project := fs.Bool("project", false, "")
	backend := fs.String("backend", "", "")
	code := fs.String("code", "", "")
	study := fs.String("study", "", "")
	yes := fs.Bool("yes", false, "")
	help := fs.Bool("help", false, "")
	fs.BoolVar(help, "h", false, "")
	if err := fs.Parse(ctx.Args); err != nil {
		return err
	}
	if *help {
		printSetupUsage()
		return nil
	}

	det := detectEnv(probeTCP)
	printSetupDetected(det)

	// Resolve the backend: --free / --backend skip the prompt; otherwise ask.
	choice := strings.TrimSpace(*backend)
	if *free {
		choice = "free"
	}
	if choice == "" {
		c, err := chooseBackend(det, bufio.NewReader(os.Stdin), os.Stdout)
		if err != nil {
			return err
		}
		choice = c
	}

	cfg, err := buildConfig(choice, *code, *study, func() []string {
		return ollamaTags(ollamaEndpoint, httpGet)
	})
	if err != nil {
		return err
	}

	// Resolve the target path (user by default).
	path := userCfgPath()
	if *project {
		path, err = projectCfgPath()
		if err != nil {
			return err
		}
	}

	// Idempotent: don't clobber an existing config without consent.
	if _, statErr := os.Stat(path); statErr == nil && !*yes {
		fmt.Printf("%s already exists. Overwrite? [y/N] ", path)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("Left existing config in place.")
			return nil
		}
	}

	if err := cfg.writeTo(path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	printSummary(cfg, path)
	return nil
}

func printSetupDetected(det detected) {
	fmt.Println("Detected:")
	fmt.Printf("  Ollama (:11434):          %s\n", yesNo(det.ollama))
	fmt.Printf("  LiteLLM (:4000):          %s\n", yesNo(det.litellm))
	fmt.Printf("  OPENROUTER_API_KEY env:   %s\n", yesNo(det.openRouterKey))
	fmt.Printf("  ANTHROPIC_API_KEY env:    %s\n", yesNo(det.anthropicKey))
	fmt.Println()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// printSummary echoes the written config and the next step (the key env var to
// set, with a privacy note for remote backends).
func printSummary(cfg cfgFile, path string) {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf("\nWrote %s:\n%s\n", path, b)
	if cfg.Backend.KeyEnv != "" {
		fmt.Printf("\nSet your key:  export %s=...\n", cfg.Backend.KeyEnv)
	}
	if cfg.Backend.Type == "openrouter" {
		fmt.Println("Note: prompts are sent to OpenRouter and leave your machine. For a private setup, point at a local Ollama/LiteLLM backend.")
	}
}

func printSetupUsage() {
	fmt.Println(`Usage: cortex setup [flags]

Detects your environment and writes a Cortex model config.

Flags:
  --free            Use OpenRouter free-tier models (no cost)
  --backend <name>  free | openrouter | local (skips the interactive prompt)
  --code <model>    Pin the code (agent) model
  --study <model>   Pin the study model
  --project         Write ./.cortex/config.json instead of the user config
  --yes             Non-interactive: take defaults, overwrite without asking
  -h, --help        Show this help

Writes ~/.cortex/config.json by default; projects inherit it and can override
per-role with their own ./.cortex/config.json.`)
}
