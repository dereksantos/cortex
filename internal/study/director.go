package study

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Director decides where the FIRST study pass should sample, before any
// content has been read. It is the pre-pass sibling of the Curator: the
// curator deepens AFTER a pass (digest + leads + coverage in hand); the
// director aims BEFORE the first pass (only the goal + project map in
// hand). A nil focus means "no direction — use mechanical sampling,"
// preserving the goal-blind first pass the HierarchicalSampler has always
// done.
//
// The director is a FOCUS, not a FILTER: the FocusSampler it feeds still
// falls back to the mechanical sampler when the in-focus set is exhausted,
// so the first pass never samples FEWER regions than it would without
// direction — it just front-loads the goal-relevant ones.
type Director interface {
	Direct(goal string) *Focus
}

// HeuristicDirector is the deterministic no-op director: it never directs
// (returns nil), so the first pass falls back to the mechanical sampler.
// This is the default and the fallback for ModelDirector.
type HeuristicDirector struct{}

// Direct implements Director.
func (HeuristicDirector) Direct(string) *Focus { return nil }

// ModelDirector is the LLM-backed director: a small bounded call over the
// goal + project map, returning a Focus that biases the first pass toward
// goal-relevant regions. Any failure (unavailable, error, unparseable, or
// no goal/map to direct from) degrades to nil — no direction → mechanical
// sampling, so the first pass is never worse than today.
type ModelDirector struct {
	Provider   llm.Provider
	Fallback   Director
	ProjectMap string
}

// Direct implements Director.
func (m ModelDirector) Direct(goal string) *Focus {
	if m.Provider == nil || !m.Provider.IsAvailable() {
		if m.Fallback != nil {
			return m.Fallback.Direct(goal)
		}
		return nil
	}
	// Without a goal or a map there's nothing to direct from — the
	// mechanical sampler's breadth is the right default.
	if strings.TrimSpace(goal) == "" || strings.TrimSpace(m.ProjectMap) == "" {
		return nil
	}
	sys, user := buildDirectorPrompt(goal, m.ProjectMap)
	out, err := m.Provider.GenerateWithSystem(context.Background(), user, sys)
	if err != nil {
		if m.Fallback != nil {
			return m.Fallback.Direct(goal)
		}
		return nil
	}
	return parseDirectorResponse(out)
}

const directorSystemPrompt = `You direct the first sampling pass of a codebase study. You are given a task and the full project map (every file and its symbols). No content has been read yet. Your job is to pick the single region the first pass should prioritize to make the most progress on the task.

Prefer specificity: if the task names a concern (e.g., "how does routing work"), pick the file whose symbols suggest it handles that concern. If the task is generic (e.g., "overview"), skip — the mechanical sampler spreads draws across the whole tree, which is better for breadth.

Respond with a single JSON object and nothing else:
{"skip":true}
or
{"focus_path":"relpath","focus_lines":[start,end]}

focus_path is the file or subdirectory to bias toward (workdir-relative, slash-separated). focus_lines is optional — omit it to bias toward the whole file, or provide [start,end] to narrow to a line range within it.`

func buildDirectorPrompt(goal, projectMap string) (system, user string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n", goal)
	b.WriteString("\nProject map (the FULL file tree — no content has been read yet; pick where to start):\n")
	b.WriteString(projectMap)
	if !strings.HasSuffix(projectMap, "\n") {
		b.WriteString("\n")
	}
	return directorSystemPrompt, b.String()
}

// parseDirectorResponse extracts a Focus from the model's JSON response.
// Returns nil for skip, empty focus, or any parse failure — nil means
// "no direction," and the caller falls back to mechanical sampling.
func parseDirectorResponse(raw string) *Focus {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return nil
	}
	var j struct {
		Skip       bool   `json:"skip"`
		FocusPath  string `json:"focus_path"`
		FocusLines []int  `json:"focus_lines"`
	}
	if err := json.Unmarshal([]byte(obj), &j); err != nil {
		return nil
	}
	if j.Skip {
		return nil
	}
	path := strings.TrimSpace(j.FocusPath)
	if path == "" && len(j.FocusLines) == 0 {
		return nil
	}
	f := &Focus{Path: path}
	if len(j.FocusLines) == 2 {
		f.Lines = [2]int{j.FocusLines[0], j.FocusLines[1]}
	}
	return f
}
