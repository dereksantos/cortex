// bootstrap.go — the BackendResolver chain (Phase 1 / M1.2): pick a
// working LLM backend on first run without a config file. Resolution
// order (docs/cortex-web.md Phase 1, GOAL.md §3 P1 backend chain):
//
//  1. existing config — if config already resolves a backend, use it
//  2. an OpenRouter key from env or keychain
//  3. a local model server (Ollama, LM Studio, llama.cpp — bootstrap_local.go)
//  4. guided OpenRouter free-tier setup (last resort)
//
// Every stage that seats a candidate backend must also pass a one-shot
// tool-call smoke probe (free models vary in tool-calling quality); a
// candidate that fails the smoke probe is abandoned and the chain
// falls through to the next stage. Each stage is an interface so the
// chain is pure logic over probe results — table-driven tests fake
// every stage, no real network/keychain calls (GOAL.md §3 P1).
//
// This file defines the chain only. Persisting a resolved backend to
// the user config, and the ConfigProbe that reads it back (M1.3), live
// in bootstrap_persist.go; bootstrap_wire.go runs the chain at startup.
package main

import (
	"errors"

	"github.com/dereksantos/cortex/pkg/llm"
)

// ResolvedBackend is what the BackendResolver chain seats: enough to
// construct a pkg/llm client, plus an attribution string for logging
// and for M1.3's config persistence.
type ResolvedBackend struct {
	// Source names which chain stage produced this backend: "config",
	// "openrouter-env", "openrouter-keychain", "ollama-local",
	// "lmstudio-local", "llamacpp-local", or "openrouter-guided".
	Source string
	// Backend is the pkg/llm.Backend to construct.
	Backend llm.Backend
	// Endpoint is the OpenAI-compatible base URL to persist as
	// backend.endpoint. Every seating stage sets it: with backend.endpoint
	// unset, cmd/cortex sends to defaultEndpoint (localhost:4000) whatever
	// backend.type says. The config stage leaves it empty (nothing is
	// persisted from that stage).
	Endpoint string
	// Model is the model id to request (e.g. curatedTopPick().ID).
	// Empty lets the constructed client fall back to its own default.
	Model string
	// Window is the model's context window, carried alongside Model so
	// PersistBackend can seed "models.<role>.window" — not just the id —
	// when Model came from the curated table (curated.go). Zero means
	// "no opinion"; PersistBackend leaves window unset.
	Window int
	// KeyEnv/KeyService optionally record where the key for this backend
	// can be found again: an env var name, or a macOS Keychain service
	// name. The chain itself stays agnostic to key storage — KeyProbe and
	// GuidedSetup only report whether a key was found/stored, not how —
	// so Resolve() never sets these; production wiring (bootstrap_wire.go)
	// fills them in from ResolvedBackend.Source after a successful
	// Resolve(), and PersistBackend writes whichever is set into
	// "backend.key_env"/"backend.key_service" so a fresh CortexSession's
	// resolveKey(...) can find the key without the user re-entering it.
	KeyEnv     string
	KeyService string
}

// ErrNoBackend is returned when every chain stage is exhausted (config
// missing, no key reachable, no usable local server, guided setup declined) or
// every seated candidate fails its smoke probe.
var ErrNoBackend = errors.New("bootstrap: no backend resolved")

// openRouterEndpoint is the backend.endpoint the OpenRouter stages persist
// (the value docs/configuration.md's examples use).
const openRouterEndpoint = "https://openrouter.ai/api/v1"

// ConfigProbe inspects existing config for an already-resolved backend
// (chain stage 1: a config hit short-circuits the rest of the chain).
type ConfigProbe interface {
	Probe() (ResolvedBackend, bool)
}

// KeyProbe resolves an OpenRouter API key from env or keychain (chain
// stage 2). ok is false when no source has a key; source distinguishes
// "openrouter-env" vs "openrouter-keychain" for ResolvedBackend.Source.
type KeyProbe interface {
	Probe() (source string, ok bool)
}

// LocalProbe offers a model on a local model server (chain stage 3). ok is
// false when no server has a usable model or the user declined the offer.
// The returned backend carries Endpoint, Model and Window.
type LocalProbe interface {
	Probe() (ResolvedBackend, bool)
}

// GuidedSetup walks the user through pasting an OpenRouter key and
// persisting it to the keychain (chain stage 4, last resort). ok is
// false when the user declines or the guided flow can't complete.
type GuidedSetup interface {
	Guide() (ok bool)
}

// SmokeProbe runs the one-shot tool-call smoke test against a seated
// candidate backend. A candidate that fails the smoke probe is
// abandoned; the chain falls through to the next stage.
type SmokeProbe interface {
	Smoke(ResolvedBackend) bool
}

// BackendResolver runs the four-stage chain described in the package
// doc comment. Every field is an interface so tests fake each stage
// independently.
type BackendResolver struct {
	Config ConfigProbe
	Key    KeyProbe
	Local  LocalProbe
	Guided GuidedSetup
	Smoke  SmokeProbe
}

// smokeTest runs r.Smoke against b, treating a nil Smoke as an
// always-pass stub (some callers — e.g. a config stage that already
// trusts a previously smoke-tested backend — may not wire one).
func (r *BackendResolver) smokeTest(b ResolvedBackend) bool {
	if r.Smoke == nil {
		return true
	}
	return r.Smoke.Smoke(b)
}

// Resolve runs the chain in order, returning the first seated
// candidate whose smoke probe passes. Exhausting every stage, or every
// seated candidate failing its smoke probe, returns ErrNoBackend.
func (r *BackendResolver) Resolve() (ResolvedBackend, error) {
	if r.Config != nil {
		if b, ok := r.Config.Probe(); ok && r.smokeTest(b) {
			return b, nil
		}
	}
	if r.Key != nil {
		if source, ok := r.Key.Probe(); ok {
			pick := curatedTopPick()
			b := ResolvedBackend{Source: source, Backend: llm.BackendOpenRouter, Endpoint: openRouterEndpoint, Model: pick.ID, Window: pick.Window}
			if r.smokeTest(b) {
				return b, nil
			}
		}
	}
	if r.Local != nil {
		if b, ok := r.Local.Probe(); ok && r.smokeTest(b) {
			return b, nil
		}
	}
	if r.Guided != nil {
		if ok := r.Guided.Guide(); ok {
			pick := curatedTopPick()
			b := ResolvedBackend{Source: "openrouter-guided", Backend: llm.BackendOpenRouter, Endpoint: openRouterEndpoint, Model: pick.ID, Window: pick.Window}
			if r.smokeTest(b) {
				return b, nil
			}
		}
	}
	return ResolvedBackend{}, ErrNoBackend
}
