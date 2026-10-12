// bootstrap_wire.go — wires the BackendResolver chain (bootstrap.go,
// bootstrap_local.go, bootstrap_persist.go) into startup
// (docs/completion-roadmap.md Gate E), plus the concrete probes the chain
// uses in production.
//
// Two entry points run it, both only on a true first run (no user config,
// no project config, no $CORTEX_BACKEND):
//
//   - the REPL (bare `cortex` / `cortex resume`), via maybeRunGuidedBootstrap:
//     on a terminal, the full chain with prompts; with piped stdin, the
//     non-interactive chain below.
//   - `cortex turn`, via maybeRunHeadlessBootstrap: always the
//     non-interactive chain, with every message on stderr (stdout stays the
//     answer only).
//
// The non-interactive chain runs the prompt-free stages only — an
// OpenRouter key from env/keychain, then a local model server — and writes
// no secret (key_env/key_service name where the key lives). It leaves the
// machine alone when something already answers on the default endpoint
// (localhost:4000, the zero-config LiteLLM port): that setup works without
// a config file.
//
// The SmokeProbe stage is left unwired (nil — BackendResolver.smokeTest
// treats that as always-pass). A curated OpenRouter pick that's gone stale
// self-heals at the next session start via preflightCuratedModels
// (preflight.go); a local pick is a model the server itself listed.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/lineedit"
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
	// bootstrapNone: a backend is already resolvable without the chain
	// (a config file exists, or CORTEX_BACKEND is set) — do nothing.
	bootstrapNone bootstrapAction = iota
	// bootstrapGuided: a true first run on an interactive terminal — run
	// the full chain, with prompts, before session construction.
	bootstrapGuided
	// bootstrapNonInteractive: a true first run, but stdin isn't a
	// terminal (piped input, CI, a driver script) — run the prompt-free
	// stages; print one hint when none resolves.
	bootstrapNonInteractive
)

// decideBootstrap is the pure wiring predicate: whether main() should run
// the interactive chain before session construction, the non-interactive
// one, or nothing because a backend is already resolvable without it. Pure
// over its four inputs so it's table-driven-testable without touching the
// filesystem, env, or a real terminal.
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
	return bootstrapNonInteractive
}

// Production seams the bootstrap reads. Package vars so tests (TestMain)
// point them at fakes: no test may probe the real localhost servers, read
// the real keychain, or shell out for RAM.
var (
	bootstrapLocalSpecs           = defaultLocalServerSpecs
	bootstrapDefaultRoot          = defaultEndpoint
	bootstrapKeyProbe    KeyProbe = envKeychainKeyProbe{}
	bootstrapRAM                  = totalSystemRAM
	bootstrapHTTPClient           = &http.Client{}
)

// maybeRunGuidedBootstrap runs decideBootstrap over the real environment
// and acts on it. Called from main() right before NewCortexSession(), on
// the interactive-REPL path (bare `cortex` / `cortex resume`).
func maybeRunGuidedBootstrap() {
	switch currentBootstrapAction() {
	case bootstrapGuided:
		runGuidedBootstrap()
	case bootstrapNonInteractive:
		runNonInteractiveBootstrap()
	}
}

// maybeRunHeadlessBootstrap is `cortex turn`'s entry point: on a true first
// run it runs the non-interactive chain whether or not stdin is a terminal
// (a headless turn never prompts).
func maybeRunHeadlessBootstrap() {
	if currentBootstrapAction() != bootstrapNone {
		runNonInteractiveBootstrap()
	}
}

// currentBootstrapAction gathers decideBootstrap's four inputs from the
// real filesystem/env/terminal and evaluates the predicate. Split out
// from maybeRunGuidedBootstrap so tests can assert the gathered decision
// against a real (CORTEX_HOME-isolated) environment without also
// exercising the prompts or their side effects.
func currentBootstrapAction() bootstrapAction {
	return decideBootstrap(
		pathExists(userConfigPath()),
		pathExists(findConfigPath()),
		os.Getenv("CORTEX_BACKEND") != "",
		lineedit.IsInteractive(os.Stdin),
	)
}

// printFirstRunHint is the non-interactive fallback when no stage resolved
// a backend: Cortex goes on to target localhost:4000 and fail after
// retries, and the operator gets one pointer to what to do about it
// instead of a bare connection error.
func printFirstRunHint(w io.Writer) {
	fmt.Fprintf(w,
		"cortex: no backend configured (no %s, no project .cortex/config.json, no $CORTEX_BACKEND) "+
			"and no local model server with a usable model was found. Start Ollama, LM Studio or "+
			"llama-server with a model, set $CORTEX_BACKEND, write a config file, or run `cortex` from "+
			"a terminal once to be walked through it — see docs/configuration.md.\n", userConfigPath())
}

// runGuidedBootstrap is the interactive first run: prompts on stdin,
// messages on stdout, failures on stderr.
func runGuidedBootstrap() {
	bootstrapRun{
		Interactive: true,
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		ConfigPath:  userConfigPath(),
		Specs:       bootstrapLocalSpecs,
		Tier:        machineTier(),
		Key:         bootstrapKeyProbe,
		Guided:      interactiveGuidedSetup{In: os.Stdin, Out: os.Stdout},
		Client:      bootstrapHTTPClient,
	}.run()
}

// runNonInteractiveBootstrap is the prompt-free first run: every message on
// stderr, no guided stage.
func runNonInteractiveBootstrap() {
	bootstrapRun{
		Out:         os.Stderr,
		Err:         os.Stderr,
		ConfigPath:  userConfigPath(),
		Specs:       bootstrapLocalSpecs,
		DefaultRoot: bootstrapDefaultRoot,
		Tier:        machineTier(),
		Key:         bootstrapKeyProbe,
		Client:      bootstrapHTTPClient,
	}.run()
}

// machineTier is the RAM-tier suggestion for this machine (model.go).
func machineTier() ModelSuggestion {
	ram, err := bootstrapRAM()
	if err != nil {
		ram = 0
	}
	return suggestModels(ram, runtime.GOARCH)
}

// bootstrapRun is one first-run resolution with every input explicit, so
// tests drive the same code main() runs with fake servers and io.
type bootstrapRun struct {
	// Interactive selects the prompting chain (local-server confirmation,
	// guided OpenRouter key). False runs the prompt-free stages only.
	Interactive bool
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	ConfigPath  string
	Specs       []localServerSpec
	// DefaultRoot is probed in non-interactive runs only: when anything
	// answers there, the run does nothing (a zero-config LiteLLM works).
	DefaultRoot string
	Tier        ModelSuggestion
	Key         KeyProbe
	Guided      GuidedSetup
	Client      *http.Client
}

// bootstrapOutcome reports what run did, for tests.
type bootstrapOutcome int

const (
	// outcomePersisted: a backend resolved and was saved to ConfigPath.
	outcomePersisted bootstrapOutcome = iota
	// outcomeDefaultAnswers: non-interactive, and the default endpoint
	// answered — nothing done.
	outcomeDefaultAnswers
	// outcomeNone: no stage resolved a backend (or saving it failed).
	outcomeNone
)

// run detects local servers (concurrently with the default-endpoint check
// in non-interactive runs), resolves the chain, and persists the result.
func (b bootstrapRun) run() (ResolvedBackend, bootstrapOutcome) {
	ctx := context.Background()
	var (
		det     localDetection
		answers bool
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		det = detectLocal(ctx, b.Client, b.Specs, b.Tier)
	}()
	if !b.Interactive {
		answers = endpointAnswers(ctx, b.Client, b.DefaultRoot)
	}
	wg.Wait()
	if answers {
		return ResolvedBackend{}, outcomeDefaultAnswers
	}

	resolver := &BackendResolver{
		Config: fileConfigProbe{Path: b.ConfigPath},
		Key:    b.Key,
		Local:  localServerStage{Detection: det, Interactive: b.Interactive, In: b.In, Out: b.Out, Client: b.Client},
	}
	if b.Interactive && b.Guided != nil {
		resolver.Guided = b.Guided
	}

	resolved, err := resolver.Resolve()
	if err != nil {
		if b.Interactive {
			fmt.Fprintln(b.Err,
				"cortex: no backend available — continuing with the local default (http://localhost:4000); "+
					"see docs/configuration.md to configure one.")
		} else {
			printFirstRunHint(b.Err)
		}
		return ResolvedBackend{}, outcomeNone
	}

	resolved = attachKeyRef(resolved)
	if b.ConfigPath == "" {
		fmt.Fprintln(b.Err, "cortex: could not resolve the user config path ($CORTEX_HOME/config.json) — skipping persistence for this run.")
		return resolved, outcomeNone
	}
	if err := PersistBackend(b.ConfigPath, resolved); err != nil {
		fmt.Fprintf(b.Err, "cortex: resolved a %s backend but failed to save it: %v\n", resolved.Source, err)
		return resolved, outcomeNone
	}

	if o := det.Offer; o != nil && resolved.Source == o.Source() {
		fmt.Fprintf(b.Out, "cortex: using %s with %s (%s context) — saved to %s\n",
			o.Spec.Name, resolved.Model, windowLabel(resolved.Window), b.ConfigPath)
		return resolved, outcomePersisted
	}
	fmt.Fprintf(b.Out, "cortex: configured a %s backend (%s) — saved to %s\n", resolved.Backend, resolved.Source, b.ConfigPath)
	if o := det.Offer; o != nil {
		fmt.Fprintf(b.Out, "cortex: also found %s with %s (%s context). To use it instead, see \"Interactive first-run setup\" in docs/configuration.md.\n",
			o.Spec.Name, o.BaseModel, windowLabel(o.Window))
	}
	return resolved, outcomePersisted
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

// defaultOllamaProbeTimeout bounds the concurrent local-server listing
// phase of the first-run chain (bootstrap_local.go's localListTimeout).
// The servers are local, so this stays short; a slow/absent server just
// falls through to the next stage.
//
// network.ollama_probe_timeout_sec exists in the config schema for
// completeness with the rest of docs/configuration.md's `network.*`
// section, but this probe only ever runs on a TRUE first run — no user
// config, no project config, no $CORTEX_BACKEND — so a config file setting
// that field can never be loaded before the probe runs. Left a plain const
// for that reason.
const defaultOllamaProbeTimeout = 2 * time.Second

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
