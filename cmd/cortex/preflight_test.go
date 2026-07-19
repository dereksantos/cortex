package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/llm"
)

func openrouterCfg() *Config {
	return &Config{Backend: Backend{Type: "openrouter"}}
}

func fakeListModels(models []llm.OpenRouterModel, err error) listModelsFn {
	return func(context.Context) ([]llm.OpenRouterModel, error) {
		return models, err
	}
}

// captureStderr is defined in main_test.go and reused here.

// readModelSubstitutions reads every model.substitution entry written to
// dir, in order.
func readModelSubstitutions(t *testing.T, dir string) []journal.ModelSubstitutionPayload {
	t.Helper()
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Fatalf("journal.NewReader: %v", err)
	}
	defer r.Close()

	var out []journal.ModelSubstitutionPayload
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Reader.Next: %v", err)
		}
		p, err := journal.ParseModelSubstitution(e)
		if err != nil {
			t.Fatalf("ParseModelSubstitution: %v", err)
		}
		out = append(out, *p)
	}
	return out
}

// TestPreflightCuratedModelsSkipsNonOpenRouter pins that the preflight never
// calls listModels at all for a non-openrouter backend, regardless of
// whether the bound model happens to collide with a curated id.
func TestPreflightCuratedModelsSkipsNonOpenRouter(t *testing.T) {
	calls := 0
	listModels := func(context.Context) ([]llm.OpenRouterModel, error) {
		calls++
		return nil, nil
	}
	cfg := &Config{Backend: Backend{Type: "litellm"}}
	code := ModelSpec{Model: curatedTopPick().ID}
	study := ModelSpec{Model: "study-model"}

	gotCode, gotStudy := preflightCuratedModels(context.Background(), cfg, code, study, t.TempDir(), listModels)

	if calls != 0 {
		t.Errorf("listModels called %d times, want 0 for a non-openrouter backend", calls)
	}
	if gotCode != code || gotStudy != study {
		t.Errorf("bindings changed: got (%+v, %+v), want unchanged (%+v, %+v)", gotCode, gotStudy, code, study)
	}
}

// TestPreflightCuratedModelsSkipsNonCuratedModel pins that a bound model
// outside the curated table (a user's own pin) is left entirely alone, with
// no network call at all.
func TestPreflightCuratedModelsSkipsNonCuratedModel(t *testing.T) {
	calls := 0
	listModels := func(context.Context) ([]llm.OpenRouterModel, error) {
		calls++
		return nil, nil
	}
	code := ModelSpec{Model: "anthropic/claude-haiku-4.5"}
	study := ModelSpec{Model: "some/other-model"}

	gotCode, gotStudy := preflightCuratedModels(context.Background(), openrouterCfg(), code, study, t.TempDir(), listModels)

	if calls != 0 {
		t.Errorf("listModels called %d times, want 0 when neither binding is a curated id", calls)
	}
	if gotCode != code || gotStudy != study {
		t.Errorf("bindings changed: got (%+v, %+v), want unchanged", gotCode, gotStudy)
	}
}

// TestPreflightCuratedModelsBoundModelStillServedNoOp pins the common case:
// the curated top pick is still being served, so nothing changes and
// nothing is journaled.
func TestPreflightCuratedModelsBoundModelStillServedNoOp(t *testing.T) {
	top := curatedTopPick()
	served := []llm.OpenRouterModel{{ID: top.ID, ContextLength: top.Window}}
	dir := t.TempDir()

	var code, study ModelSpec
	stderr := captureStderr(t, func() {
		code, study = preflightCuratedModels(context.Background(), openrouterCfg(),
			ModelSpec{Model: top.ID}, ModelSpec{Model: top.ID}, dir, fakeListModels(served, nil))
	})

	if code.Model != top.ID || study.Model != top.ID {
		t.Errorf("bindings = (%q, %q), want both unchanged at %q", code.Model, study.Model, top.ID)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty (no substitution happened)", stderr)
	}
	if got := readModelSubstitutions(t, dir); len(got) != 0 {
		t.Errorf("journaled %d substitution events, want 0", len(got))
	}
}

// TestPreflightCuratedModelsMissingSubstitutesNextCurated is the
// deterministic-first case: the bound (curated) model is missing from the
// live catalog, but a lower-preference curated entry is still served — the
// preflight should pick THAT, not fall through to discovery.
func TestPreflightCuratedModelsMissingSubstitutesNextCurated(t *testing.T) {
	if len(curatedFreeModels) < 2 {
		t.Fatal("test requires at least 2 curated entries")
	}
	missing := curatedFreeModels[0]
	survivor := curatedFreeModels[1]
	served := []llm.OpenRouterModel{
		{ID: survivor.ID, ContextLength: survivor.Window},
		{ID: "some/unrelated-model:free", ContextLength: 4096},
	}
	dir := t.TempDir()

	var code, study ModelSpec
	stderr := captureStderr(t, func() {
		code, study = preflightCuratedModels(context.Background(), openrouterCfg(),
			ModelSpec{Model: missing.ID}, ModelSpec{Model: "not-curated/model"}, dir, fakeListModels(served, nil))
	})

	if code.Model != survivor.ID {
		t.Errorf("code.Model = %q, want the next surviving curated pick %q", code.Model, survivor.ID)
	}
	if code.Window != survivor.Window {
		t.Errorf("code.Window = %d, want %d", code.Window, survivor.Window)
	}
	if study.Model != "not-curated/model" {
		t.Errorf("study.Model = %q, want unchanged (not a curated id)", study.Model)
	}

	// stderr message shape: names old, new, and gives a reason.
	if !strings.Contains(stderr, "code") || !strings.Contains(stderr, missing.ID) || !strings.Contains(stderr, survivor.ID) {
		t.Errorf("stderr = %q, want it to name role %q, old %q, and new %q", stderr, "code", missing.ID, survivor.ID)
	}
	if strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want exactly one line", stderr)
	}

	events := readModelSubstitutions(t, dir)
	if len(events) != 1 {
		t.Fatalf("journaled %d substitution events, want 1 (only code changed)", len(events))
	}
	if events[0].Role != roleCode || events[0].Old != missing.ID || events[0].New != survivor.ID {
		t.Errorf("journaled event = %+v, want Role=%q Old=%q New=%q", events[0], roleCode, missing.ID, survivor.ID)
	}
	if events[0].Reason == "" {
		t.Error("journaled event has empty Reason")
	}
}

// TestPreflightCuratedModelsAllCuratedMissingFallsBackToDiscovery is the
// adaptive case: every curated entry has gone missing from the live
// catalog, so the preflight must fall back to the coder-name-then-context
// discovery heuristic over the served :free catalog.
func TestPreflightCuratedModelsAllCuratedMissingFallsBackToDiscovery(t *testing.T) {
	top := curatedTopPick()
	served := []llm.OpenRouterModel{
		// None of the curated ids are present.
		{ID: "some/plain-model:free", ContextLength: 32000},
		{ID: "some/coder-special:free", ContextLength: 16000},      // coder-ish name, smaller context
		{ID: "some/bigger-coder-model:free", ContextLength: 64000}, // coder-ish name, largest context among coder matches
		{ID: "not-free/paid-model", ContextLength: 1000000},        // not :free — must be excluded
	}
	dir := t.TempDir()

	var code, study ModelSpec
	stderr := captureStderr(t, func() {
		code, study = preflightCuratedModels(context.Background(), openrouterCfg(),
			ModelSpec{Model: top.ID}, ModelSpec{Model: top.ID}, dir, fakeListModels(served, nil))
	})

	want := "some/bigger-coder-model:free"
	if code.Model != want {
		t.Errorf("code.Model = %q, want the largest coder-named :free model %q", code.Model, want)
	}
	if study.Model != want {
		t.Errorf("study.Model = %q, want %q", study.Model, want)
	}
	if code.Window != 64000 {
		t.Errorf("code.Window = %d, want 64000", code.Window)
	}

	if strings.Count(stderr, "\n") != 2 {
		t.Errorf("stderr = %q, want exactly two lines (code + study)", stderr)
	}

	events := readModelSubstitutions(t, dir)
	if len(events) != 2 {
		t.Fatalf("journaled %d substitution events, want 2", len(events))
	}
	for _, e := range events {
		if e.New != want {
			t.Errorf("journaled event New = %q, want %q", e.New, want)
		}
	}
}

// TestPreflightCuratedModelsNetworkErrorLeavesUnchanged pins the "never
// block startup" contract: a listModels failure (timeout, network down)
// leaves both bindings exactly as configured, with no stderr and no journal
// event.
func TestPreflightCuratedModelsNetworkErrorLeavesUnchanged(t *testing.T) {
	top := curatedTopPick()
	dir := t.TempDir()

	var code, study ModelSpec
	stderr := captureStderr(t, func() {
		code, study = preflightCuratedModels(context.Background(), openrouterCfg(),
			ModelSpec{Model: top.ID}, ModelSpec{Model: top.ID}, dir,
			fakeListModels(nil, errors.New("dial tcp: connection refused")))
	})

	if code.Model != top.ID || study.Model != top.ID {
		t.Errorf("bindings = (%q, %q), want unchanged at %q after a network error", code.Model, study.Model, top.ID)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on a preflight network failure", stderr)
	}
	if got := readModelSubstitutions(t, dir); len(got) != 0 {
		t.Errorf("journaled %d substitution events, want 0 on a preflight network failure", len(got))
	}
}

// TestModelSubstitutionJournalDirLocation pins the class-dir convention:
// <contextDir>/journal/model, matching emitSessionMetrics's
// <ContextDir>/journal/eval sibling.
func TestModelSubstitutionJournalDirLocation(t *testing.T) {
	got := modelSubstitutionJournalDir("/tmp/proj/.cortex")
	want := filepath.Join("/tmp/proj/.cortex", "journal", "model")
	if got != want {
		t.Errorf("modelSubstitutionJournalDir(...) = %q, want %q", got, want)
	}
}
