// mentions.go — @path mention attachment (issue #108).
//
// A submitted line may contain one or more @path mentions. On submit, each
// mentioned file is attached to the turn through the SAME size rules as
// read_file (internal/tools): a small file is inlined verbatim, a large file
// is reduced to a structural outline with a pointer to study so the model can
// drill in. A mention that names a non-existent or unreadable path is
// reported inline (the model can course-correct) and the line is NOT
// silently dropped.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/outline"
)

// mentionRe matches an @path mention: an "@" followed by a run of
// non-whitespace, non-ASCII-printable characters (a path is ASCII in this
// codebase). The match is the complete mention (including the @).
var mentionRe = regexp.MustCompile(`@\S+`)

// processMentions scans input for @path mentions and returns:
//   - the input with every mention replaced by a short "[@path attached]"
//     marker (so the model's prompt stays clean);
//   - the attachment block: for each mention, either the file content (small)
//     or a skeleton (large), prefixed with the path and a size note.
//
// A mention that cannot be read (missing, outside the workspace, a directory)
// produces a one-line note in the attachment block explaining why, so the
// model knows the mention was seen but not satisfied.
func processMentions(workspaceRoot, input string) (cleanInput, attachment string) {
	matches := mentionRe.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return input, ""
	}
	var b strings.Builder
	var attach strings.Builder
	last := 0
	for _, m := range matches {
		mention := input[m[0]:m[1]]
		// The path is the mention minus the @ marker.
		rel := mention[1:]
		// Confine to the workspace (same rules as ConfinePath: lexical +
		// symlink-resolved). A refusal produces a note, not an attachment.
		if err := confineMention(workspaceRoot, rel); err != nil {
			fmt.Fprintf(&attach, "[@%s — refused: %v]\n", rel, err)
			b.WriteString(input[last:m[0]])
			b.WriteString("[" + mention + " refused]")
			last = m[1]
			continue
		}
		abs := filepath.Join(workspaceRoot, filepath.Clean(rel))
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(&attach, "[@%s — not found]\n", rel)
			b.WriteString(input[last:m[0]])
			b.WriteString("[" + mention + " not found]")
			last = m[1]
			continue
		}
		if info.IsDir() {
			fmt.Fprintf(&attach, "[@%s — is a directory; use a file path]\n", rel)
			b.WriteString(input[last:m[0]])
			b.WriteString("[" + mention + " is a directory]")
			last = m[1]
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(&attach, "[@%s — unreadable: %v]\n", rel, err)
			b.WriteString(input[last:m[0]])
			b.WriteString("[" + mention + " unreadable]")
			last = m[1]
			continue
		}
		size := len(data)
		if size <= maxMentionInlineBytes {
			// Small file: inline verbatim.
			fmt.Fprintf(&attach, "=== @%s (%d bytes, inlined) ===\n%s\n", rel, size, string(data))
		} else {
			// Large file: structural outline + pointer to study, mirroring
			// read_file's large-file behavior (internal/tools.fileSkeleton).
			skel, skelErr := outline.Render(abs, 8000)
			if skelErr != nil || strings.TrimSpace(skel) == "" {
				skel = "(outline unavailable)"
			}
			fmt.Fprintf(&attach, "=== @%s (%d bytes — too large to inline; outline below, study for detail) ===\n%s\n", rel, size, skel)
		}
		b.WriteString(input[last:m[0]])
		b.WriteString("[" + mention + " attached]")
		last = m[1]
	}
	b.WriteString(input[last:])
	return b.String(), attach.String()
}

// maxMentionInlineBytes is the ceiling for inlining a mentioned file verbatim.
// Files above this get a structural outline instead (same rule as read_file's
// CurationBudgetTokens: ~16k tokens ≈ 64k bytes).
const maxMentionInlineBytes = 64000

// confineMention checks that rel is inside workspaceRoot using the same
// two-part check tools.ConfinePath uses: lexical containment plus
// symlink-resolved containment. It returns an error describing the refusal.
func confineMention(workspaceRoot, rel string) error {
	if filepath.IsAbs(rel) {
		return fmt.Errorf("absolute paths are refused")
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == ".." {
			return fmt.Errorf("path escapes the workspace (..)")
		}
	}
	abs := filepath.Join(workspaceRoot, filepath.Clean(rel))
	resolvedRoot, err := resolveSymlinksMention(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	resolved, err := resolveSymlinksMention(abs)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	relResolved, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the workspace (symlink)")
	}
	return nil
}

// resolveSymlinksMention mirrors internal/lineedit's resolveSymlinks: it
// resolves every symlink along path, walking up to the deepest existing
// ancestor when the full path does not exist yet.
func resolveSymlinksMention(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Dir(path)
	if dir == path {
		return path, nil
	}
	resolvedDir, err := resolveSymlinksMention(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedDir, filepath.Base(path)), nil
}

// mentionCompleter builds the three completer sources the REPL wires into
// lineedit's SetCompletion. It is kept here (rather than in main.go) so the
// REPL's loop stays focused on the turn pipeline.
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
		"model": lineedit.ModelCompleter{Names: func() []string {
			return modelIDs(session)
		}},
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
