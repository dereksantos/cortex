// bootstrap.go — the BackendResolver chain (Phase 1 / M1.2): pick a
// working LLM backend on first run without a config file. Resolution
// order (docs/cortex-web.md Phase 1, GOAL.md §3 P1 backend chain):
//
//  1. existing config — if config already resolves a backend, use it
//  2. an OpenRouter key from env or keychain
//  3. a local Ollama probe
//  4. guided OpenRouter free-tier setup (last resort)
//
// Every stage that seats a candidate backend must also pass a one-shot
// tool-call smoke probe (free models vary in tool-calling quality); a
// candidate that fails the smoke probe is abandoned and the chain
// falls through to the next stage. Each stage is an interface so the
// chain is pure logic over probe results — table-driven tests fake
// every stage, no real network/keychain calls (GOAL.md §3 P1).
//
// This file defines the chain only; wiring it into NewCortexSession
// and persisting the result (M1.3) land in later increments.
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
	// "openrouter-env", "openrouter-keychain", "ollama-local", or
	// "openrouter-guided".
	Source string
	// Backend is the pkg/llm.Backend to construct.
	Backend llm.Backend
	// Model is the model id to request (e.g. "openrouter/free").
	// Empty lets the constructed client fall back to its own default.
	Model string
}

// ErrNoBackend is returned when every chain stage is exhausted (config
// missing, no key reachable, Ollama down, guided setup declined) or
// every seated candidate fails its smoke probe.
var ErrNoBackend = errors.New("bootstrap: no backend resolved")

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

// OllamaProbe reports whether a local Ollama instance is reachable
// (chain stage 3).
type OllamaProbe interface {
	Probe() bool
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
	Ollama OllamaProbe
	Guided GuidedSetup
	Smoke  SmokeProbe
}

// openRouterFreeModel is the free-tier auto-router model id used for
// any stage that seats OpenRouter without a config-pinned model
// (D1: no pinned ":free" id — the free catalog churns).
const openRouterFreeModel = "openrouter/free"

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
			b := ResolvedBackend{Source: source, Backend: llm.BackendOpenRouter, Model: openRouterFreeModel}
			if r.smokeTest(b) {
				return b, nil
			}
		}
	}
	if r.Ollama != nil && r.Ollama.Probe() {
		b := ResolvedBackend{Source: "ollama-local", Backend: llm.BackendOllama}
		if r.smokeTest(b) {
			return b, nil
		}
	}
	if r.Guided != nil {
		if ok := r.Guided.Guide(); ok {
			b := ResolvedBackend{Source: "openrouter-guided", Backend: llm.BackendOpenRouter, Model: openRouterFreeModel}
			if r.smokeTest(b) {
				return b, nil
			}
		}
	}
	return ResolvedBackend{}, ErrNoBackend
}
