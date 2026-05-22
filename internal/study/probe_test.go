package study

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeProvider is a minimal llm.Provider for measuring latency. It
// sleeps for `latency` and returns "ok". Not a real OpenAI-compat
// client, so probeCtxWindow falls through to InferContextClass.
type fakeProvider struct {
	latency time.Duration
	calls   int
}

func (f *fakeProvider) Generate(ctx context.Context, prompt string) (string, error) {
	f.calls++
	select {
	case <-time.After(f.latency):
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeProvider) GenerateWithSystem(ctx context.Context, prompt, system string) (string, error) {
	return f.Generate(ctx, prompt)
}

func (f *fakeProvider) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	s, err := f.Generate(ctx, prompt)
	return s, llm.GenerationStats{InputTokens: 5, OutputTokens: 1}, err
}

func (f *fakeProvider) IsAvailable() bool { return true }
func (f *fakeProvider) Name() string      { return "fake" }

func TestProbe_FreshWritesCache(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{latency: 10 * time.Millisecond}

	p, err := Probe(context.Background(), prov, "qwen3-coder-30b", "http://example/v1", dir, time.Hour)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.CtxWindowTokens == 0 {
		t.Error("CtxWindowTokens = 0; expected non-zero (inferred fallback)")
	}
	if p.LatencyMS < 5 {
		t.Errorf("LatencyMS = %d, want ≥ 5ms", p.LatencyMS)
	}
	if prov.calls != 1 {
		t.Errorf("provider.calls = %d, want 1", prov.calls)
	}

	// Cache file should exist.
	cachePath := filepath.Join(dir, "db", "study_probes.json")
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache file missing: %v", err)
	}
}

func TestProbe_CacheHitNoProvider(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{latency: 5 * time.Millisecond}

	first, err := Probe(context.Background(), prov, "m", "ep", dir, time.Hour)
	if err != nil {
		t.Fatalf("Probe 1: %v", err)
	}
	provCallsAfterFirst := prov.calls

	second, err := Probe(context.Background(), prov, "m", "ep", dir, time.Hour)
	if err != nil {
		t.Fatalf("Probe 2: %v", err)
	}
	if prov.calls != provCallsAfterFirst {
		t.Errorf("provider.calls = %d, want unchanged (%d) — cache should have served the second call", prov.calls, provCallsAfterFirst)
	}
	if second.Source != "cached" {
		t.Errorf("Source = %q, want cached", second.Source)
	}
	if second.CtxWindowTokens != first.CtxWindowTokens {
		t.Errorf("ctx tokens differ: first=%d second=%d", first.CtxWindowTokens, second.CtxWindowTokens)
	}
}

func TestProbe_StaleEntryReprobes(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{latency: 5 * time.Millisecond}

	// Write a stale entry by hand.
	cachePath := filepath.Join(dir, "db", "study_probes.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := probeFile{
		SchemaVersion: "1",
		Probes: map[string]ModelProbe{
			ProbeKey("m", "ep"): {
				ModelID:         "m",
				Endpoint:        "ep",
				CtxWindowTokens: 999,
				LatencyMS:       9999,
				ProbedAt:        time.Now().Add(-time.Hour),
				Source:          "openai_compat_models",
			},
		},
	}
	bb, _ := json.MarshalIndent(stale, "", "  ")
	if err := os.WriteFile(cachePath, bb, 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// TTL of 1 minute → stale.
	p, err := Probe(context.Background(), prov, "m", "ep", dir, time.Minute)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.CtxWindowTokens == 999 {
		t.Errorf("got stale ctx %d; expected re-probe", p.CtxWindowTokens)
	}
	if p.LatencyMS == 9999 {
		t.Errorf("got stale latency %d; expected fresh measurement", p.LatencyMS)
	}
}

func TestProbe_TwoKeysCoexist(t *testing.T) {
	dir := t.TempDir()
	prov := &fakeProvider{latency: 5 * time.Millisecond}

	if _, err := Probe(context.Background(), prov, "model-a", "ep1", dir, time.Hour); err != nil {
		t.Fatalf("Probe A: %v", err)
	}
	if _, err := Probe(context.Background(), prov, "model-b", "ep2", dir, time.Hour); err != nil {
		t.Fatalf("Probe B: %v", err)
	}

	cachePath := filepath.Join(dir, "db", "study_probes.json")
	bb, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var pf probeFile
	if err := json.Unmarshal(bb, &pf); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if len(pf.Probes) != 2 {
		t.Errorf("expected 2 cache entries, got %d", len(pf.Probes))
	}
	if _, ok := pf.Probes[ProbeKey("model-a", "ep1")]; !ok {
		t.Errorf("missing key for model-a")
	}
	if _, ok := pf.Probes[ProbeKey("model-b", "ep2")]; !ok {
		t.Errorf("missing key for model-b")
	}
}

func TestProbe_NilProviderReturnsInferred(t *testing.T) {
	dir := t.TempDir()
	p, err := Probe(context.Background(), nil, "claude-haiku-4.5", "", dir, time.Hour)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.CtxWindowTokens == 0 {
		t.Error("expected inferred ctx window for nil provider")
	}
	if p.Source != "inferred" {
		t.Errorf("Source = %q, want inferred", p.Source)
	}
	if p.LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0 with nil provider", p.LatencyMS)
	}
}
