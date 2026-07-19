package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestPersistBackendRoundTrip pins that PersistBackend writes a backend
// type and model recoverable by fileConfigProbe, and that unrelated
// top-level fields already in the file survive untouched.
func TestPersistBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"tools": {"allow_delete": false}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resolved := ResolvedBackend{Source: "openrouter-guided", Backend: llm.BackendOpenRouter, Model: "openrouter/free"}
	if err := PersistBackend(path, resolved); err != nil {
		t.Fatalf("PersistBackend: %v", err)
	}

	got, ok := (fileConfigProbe{Path: path}).Probe()
	if !ok {
		t.Fatalf("fileConfigProbe.Probe() ok = false, want true")
	}
	if got.Backend != llm.BackendOpenRouter {
		t.Errorf("Probe().Backend = %q, want %q", got.Backend, llm.BackendOpenRouter)
	}
	if got.Model != "openrouter/free" {
		t.Errorf("Probe().Model = %q, want %q", got.Model, "openrouter/free")
	}
	if got.Source != "config" {
		t.Errorf("Probe().Source = %q, want %q", got.Source, "config")
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if !jsonEqual(t, doc["tools"], []byte(`{"allow_delete": false}`)) {
		t.Errorf("tools field mutated: %s", doc["tools"])
	}
}

// TestPersistBackendSeedsStudyFromCuratedPick pins E1+E2's "same model by
// default" seam: persisting a freshly resolved OpenRouter backend (bootstrap
// only ever resolves the code role) also seeds models.study with the same
// model + window, so a bootstrap-only config doesn't leave the Study
// subagent pointed at an empty model id.
func TestPersistBackendSeedsStudyFromCuratedPick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	pick := curatedTopPick()
	resolved := ResolvedBackend{Source: "openrouter-guided", Backend: llm.BackendOpenRouter, Model: pick.ID, Window: pick.Window}
	if err := PersistBackend(path, resolved); err != nil {
		t.Fatalf("PersistBackend: %v", err)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	var models map[string]ModelSpec
	if err := json.Unmarshal(doc["models"], &models); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	if models[roleCode].Model != pick.ID || models[roleCode].Window != pick.Window {
		t.Errorf("models.code = %+v, want model=%q window=%d", models[roleCode], pick.ID, pick.Window)
	}
	if models[roleStudy].Model != pick.ID || models[roleStudy].Window != pick.Window {
		t.Errorf("models.study = %+v, want model=%q window=%d (same model by default)", models[roleStudy], pick.ID, pick.Window)
	}
}

// TestPersistBackendNeverClobbersExistingStudyPin pins the guard: a config
// that already carries a models.study.model keeps it untouched, even when
// PersistBackend seats a new OpenRouter code model.
func TestPersistBackendNeverClobbersExistingStudyPin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"models": {"study": {"model": "user/own-pin"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pick := curatedTopPick()
	resolved := ResolvedBackend{Source: "openrouter-env", Backend: llm.BackendOpenRouter, Model: pick.ID, Window: pick.Window}
	if err := PersistBackend(path, resolved); err != nil {
		t.Fatalf("PersistBackend: %v", err)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	var models map[string]ModelSpec
	if err := json.Unmarshal(doc["models"], &models); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	if models[roleStudy].Model != "user/own-pin" {
		t.Errorf("models.study.model = %q, want unchanged %q", models[roleStudy].Model, "user/own-pin")
	}
}

// TestPersistBackendOllamaDoesNotSeedStudy pins that the study-seeding step
// is OpenRouter-specific: an Ollama backend (no Model set at all, per
// BackendResolver's ollama-local stage) never touches models.study.
func TestPersistBackendOllamaDoesNotSeedStudy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	resolved := ResolvedBackend{Source: "ollama-local", Backend: llm.BackendOllama}
	if err := PersistBackend(path, resolved); err != nil {
		t.Fatalf("PersistBackend: %v", err)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if _, ok := doc["models"]; ok {
		t.Errorf("models field present = %s, want absent (ollama-local sets no Model)", doc["models"])
	}
}

// TestFileConfigProbeMissingOrEmptyMisses pins the negative cases: no
// file, and a file with no backend key, both miss (ok=false) rather
// than panicking or returning a zero-value hit.
func TestFileConfigProbeMissingOrEmptyMisses(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, ok := (fileConfigProbe{Path: filepath.Join(dir, "nope.json")}).Probe()
		if ok {
			t.Errorf("Probe() ok = true, want false for a missing file")
		}
	})

	t.Run("no backend key", func(t *testing.T) {
		path := filepath.Join(dir, "no-backend.json")
		if err := os.WriteFile(path, []byte(`{"tools": {}}`), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, ok := (fileConfigProbe{Path: path}).Probe()
		if ok {
			t.Errorf("Probe() ok = true, want false when no backend key is present")
		}
	})
}

// TestBackendResolverChainSecondRunShortCircuitsOnPersistedConfig is the
// M1.3 end-to-end case named in GOAL.md: resolve once with no config
// (falling through to the guided stage), persist the result, then
// resolve again wired to the persisted file as the Config stage — the
// second run must hit the config stage and never touch key/ollama/
// guided.
func TestBackendResolverChainSecondRunShortCircuitsOnPersistedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	firstRun := &BackendResolver{
		Config: &fakeConfigProbe{ok: false},
		Key:    &fakeKeyProbe{ok: false},
		Ollama: &fakeOllamaProbe{up: false},
		Guided: &fakeGuidedSetup{ok: true},
		Smoke:  &fakeSmokeProbe{},
	}
	resolved, err := firstRun.Resolve()
	if err != nil {
		t.Fatalf("first Resolve(): %v", err)
	}
	if resolved.Source != "openrouter-guided" {
		t.Fatalf("first Resolve().Source = %q, want %q", resolved.Source, "openrouter-guided")
	}
	if err := PersistBackend(path, resolved); err != nil {
		t.Fatalf("PersistBackend: %v", err)
	}

	key := &fakeKeyProbe{source: "openrouter-env", ok: true}
	ollama := &fakeOllamaProbe{up: true}
	guided := &fakeGuidedSetup{ok: true}
	secondRun := &BackendResolver{
		Config: fileConfigProbe{Path: path},
		Key:    key,
		Ollama: ollama,
		Guided: guided,
		Smoke:  &fakeSmokeProbe{},
	}
	got, err := secondRun.Resolve()
	if err != nil {
		t.Fatalf("second Resolve(): %v", err)
	}
	if got.Source != "config" {
		t.Errorf("second Resolve().Source = %q, want %q", got.Source, "config")
	}
	if key.calls != 0 {
		t.Errorf("key probe called %d times, want 0 (persisted config should short-circuit)", key.calls)
	}
	if ollama.calls != 0 {
		t.Errorf("ollama probe called %d times, want 0 (persisted config should short-circuit)", ollama.calls)
	}
	if guided.calls != 0 {
		t.Errorf("guided setup called %d times, want 0 (persisted config should short-circuit)", guided.calls)
	}
}
