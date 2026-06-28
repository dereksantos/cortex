package main

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/internal/outline"
	"github.com/dereksantos/cortex/internal/tools"
)

// study.go wires the study subagent onto the unified engine. The study tool
// (internal/tools) seeds via Outliner and runs via SubAgentRunner; this file
// supplies both on *CortexSession — the composition root. There is no second
// loop and no recursion: study is the Study profile on the one runLoop.
// See docs/study-subagent.md §1.

// Outline renders the structural map of a path (the Outliner seam) — the study
// seed and the outline tool both read it.
func (cs *CortexSession) Outline(path string, budget int) (string, error) {
	return outline.Render(path, budget)
}

// specForRole resolves a subagent profile's model binding. Today every profile
// (only Study) draws from the study binding (the reasoner tag, thinking ON).
func (cs *CortexSession) specForRole(role string) ModelSpec {
	return cs.Study
}

// root is the workspace root the study door-guard (ConfinePath) confines reads
// to — the delete root when set, else the working directory.
func (cs *CortexSession) root() string {
	if cs.deleteRoot != "" {
		return cs.deleteRoot
	}
	return "."
}

// RunSubagent resolves the model, builds the request + the profile's toolset, and
// hands off to the shared runLoop. The blocking sender keeps the subagent off the
// main conversation; its usage folds into the session totals. Satisfies
// tools.SubAgentRunner.
func (cs *CortexSession) RunSubagent(ctx context.Context, sa tools.Subagent, seed string) (string, error) {
	digest, _, err := cs.runSubagentStats(ctx, sa, seed)
	return digest, err
}

// runSubagentStats is RunSubagent with the engine's run stats exposed — the eval
// reads them for the mechanical scorer (study phase 5); normal callers use
// RunSubagent and discard them.
func (cs *CortexSession) runSubagentStats(ctx context.Context, sa tools.Subagent, seed string) (string, loopStats, error) {
	spec := cs.specForRole(sa.Role)
	if !cs.quiet {
		fmt.Println(withColor(fmt.Sprintf("  ▸ %s via %s", sa.Name, spec.Model), green))
	}
	req := requestFor(spec, sa.System, seed, sa.Tools, sa.Bounds.MaxTokens)
	ts := Toolset{Tools: sa.Tools, Dispatch: cs.dispatcherFor(sa)}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	digest, stats, err := runLoop(ctx, cs.blockingSender(), req, ts, sa.Bounds, nil, appendMsg)
	// Fold the subagent's billed usage into the session totals (it does not set
	// LastPromptTokens — that gauge belongs to the coder's own context).
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.costUSD += stats.Cost
	return digest, stats, err
}

// dispatcherFor builds a profile's dispatcher: the offered-tool allowlist plus
// the study read transforms (path confinement + targeted read), composed HERE so
// tools.Execute stays caller-agnostic (no "subagent mode" flags). The one
// door-guard confines the path arg of EVERY path-taking tool, exactly once.
func (cs *CortexSession) dispatcherFor(sa tools.Subagent) AgentDispatcher {
	allow := tools.AllowOf(sa.Tools)
	return DispatchFunc(func(ctx context.Context, call ToolCall) string {
		if !allow[call.Function.Name] {
			return "Error: " + call.Function.Name + " is not available here."
		}
		call, err := tools.ConfinePath(call, cs.root())
		if err != nil {
			return "Error: " + err.Error()
		}
		call, refusal := tools.TargetedRead(call)
		if refusal != "" {
			return refusal
		}
		out, err := tools.Execute(ctx, call, cs)
		if err != nil {
			return "Error: " + err.Error()
		}
		return out
	})
}
