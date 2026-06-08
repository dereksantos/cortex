package study

import (
	"context"
	"errors"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// scriptedCuratorProvider is a minimal llm.Provider returning a canned
// response, for testing the model-backed curator without a real LLM.
type scriptedCuratorProvider struct {
	resp  string
	avail bool
	err   error
}

func (p scriptedCuratorProvider) Generate(context.Context, string) (string, error) {
	return p.resp, p.err
}
func (p scriptedCuratorProvider) GenerateWithSystem(context.Context, string, string) (string, error) {
	return p.resp, p.err
}
func (p scriptedCuratorProvider) GenerateWithStats(context.Context, string) (string, llm.GenerationStats, error) {
	return p.resp, llm.GenerationStats{}, p.err
}
func (p scriptedCuratorProvider) IsAvailable() bool { return p.avail }
func (p scriptedCuratorProvider) Name() string      { return "scripted" }

func TestHeuristicCurator_LowCoverageDensifies(t *testing.T) {
	resp := StudyResponse{
		Coverage: Coverage{Pct: 0.10},
		Deepen:   Deepen{Densify: DeepenRef{Density: "dense"}},
	}
	d := HeuristicCurator{}.Decide(resp, "")
	if d.Kind != DecisionDensify {
		t.Fatalf("Kind = %q, want DENSIFY", d.Kind)
	}
	if ResolveDensity(d.Density) != densityDenseK {
		t.Errorf("densify should carry the next density, got k=%d", ResolveDensity(d.Density))
	}
}

func TestHeuristicCurator_StrongLeadTargets(t *testing.T) {
	resp := StudyResponse{
		Coverage: Coverage{Pct: 0.10},
		Leads:    []Lead{{RelPath: "a.go", NearLine: 1400, Why: "references PgStorage"}},
	}
	d := HeuristicCurator{}.Decide(resp, "")
	if d.Kind != DecisionTarget {
		t.Fatalf("Kind = %q, want TARGET", d.Kind)
	}
	if d.Focus == nil || d.Focus.Lines[0] > 1400 || d.Focus.Lines[1] < 1400 {
		t.Errorf("TARGET focus should bracket the lead line 1400, got %+v", d.Focus)
	}
}

func TestHeuristicCurator_ExhaustedDone(t *testing.T) {
	d := HeuristicCurator{}.Decide(StudyResponse{Exhausted: true, Leads: []Lead{{NearLine: 5}}}, "")
	if d.Kind != DecisionDone {
		t.Errorf("exhausted study should be DONE regardless of leads, got %q", d.Kind)
	}
}

func TestHeuristicCurator_GroundedDone(t *testing.T) {
	resp := StudyResponse{Coverage: Coverage{Pct: 0.85}, Digest: "the file does X"}
	d := HeuristicCurator{}.Decide(resp, "")
	if d.Kind != DecisionDone {
		t.Errorf("well-covered, lead-free study should be DONE, got %q", d.Kind)
	}
}

func TestModelCurator_ParsesDecision(t *testing.T) {
	prov := scriptedCuratorProvider{resp: `{"kind":"TARGET","focus_lines":[100,200]}`, avail: true}
	d := ModelCurator{Provider: prov}.Decide(StudyResponse{Coverage: Coverage{Pct: 0.1}}, "find the bug")
	if d.Kind != DecisionTarget {
		t.Fatalf("Kind = %q, want TARGET", d.Kind)
	}
	if d.Focus == nil || d.Focus.Lines != [2]int{100, 200} {
		t.Errorf("focus = %+v, want lines [100 200]", d.Focus)
	}
}

func TestModelCurator_FallsBackOnError(t *testing.T) {
	// Provider errors → fall back to the heuristic, which DONEs an
	// exhausted study.
	prov := scriptedCuratorProvider{avail: true, err: errors.New("model down")}
	d := ModelCurator{Provider: prov}.Decide(StudyResponse{Exhausted: true}, "")
	if d.Kind != DecisionDone {
		t.Errorf("error should fall back to heuristic DONE, got %q", d.Kind)
	}

	// Unavailable provider → heuristic too.
	d2 := ModelCurator{Provider: scriptedCuratorProvider{avail: false}}.Decide(
		StudyResponse{Coverage: Coverage{Pct: 0.1}, Deepen: Deepen{Densify: DeepenRef{Density: "normal"}}}, "")
	if d2.Kind != DecisionDensify {
		t.Errorf("unavailable provider should fall back to heuristic DENSIFY, got %q", d2.Kind)
	}
}
