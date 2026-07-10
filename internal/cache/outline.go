package cache

import (
	"fmt"
	"strings"
)

// OutlineEntry is the demoted form of one turn, rendered mechanically — no LLM.
// Turn is 1-based. User is the user message verbatim (the caller truncates huge
// pastes before building the entry). Actions are one-line tool summaries like
// "edit cmd/cortex/study_eval.go [ok]". ReplyHead is the first line of the final
// assistant reply. Citation is a deterministic transcript coordinate like
// "@session/20260701-143210:L120-134".
type OutlineEntry struct {
	Turn      int
	User      string
	Actions   []string
	ReplyHead string
	Citation  string
}

// Render returns the deterministic multi-line rendering of an OutlineEntry.
// Line 1: "t<Turn> · user: " + the User text. If User contains newlines, keep
// them verbatim but indent every continuation line with exactly six spaces.
// Line 2 (only if len(Actions) > 0): six spaces + Actions joined with " · ".
// Line 3 (only if ReplyHead != ""): six spaces + "⤷ " + ReplyHead.
// Line 4 (only if Citation != ""): six spaces + "[" + Citation + "]".
// No trailing newline.
func (e OutlineEntry) Render() string {
	lines := []string{}

	// Line 1: "t<Turn> · user: " + User text with indented continuations
	userLines := strings.Split(e.User, "\n")
	line1 := fmt.Sprintf("t%d · user: %s", e.Turn, userLines[0])
	lines = append(lines, line1)

	// Indent continuation lines with six spaces
	for i := 1; i < len(userLines); i++ {
		lines = append(lines, "      "+userLines[i])
	}

	// Line 2: Actions if any
	if len(e.Actions) > 0 {
		lines = append(lines, "      "+strings.Join(e.Actions, " · "))
	}

	// Line 3: ReplyHead if not empty
	if e.ReplyHead != "" {
		lines = append(lines, "      ⤷ "+e.ReplyHead)
	}

	// Line 4: Citation if not empty
	if e.Citation != "" {
		lines = append(lines, "      ["+e.Citation+"]")
	}

	return strings.Join(lines, "\n")
}

// RenderOutline returns the entries' Render outputs joined by a single blank line.
// Empty slice → "". (The zone header text is the caller's job, not this package's.)
func RenderOutline(entries []OutlineEntry) string {
	if len(entries) == 0 {
		return ""
	}

	lines := []string{}
	for _, e := range entries {
		lines = append(lines, e.Render())
	}

	return strings.Join(lines, "\n\n")
}
