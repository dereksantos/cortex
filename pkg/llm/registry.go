// Package llm — unified model registry.
//
// ModelRegistry is the single-point-of-truth accessor for "which
// models are available and what are their attributes". Consumers
// (`decide.next` catalog injection, the Ollama auto-pick probe,
// `act.read_file`'s size-vs-window decision, the `cortex models`
// command) should consult the registry rather than reach for
// per-backend probing or hardcoded curated lists.
//
// The registry composes per-backend Probes (Ollama, OpenAI-compat,
// OpenRouter) into a flat `[]ModelInfo`. ModelInfo flattens existing
// substrate (CompatModel + EndpointCatalog) into a shape consumers can
// filter and select on directly.
//
// Sourcing the *effective* context window (runtime, not model-
// theoretical) is the registry's most load-bearing invariant. A
// lemonade-hosted Qwen3-Coder-30B advertises a 262144 model max via
// /v1/models, but the running llama-server may have been booted with
// `--ctx-size 65536` — the registry must report 65536 so downstream
// size-vs-window decisions don't overflow the actual deployment.
package llm

import (
	"context"
	"sync"
	"time"
)

// ModelInfo is the flattened registry-view of one model available on
// one endpoint. Composes pkg/llm's existing substrate (CompatModel +
// EndpointCatalog + EffectiveLabels + parseParamCount) into one
// consumer-facing record.
type ModelInfo struct {
	// ID is the model id as the backend exposes it. For Ollama this
	// is "<name>:<tag>" (e.g. "qwen2.5-coder:7b"); for OpenAI-compat
	// endpoints it's whatever the endpoint returns from /v1/models
	// (e.g. "Qwen3-Coder-30B-A3B-Instruct-GGUF"); for OpenRouter it's
	// the slash-prefixed canonical id (e.g.
	// "anthropic/claude-haiku-4.5").
	ID string

	// Endpoint is the registry-level identifier for the backend hosting
	// this model. "ollama", "openrouter", or a configured compat
	// endpoint name (e.g. "chatterbox").
	Endpoint string

	// BaseURL is the OpenAI-compat root for this endpoint, or empty
	// when the backend has a default (Ollama uses DefaultOllamaURL;
	// OpenRouter uses its own client). Consumers building a Provider
	// from this entry pass BaseURL through to NewOpenAICompatClient.
	BaseURL string

	// IsLocal flags whether the endpoint runs on the local machine /
	// LAN. Drives "prefer local" decisions in routing and the
	// recommender.
	IsLocal bool

	// EffectiveContextWindow is the runtime context size the deployment
	// will actually accept, in tokens. NOT the model's theoretical
	// max — that distinction matters because llama-server / vLLM can
	// boot a 256K-window model at --ctx-size 64K and downstream
	// size-vs-window math must respect the deployment. Zero means
	// unknown.
	EffectiveContextWindow int

	// SizeBillion is the model's parameter count in billions, best-
	// effort from id parsing (parseParamCount). 0 when undetectable.
	// Used by the Ollama auto-pick scorer and by display formatting.
	SizeBillion float64

	// Capabilities are the model's capability tags (Cap* constants in
	// capabilities.go). Sourced from the endpoint's labels when
	// available (Lemonade), otherwise inferred from the id.
	Capabilities []string
}

// HasCapability returns true when c is among the model's Capabilities.
// Convenience wrapper around hasLabel for the registry-flat shape.
func (m ModelInfo) HasCapability(c string) bool {
	return hasLabel(m.Capabilities, c)
}

// Probe is a per-backend source of ModelInfo. Each implementation
// talks to exactly one kind of backend (Ollama, an OpenAI-compat
// endpoint, OpenRouter) and returns normalized ModelInfo records.
//
// Probes are stateless w.r.t. caching — the registry handles the TTL
// cache and concurrent fan-out. A Probe should make at most one
// network round-trip per call and return promptly (or honor ctx
// cancellation).
type Probe interface {
	// Name identifies the backend in telemetry and dedupe ordering.
	// Examples: "ollama", "openrouter", "compat:chatterbox".
	Name() string

	// Probe fetches the current model list from this backend. Returning
	// (nil, err) on failure is fine — the registry tolerates per-backend
	// errors and surfaces whatever succeeded.
	Probe(ctx context.Context) ([]ModelInfo, error)
}

// ModelRegistry is the unified accessor. List/Get/Filter are cached
// (TTL-bounded); Refresh forces a re-probe across all backends.
//
// Errors from individual probes are not surfaced through this
// interface — the registry logs them via the ErrorReporter and returns
// the best available view. Consumers wanting strict freshness should
// call Refresh first.
type ModelRegistry interface {
	// List returns every known model across all backends. Cached.
	List(ctx context.Context) []ModelInfo

	// Get looks up a model by id. Cached. Returns false when the id
	// doesn't match any known entry.
	Get(ctx context.Context, id string) (ModelInfo, bool)

	// Filter returns the subset matching predicate. Cached snapshot;
	// the predicate is called against the cached list, not re-probed.
	Filter(ctx context.Context, predicate func(ModelInfo) bool) []ModelInfo

	// Refresh forces a re-probe of every backend and warms the cache.
	// Returns the first error encountered (further errors are logged
	// via the ErrorReporter but don't shortcut). Useful after a model
	// install / removal (the `cortex models pull X` flow).
	Refresh(ctx context.Context) error
}

// ErrorReporter is the channel through which per-backend probe
// failures surface. nil ErrorReporter discards errors silently — the
// registry never fails hard from a single backend being down.
type ErrorReporter func(probeName string, err error)

// RegistryConfig wires a composite registry's behavior.
type RegistryConfig struct {
	// Probes are the backends this registry queries. Order determines
	// dedupe precedence when two backends advertise the same id: the
	// first probe wins. Typically: local-first (Ollama, compat) then
	// remote (OpenRouter) so local installs override cloud entries of
	// the same name.
	Probes []Probe

	// TTL bounds cache staleness. Zero or negative disables caching
	// (every call re-probes). 5*time.Minute is a reasonable default
	// for interactive use — long enough to absorb the per-turn
	// catalog injection, short enough that a freshly-pulled model
	// surfaces within a session.
	TTL time.Duration

	// ProbeTimeout caps how long a single backend probe is allowed to
	// block during List / Refresh. Per-backend; the slowest probe
	// gates List latency. Zero defaults to 5*time.Second.
	ProbeTimeout time.Duration

	// OnError, when non-nil, receives per-backend probe errors as they
	// occur. A nil reporter discards errors silently. Hook this to
	// the REPL ui.Warn or a log.Logger so probe failures don't go
	// invisible.
	OnError ErrorReporter
}

// NewCompositeRegistry builds a ModelRegistry composing the given
// probes with TTL caching and concurrent per-backend fan-out.
//
// On the first List/Get/Filter call (or on Refresh), every probe is
// run concurrently with ProbeTimeout. Their results are merged: later
// probes don't overwrite earlier ones (first probe wins on id
// collision — see RegistryConfig.Probes ordering).
func NewCompositeRegistry(cfg RegistryConfig) ModelRegistry {
	if cfg.TTL < 0 {
		cfg.TTL = 0
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 5 * time.Second
	}
	return &compositeRegistry{cfg: cfg}
}

// compositeRegistry is the default ModelRegistry. Holds the cache +
// the fan-out machinery. Safe for concurrent use.
type compositeRegistry struct {
	cfg RegistryConfig

	mu        sync.Mutex
	cache     []ModelInfo
	cacheAt   time.Time
	cachedIdx map[string]int // id → index in cache
}

func (r *compositeRegistry) List(ctx context.Context) []ModelInfo {
	r.ensureFresh(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelInfo, len(r.cache))
	copy(out, r.cache)
	return out
}

func (r *compositeRegistry) Get(ctx context.Context, id string) (ModelInfo, bool) {
	r.ensureFresh(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if i, ok := r.cachedIdx[id]; ok {
		return r.cache[i], true
	}
	return ModelInfo{}, false
}

func (r *compositeRegistry) Filter(ctx context.Context, predicate func(ModelInfo) bool) []ModelInfo {
	r.ensureFresh(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelInfo, 0, len(r.cache))
	for _, m := range r.cache {
		if predicate(m) {
			out = append(out, m)
		}
	}
	return out
}

func (r *compositeRegistry) Refresh(ctx context.Context) error {
	return r.reprobe(ctx)
}

// ensureFresh re-probes when the cache is empty or older than TTL.
// Re-probe failures keep the stale cache in place rather than blank
// out the registry on a transient backend hiccup.
func (r *compositeRegistry) ensureFresh(ctx context.Context) {
	r.mu.Lock()
	stale := r.cache == nil || (r.cfg.TTL > 0 && time.Since(r.cacheAt) > r.cfg.TTL)
	r.mu.Unlock()
	if !stale {
		return
	}
	_ = r.reprobe(ctx)
}

// reprobe fans out to every probe concurrently, each bounded by
// ProbeTimeout. Probe errors are surfaced via OnError but never
// shortcut the rest. Returns the first error encountered, or nil.
func (r *compositeRegistry) reprobe(ctx context.Context) error {
	type result struct {
		name   string
		models []ModelInfo
		err    error
	}
	results := make(chan result, len(r.cfg.Probes))
	for _, p := range r.cfg.Probes {
		p := p
		go func() {
			pctx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
			defer cancel()
			models, err := p.Probe(pctx)
			results <- result{name: p.Name(), models: models, err: err}
		}()
	}
	merged := make([]ModelInfo, 0, 32)
	idx := make(map[string]int, 32)
	var firstErr error
	// Preserve probe order on dedupe by collecting in a map keyed by
	// probe-name, then walking r.cfg.Probes in order.
	byProbe := make(map[string][]ModelInfo, len(r.cfg.Probes))
	for range r.cfg.Probes {
		res := <-results
		if res.err != nil {
			if r.cfg.OnError != nil {
				r.cfg.OnError(res.name, res.err)
			}
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		byProbe[res.name] = res.models
	}
	for _, p := range r.cfg.Probes {
		for _, m := range byProbe[p.Name()] {
			if _, dupe := idx[m.ID]; dupe {
				continue
			}
			idx[m.ID] = len(merged)
			merged = append(merged, m)
		}
	}
	r.mu.Lock()
	r.cache = merged
	r.cachedIdx = idx
	r.cacheAt = time.Now()
	r.mu.Unlock()
	return firstErr
}
