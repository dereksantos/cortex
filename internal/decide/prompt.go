package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SystemPrompt is the decider's system prompt. It is a micro-prompt: it
// states how to judge, never a recipe for any task shape, and it stays free of
// language- and ecosystem-specific terms (TestHarnessTextIsLanguageNeutral in
// cmd/cortex reads it). Any change here is a new PromptVersion and needs the
// decider eval re-run before its rows are compared with older ones.
const SystemPrompt = `You watch a coding agent work and answer short questions about its progress. You never act; you only judge.

Judge from the record alone. If the record does not show that something happened, it did not happen. A step that was blocked or errored did not do its work.

Answer every question with only the choices it lists. Reply with one JSON object that has one key per question and nothing else.`

// questionText is the one-line definition of each question shown to the
// decider. Kept beside SystemPrompt so both count toward PromptVersion.
var questionText = map[Question]string{
	NextMove: "next_move: the single most useful next action toward the task. " +
		"read = look at file contents; search = locate where code lives; edit = change files; " +
		"test = run a check of the change; run = run some other command; recall = fetch earlier context; " +
		"finalize = stop and report.",
	RecallTarget: "recall_target: which earlier context to fetch back, or none.",
	Looping: "looping: the ids of recent steps that repeat an earlier step without yielding anything new, " +
		"or [\"none\"] if the agent is not repeating itself.",
	ClaimCheck: "claim_check: does the final reply describe work the record does not show? " +
		"supported = every change or result it claims has a step behind it; " +
		"unsupported = some claim has no step behind it, or rests on a step that was blocked or failed; " +
		"no_claims = the reply claims no work.",
}

// PromptVersion is a short hash of every piece of prompt text the decider
// sees outside the snapshot itself.
func PromptVersion() string {
	h := sha256.New()
	h.Write([]byte(SystemPrompt))
	for _, q := range []Question{NextMove, RecallTarget, Looping, ClaimCheck} {
		h.Write([]byte(questionText[q]))
	}
	h.Write([]byte(renderVersionTag))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:12]
}

// renderVersionTag bumps PromptVersion when RenderPrompt's layout changes.
const renderVersionTag = "render/v2"

// RenderPrompt builds the user message for one decision: the snapshot, then
// the questions, then the answer shape.
func RenderPrompt(s Snapshot, asks []Ask) string {
	var b strings.Builder
	b.WriteString("Task:\n")
	b.WriteString(s.Task)
	b.WriteString("\n\n")
	if s.StepsSeen > len(s.Steps) {
		fmt.Fprintf(&b, "Record (the last %d of %d steps, oldest first):\n", len(s.Steps), s.StepsSeen)
	} else {
		b.WriteString("Record (oldest first):\n")
	}
	if len(s.Steps) == 0 {
		b.WriteString("(no steps yet)\n")
	}
	for _, st := range s.Steps {
		mark := "ok"
		if st.Err {
			mark = "FAILED"
		}
		fmt.Fprintf(&b, "%s %s %s\n  -> %s: %s\n", st.ID, st.Tool, st.Args, mark, st.Result)
	}
	b.WriteString("\nWhole-turn ledger:\n")
	b.WriteString(renderLedger(s.Ledger))
	if s.Reply != "" {
		b.WriteString("\nFinal reply:\n")
		b.WriteString(s.Reply)
		b.WriteString("\n")
	}
	b.WriteString("\nQuestions:\n")
	shape := make([]string, 0, len(asks))
	for _, a := range asks {
		fmt.Fprintf(&b, "- %s\n  choices: %s\n", questionText[a.Q], strings.Join(a.Choices, ", "))
		if a.Multi {
			shape = append(shape, fmt.Sprintf("%q: [...]", a.Q))
		} else {
			shape = append(shape, fmt.Sprintf("%q: \"...\"", a.Q))
		}
	}
	b.WriteString("\nReply as {" + strings.Join(shape, ", ") + "}\n")
	return b.String()
}

func renderLedger(l Ledger) string {
	var b strings.Builder
	names := make([]string, 0, len(l.ToolCounts))
	for n := range l.ToolCounts {
		names = append(names, n)
	}
	slices.Sort(names)
	counts := make([]string, 0, len(names))
	for _, n := range names {
		counts = append(counts, fmt.Sprintf("%s x%d", n, l.ToolCounts[n]))
	}
	if len(counts) == 0 {
		counts = append(counts, "none")
	}
	fmt.Fprintf(&b, "tool calls: %s; failed steps: %d\n", strings.Join(counts, ", "), l.Errors)
	if len(l.FilesEdited) > 0 {
		fmt.Fprintf(&b, "files changed: %s\n", strings.Join(l.FilesEdited, ", "))
	} else {
		b.WriteString("files changed: none\n")
	}
	if len(l.Commands) > 0 {
		b.WriteString("commands run:\n")
		for _, c := range l.Commands {
			fmt.Fprintf(&b, "  %s\n", c)
		}
	}
	return b.String()
}

// ParseAnswers reads the decider's reply against the asks. It tolerates a
// fenced block or prose around the object, but every pick must come from its
// ask's choices and a single-choice ask must have exactly one pick; anything
// else is a parse error (an off-menu answer is never kept).
func ParseAnswers(reply string, asks []Ask) ([]Answer, error) {
	obj := extractObject(reply)
	if obj == "" {
		return nil, fmt.Errorf("no JSON object in reply")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return nil, fmt.Errorf("reply is not a JSON object: %w", err)
	}
	out := make([]Answer, 0, len(asks))
	for _, a := range asks {
		v, ok := raw[string(a.Q)]
		if !ok {
			return nil, fmt.Errorf("missing answer for %s", a.Q)
		}
		picks, err := readPicks(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.Q, err)
		}
		if len(picks) == 0 {
			return nil, fmt.Errorf("%s: empty answer", a.Q)
		}
		if !a.Multi && len(picks) != 1 {
			return nil, fmt.Errorf("%s: want one choice, got %d", a.Q, len(picks))
		}
		for _, p := range picks {
			if !slices.Contains(a.Choices, p) {
				return nil, fmt.Errorf("%s: %q is not one of the choices", a.Q, p)
			}
		}
		if a.Multi && len(picks) > 1 && slices.Contains(picks, None) {
			return nil, fmt.Errorf("%s: %q mixed with other picks", a.Q, None)
		}
		out = append(out, Answer{Q: a.Q, Picks: picks})
	}
	return out, nil
}

// readPicks accepts a string or an array of strings.
func readPicks(v json.RawMessage) ([]string, error) {
	var one string
	if err := json.Unmarshal(v, &one); err == nil {
		return []string{strings.TrimSpace(one)}, nil
	}
	var many []string
	if err := json.Unmarshal(v, &many); err != nil {
		return nil, fmt.Errorf("answer is neither a string nor a list of strings")
	}
	for i := range many {
		many[i] = strings.TrimSpace(many[i])
	}
	return many, nil
}

// extractObject returns the last balanced top-level {...} in s (a model that
// reasons in prose before answering puts the answer last), or "".
func extractObject(s string) string {
	last := ""
	depth, start := 0, -1
	inStr, esc := false, false
	for i, r := range s {
		if inStr {
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			if depth > 0 {
				inStr = true
			}
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					last = s[start : i+1]
				}
			}
		}
	}
	return last
}
