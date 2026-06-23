package study

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestHeuristicDirector_NeverDirects(t *testing.T) {
	if f := (HeuristicDirector{}).Direct("any goal"); f != nil {
		t.Errorf("HeuristicDirector should never direct, got %+v", f)
	}
}

func TestModelDirector_NilProviderFallsBack(t *testing.T) {
	// No provider → nil (no direction). A fallback director is consulted.
	fb := stubDirector{focus: &Focus{Path: "fallback.go"}}
	d := ModelDirector{Provider: nil, Fallback: fb}
	if f := d.Direct("goal"); f == nil || f.Path != "fallback.go" {
		t.Errorf("nil provider should fall back, got %+v", f)
	}
}

func TestModelDirector_UnavailableFallsBack(t *testing.T) {
	prov := scriptedCuratorProvider{avail: false}
	fb := stubDirector{focus: &Focus{Path: "fallback.go"}}
	d := ModelDirector{Provider: prov, Fallback: fb}
	if f := d.Direct("goal"); f == nil || f.Path != "fallback.go" {
		t.Errorf("unavailable provider should fall back, got %+v", f)
	}
}

func TestModelDirector_NoGoalReturnsNil(t *testing.T) {
	prov := scriptedCuratorProvider{resp: `{"focus_path":"a.go"}`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	if f := d.Direct(""); f != nil {
		t.Errorf("empty goal should not direct, got %+v", f)
	}
	if f := d.Direct("   "); f != nil {
		t.Errorf("blank goal should not direct, got %+v", f)
	}
}

func TestModelDirector_NoMapReturnsNil(t *testing.T) {
	prov := scriptedCuratorProvider{resp: `{"focus_path":"a.go"}`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: ""}
	if f := d.Direct("real goal"); f != nil {
		t.Errorf("empty project map should not direct, got %+v", f)
	}
}

func TestModelDirector_ParsesFocusPath(t *testing.T) {
	prov := scriptedCuratorProvider{
		resp:  `{"focus_path":"internal/study/controller.go"}`,
		avail: true,
	}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	f := d.Direct("how does the controller halt")
	if f == nil {
		t.Fatal("expected a focus, got nil")
	}
	if f.Path != "internal/study/controller.go" {
		t.Errorf("Path = %q, want internal/study/controller.go", f.Path)
	}
	if f.Lines[0] != 0 || f.Lines[1] != 0 {
		t.Errorf("no focus_lines in response → Lines should be zero, got %v", f.Lines)
	}
}

func TestModelDirector_ParsesFocusPathAndLines(t *testing.T) {
	prov := scriptedCuratorProvider{
		resp:  `{"focus_path":"internal/study/controller.go","focus_lines":[150,300]}`,
		avail: true,
	}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	f := d.Direct("how does the controller halt")
	if f == nil {
		t.Fatal("expected a focus, got nil")
	}
	if f.Path != "internal/study/controller.go" {
		t.Errorf("Path = %q, want internal/study/controller.go", f.Path)
	}
	if f.Lines != [2]int{150, 300} {
		t.Errorf("Lines = %v, want [150 300]", f.Lines)
	}
}

func TestModelDirector_SkipReturnsNil(t *testing.T) {
	prov := scriptedCuratorProvider{resp: `{"skip":true}`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	if f := d.Direct("generic overview"); f != nil {
		t.Errorf("skip should return nil focus, got %+v", f)
	}
}

func TestModelDirector_MalformedReturnsNil(t *testing.T) {
	// No JSON object → nil.
	prov := scriptedCuratorProvider{resp: `I think you should look at controller.go`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	if f := d.Direct("goal"); f != nil {
		t.Errorf("malformed response should return nil, got %+v", f)
	}
}

func TestModelDirector_EmptyFocusReturnsNil(t *testing.T) {
	// Valid JSON but no path and no lines → nil.
	prov := scriptedCuratorProvider{resp: `{}`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: "some map"}
	if f := d.Direct("goal"); f != nil {
		t.Errorf("empty focus should return nil, got %+v", f)
	}
}

func TestModelDirector_ProviderErrorFallsBack(t *testing.T) {
	prov := scriptedCuratorProvider{avail: true, err: errors.New("model down")}
	fb := stubDirector{focus: &Focus{Path: "fallback.go"}}
	d := ModelDirector{Provider: prov, Fallback: fb, ProjectMap: "some map"}
	if f := d.Direct("goal"); f == nil || f.Path != "fallback.go" {
		t.Errorf("provider error should fall back, got %+v", f)
	}
}

func TestModelDirector_PromptIncludesGoalAndMap(t *testing.T) {
	const mapText = "pkg/ — 2 files\n\n  a.go (50)\n    FuncA:10\n  b.go (50)\n    FuncB:10\n"
	prov := &capturingCuratorProvider{resp: `{"skip":true}`, avail: true}
	d := ModelDirector{Provider: prov, ProjectMap: mapText}
	d.Direct("how does FuncB work")
	if !strings.Contains(prov.lastUser, mapText) {
		t.Errorf("director prompt missing project map:\n%s", prov.lastUser)
	}
	if !strings.Contains(prov.lastUser, "how does FuncB work") {
		t.Errorf("director prompt missing goal:\n%s", prov.lastUser)
	}
}

// stubDirector is a test Director returning a fixed focus.
type stubDirector struct {
	focus *Focus
}

func (s stubDirector) Direct(string) *Focus { return s.focus }

// TestStudyLoop_DirectorSetsFirstPassFocus verifies the end-to-end wiring:
// a Director that returns a focus biases the first pass's sample toward
// that region, and the focus is cleared for subsequent passes (the
// curator owns deepening from pass 1 on).
func TestStudyLoop_DirectorSetsFirstPassFocus(t *testing.T) {
	dir := t.TempDir()
	blob := make([]byte, 200000)
	for i := range blob {
		if (i+1)%50 == 0 {
			blob[i] = '\n'
		} else {
			blob[i] = 'a'
		}
	}
	// Two files so the focus path can target one.
	pathA := dir + "/a.txt"
	pathB := dir + "/b.txt"
	if err := writeFile(pathA, blob); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(pathB, blob); err != nil {
		t.Fatal(err)
	}

	// Director targets file b.txt; curator immediately DONEs.
	director := stubDirector{focus: &Focus{Path: "b.txt"}}
	cur := &scriptedCurator{decisions: []Decision{{Kind: DecisionDone}}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path:     dir,
		Window:   8192,
		Density:  "sparse",
		Session:  "dir",
		Infer:    passDigest,
		Director: director,
	}, cur, 3)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(res.Passes) == 0 {
		t.Fatal("expected at least one pass")
	}
	// The first pass should have sampled b.txt (the directed file).
	sampledB := false
	for _, s := range res.Passes[0].Response.Sampled {
		if s.RelPath == "b.txt" {
			sampledB = true
			break
		}
	}
	if !sampledB {
		t.Errorf("director targeted b.txt but first pass did not sample it")
	}
}

// TestStudyLoop_NilDirectorUnchanged verifies that a nil director leaves
// the first pass mechanical (no focus set).
func TestStudyLoop_NilDirectorUnchanged(t *testing.T) {
	path := writeBytesFile(t, 120000)
	cur := &scriptedCurator{decisions: []Decision{{Kind: DecisionDone}}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path:    path,
		Window:  8192,
		Density: "sparse",
		Session: "nil-dir",
		Infer:   passDigest,
		// Director intentionally nil.
	}, cur, 2)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(res.Passes) == 0 {
		t.Fatal("expected at least one pass")
	}
	// A mechanical first pass samples SOMETHING (not empty).
	if len(res.Passes[0].Response.Sampled) == 0 {
		t.Errorf("nil director should still sample mechanically, got empty sample")
	}
}

// TestStudyLoop_ExplicitFocusBeatsDirector verifies that an explicit
// Focus on the request wins over the director — the director only fires
// when no focus is set.
func TestStudyLoop_ExplicitFocusBeatsDirector(t *testing.T) {
	path := writeBytesFile(t, 200000)
	// Director would target lines 1-10, but the explicit focus targets
	// lines 2000-2100. The first pass should follow the explicit focus.
	director := stubDirector{focus: &Focus{Lines: [2]int{1, 10}}}
	cur := &scriptedCurator{decisions: []Decision{{Kind: DecisionDone}}}
	res, err := StudyLoop(context.Background(), StudyRequest{
		Path:     path,
		Window:   8192,
		Density:  "sparse",
		Session:  "explicit",
		Infer:    passDigest,
		Focus:    &Focus{Lines: [2]int{2000, 2100}},
		Director: director,
	}, cur, 2)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(res.Passes) == 0 {
		t.Fatal("expected at least one pass")
	}
	// The first pass should sample near 2000-2100, not near 1-10.
	inExplicit := 0
	for _, s := range res.Passes[0].Response.Sampled {
		if s.LineStart <= 2100 && s.LineEnd >= 2000 {
			inExplicit++
		}
	}
	if inExplicit == 0 {
		t.Errorf("explicit focus should beat director, but first pass did not sample near 2000-2100")
	}
}

// writeFile is a test helper that writes data to path.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
