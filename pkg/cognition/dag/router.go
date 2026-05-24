// Package dag — per-node provider resolution.
//
// Router is the contract the Executor uses to pick which LLM provider
// runs each spawned node. The default implementation composes the
// pkg/llm registry's PickForCapabilities chain with the override /
// fallback semantics from docs/per-node-routing-plan.md "Resolution at
// spawn time":
//
//  1. NodeSpec.Attrs["model"] explicit override wins.
//  2. NodeSpec.Requires chain → registry.PickForCapabilities → factory.Get.
//  3. Fallback to session-default provider.
//
// Router lives behind an interface so tests can stub it without wiring
// the full registry + factory + default-provider stack, and so future
// pickers (bandit, learned classifier per docs/picker-as-node.md) can
// drop in by implementing the same shape.

package dag

import (
	"context"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Router resolves the provider for one spawned node. Returns the
// chosen Provider, the model id (for trace attribution; empty when
// falling back to the session default), and a short Reason label the
// executor records on the trace entry.
//
// Reason values currently in use:
//   - "override"            — Attrs["model"] supplied at spawn time
//   - "requires:<modelID>"  — picked from NodeSpec.Requires chain
//   - "default"             — fallback to session-default provider
//   - "no-match"            — no rule matched and no default; provider is nil
//
// Reason is opaque to the executor; callers post-process traces by
// prefix. Picker-as-node (docs/picker-as-node.md) extends this with
// bandit/classifier reasons like "bandit:thompson@0.94".
type Router interface {
	Resolve(ctx context.Context, spec NodeSpec) (provider llm.Provider, modelID string, reason string)
}

// RouterDeps bundles the dependencies the default Router needs. Each
// is optional; an unset Registry skips Requires resolution, an unset
// ProviderFactory skips override resolution, an unset Default returns
// nil provider on full fallback. The combination determines what
// resolution paths are reachable.
type RouterDeps struct {
	Registry        llm.ModelRegistry
	ProviderFactory llm.ProviderFactory
	Default         llm.Provider
}

// NewDefaultRouter constructs a Router with the resolution order from
// docs/per-node-routing-plan.md. Pass the same RouterDeps the REPL
// uses to wire ModelRegistry + ProviderFactory at session start.
func NewDefaultRouter(deps RouterDeps) Router {
	return &defaultRouter{deps: deps}
}

type defaultRouter struct {
	deps RouterDeps
}

func (r *defaultRouter) Resolve(ctx context.Context, spec NodeSpec) (llm.Provider, string, string) {
	// 1. Explicit override wins.
	if override, ok := spec.Attrs["model"].(string); ok && override != "" {
		if r.deps.ProviderFactory != nil {
			if p, err := r.deps.ProviderFactory.Get(override); err == nil && p != nil {
				return p, override, "override"
			}
			// Override that errored falls through to Requires / default
			// rather than blocking the spawn — keeps the harness usable
			// when a stale override references a model that no longer
			// exists.
		}
	}

	// 2. Requires chain → registry → factory.
	if len(spec.Requires) > 0 && r.deps.Registry != nil {
		if mi, ok := r.deps.Registry.PickForCapabilities(ctx, spec.Requires); ok {
			if r.deps.ProviderFactory != nil {
				if p, err := r.deps.ProviderFactory.Get(mi.ID); err == nil && p != nil {
					return p, mi.ID, "requires:" + mi.ID
				}
			}
		}
	}

	// 3. Session default.
	if r.deps.Default != nil {
		return r.deps.Default, "", "default"
	}

	// 4. Nothing — handler will fall back to its own cfg.Provider.
	return nil, "", "no-match"
}
