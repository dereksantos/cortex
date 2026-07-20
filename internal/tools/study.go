package tools

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dereksantos/cortex/internal/agent"
)

// study.go defines the Subagent shape and the name→profile registry that lets
// every inheritor (study today; reflect/dream later) share ONE dispatch path
// in Execute, instead of a bespoke switch case per profile. Study is the only
// registered profile today; adding another (e.g. Reflect) is a `var` + a
// Register call. See docs/study-subagent.md §1.

// Subagent is a read-only (or otherwise scoped) agent profile: the variation a
// caller injects into the shared engine. Tools is both what's offered to the
// model and the execution allowlist.
type Subagent struct {
	Name   string       // banner / telemetry label
	Role   string       // model-role binding the runner resolves (e.g. "study"); also the tool's dispatch name, i.e. Function.Name on Declaration
	System string       // system prompt
	Tools  []Tool       // offered == execution allowlist
	Bounds agent.Bounds // MaxTokens (mandatory), MaxIter, ReadBudgetBytes

	// Declaration is this profile's own dispatchable tool (name/description/
	// schema) — what goes in a tool set like All. Kept explicit per profile
	// (not auto-generated) so each inheritor's description stays hand-tuned.
	Declaration Tool

	// Seed builds the subagent's opening user message from the goal, path, and
	// structural outline runSubagent assembled. A profile supplies its own to
	// vary the seed shape (e.g. a future reflect/dream profile); a nil Seed
	// falls back to StudySeed in runSubagent, so leaving it unset preserves the
	// pre-seam behavior.
	Seed func(goal, path, outline string) string

	// Model is a per-invocation model override, stamped by the dispatcher from
	// the tool call's optional "model" argument onto the profile copy it hands
	// RunSubagent. Registered profiles leave it empty — then the runner's
	// default binding decides (the study role's spec for Study; the coder's
	// own live model for agent).
	Model string

	// DepthCap is how many additional levels of subagent nesting a run of this
	// profile is allowed to spawn, counted from ITS OWN invocation (depth 0):
	// 0 (the zero value, Study's setting) means a running instance of this
	// profile can never itself dispatch a subagent tool call — the old blanket
	// "subagents can't recurse" rule, now explicit instead of an accident of
	// Study's toolset never offering one. A future cap-1 profile may dispatch
	// one nested subagent; that child's own subagent calls are refused, since
	// they would be depth 2. Enforced in the shared runner
	// (cmd/cortex CortexSession.runSubagentStats), not per-profile, so the
	// cap holds even if a profile's Tools includes a subagent tool.
	DepthCap int
}

// AsTool returns the profile's dispatchable tool declaration.
func (sa Subagent) AsTool() Tool {
	return sa.Declaration
}

// subagentRegistry maps a tool name (Subagent.Role) back to its profile, so
// Execute can resolve any registered subagent tool through one shared path
// instead of a per-profile switch case.
type subagentRegistry struct {
	mu       sync.Mutex
	profiles map[string]Subagent
}

var registered = &subagentRegistry{profiles: make(map[string]Subagent)}

// Register installs a subagent profile under its Role so the shared dispatch
// path (runSubagent) can resolve a tool call back to its profile. Panics on a
// duplicate or empty Role — that's a wiring bug, not a runtime condition.
func Register(sa Subagent) {
	if strings.TrimSpace(sa.Role) == "" {
		panic("tools.Register: subagent has empty Role")
	}
	registered.mu.Lock()
	defer registered.mu.Unlock()
	if _, dup := registered.profiles[sa.Role]; dup {
		panic("tools.Register: duplicate subagent " + sa.Role)
	}
	registered.profiles[sa.Role] = sa
}

// Lookup resolves a tool name back to its subagent profile (study, ...) for
// the shared dispatch path. Returns the zero profile + false if unregistered.
func Lookup(name string) (Subagent, bool) {
	registered.mu.Lock()
	defer registered.mu.Unlock()
	p, ok := registered.profiles[name]
	return p, ok
}

// StudySeedBudget is the outline budget for the study seed: enough structure to
// orient the subagent without the map dominating its own context.
const StudySeedBudget = 6000

const studySystem = `You are a code researcher. You're given a GOAL and an OUTLINE of a path. Find the parts relevant to the goal, read them, and explain what you found.

Your tools are read-only:
- grep(pattern, path): find where text or a regex (RE2 — no lookahead or backreferences) occurs — returns file:line. This is your primary locator. Use PATH as the grep path; don't broaden to "." unless PATH is ".".
- outline(path, budget): the structure of a file or directory — entries with line spans (a file lists its declarations; a directory lists its files). Outline a path you haven't seen to orient.
- read_file(path, start, end): read a specific line range. Give start and end; a whole-file read of a large file is refused (outline or grep it first).

Locate, then read: grep or outline under PATH to find exactly where the answer lives, then read_file only those spans. Spend your limited reads on what the goal needs. If a tool is refused or errors, adapt — don't repeat it.

Then STOP and answer the goal directly. Don't deliberate at length or keep exploring once you have the answer — a few targeted lookups are enough. You don't need to chase every referenced symbol to its definition; once you've read enough to answer what the goal asks, answer. Explain concretely how the relevant code works and how the pieces fit, naming the key symbols and citing file:line. Base your answer only on what you read; if the premise of the goal is false, say so and describe what the code actually does. Write the answer in plain prose, referring to tool calls and syntax by name — never paste literal tool-call, XML, or <function …>/<tool_call> markup into your answer. Be concise.`

// agentSystem is the Agent profile's system prompt (docs/agent-tool.md): a
// bounded implementation subagent, not a read-only researcher. It gets
// write_file/edit_file/bash on top of Study's read/search tools, and is
// expected to verify its own change before reporting back.
const agentSystem = `You are a coding agent handed one bounded unit of implementation work. You're given a GOAL and an OUTLINE of a path. Do the work end to end: locate the relevant code, make the change, and verify it.

Your tools:
- grep(pattern, path): find where text or a regex (RE2 — no lookahead or backreferences) occurs — returns file:line. Use PATH as the grep path unless PATH is ".".
- outline(path, budget): the structure of a file or directory — entries with line spans. Outline a path you haven't seen to orient.
- read_file(path, start, end): read a specific line range; a whole-file read of a large file is refused (outline or grep it first).
- write_file(path, content): write a new file or overwrite one — prefer edit_file for changes to an existing file.
- edit_file(path, old_string, new_string): exact-match edit, whitespace-tolerant on retry.
- bash(command): run a shell command — build it, test it, inspect it. Risky commands are refused outright; there is no one to ask for approval in this loop, so don't attempt anything destructive or irreversible.

Locate, then change, then verify: use grep/outline to find exactly where the goal's work belongs, make the change, then run the relevant build/test command with bash to confirm it before you stop. If a tool is refused or errors, adapt — don't repeat it.

Then STOP and report what you changed and how you verified it. Be concise and concrete: name the files and the change, and state the verification result (what you ran, what it showed). If the goal can't be completed with your tools, say so and explain what's blocking it rather than guessing. Write the report in plain prose — never paste literal tool-call, XML, or <function …>/<tool_call> markup into it.`

// learnSystem is the Learn profile's system prompt (docs/learning-loop.md): a
// background, read-mostly analysis pass over a window of already-finished
// turns from the project journal, looking for what the foreground coder had
// no task-shaped reason to save. Shaped after the deleted
// internal/cognition.DreamAnalysisPrompt's category taxonomy (decisions/
// patterns/constraints/corrections + a NO_INSIGHT escape) — salvaged for its
// SHAPE, not its code. Learn is not offered to the coder as a callable tool
// (no Declaration, not Registered) — its only entry points are `cortex
// learn` and the loop scheduler's kind:"learn" firing (both cmd/cortex), so
// this prompt never needs to defend against being invoked mid-conversation.
//
// Deliberately does NOT carry memory-tools.md's "saving is rare"/"if in
// doubt, don't save" discipline: that posture is written for the coder,
// deciding in the moment whether ITS OWN turn is worth interrupting itself
// over. Learn runs after the fact, specifically over what the coder already
// declined to save — inheriting the coder's own hesitation here would make
// Learn a no-op by construction. A live gate run (docs/think-dream-eval.md's
// G2) caught exactly this: a scanned turn stated two specific facts with "no
// need to save," and the first cut of this prompt took that caveat at face
// value and answered NO_INSIGHT. The posture below narrows what counts
// (specific, durable, not already indexed) rather than how readily it's
// applied.
const learnSystem = `You are a background pass that reviews a finished coding session for facts the coder should have saved to memory but didn't. You are the safety net, not the coder: you decide for yourself, you do not defer to what the coder chose to do in the moment.

You are given the MEMORY INDEX (notes already saved) and TURNS from the session journal (what was said and done) that no note yet covers.

Scan the TURNS for specific facts not already in the MEMORY INDEX: identifiers, codewords (e.g. "GARNET-77"), exact quantities (e.g. "14 shards"), decisions, or constraints. If you find one, call memory_write to save it — even if the turn also says something like "no need to save this" or "just thinking out loud." That phrase describes what the coder chose in the moment; it is not a reason for you to skip a fact that is actually there. Skipping a specific fact you can see is the exact failure this pass exists to catch.

Name each note with whichever fits: decisions (a choice and why), patterns (a reusable approach), constraints (something to avoid or a boundary), corrections (a mistake not to repeat). If none fit, use "note" — the label matters less than saving the fact. Before writing, check the index so you update an existing note instead of duplicating it.

If a window truly has nothing this specific — most turns are routine edits or reads — answer with exactly NO_INSIGHT and nothing else. Otherwise, call memory_write for what you found, then reply with a short summary naming the notes you saved (don't repeat their bodies).

Tools: outline/grep/read_file to check the code behind a turn if needed; memory_read/memory_search to check the index; memory_write to save or update.`

// learnUserSystem is the LearnUser profile's system prompt
// (docs/cross-source-learning.md piece 1's promotion half): a machine-wide
// background pass that decides whether a mechanically-detected cross-project
// recurrence is actually the same durable fact, and if so promotes it to the
// shared user-tier memory. Unlike Learn, it never sees code or a journal —
// its only inputs are a CANDIDATE GROUP (notes from >=2 projects a
// deterministic overlap check flagged) and the user memory index, and its
// only write surface is memory_write against the user tier.
//
// The prompt deliberately tells the model NOT to write its own "Promoted
// from" line: cmd/cortex's dispatcherFor intercepts every memory_write this
// profile issues (preparePromotionWrite, learn_user.go) and appends the
// correct one mechanically from the candidate group the harness already
// knows — the same "harness decides what it gets to look at, model decides
// what's worth saving" split memory-tools.md and learning-loop.md both use,
// pushed one step further here because the exact source-project list is
// harness-known data, not something worth asking a small model to transcribe
// correctly (docs/cross-source-learning.md Δ2: "no more, no fewer").
//
// UNLIKE Study and Agent (and like Learn), LearnUser is deliberately NOT
// Register()'d and carries no Declaration: not offered to the coder as a
// callable tool. Its only entry point is cmd/cortex/learn_user.go's
// RunLearnUserPass (via `cortex learn --user` or the loop scheduler's
// kind:"learn"+scope:"user" firing).
const learnUserSystem = `You are a background pass that looks for the SAME fact recurring across multiple projects on this machine and, when it does, promotes it to the shared cross-project memory tier.

You are given a CANDIDATE GROUP: notes from two or more different projects that a mechanical check flagged as textually similar, plus the USER MEMORY INDEX (what's already promoted — do not duplicate).

Read the candidate notes carefully. If they genuinely describe the same durable fact, decision, constraint, or pattern — not just a coincidental word overlap between unrelated notes — call memory_write once with a short kebab-case name and a body stating the fact in general, project-independent terms (drop anything specific to only one project; a fact worth promoting is one that's actually true everywhere it was seen). If the group is already covered by an existing user-tier note, or the notes only share vocabulary but describe different things, write nothing.

Do not name a scope on memory_write — every note you write lands in the user tier automatically, regardless of what you pass. Do not write your own "Promoted from" line — one is added for you automatically from the candidate group's actual source projects.

If you promote a note, reply with a short summary naming what you saved. If you decide NOT to promote this group, reply with exactly NO_PROMOTION and nothing else.

Tools: memory_read/memory_search to check the user index further; memory_write to promote.`

// studySeed builds the subagent's opening user message: the goal, the path, and
// the structural outline it starts from.
func StudySeed(goal, path, ol string) string {
	if strings.TrimSpace(goal) == "" {
		goal = "Summarize what this code does and how its parts fit together."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL: %s\nPATH: %s\n\n", goal, path)
	fmt.Fprintf(&b, "Search scope: keep grep/outline/read_file under PATH (%s). Start with grep for symbols, strings, facts, logs, journals, or multi-file questions; use outline when structure is missing. If the goal asks what/which file, grep for filename-shaped text first (for example `[A-Za-z0-9_.-]+\\.(go|md|json|yaml|toml)`).\n\n", path)
	if strings.TrimSpace(ol) != "" {
		b.WriteString("OUTLINE (each unit's line span is what you read with read_file):\n\n")
		b.WriteString(ol)
		b.WriteString("\n")
	} else {
		b.WriteString("No outline was available for this path; use grep and read_file to explore.\n")
	}
	return b.String()
}

// AllowOf builds the execution allowlist from a profile's offered tools — the
// names a subagent may dispatch. A model that hallucinates a call to a tool it
// wasn't offered gets a refusal observation instead of execution.
func AllowOf(ts []Tool) map[string]bool {
	allow := make(map[string]bool, len(ts))
	for _, t := range ts {
		allow[t.Function.Name] = true
	}
	return allow
}
