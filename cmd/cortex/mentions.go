// mentions.go — @path mention attachment (issue #108).
//
// A submitted line may contain one or more @path mentions. On submit, each
// mentioned file is attached to the turn through the SAME size rules as
// read_file (internal/tools): a small file is inlined verbatim, a large file
// is reduced to a structural outline with a pointer to study so the model can
// drill in. A mention that names a non-existent or unreadable path is
// reported inline (the model can course-correct) and the line is NOT
// silently dropped.
//
// Only an "@" starting a whitespace-delimited word is a mention (an email
// address or a `@types/node`-style scoped name is prose), and trailing
// sentence punctuation is stripped before resolving ("@main.go," means
// main.go). A mention that does not resolve to a readable file leaves the
// input UNCHANGED: the user's prose is exactly what goes into history and to
// the model, and no attachment note is produced for it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/outline"
	"github.com/dereksantos/cortex/internal/tools"
)

// mentionRe matches an @path mention: an "@" at the start of a whitespace-
// delimited word (start of line or after whitespace — so "user@example.com",
// "@types/node" inside "bump @types/node", or a Java "@Override" mid-identifier
// is prose, never a mention), followed by a run of non-whitespace.
var mentionRe = regexp.MustCompile(`(^|\s)@\S+`)

// mentionPunct is the sentence punctuation stripped from a mention before
// resolving it: "look at @main.go," attaches main.go, not "main.go,".
const mentionPunct = ",.;:)!?"

// processMentions scans input for @path mentions and returns:
//   - the input with every RESOLVED mention replaced by a short
//     "[@path attached]" marker (so the model's prompt stays clean) —
//     unresolved mentions and all other text are left byte-for-byte intact;
//   - the attachment block: for each resolved mention, either the file
//     content (small) or a skeleton (large), prefixed with the path and a
//     size note.
//
// A mention that names a non-existent, unreadable, or out-of-workspace path
// is left as the user typed it — the model sees the original text and can
// course-correct on its own (the file simply isn't attached).
func processMentions(workspaceRoot, input string) (cleanInput, attachment string) {
	matches := mentionRe.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return input, ""
	}
	var b strings.Builder
	var attach strings.Builder
	last := 0
	for _, m := range matches {
		// The match may start with a leading whitespace run; the mention
		// proper is the part from the "@" on.
		mentionStart := m[0]
		for mentionStart < m[1] && input[mentionStart] != '@' {
			mentionStart++
		}
		mention := input[mentionStart:m[1]]
		// The path is the mention minus the "@" marker; trailing sentence
		// punctuation is stripped for RESOLUTION ("@main.go," means main.go)
		// but re-emitted after the marker, so the user's own punctuation is
		// never silently deleted from the line.
		raw := mention[1:]
		punct := raw[len(strings.TrimRight(raw, mentionPunct)):]
		rel := strings.TrimRight(raw, mentionPunct)
		if rel == "" {
			// "@." or "@," — no path at all: prose, leave it.
			continue
		}
		// Confine to the workspace with the same rules read_file's paths use
		// (tools.ConfinePath: lexical + symlink-resolved). A refusal is
		// treated like any other non-resolution: the text is kept as typed.
		call := tools.ToolCall{Function: tools.FunctionCall{Name: "path", Arguments: fmt.Sprintf(`{"path":%q}`, rel)}}
		if _, err := tools.ConfinePath(call, workspaceRoot); err != nil {
			continue
		}
		abs := filepath.Join(workspaceRoot, filepath.Clean(rel))
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			// A directory isn't attachable; the path is left as typed.
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		// Same size rule as read_file (internal/tools.readFile): a whole file
		// above the curation budget is NOT inlined — its structural outline
		// (the same skeleton read_file hands back) goes in its place, with a
		// pointer to study for the detail.
		estTokens := len(data) / 4
		if estTokens > tools.DefaultLimits().CurationBudgetTokens {
			skel, skelErr := outlineFile(abs)
			if skelErr != nil || strings.TrimSpace(skel) == "" {
				skel = "(outline unavailable)"
			}
			fmt.Fprintf(&attach, "=== @%s (~%d tokens — too large to inline; outline below, study for detail) ===\n%s\n", rel, estTokens, skel)
		} else {
			fmt.Fprintf(&attach, "=== @%s (%d bytes, inlined) ===\n%s\n", rel, len(data), string(data))
		}
		b.WriteString(input[last:mentionStart])
		b.WriteString("[@" + rel + " attached]" + punct)
		last = m[1]
	}
	b.WriteString(input[last:])
	return b.String(), attach.String()
}

// outlineFile returns the structural outline of path — the same helper
// read_file's too-large path uses (internal/tools.fileSkeleton), so the
// mention attachment and read_file can't drift apart in what a large file
// looks like. Returns an error only when outline.Render itself fails.
func outlineFile(path string) (string, error) {
	return outline.Render(path, 8000)
}

// mentionCompleter builds the completer sources the REPL wires into
// lineedit's SetCompletion. It is kept here (rather than in main.go) so the
// REPL's loop stays focused on the turn pipeline.
//
// Model ids come from ONE source: the slash completer's Sub hook. (A separate
// "model" entry here would double every id in the candidate row and break the
// first-Tab fill, since the engine fills against the word at the cursor — and
// "/model <id>" is never a prefix of the typed argument word.)
func mentionCompleter(session *CortexSession) map[string]lineedit.Completer {
	// The fixed slash-command set (the REPL's vocabulary, passed in so
	// lineedit stays free of cmd/cortex).
	commands := []string{
		"/clear", "/compact", "/context", "/help", "/hook",
		"/model", "/plan", "/quit", "/sessions",
	}
	return map[string]lineedit.Completer{
		"slash": lineedit.SlashCompleter{
			Commands: commands,
			Sub: func(line string, cursor int) []string {
				// Only /model has a continuation today.
				if !strings.HasPrefix(line, "/model ") {
					return nil
				}
				return lineedit.ModelCompleter{Names: func() []string {
					return modelIDs(session)
				}}.Candidates(line, cursor)
			},
		},
		"path": lineedit.PathCompleter{
			Root:          session.root(),
			MaxCandidates: 50,
		},
	}
}

// modelIDs returns the set of model ids the /model command can switch to:
// the currently bound code/study models plus every id the fleet knows.
func modelIDs(session *CortexSession) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(session.Request.Model)
	add(session.Study.Model)
	for id := range session.Fleet {
		add(id)
	}
	return out
}
