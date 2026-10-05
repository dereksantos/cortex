package shellrisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"time"

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
- creating/moving/editing/deleting files WITHIN the project tree, including build/output dirs (mkdir, touch, mv within the tree, rm of ./build or ./dist or node_modules, in-place script rewrites of project files such as sed -i)
- inspecting version-control state (git status/log/diff/show)
- running the project's own programs, scripts, or test binaries locally

risky — consequential or hard to undo:
- deleting/overwriting OUTSIDE the project, or wholesale (rm -rf of ~, /, $HOME, or many unrelated paths)
- pushing, publishing, deploying, releasing (git push, npm publish, gh release, docker push)
- installing/uninstalling software or changing dependencies (apt, brew, npm/pip install, go get/install)
- outbound network requests that send data out or download-and-run code
- rewriting version-control history (git rebase, reset --hard, force push, clean -fdx)
- git operations that discard or move UNCOMMITTED working-tree state — stashing (git stash pop/apply/clear), git checkout -- <path>, git restore of local edits — because a conflicting pop or an uncommitted edit can be lost
- changing global/system or git config, file permissions, or ownership
- starting long-running daemons or servers

Only mark "risky" for a concrete reason above; if the command is ordinary project work, mark it "safe". Reserve "risky" for genuine ambiguity, not for routine in-tree changes.

Respond with ONLY a single JSON object, no prose:
{"risk":"safe","reason":"<short reason>"}`

// DefaultMaxTaskContextChars bounds the task context folded into the
// classifier prompt so a long turn can't bloat the small model's input (or
// crowd out the command). Exported so cmd/cortex's config resolver
// (limits.max_task_context_chars) has a documented default to fall back to
// — see Config.maxTaskContextChars in cmd/cortex/config.go.
const DefaultMaxTaskContextChars = 800

// ProviderClassifier builds the LLM-backed gray-zone ClassifyFn from a
// provider, using DefaultMaxTaskContextChars. It is the tier-3 classifier
// Classify consults for commands that cleared the deny-floor and missed the
// safe path. See ProviderClassifierWithLimit for the config-bound variant.
func ProviderClassifier(p llm.Provider, taskContext string) ClassifyFn {
	return ProviderClassifierWithLimit(p, taskContext, DefaultMaxTaskContextChars)
}

// ProviderClassifierWithLimit is ProviderClassifier with an explicit
// maxContextChars override (cmd/cortex's limits.max_task_context_chars); a
// non-positive value falls back to DefaultMaxTaskContextChars.
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
//
// Transient transport errors are retried before giving up (issue #132): a
// single dropped connection (an EOF off a small local backend) used to fail
// the classification closed and block a routine command, costing the model a
// step with no chance to recover. classifyWithRetry re-sends a bounded number
// of times on jittered backoff; a command context cancel is never retried, and
// when the retries run out the last error is returned — failing closed stays
// the terminal behavior.
func ProviderClassifierWithLimit(p llm.Provider, taskContext string, maxContextChars int) ClassifyFn {
	if maxContextChars <= 0 {
		maxContextChars = DefaultMaxTaskContextChars
	}
	return func(ctx context.Context, command string) (Level, string, error) {
		var user strings.Builder
		if tc := strings.TrimSpace(taskContext); tc != "" {
			if len(tc) > maxContextChars {
				tc = tc[:maxContextChars] + "…"
			}
			fmt.Fprintf(&user, "Task the agent is working on:\n%s\n\n", tc)
		}
		fmt.Fprintf(&user, "Command:\n%s\n\nClassify its risk.", command)
		raw, err := classifyWithRetry(ctx, func() (string, error) {
			return p.GenerateWithSystem(ctx, user.String(), classifierSystemPrompt)
		})
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

// classifyRetryAttempts / classifyRetryBaseDelay are the classifier's retry
// policy (issue #132): two retries (three sends total) on jittered linear
// backoff. Bounded on purpose — the classifier's whole job is to unblock a
// routine command quickly, so the fail-closed cost of a truly dead backend
// stays at most one short backoff, not a long stall in the tool loop.
const (
	classifyRetryAttempts   = 2
	classifyRetryBaseDelay  = 250 * time.Millisecond
	classifyRetryJitterFull = 1 // jitter uniform in [0.5, 1.5] × the base delay
)

// classifyWithRetry runs attempt once, re-running it on a transient transport
// error: a context error, a *net.Error (EOF, refused/reset connections — the
// recorded shape), or a net.Error wrapped in another error. Context
// cancellation is never retried: the turn is over, and retrying would just
// delay the interrupt. Non-transport errors (an HTTP 4xx, a parse failure) are
// returned immediately — retrying a rejected request burns budget without
// changing the answer. When the attempts run out, the LAST attempt's error is
// returned so Classify's fail-closed verdict names what actually happened.
func classifyWithRetry(ctx context.Context, attempt func() (string, error)) (string, error) {
	raw, err := attempt()
	if err == nil || !isTransientClassifierError(ctx, err) {
		return raw, err
	}
	lastErr := err
	for i := 0; i < classifyRetryAttempts; i++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		d := time.Duration(float64(classifyRetryBaseDelay) * float64(i+1) * (1 + rand.Float64()*classifyRetryJitterFull))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(d):
		}
		if raw, lastErr = attempt(); lastErr == nil || !isTransientClassifierError(ctx, lastErr) {
			return raw, lastErr
		}
	}
	return "", lastErr
}

// isTransientClassifierError reports whether err is the kind of transport
// hiccup a retry can plausibly fix (context alive required). isContextError
// short-circuits a wrapped context error first so a cancelled ctx is never
// mistaken for a network hiccup; net.Error covers EOF and the like directly
// and also through any wrapper that carries it in an errors chain.
func isTransientClassifierError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne)
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
