package tools

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/agent"
)

// study.go defines the Study subagent profile: inert data (name, role, system
// prompt, offered tools, bounds) run on the one shared engine via SubAgentRunner.
// Study is the only profile today; adding another (e.g. Reflect) is a `var`.
// See docs/study-subagent.md §1.

// Subagent is a read-only (or otherwise scoped) agent profile: the variation a
// caller injects into the shared engine. Tools is both what's offered to the
// model and the execution allowlist.
type Subagent struct {
	Name   string       // banner / telemetry label
	Role   string       // model-role binding the runner resolves (e.g. "study")
	System string       // system prompt
	Tools  []Tool       // offered == execution allowlist
	Bounds agent.Bounds // MaxTokens (mandatory), MaxIter, ReadBudgetBytes
}

// StudySeedBudget is the outline budget for the study seed: enough structure to
// orient the subagent without the map dominating its own context.
const StudySeedBudget = 6000

const studySystem = `You are a code researcher. You're given a GOAL and an OUTLINE of a path. Find the parts relevant to the goal, read them, and explain what you found.

Your tools are read-only:
- grep(pattern, path): find where text or a regex (RE2 — no lookahead or backreferences) occurs — returns file:line. This is your primary locator: to find where a symbol, string, or fact lives, grep for it. It searches every file under the path, so it is the way to find something across many files (a package, or a directory of logs/records) — never read those files one by one.
- outline(path, budget): the structure of a file or directory — entries with line spans (a file lists its declarations; a directory lists its files). Outline a path you haven't seen to orient.
- read_file(path, start, end): read a specific line range. Give start and end; a whole-file read of a large file is refused (outline or grep it first).

Locate, then read: grep or outline to find exactly where the answer lives, then read_file only those spans. Spend your limited reads on what the goal needs. If a tool is refused or errors, adapt — don't repeat it.

Then STOP and answer the goal directly. Don't deliberate at length or keep exploring once you have the answer — a few targeted lookups are enough. Explain concretely how the relevant code works and how the pieces fit, naming the key symbols and citing file:line. Base your answer only on what you read; if the premise of the goal is false, say so and describe what the code actually does. Write the answer in plain prose, referring to tool calls and syntax by name — never paste literal tool-call, XML, or <function …>/<tool_call> markup into your answer. Be concise.`

// studySeed builds the subagent's opening user message: the goal, the path, and
// the structural outline it starts from.
func StudySeed(goal, path, ol string) string {
	if strings.TrimSpace(goal) == "" {
		goal = "Summarize what this code does and how its parts fit together."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL: %s\nPATH: %s\n\n", goal, path)
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
