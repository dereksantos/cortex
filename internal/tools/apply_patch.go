package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// apply_patch.go is the patch-envelope edit format: the alternative to
// edit_file for model families post-trained on it (the "V4A" envelope the
// OpenAI Codex harness uses — gpt-oss, GPT-4.1/5, Codex models). A model
// works best in the edit format it was trained on; a session exposes exactly
// one of edit_file / apply_patch (tools.edit_format, cmd/cortex), never both.
//
// The envelope is only a front end. Each Update hunk is applied through the
// same matcher as edit_file (applyEdit: exact first, then whitespace-
// tolerant, a match must be unique), and each updated file goes through the
// same finish (finishFileEdit: diff display, removal/guard warnings, the
// changed-region snippet, the post-edit hook, the write-sanity pass). Add
// and Delete go through write_file and remove_path themselves, so their
// gates (taint confinement, tools.allow_delete) hold unchanged.
//
//	*** Begin Patch
//	*** Add File: path/new.txt
//	+line
//	*** Update File: path/old.txt
//	*** Move to: path/renamed.txt      (optional)
//	@@ optional anchor line
//	 context
//	-removed
//	+added
//	*** Delete File: path/gone.txt
//	*** End Patch

// FunctionApplyPatch is the patch-envelope edit tool's name.
const FunctionApplyPatch = "apply_patch"

// ApplyPatch is the apply_patch tool declaration. Its one argument is the
// whole patch text, as a plain function argument (the form gpt-oss accepts).
var ApplyPatch = newTool(FunctionApplyPatch,
	"Edit files with a patch. `input` is the whole patch: '*** Begin Patch', then one or more "+
		"file sections, then '*** End Patch'. Sections: '*** Add File: <path>' followed by the new "+
		"file's lines, each prefixed '+'; '*** Delete File: <path>'; '*** Update File: <path>' "+
		"(optionally followed by '*** Move to: <new path>') followed by hunks. A hunk starts with "+
		"'@@', optionally followed by a line from the file that the change comes after (to tell "+
		"apart repeated code); its lines are prefixed ' ' (unchanged context), '-' (remove) or '+' "+
		"(add). Include about 3 lines of context around each change; the context and removed lines "+
		"must match the file as it is now and be unique within it (or after the '@@' line). Paths "+
		"are relative to the workspace. Each updated file's result shows the current changed "+
		"region, so the next patch can anchor on the file's actual content.",
	objectSchema(map[string]any{
		"input": stringProp("The full patch text, from '*** Begin Patch' to '*** End Patch'."),
	}, "input"))

type patchOpKind int

const (
	patchAdd patchOpKind = iota
	patchDelete
	patchUpdate
)

// patchHunk is one '@@' hunk: an optional anchor line, then the block the
// hunk replaces (context + removed lines) and its replacement (context +
// added lines), each newline-terminated.
type patchHunk struct {
	anchor   string
	old, new string
}

type patchOp struct {
	kind   patchOpKind
	path   string
	moveTo string      // update only
	body   string      // add only: the new file's content
	hunks  []patchHunk // update only
}

const (
	patchBegin   = "*** Begin Patch"
	patchEnd     = "*** End Patch"
	patchAddHdr  = "*** Add File: "
	patchDelHdr  = "*** Delete File: "
	patchUpdHdr  = "*** Update File: "
	patchMoveHdr = "*** Move to: "
	patchEOF     = "*** End of File"
)

// parsePatch parses a patch envelope. It is lenient where models commonly
// drift and strict where a guess could edit the wrong text: a shell heredoc
// wrapper around the envelope is tolerated, and a blank line inside a hunk
// counts as blank context; an unprefixed non-blank line is an error.
func parsePatch(text string) ([]patchOp, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == patchBegin && start < 0 {
			start = i
		}
		if t == patchEnd {
			end = i
		}
	}
	if start < 0 || end < start {
		return nil, fmt.Errorf("the patch must start with '%s' and end with '%s'", patchBegin, patchEnd)
	}
	body := lines[start+1 : end]

	var ops []patchOp
	var cur *patchOp
	var hunk *patchHunk
	flushHunk := func() {
		if cur != nil && hunk != nil {
			if hunk.old != "" || hunk.new != "" {
				cur.hunks = append(cur.hunks, *hunk)
			}
			hunk = nil
		}
	}
	flushOp := func() error {
		flushHunk()
		if cur == nil {
			return nil
		}
		if cur.kind == patchUpdate && len(cur.hunks) == 0 && cur.moveTo == "" {
			return fmt.Errorf("'%s%s' has no hunks", patchUpdHdr, cur.path)
		}
		ops = append(ops, *cur)
		cur = nil
		return nil
	}
	header := func(l, prefix string) (string, bool) {
		if !strings.HasPrefix(l, prefix) {
			return "", false
		}
		return strings.TrimSpace(strings.TrimPrefix(l, prefix)), true
	}
	for n, l := range body {
		lineNo := start + 2 + n
		if p, ok := header(l, patchAddHdr); ok {
			if err := flushOp(); err != nil {
				return nil, err
			}
			cur = &patchOp{kind: patchAdd, path: p}
			continue
		}
		if p, ok := header(l, patchDelHdr); ok {
			if err := flushOp(); err != nil {
				return nil, err
			}
			ops = append(ops, patchOp{kind: patchDelete, path: p})
			continue
		}
		if p, ok := header(l, patchUpdHdr); ok {
			if err := flushOp(); err != nil {
				return nil, err
			}
			cur = &patchOp{kind: patchUpdate, path: p}
			continue
		}
		if cur == nil {
			if strings.TrimSpace(l) == "" {
				continue
			}
			return nil, fmt.Errorf("patch line %d: expected a '*** Add File:', '*** Update File:' or '*** Delete File:' header, got %q", lineNo, l)
		}
		switch cur.kind {
		case patchAdd:
			if !strings.HasPrefix(l, "+") {
				if strings.TrimSpace(l) == "" {
					continue
				}
				return nil, fmt.Errorf("patch line %d: every line of an added file starts with '+', got %q", lineNo, l)
			}
			cur.body += strings.TrimPrefix(l, "+") + "\n"
		case patchUpdate:
			if p, ok := header(l, patchMoveHdr); ok && hunk == nil && len(cur.hunks) == 0 {
				cur.moveTo = p
				continue
			}
			if strings.TrimSpace(l) == patchEOF {
				continue
			}
			if strings.HasPrefix(l, "@@") {
				flushHunk()
				hunk = &patchHunk{anchor: strings.TrimSpace(strings.TrimPrefix(l, "@@"))}
				continue
			}
			if hunk == nil {
				hunk = &patchHunk{} // the first hunk may omit its '@@' line
			}
			switch {
			case l == "":
				hunk.old += "\n"
				hunk.new += "\n"
			case l[0] == ' ':
				hunk.old += l[1:] + "\n"
				hunk.new += l[1:] + "\n"
			case l[0] == '-':
				hunk.old += l[1:] + "\n"
			case l[0] == '+':
				hunk.new += l[1:] + "\n"
			default:
				return nil, fmt.Errorf("patch line %d: a hunk line starts with ' ', '-' or '+', got %q", lineNo, l)
			}
		}
	}
	if err := flushOp(); err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("the patch has no file sections")
	}
	for _, op := range ops {
		if op.path == "" {
			return nil, fmt.Errorf("a file section is missing its path")
		}
	}
	return ops, nil
}

// applyHunks applies an Update section's hunks to content, in order. A hunk
// with an anchor takes the FIRST match after the anchor's line — that is
// what the anchor is for, telling a repeated block apart — while a hunk
// without one must match uniquely, exactly as edit_file does. A hunk with no
// context and no removed lines inserts after its anchor, or at end of file.
func applyHunks(content string, hunks []patchHunk) (string, int, error) {
	total := 0
	for i, h := range hunks {
		from := 0
		if h.anchor != "" {
			at, ok := anchorEnd(content, h.anchor)
			if !ok {
				return "", 0, fmt.Errorf("hunk %d: its '@@ %s' line is not in the file", i+1, h.anchor)
			}
			from = at
		}
		if h.old == "" {
			if h.anchor == "" {
				if content != "" && !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += h.new
			} else {
				content = content[:from] + h.new + content[from:]
			}
			total++
			continue
		}
		var region string
		var n int
		var err error
		if h.anchor != "" {
			region, n, err = applyEditFirst(content[from:], h.old, h.new)
		} else {
			region, n, err = applyEdit(content[from:], h.old, h.new, false)
		}
		if err != nil {
			return "", 0, fmt.Errorf("hunk %d: %s", i+1, patchErrorText(err))
		}
		content = content[:from] + region
		total += n
	}
	return content, total, nil
}

// applyEditFirst replaces the first match of old in content under
// applyEdit's own tiers (exact, then trailing-whitespace-, then indentation-
// tolerant, re-indented to the file). When nothing matches it defers to
// applyEdit for the error, so the not-found message and its nearest-region
// hint are edit_file's.
func applyEditFirst(content, old, new string) (string, int, error) {
	if old == "" || old == new {
		return applyEdit(content, old, new, false)
	}
	if i := strings.Index(content, old); i >= 0 {
		return content[:i] + new + content[i+len(old):], 1, nil
	}
	fileLines := strings.SplitAfter(content, "\n")
	oldLines := dropTrailingEmpty(strings.SplitAfter(old, "\n"))
	k := len(oldLines)
	for tier := 1; tier <= 2 && k > 0; tier++ {
		off := 0
		for i := 0; i+k <= len(fileLines); i++ {
			if windowMatches(fileLines[i:i+k], oldLines, tier) {
				end := off
				for _, l := range fileLines[i : i+k] {
					end += len(l)
				}
				// The window is unique by construction, so tolerantEdit's
				// re-indentation applies to exactly this occurrence.
				seg, n, err := tolerantEdit(content[off:end], old, new, false)
				if err == nil {
					return content[:off] + seg + content[end:], n, nil
				}
			}
			off += len(fileLines[i])
		}
	}
	return applyEdit(content, old, new, false)
}

// anchorEnd returns the byte offset just past the first line of content
// equal to anchor (ignoring surrounding whitespace), else the first line
// containing it.
func anchorEnd(content, anchor string) (int, bool) {
	want := strings.TrimSpace(anchor)
	for pass := 0; pass < 2; pass++ {
		off := 0
		for _, l := range strings.SplitAfter(content, "\n") {
			t := strings.TrimSpace(l)
			if (pass == 0 && t == want) || (pass == 1 && strings.Contains(t, want)) {
				return off + len(l), true
			}
			off += len(l)
		}
	}
	return 0, false
}

// patchErrorText rewords applyEdit's errors, written for edit_file's
// arguments, into this format's terms — the model on apply_patch has no
// old_string, replace_all, or edit_file to retry with.
func patchErrorText(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, notFoundDirective, " — re-read the current span with read_file and send a new patch built from it.")
	s = strings.ReplaceAll(s, "; add surrounding context to make it unique, or set replace_all", "; add surrounding context lines or an '@@' line above the change to make it unique")
	s = strings.ReplaceAll(s, "old_string and new_string are identical", "the hunk removes and adds the same lines")
	s = strings.ReplaceAll(s, "old_string", "the hunk's context and removed lines")
	return s
}

// applyPatch runs an apply_patch call. Every section is parsed and its
// preconditions checked (Update/Delete targets exist, Add targets do not)
// before anything is written; sections are then applied in order. A failure
// part-way stops there, and the result says which sections already landed.
func applyPatch(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
	var a struct {
		Input string `json:"input"`
	}
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &a); err != nil {
			return "", fmt.Errorf("parse apply_patch args: %w", err)
		}
	}
	if strings.TrimSpace(a.Input) == "" {
		return "", fmt.Errorf("input is required: the full patch text, from '%s' to '%s'", patchBegin, patchEnd)
	}
	ops, err := parsePatch(a.Input)
	if err != nil {
		return "", err
	}
	// Preconditions, all before the first write: the paths each section names
	// exist (or, for Add, don't), every written path passes the tainted-turn
	// confinement, and a section that removes a file (Delete, or the old side
	// of a Move) is allowed by remove_path's own policy — so a patch fails
	// whole on anything knowable up front, never half-applied.
	for _, op := range ops {
		for _, p := range []string{op.path, op.moveTo} {
			if p == "" {
				continue
			}
			if err := ConfineWrites(syntheticCall(FunctionEditFile, map[string]any{"path": p}), deps); err != nil {
				return "", err
			}
		}
		if op.kind == patchDelete || op.moveTo != "" {
			root, allowed := deps.AllowDelete()
			if !allowed {
				return "", fmt.Errorf("%s: deleting or moving a file needs remove_path, which is disabled", op.path)
			}
			if _, err := confinedPath(root, op.path); err != nil {
				return "", err
			}
		}
		_, statErr := os.Stat(resolveWorkdir(deps, op.path))
		switch op.kind {
		case patchAdd:
			if statErr == nil {
				return "", fmt.Errorf("'%s%s': the file already exists; use '%s' to change it", patchAddHdr, op.path, patchUpdHdr)
			}
		case patchUpdate, patchDelete:
			if statErr != nil {
				return "", fmt.Errorf("%s: no such file — check the path with outline or grep", op.path)
			}
		}
		if op.moveTo != "" {
			if _, err := os.Stat(resolveWorkdir(deps, op.moveTo)); err == nil {
				return "", fmt.Errorf("'%s%s': %s already exists", patchMoveHdr, op.moveTo, op.moveTo)
			}
		}
	}

	var done []string
	partial := func(err error) error {
		if len(done) == 0 {
			return err
		}
		return fmt.Errorf("%w (already applied by this patch: %s)", err, strings.Join(done, "; "))
	}
	var results []string
	for _, op := range ops {
		var out string
		var err error
		switch op.kind {
		case patchAdd:
			// write_file does not create directories; an added file may be
			// the first in a new one (the path passed confinement above).
			if err := os.MkdirAll(filepath.Dir(resolveWorkdir(deps, op.path)), 0o755); err != nil {
				return "", partial(fmt.Errorf("create the directory for %s: %w", op.path, err))
			}
			out, err = writeFile(ctx, syntheticCall(FunctionWriteFile, map[string]any{"path": op.path, "content": op.body}), deps)
		case patchDelete:
			out, err = removePath(syntheticCall(FunctionRemove, map[string]any{"path": op.path}), deps)
		case patchUpdate:
			out, err = updateFromPatch(ctx, op, deps)
		}
		if err != nil {
			return "", partial(err)
		}
		done = append(done, op.path)
		results = append(results, out)
	}
	return strings.Join(results, "\n"), nil
}

// updateFromPatch applies one Update section: its hunks through applyEdit,
// then edit_file's own finish. A Move writes the result at the new path and
// removes the old one through write_file/remove_path, so their gates apply.
func updateFromPatch(ctx context.Context, op patchOp, deps ToolDeps) (string, error) {
	if err := ConfineWrites(syntheticCall(FunctionEditFile, map[string]any{"path": op.path}), deps); err != nil {
		return "", err
	}
	fsPath := resolveWorkdir(deps, op.path)
	info, err := os.Stat(fsPath)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", op.path, err)
	}
	data, err := os.ReadFile(fsPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", op.path, err)
	}
	updated, total, err := applyHunks(string(data), op.hunks)
	if err != nil {
		return "", fmt.Errorf("%s %s", op.path, err)
	}
	if op.moveTo != "" {
		out, err := writeFile(ctx, syntheticCall(FunctionWriteFile, map[string]any{"path": op.moveTo, "content": updated}), deps)
		if err != nil {
			return "", err
		}
		if _, err := removePath(syntheticCall(FunctionRemove, map[string]any{"path": op.path}), deps); err != nil {
			return "", fmt.Errorf("moved %s to %s but could not remove the original: %w", op.path, op.moveTo, err)
		}
		return fmt.Sprintf("moved %s to %s\n%s", op.path, op.moveTo, out), nil
	}
	if err := os.WriteFile(fsPath, []byte(updated), info.Mode()); err != nil {
		return "", fmt.Errorf("write %s: %w", op.path, err)
	}
	printToolAction(deps, fmt.Sprintf("apply_patch(%s)", op.path))
	editsNoun := ""
	if len(op.hunks) > 1 {
		editsNoun = CountNoun(len(op.hunks), "hunk")
	}
	return finishFileEdit(ctx, deps, op.path, fsPath, string(data), updated, editsNoun, total, false), nil
}

// syntheticCall builds a tool call to run another tool's implementation on
// this tool's behalf (an Add through write_file, a Delete through
// remove_path), so those tools' gates and displays apply unchanged.
func syntheticCall(name string, args map[string]any) ToolCall {
	b, _ := json.Marshal(args)
	return ToolCall{Function: FunctionCall{Name: name, Arguments: string(b)}}
}

// MutatedPaths returns the workspace paths a file-mutating tool call names:
// the `path` of write_file / edit_file / remove_path, and every file an
// apply_patch envelope adds, updates, moves or deletes. It is what the
// harness's edit bookkeeping (turn-end lint, the footer's changed files, the
// test-file watch) reads, so apply_patch edits are counted exactly like
// edit_file's. Nil for any other tool, or a call whose arguments don't parse.
func MutatedPaths(call ToolCall) []string {
	switch call.Function.Name {
	case FunctionWriteFile, FunctionEditFile, FunctionRemove:
		if p, err := call.StringArg("path"); err == nil && p != "" {
			return []string{p}
		}
	case FunctionApplyPatch:
		in, err := call.StringArg("input")
		if err != nil {
			return nil
		}
		ops, err := parsePatch(in)
		if err != nil {
			return nil
		}
		var out []string
		for _, op := range ops {
			out = append(out, op.path)
			if op.moveTo != "" {
				out = append(out, op.moveTo)
			}
		}
		return out
	}
	return nil
}

// WritesFiles reports whether a tool call writes file content (write_file,
// edit_file, apply_patch) as opposed to deleting (remove_path) or not
// touching files at all — the set whose results turn-end lint checks.
func WritesFiles(name string) bool {
	switch name {
	case FunctionWriteFile, FunctionEditFile, FunctionApplyPatch:
		return true
	}
	return false
}
