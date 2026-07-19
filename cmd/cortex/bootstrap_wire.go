// bootstrap_wire.go — wires the BackendResolver/GuidedSetup chain
// (bootstrap.go, bootstrap_persist.go) into the interactive REPL's
// startup path (docs/completion-roadmap.md Gate E). Everything the chain
// needed already existed and was fully tested in isolation; nothing
// called Resolve() outside tests. This file is that call, plus the small
// set of concrete probe implementations Resolve() needs in production
// (none existed — every existing test drove the chain with fakes).
//
// Scope, deliberately kept small: the SmokeProbe stage is left unwired
// (nil — BackendResolver.smokeTest treats that as always-pass, per its own
// doc comment). A curated OpenRouter pick that's gone stale is already
// self-healing at the next session start via preflightCuratedModels
// (preflight.go), which runs on every launch regardless of how the config
// got there; a bespoke one-shot smoke call here would duplicate that
// safety net for a first-run-only path. Ollama's smoke behavior is
// unverified beyond reachability (ollamaLocalProbe) for the same reason —
// out of scope for this pass.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/pkg/llm"
)

// openRouterEnvVar is the env var name docs/configuration.md's examples
// use for "backend.key_env" — the canonical name this wiring persists and
// checks, matching what interactiveGuidedSetup exports into the current
// process.
const openRouterEnvVar = "OPENROUTER_API_KEY"

// openRouterKeychainService is the macOS Keychain service name this
// wiring reads and writes — the same convention pkg/secret and
// pkg/llm/client.go already use for OpenRouter keys elsewhere in the
// codebase ("cortex-openrouter").
const openRouterKeychainService = "cortex-openrouter"

// bootstrapAction is what maybeRunGuidedBootstrap does before session
// construction.
type bootstrapAction int

const (
	// bootstrapNone: a backend is already resolvable without the guided
	// flow (a config file exists, or CORTEX_BACKEND is set) — do nothing.
	bootstrapNone bootstrapAction = iota
	// bootstrapGuided: a true first run on an interactive terminal — run
	// GuidedSetup before session construction.
	bootstrapGuided
	// bootstrapHint: a true first run, but stdin isn't a terminal (piped
	// input, CI, a driver script) — print one hint and fall through to
	// today's behavior unchanged.
	bootstrapHint
)

// decideBootstrap is the pure wiring predicate: whether main() should run
// the interactive GuidedSetup flow before session construction, print a
// one-line hint and fall through unchanged, or do nothing because a
// backend is already resolvable without it. Pure over its four inputs so
// it's table-driven-testable without touching the filesystem, env, or a
// real terminal.
//
// "True first run" here (no user config, no project config, no
// CORTEX_BACKEND env) is deliberately a different — narrower — question
// than IsFirstRun (firstrun.go, config-file-or-greeting-marker): that
// predicate gates the one-time greeting turn; this one gates whether a
// backend needs to be resolved at all before ANY turn (greeting or
// otherwise) can run.
func decideBootstrap(userConfigExists, projectConfigExists, hasBackendEnv, interactive bool) bootstrapAction {
	if userConfigExists || projectConfigExists || hasBackendEnv {
		return bootstrapNone
	}
	if interactive {
		return bootstrapGuided
	}
	return bootstrapHint
}

// maybeRunGuidedBootstrap runs decideBootstrap over the real environment
// and acts on it. Called from main() right before NewCortexSession(), on
// the interactive-REPL path only (bare `cortex` / `cortex resume`) —
// every headless subcommand (`turn`, `serve`, `discord`, `study`, `scan`,
// `change`, `model`, `project`, `study-eval`) returns from main() earlier
// and never reaches this call, so headless behavior is unchanged.
func maybeRunGuidedBootstrap() {
	switch currentBootstrapAction() {
	case bootstrapGuided:
		runGuidedBootstrap()
	case bootstrapHint:
		printFirstRunHint()
	}
}

// currentBootstrapAction gathers decideBootstrap's four inputs from the
// real filesystem/env/terminal and evaluates the predicate. Split out
// from maybeRunGuidedBootstrap so tests can assert the gathered decision
// against a real (CORTEX_HOME-isolated) environment without also
// exercising the interactive prompt or the stderr hint's side effects.
func currentBootstrapAction() bootstrapAction {
	return decideBootstrap(
		pathExists(userConfigPath()),
		pathExists(findConfigPath()),
		os.Getenv("CORTEX_BACKEND") != "",
		lineedit.IsInteractive(os.Stdin),
	)
}

// printFirstRunHint is the non-interactive first-run fallback: today's
// behavior (target localhost:4000, fail after retries) is unchanged, but
// the operator gets one clear pointer to what to do about it instead of a
// bare connection error.
func printFirstRunHint() {
	fmt.Fprintf(os.Stderr,
		"cortex: no backend configured (no %s, no project .cortex/config.json, no $CORTEX_BACKEND) "+
			"and stdin isn't a terminal, so the interactive setup is skipped. Set $CORTEX_BACKEND, write "+
			"a config file, or run `cortex` from a terminal once to be walked through it — see "+
			"docs/configuration.md.\n", userConfigPath())
}

// runGuidedBootstrap builds the BackendResolver chain from production
// probes, resolves a backend, and persists it — the M1.2–M1.3 pieces
// (bootstrap.go, bootstrap_persist.go) wired together for the first time.
// A failed or declined resolution (ErrNoBackend, or the user pressed
// Enter to skip) leaves no config behind and falls through to today's
// behavior: NewCortexSession() proceeds exactly as it would have without
// this call.
func runGuidedBootstrap() {
	resolver := &BackendResolver{
		Config: fileConfigProbe{Path: userConfigPath()},
		Key:    envKeychainKeyProbe{},
		Ollama: ollamaLocalProbe{},
		Guided: interactiveGuidedSetup{In: os.Stdin, Out: os.Stdout},
	}

	resolved, err := resolver.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"cortex: no backend available — continuing with the local default (http://localhost:4000); "+
				"see docs/configuration.md to configure one.")
		return
	}

	resolved = attachKeyRef(resolved)
	path := userConfigPath()
	if path == "" {
		fmt.Fprintln(os.Stderr, "cortex: could not resolve the user config path ($CORTEX_HOME/config.json) — skipping persistence for this run.")
		return
	}
	if err := PersistBackend(path, resolved); err != nil {
		fmt.Fprintf(os.Stderr, "cortex: resolved a %s backend but failed to save it: %v\n", resolved.Source, err)
		return
	}
	fmt.Fprintf(os.Stdout, "cortex: configured a %s backend (%s) — saved to %s\n", resolved.Backend, resolved.Source, path)
}

// attachKeyRef fills ResolvedBackend.KeyEnv/KeyService from b.Source. See
// the field's doc comment on ResolvedBackend (bootstrap.go) for why this
// lives here instead of inside Resolve(): the chain stays agnostic to key
// storage, so the production wiring — which knows exactly which concrete
// probe produced which source — is where the mapping belongs.
func attachKeyRef(b ResolvedBackend) ResolvedBackend {
	switch b.Source {
	case "openrouter-env":
		b.KeyEnv = openRouterEnvVar
	case "openrouter-keychain", "openrouter-guided":
		b.KeyService = openRouterKeychainService
	}
	return b
}

// envKeychainKeyProbe is the production KeyProbe (bootstrap.go chain
// stage 2): an OpenRouter key from $OPENROUTER_API_KEY, falling back to
// the macOS Keychain entry interactiveGuidedSetup writes to.
type envKeychainKeyProbe struct{}

func (envKeychainKeyProbe) Probe() (string, bool) {
	if strings.TrimSpace(os.Getenv(openRouterEnvVar)) != "" {
		return "openrouter-env", true
	}
	if keychainKey(openRouterKeychainService) != "" {
		return "openrouter-keychain", true
	}
	return "", false
}

// defaultOllamaProbeTimeout bounds the one /api/tags reachability check the
// bootstrap chain's Ollama stage makes — Ollama is local, so this stays
// short; a slow/absent daemon just falls through to the guided stage.
//
// network.ollama_probe_timeout_sec exists in the config schema
// (Config.ollamaProbeTimeout) for completeness with the rest of
// docs/configuration.md's `network.*` section, but this probe only ever
// runs from GuidedSetup on a TRUE first run — no user config, no project
// config, no $CORTEX_BACKEND (docs/configuration.md "Interactive
// first-run setup") — which means a config file setting this field can
// never actually be loaded before this stage runs. Left a plain const
// (not a var some call site overrides) for that reason: there is no
// reachable production call site to wire it from.
const defaultOllamaProbeTimeout = 2 * time.Second

const ollamaProbeTimeout = defaultOllamaProbeTimeout

// ollamaLocalProbe is the production OllamaProbe (bootstrap.go chain
// stage 3): wraps pkg/llm's existing /api/tags reachability probe
// (pkg/llm/probe_ollama.go) rather than re-implementing it.
type ollamaLocalProbe struct{}

func (ollamaLocalProbe) Probe() bool {
	ctx, cancel := context.WithTimeout(context.Background(), ollamaProbeTimeout)
	defer cancel()
	_, err := llm.NewOllamaProbe(llm.OllamaProbeConfig{}).Probe(ctx)
	return err == nil
}

// interactiveGuidedSetup is the production GuidedSetup (bootstrap.go
// chain stage 4, last resort): prompts for an OpenRouter API key on In,
// stores it in the macOS Keychain (openRouterKeychainService) and sets it
// in the current process's environment so this run's first turn sees it
// immediately without waiting on a fresh `security` round-trip. In/Out
// are an explicit io seam — production wires os.Stdin/os.Stdout; a test
// can drive a strings.Reader/bytes.Buffer instead of a real terminal.
type interactiveGuidedSetup struct {
	In  io.Reader
	Out io.Writer
}

func (g interactiveGuidedSetup) Guide() bool {
	fmt.Fprintln(g.Out, "cortex needs a backend. It works out of the box with a free OpenRouter API key.")
	fmt.Fprintln(g.Out, "Create one at https://openrouter.ai/keys, then paste it below (Enter to skip):")
	fmt.Fprint(g.Out, "OpenRouter API key: ")

	line, _ := bufio.NewReader(g.In).ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		fmt.Fprintln(g.Out, "Skipped. cortex will try http://localhost:4000 and report a connection error; "+
			"see docs/configuration.md to configure a backend by hand.")
		return false
	}

	if err := storeOpenRouterKeychainKey(key); err != nil {
		fmt.Fprintf(g.Out, "warning: could not save the key to the keychain (%v) — it will only last this session.\n", err)
	}
	os.Setenv(openRouterEnvVar, key)
	fmt.Fprintln(g.Out, "Key saved.")
	return true
}

// storeOpenRouterKeychainKey shells out to macOS `security` to store key
// under openRouterKeychainService, matching the read side (keychainKey in
// config.go) and pkg/secret's existing convention for the same service
// name. -U updates an existing entry instead of erroring on a re-run.
// Fails cleanly (returns an error, no panic) on non-macOS or when
// `security` isn't on PATH — interactiveGuidedSetup degrades to
// session-only auth via os.Setenv in that case.
func storeOpenRouterKeychainKey(key string) error {
	cmd := exec.Command("security", "add-generic-password",
		"-a", "openrouter", "-s", openRouterKeychainService, "-w", key, "-U")
	return cmd.Run()
}
