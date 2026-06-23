package shellrisk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// classifierSystemPrompt is the gray-zone contract. It states the safe/risky
// boundary and the fail-toward-risky default. Kept terse for small local
// models (the small-model-amplifier role): one decision, one JSON object.
const classifierSystemPrompt = `You are a safety gate for an autonomous coding agent working inside a software project. Classify ONE shell command as "safe" or "risky".

Judge by EFFECT, not by how dangerous a command looks in isolation. Routine local development is safe even when it creates, moves, or deletes files INSIDE the project — that is the agent's job. When task context is given, use it to tell ordinary work apart from overreach.

safe — reversible, local, no external side effects:
- reading/searching/inspecting (cat, ls, grep, find without -exec/-delete)
- building, testing, linting, formatting, type-checking (make, go test, npm test, eslint)
- creating/moving/editing/deleting files WITHIN the project tree, including build/output dirs (mkdir, touch, mv within the tree, rm of ./build or ./dist or node_modules, sed -i on a project file)
- inspecting version-control state (git status/log/diff/show)
- running the project's own programs, scripts, or test binaries locally

risky — consequential or hard to undo:
- deleting/overwriting OUTSIDE the project, or wholesale (rm -rf of ~, /, $HOME, or many unrelated paths)
- pushing, publishing, deploying, releasing (git push, npm publish, gh release, docker push)
- installing/uninstalling software or changing dependencies (apt, brew, npm/pip install, go get/install)
- outbound network requests that send data out or download-and-run code
- rewriting version-control history (git rebase, reset --hard, force push, clean -fdx)
- changing global/system or git config, file permissions, or ownership
- starting long-running daemons or servers

Only mark "risky" for a concrete reason above; if the command is ordinary project work, mark it "safe". Reserve "risky" for genuine ambiguity, not for routine in-tree changes.

Respond with ONLY a single JSON object, no prose:
{"risk":"safe","reason":"<short reason>"}`

// maxTaskContextChars bounds the task context folded into the classifier prompt
// so a long turn can't bloat the small model's input (or crowd out the command).
const maxTaskContextChars = 800

// ProviderClassifier builds the LLM-backed gray-zone ClassifyFn from a
// provider. It is the tier-3 classifier Classify consults for commands that
// cleared the deny-floor and missed the safe path.
//
// taskContext is the agent's current intent (typically the user's turn
// request). It is folded into the prompt so the classifier can tell routine
// in-tree work apart from overreach — a destructive-looking command that's
// clearly part of the task reads as safe; the same command with no bearing on
// the task reads as risky. Pass "" when no context is available.
//
// Failure is fail-closed by construction: a transport error or an unparseable
// response is returned as an error, which Classify turns into a Risky/
// fail-closed verdict. The classifier is never allowed to default to Safe.
func ProviderClassifier(p llm.Provider, taskContext string) ClassifyFn {
	return func(ctx context.Context, command string) (Level, string, error) {
		var user strings.Builder
		if tc := strings.TrimSpace(taskContext); tc != "" {
			if len(tc) > maxTaskContextChars {
				tc = tc[:maxTaskContextChars] + "…"
			}
			fmt.Fprintf(&user, "Task the agent is working on:\n%s\n\n", tc)
		}
		fmt.Fprintf(&user, "Command:\n%s\n\nClassify its risk.", command)
		raw, err := p.GenerateWithSystem(ctx, user.String(), classifierSystemPrompt)
		if err != nil {
			return Risky, "", err
		}
		lvl, reason, ok := parseClassifierResponse(raw)
		if !ok {
			return Risky, "", fmt.Errorf("unparseable classifier response")
		}
		return lvl, reason, nil
	}
}

// classifierJSON is the wire shape the model is asked to emit.
type classifierJSON struct {
	Risk   string `json:"risk"`
	Reason string `json:"reason"`
}

// parseClassifierResponse extracts the verdict from a model response,
// tolerating surrounding prose / code fences. Returns ok=false when no JSON
// object with a recognizable risk field is present (→ fail closed). An
// unrecognized risk value parses as Risky, not as a failure: the model
// committed to a verdict, just not the word "safe".
func parseClassifierResponse(raw string) (Level, string, bool) {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return Risky, "", false
	}
	var j classifierJSON
	if err := json.Unmarshal([]byte(obj), &j); err != nil {
		return Risky, "", false
	}
	risk := strings.ToLower(strings.TrimSpace(j.Risk))
	if risk == "" {
		return Risky, "", false
	}
	if risk == "safe" {
		return Safe, strings.TrimSpace(j.Reason), true
	}
	return Risky, strings.TrimSpace(j.Reason), true
}

// extractJSONObject returns the substring from the first '{' to the matching
// last '}'. Good enough for a single-object response wrapped in prose or
// ```json fences.
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return "", false
	}
	return s[start : end+1], true
}
