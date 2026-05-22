package study

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// ModelProbe is the cached observation of one (model, endpoint)'s
// context window + per-call latency. Source records *how* we got
// each piece — useful when a surprise value shows up in a study run
// and you want to know whether to trust it.
type ModelProbe struct {
	ModelID         string    `json:"model_id"`
	Endpoint        string    `json:"endpoint"`
	CtxWindowTokens int       `json:"ctx_window_tokens"`
	LatencyMS       int       `json:"latency_ms"`
	ProbedAt        time.Time `json:"probed_at"`
	Source          string    `json:"source"` // "openai_compat_models" | "inferred" | "cached" | "measured"
}

// probeFile is the on-disk shape: a map keyed by "model@endpoint" so a
// project that switches between two endpoints keeps both probes hot.
type probeFile struct {
	SchemaVersion string                `json:"schema_version"`
	Probes        map[string]ModelProbe `json:"probes"`
}

// defaultStudyProbesPath is the on-disk artifact location relative to
// the project's context dir. Sibling to op_cost_hints.json by design.
const defaultStudyProbesPath = "db/study_probes.json"

// DefaultProbeTTL is how long a cached probe is considered fresh. A
// week balances "don't repay the probe cost every session" against
// "react to model swaps within reasonable time."
const DefaultProbeTTL = 7 * 24 * time.Hour

// ProbeKey returns the cache key for a (model, endpoint) pair.
// Endpoint may be empty (e.g. provider doesn't expose one — Ollama
// uses its own default URL); the key still works.
func ProbeKey(modelID, endpoint string) string {
	return modelID + "@" + endpoint
}

// Probe returns a ModelProbe for the configured provider, hitting the
// cache when possible and falling back to a fresh probe otherwise.
//
// Cache path resolution:
//   - First checks .cortex/db/study_probes.json for a fresh entry
//     keyed by (modelID, endpoint). Fresh = probed_at within ttl.
//
// Probe path on miss/stale:
//   - If provider is *OpenAICompatClient, call ListModels for the
//     ctx window. Else use llm.InferContextClass on the model id and
//     map class → conservative ctx-window bucket.
//   - Issue one tiny generation call to measure latency.
//   - Persist the result back to the cache.
//
// Errors degrade gracefully: a probe-call failure returns a probe
// with measured=0 (latency unknown) but the ctx window we already
// fetched. Callers fall back to the planner's default latency.
func Probe(ctx context.Context, provider llm.Provider, modelID, endpoint, contextDir string, ttl time.Duration) (ModelProbe, error) {
	if ttl <= 0 {
		ttl = DefaultProbeTTL
	}
	cachePath := probeCachePath(contextDir)
	key := ProbeKey(modelID, endpoint)

	if cached, ok := readProbe(cachePath, key); ok {
		if time.Since(cached.ProbedAt) < ttl {
			cached.Source = "cached"
			return cached, nil
		}
	}

	p := ModelProbe{
		ModelID:  modelID,
		Endpoint: endpoint,
		ProbedAt: time.Now().UTC(),
	}

	// Context window: try /v1/models, fall back to InferContextClass.
	ctxTokens, source := probeCtxWindow(ctx, provider, modelID)
	p.CtxWindowTokens = ctxTokens
	p.Source = source

	// Latency: one tiny generation call. Cheap on any provider;
	// errors leave LatencyMS = 0 and the planner falls back.
	if provider != nil {
		if ms, err := measureLatency(ctx, provider); err == nil {
			p.LatencyMS = ms
			if p.Source == "" {
				p.Source = "measured"
			}
		}
	}

	if err := writeProbe(cachePath, key, p); err != nil {
		// Cache write failure is non-fatal — the planner still uses
		// the probe value; we just won't reuse it next time.
		return p, fmt.Errorf("write probe cache: %w", err)
	}
	return p, nil
}

// probeCtxWindow returns (tokens, source). source is "openai_compat_models"
// when /v1/models gave us a real number, "inferred" when we mapped the
// model id through llm.InferContextClass, or "" when nothing worked.
func probeCtxWindow(ctx context.Context, provider llm.Provider, modelID string) (int, string) {
	if compat, ok := provider.(*llm.OpenAICompatClient); ok {
		models, err := compat.ListModels(ctx)
		if err == nil {
			for _, m := range models {
				if m.ID == modelID && m.ContextLength > 0 {
					return m.ContextLength, "openai_compat_models"
				}
			}
		}
	}
	class := llm.InferContextClass(modelID, 0)
	switch class {
	case llm.ContextSmall:
		return 8192, "inferred"
	case llm.ContextLarge:
		return 131072, "inferred"
	default:
		return 32768, "inferred"
	}
}

// measureLatency issues a minimal generation call and returns the
// observed wall-clock duration in ms. The prompt is intentionally
// short — we want to measure transport + first-token round-trip,
// not generation throughput.
func measureLatency(ctx context.Context, provider llm.Provider) (int, error) {
	// Reasonable upper bound; if the model is slow, the planner just
	// gets a slow estimate and budgets fewer calls.
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := provider.Generate(tctx, "Reply with the single character: ok"); err != nil {
		return 0, err
	}
	return int(time.Since(start).Milliseconds()), nil
}

// probeCachePath returns the absolute file path for the study probe
// cache, rooted under the given contextDir (typically ".cortex").
func probeCachePath(contextDir string) string {
	return filepath.Join(contextDir, defaultStudyProbesPath)
}

// readProbe returns the cached probe for key, or zero value + false
// when not found / unreadable. Missing-file is not an error.
func readProbe(path, key string) (ModelProbe, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ModelProbe{}, false
	}
	var pf probeFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return ModelProbe{}, false
	}
	p, ok := pf.Probes[key]
	return p, ok
}

// writeProbe persists p under key, merging into any existing cache
// entries. Atomic via tmp + rename so a concurrent reader never sees
// a partial file.
func writeProbe(path, key string, p ModelProbe) error {
	pf := probeFile{SchemaVersion: "1", Probes: map[string]ModelProbe{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &pf) // tolerate corrupt cache — overwrite
	}
	if pf.Probes == nil {
		pf.Probes = map[string]ModelProbe{}
	}
	pf.Probes[key] = p
	pf.SchemaVersion = "1"

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	bb, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal probe cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bb, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
