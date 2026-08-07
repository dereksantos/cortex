package main

import (
	"context"
	"fmt"

	"github.com/dereksantos/cortex/internal/outline"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
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

// SeedBudget resolves the outline budget used to seed a subagent (the
// SeedBudgeter seam) — subagents.seed_budget_tokens, falling back to
// tools.StudySeedBudget.
func (cs *CortexSession) SeedBudget() int {
	return cs.Config.seedBudget()
}

// specForRole resolves a subagent profile's config-bound model spec. Only the
// study role has a binding of its own (the reasoner tag, thinking ON); it is
// also the fallback for any profile when there is no live coder request to
// inherit from — see subagentRequest.
func (cs *CortexSession) specForRole(role string) ModelSpec {
	return cs.Study
}

// subagentRequest builds a profile's opening request. Study and Learn draw
// from the study role binding (Learn, docs/learning-loop.md, has no "coder's
// current model" to inherit in the first place — it can run with no coder
// session live at all, e.g. a scheduled loop firing); any other profile
// (agent) inherits the coder's live request — model, endpoint, key, and
// effort wire fields, so it follows /model switches and defaults to running
// as the model that spawned it. A per-call sa.Model (the agent tool's
// optional "model" argument) then pins just the model name on that
// binding — and when that pin actually diverges from the inherited model,
// the inherited effort wire fields are cleared rather than silently carried
// over to a different model (docs/thinking-models.md known seam bug #2):
// they're re-derived to "on" only if the fleet says the pinned model can
// actually reason (hybrid/levels/always), else left neutral (send nothing).
func (cs *CortexSession) subagentRequest(sa tools.Subagent, seed string) *AgentRequest {
	dialect := dialectFor(cs.Config.isOpenRouter())
	req := requestFor(cs.specForRole(sa.Role), sa.System, seed, sa.Tools, sa.Bounds.MaxTokens, dialect)
	inheritedModel := ""
	if sa.Role != roleStudy && sa.Role != roleLearn && sa.Role != roleLearnUser && cs.Request != nil {
		inheritedModel = cs.Request.Model
		req.Model = cs.Request.Model
		req.BaseURL = cs.Request.BaseURL
		req.APIKey = cs.Request.APIKey
		req.Dialect = cs.Request.Dialect
		req.Effort = cs.Request.Effort
		req.ChatTemplateKwargs = cs.Request.ChatTemplateKwargs
		req.Reasoning = cs.Request.Reasoning
		req.Temperature = cs.Request.Temperature
		// P1: the agent profile runs on the coder's model, so it runs on the
		// coder's transport budget too — not the study role's
		// request_timeout_sec/max_send_attempts/retry_backoff_ms (see
		// ModelSpec's doc comment in config.go).
		req.Timeout = cs.Request.Timeout
		req.MaxAttempts = cs.Request.MaxAttempts
		req.Backoff = cs.Request.Backoff
	}
	if sa.Model != "" {
		req.Model = sa.Model
		if inheritedModel != "" && sa.Model != inheritedModel {
			effort := llm.Effort{}
			if mode := cs.Fleet[sa.Model].thinkingMode(); mode == "hybrid" || mode == "levels" || mode == "always" {
				effort = llm.Effort{Level: llm.EffortOn}
			}
			applyEffort(req, dialect, effort)
		}
	}
	return req
}

// root is the workspace root the study door-guard (ConfinePath) confines reads
// to — the delete root when set (NewCortexSession always sets one), else the
// session's resolved Workspace, else the working directory. deleteRoot keeps
// priority: it is independently configurable (tools.delete_root) and today
// always populated by NewCortexSession, so this is a no-op fallback change
// for every session built the normal way — it only affects hand-constructed
// sessions with neither field set (in which case behavior is unchanged: "."
// still means the working directory).
func (cs *CortexSession) root() string {
	if cs.deleteRoot != "" {
		return cs.deleteRoot
	}
	if cs.workspace != nil {
		return cs.workspace.Root
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

// subagentDepthKey is the context key carrying how many subagent-call levels
// deep the current invocation already is. An unset (e.g. background) ctx
// reads as depth 0 — a fresh call from the coder's own tool dispatch or the
// study-eval driver. Threaded through runSubagentStats, the one function
// every subagent invocation funnels through (RunSubagent and study-eval both
// call it), so a subagent's own tool loop calling ANOTHER subagent tool sees
// one level deeper than its parent — see Subagent.DepthCap.
type subagentDepthKey struct{}

func subagentDepth(ctx context.Context) int {
	d, _ := ctx.Value(subagentDepthKey{}).(int)
	return d
}

func withSubagentDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, subagentDepthKey{}, d)
}

// runSubagentStats is RunSubagent with the engine's run stats exposed — the eval
// reads them for the mechanical scorer (study phase 5); normal callers use
// RunSubagent and discard them. This is the shared enforcement point for
// Subagent.DepthCap: a call at a depth beyond the profile's cap is refused
// before any model round-trip, so a cap-0 profile (Study) can never spawn a
// subagent, and a future cap-1 profile can spawn exactly one nested level —
// that child's own subagent calls land here again one level deeper and get
// refused in turn.
func (cs *CortexSession) runSubagentStats(ctx context.Context, sa tools.Subagent, seed string) (string, loopStats, error) {
	depth := subagentDepth(ctx)
	if depth > sa.DepthCap {
		return "", loopStats{}, fmt.Errorf("%s: subagent depth cap %d exceeded (called at depth %d)", sa.Name, sa.DepthCap, depth)
	}
	ctx = withSubagentDepth(ctx, depth+1)
	// subagents.study / subagents.agent override this profile's registered
	// Bounds (internal/tools.Study.Bounds / .Agent.Bounds). This mutates ONLY
	// this local copy of sa (Subagent is a value type, passed by value all
	// the way from tools.Lookup) — never the package-level tools.Study/.Agent
	// vars, so sessions (and tests) with different config never see each
	// other's overrides.
	sa.Bounds = cs.Config.subagentBounds(sa.Role, sa.Bounds)
	req := cs.subagentRequest(sa, seed)
	if !cs.quiet {
		// The banner sits in the same column as the subagent's tool lines:
		// tools.IndentPrefix() is the nesting margin the tools package is
		// already printing at (two spaces per subagent level, "" for a one-off
		// `cortex study` with no parent call), and the timestamp gutter keeps
		// it on the same left margin as every other line in the transcript.
		fmt.Println(tools.TimestampPrefix() + tools.IndentPrefix() +
			withColor(fmt.Sprintf("run: %s via %s", sa.Name, req.Model), green))
	}
	ts := Toolset{Tools: sa.Tools, Dispatch: cs.dispatcherFor(sa)}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	bounds := sa.Bounds
	bounds.EscalateEffort = cs.Config.effortEscalationEnabled()
	digest, stats, err := runLoop(ctx, cs.healingSender(sa.Role, cs.blockingSender()), req, ts, bounds, nil, appendMsg, nil)
	// Fold the subagent's billed usage into the session totals (it does not set
	// LastPromptTokens — that gauge belongs to the coder's own context).
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.reasoningTokens += stats.ReasoningTokens
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
		// LearnUser's promotion write path (docs/cross-source-learning.md
		// pieces 1+2, learn_user.go's preparePromotionWrite): every
		// memory_write call it issues is rewritten here BEFORE the
		// generic dispatch below — scope forced to "user" (LearnUser only
		// ever writes the user tier), the body's wikilinks un-linked
		// against what's really in the user tier right now, and a
		// provenance line appended mechanically — so both invariants hold
		// regardless of what the live model actually wrote.
		if sa.Role == roleLearnUser && call.Function.Name == tools.FunctionMemoryWrite {
			rewritten, err := cs.preparePromotionWrite(ctx, call)
			if err != nil {
				return "Error: " + err.Error()
			}
			call = rewritten
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
