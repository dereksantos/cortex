package study

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// The curator is the deepening decision step that sits on top of a study
// pass: given the digest + leads + coverage, it decides whether the
// answer is in hand (DONE), the region needs more density (DENSIFY), or
// a specific lead should be chased (TARGET). It is deliberately a thin,
// swappable layer — the agent loop can read StudyResponse.Deepen/Leads
// and decide itself, or delegate to a Curator. The model-backed curator
// is the dedicated micro-agent; the heuristic curator is its
// deterministic fallback and the default for tests.

// Decision kinds.
const (
	DecisionDone    = "DONE"
	DecisionDensify = "DENSIFY"
	DecisionTarget  = "TARGET"
)

// curatorGroundedCoveragePct is the coverage above which a lead-free
// study is considered grounded enough to stop.
const curatorGroundedCoveragePct = 0.5

// curatorTargetWindow is the half-width (in lines) of the focus range a
// TARGET decision builds around a lead.
const curatorTargetWindow = 40

// Decision is what a Curator returns.
type Decision struct {
	Kind    string  // DONE | DENSIFY | TARGET
	Focus   *Focus  // set for TARGET
	Density Density // set for DENSIFY (and optionally TARGET)
}

// Curator decides how (or whether) to deepen after a study pass.
type Curator interface {
	Decide(resp StudyResponse, goal string) Decision
}

// HeuristicCurator decides without an LLM: exhausted/grounded → DONE, a
// lead → TARGET it, low coverage → DENSIFY. Deterministic; also the
// fallback for the model-backed curator.
type HeuristicCurator struct{}

// Decide implements Curator.
func (HeuristicCurator) Decide(resp StudyResponse, _ string) Decision {
	if resp.Exhausted {
		return Decision{Kind: DecisionDone}
	}
	if len(resp.Leads) > 0 {
		l := resp.Leads[0]
		lo := l.NearLine - curatorTargetWindow
		if lo < 1 {
			lo = 1
		}
		return Decision{
			Kind:    DecisionTarget,
			Focus:   &Focus{Lines: [2]int{lo, l.NearLine + curatorTargetWindow}},
			Density: resp.Deepen.Target.Density,
		}
	}
	if resp.Coverage.Pct < curatorGroundedCoveragePct {
		return Decision{Kind: DecisionDensify, Density: resp.Deepen.Densify.Density}
	}
	return Decision{Kind: DecisionDone}
}

// ModelCurator is the dedicated curator micro-agent: a small bounded LLM
// call over the digest + leads + coverage + goal, returning a structured
// decision. Any failure (unavailable, error, unparseable) degrades to
// Fallback (HeuristicCurator by default).
type ModelCurator struct {
	Provider llm.Provider
	Fallback Curator
}

// Decide implements Curator.
func (m ModelCurator) Decide(resp StudyResponse, goal string) Decision {
	fb := m.Fallback
	if fb == nil {
		fb = HeuristicCurator{}
	}
	if m.Provider == nil || !m.Provider.IsAvailable() {
		return fb.Decide(resp, goal)
	}
	sys, user := buildCuratorPrompt(resp, goal)
	out, err := m.Provider.GenerateWithSystem(context.Background(), user, sys)
	if err != nil {
		return fb.Decide(resp, goal)
	}
	dec, ok := parseCuratorDecision(out)
	if !ok {
		return fb.Decide(resp, goal)
	}
	return dec
}

const curatorSystemPrompt = `You decide whether a partial study of a large file is good enough or needs to go deeper. You are given a digest, the coverage so far, and any leads (regions referenced but not yet read). Choose exactly one action:
- DONE: the digest answers the task, or coverage is sufficient.
- DENSIFY: sample more of the file at higher density (no specific target).
- TARGET: chase a specific lead — provide focus_lines [start,end].

Respond with a single JSON object and nothing else:
{"kind":"DONE|DENSIFY|TARGET","focus_lines":[start,end],"density":"sparse|normal|dense"}`

func buildCuratorPrompt(resp StudyResponse, goal string) (system, user string) {
	var b strings.Builder
	if goal != "" {
		fmt.Fprintf(&b, "Task: %s\n", goal)
	}
	fmt.Fprintf(&b, "Coverage: %.0f%% of effective lines seen; exhausted=%t\n", 100*resp.Coverage.Pct, resp.Exhausted)
	fmt.Fprintf(&b, "Digest:\n%s\n", resp.Digest)
	if len(resp.Leads) > 0 {
		b.WriteString("Leads (not yet read):\n")
		for _, l := range resp.Leads {
			fmt.Fprintf(&b, "  - %s near line %d: %s\n", l.RelPath, l.NearLine, l.Why)
		}
	}
	return curatorSystemPrompt, b.String()
}

func parseCuratorDecision(raw string) (Decision, bool) {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return Decision{}, false
	}
	var j struct {
		Kind       string `json:"kind"`
		FocusLines []int  `json:"focus_lines"`
		Density    string `json:"density"`
	}
	if err := json.Unmarshal([]byte(obj), &j); err != nil {
		return Decision{}, false
	}
	kind := strings.ToUpper(strings.TrimSpace(j.Kind))
	switch kind {
	case DecisionDone, DecisionDensify, DecisionTarget:
	default:
		return Decision{}, false
	}
	dec := Decision{Kind: kind}
	if j.Density != "" {
		dec.Density = j.Density
	}
	if len(j.FocusLines) == 2 {
		dec.Focus = &Focus{Lines: [2]int{j.FocusLines[0], j.FocusLines[1]}}
	}
	return dec, true
}
