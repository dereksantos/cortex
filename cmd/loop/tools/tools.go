// Package tools holds the tool surface the loop exposes to the model: the
// tool declarations, the ToolCall dispatcher, and each tool's implementation.
//
// The tools are extracted from cmd/loop/main.go as a pure move — no behavior
// change. The session coupling (study engine, shell-risk gate, delete
// confinement, retrieval) is exposed through the ToolDeps interface so the
// tools package doesn't import the session package (which would be a cycle,
// since session imports tools for the tool declarations).
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/cmd/loop/ui"
	"github.com/dereksantos/cortex/internal/projectindex"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/study"
)

// ToolDeps is the session surface the tools need. *CortexSession satisfies it.
// Kept minimal: each method is used by at least one tool.
type ToolDeps interface {
	// StudyModel returns the study role's model id (for the action label).
	StudyModel() string
	// StudyWindow resolves the study model's context window.
	StudyWindow() int
	// RunStudy executes the study engine over a file. Shared by the study
	// tool, the shell-output study, and compaction.
	RunStudy(ctx context.Context, path, goal string, passes, chunks int, fill float64, numbered *bool, window int, noWM bool) (study.StudyLoopResult, error)
	// GateShell runs the shell-risk gate. Returns (message, ok); ok=false
	// means the command must not run and message explains why.
	GateShell(ctx context.Context, command string) (string, bool)
	// AllowDelete reports whether remove_path is enabled, and the workspace
	// root it's confined to.
	AllowDelete() (root string, allowed bool)
	// Quiet reports whether terminal emission is suppressed (headless mode).
	Quiet() bool
}

// headlessDeps is the nil-safe ToolDeps substituted by Execute when a tool is
// dispatched without a session. It mirrors the old nil-*CortexSession path:
// the shell gate fails closed through shellrisk with no classifier and no
// interactive approver, study is unavailable (oversized output truncates), and
// delete is disabled.
type headlessDeps struct{}

func (headlessDeps) StudyModel() string { return "" }
func (headlessDeps) StudyWindow() int   { return 0 }
func (headlessDeps) RunStudy(context.Context, string, string, int, int, float64, *bool, int, bool) (study.StudyLoopResult, error) {
	return study.StudyLoopResult{}, errors.New("study unavailable: no session")
}
func (headlessDeps) GateShell(ctx context.Context, command string) (string, bool) {
	v := shellrisk.Classify(ctx, command, nil)
	switch v.Level {
	case shellrisk.Safe:
		return "", true
	case shellrisk.Blocked:
		return fmt.Sprintf("refused by the safety gate (%s). This command will not run; choose a safer approach.", v.Reason), false
	default: // Risky — no interactive approver in a headless context.
		return fmt.Sprintf("blocked (risk: %s). No interactive approval is available in this session — re-issue a safer command, or ask the user to run it.", v.Reason), false
	}
}
func (headlessDeps) AllowDelete() (string, bool) { return "", false }
func (headlessDeps) Quiet() bool                 { return false }

// Tool names — the canonical identifiers on the wire and in the dispatcher.
const (
	FunctionReadFile     = "read_file"
	FunctionWriteFile    = "write_file"
	FunctionEditFile     = "edit_file"
	FunctionStudy        = "study"
	FunctionBash         = "bash"
	FunctionRemove       = "remove_path"
	FunctionProjectIndex = "project_index"
)

// Tool is the OpenAI-format tool declaration passed in the tools array.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction carries the name, description, and JSON-Schema parameters.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// objectSchema builds a JSON Schema "object" with the given properties and
// required fields. Keeps the tool definitions readable instead of nesting
// map[string]any by hand.
func objectSchema(props map[string]any, required ...string) map[string]any {
	// Emit "required": [] not null when there are no required fields: a nil
	// []string marshals to JSON null, and strict tool-call parsers (GLM-4.7-Flash
	// on llama.cpp) reject "required": null with "type must be array, but is
	// null" when generating their constrained grammar. [] is also the correct
	// JSON Schema for "no required properties".
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func newTool(name, desc string, params map[string]any) Tool {
	return Tool{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}}
}

// --- Tool declarations --------------------------------------------------

var ReadFile = newTool(FunctionReadFile,
	"Read the whole contents of a file. Best for files that fit the curation "+
		"budget. A too-large Go file returns its declaration skeleton (funcs/types/"+
		"const/var with line numbers) instead of the content, so you can orient and "+
		"then study a region; a too-large non-Go file redirects to study.",
	objectSchema(map[string]any{
		"path": stringProp("Path to the file to read, relative to the working directory."),
	}, "path"))

var WriteFile = newTool(FunctionWriteFile,
	"Write content to a file at the given path, creating or overwriting it.",
	objectSchema(map[string]any{
		"path":    stringProp("Path to the file to write."),
		"content": stringProp("The full contents to write to the file."),
	}, "path", "content"))

var EditFile = newTool(FunctionEditFile,
	"Replace text in a file. Matching is exact first; if that finds nothing it "+
		"retries ignoring leading/trailing whitespace, so indentation needn't be "+
		"byte-perfect. old_string must still resolve to exactly one place unless "+
		"replace_all is set. Prefer this over write_file for changes to an existing "+
		"file. To make several changes at once, pass an `edits` array — they apply "+
		"in order and atomically (all succeed or the file is left untouched).",
	objectSchema(map[string]any{
		"path":        stringProp("Path to the file to edit."),
		"old_string":  stringProp("Text to find (single edit). Include enough context to be unique; indentation may differ from the file."),
		"new_string":  stringProp("Replacement text (single edit). May be empty to delete old_string."),
		"replace_all": boolProp("Replace every occurrence instead of requiring a unique match. Default false."),
		"edits": map[string]any{
			"type":        "array",
			"description": "Optional: multiple edits applied in order, atomically. When set, the top-level old_string/new_string are ignored.",
			"items": objectSchema(map[string]any{
				"old_string":  stringProp("Text to find; indentation may differ from the file."),
				"new_string":  stringProp("Replacement text; may be empty to delete."),
				"replace_all": boolProp("Replace every occurrence. Default false."),
			}, "old_string", "new_string"),
		},
	}, "path"))

var StudyTool = newTool(FunctionStudy,
	"Study a file or directory and return curated context: a size-adaptive, "+
		"relevance-deepening digest with cited file:line ranges. Prefer this over "+
		"read_file for large files, for understanding whole packages/directories, or "+
		"when you want to understand something relative to a goal. Small targets are "+
		"returned whole (a directory as every file inlined under path headers).",
	objectSchema(map[string]any{
		"path":   stringProp("Path to the file or directory to study."),
		"goal":   stringProp("What you want to learn; guides which regions get deepened."),
		"passes": map[string]any{"type": "integer", "description": "Deepening passes (more = denser coverage of relevant regions, but slower). Default 1 for files, 3 for directories."},
	}, "path"))

var ProjectIndexTool = newTool(FunctionProjectIndex,
	"Map a project or file structurally without reading contents. On a directory: "+
		"a recursive file tree plus per-file funcs and types with line numbers (Go). "+
		"On a single file: its full declaration skeleton — every top-level func, "+
		"type, const, and var in file order with line numbers — the fastest way to "+
		"see a file's seams before editing it. Cheap, high-signal orientation; call "+
		"it first. Respects .gitignore and skips vendor/build dirs and secrets. Pass "+
		"a subdirectory to scope a large repo.",
	objectSchema(map[string]any{
		"path": stringProp("Directory or file to index, relative to the working directory. A directory gives the tree+symbols map; a file gives its full declaration skeleton. Default: the whole project ('.')."),
	}))

var Bash = newTool(FunctionBash,
	"Run a shell command via bash (pipes, redirects, and chaining are supported). A risk gate assesses each command: safe commands run immediately, risky ones (deletes, pushes, installs, network calls) need approval, and catastrophic ones are refused. Prefer the dedicated read_file/write_file/remove_path tools where they fit.",
	objectSchema(map[string]any{
		"command": stringProp("The command to run, e.g. 'go test ./...' or 'ls cmd'."),
	}, "command"))

var RemoveTool = newTool(FunctionRemove,
	"Delete a file or directory (recursively). Confined to the workspace: paths "+
		"that escape it, the workspace root itself, and .git/.cortex are refused. "+
		"In a git repo prefer `git rm` for tracked files; use this for untracked "+
		"files or directories.",
	objectSchema(map[string]any{
		"path": stringProp("Path to delete, relative to the working directory."),
	}, "path"))

// All is the full tool set, in declaration order.
var All = []Tool{ReadFile, WriteFile, EditFile, StudyTool, ProjectIndexTool, Bash, RemoveTool}

// --- Wire types ---------------------------------------------------------

// FunctionCall is the function part of a tool call.
type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON-encoded *string* on the wire (e.g. `{"path":"go.mod"}`),
	// NOT a JSON object. Parse it with stringArg.
	Arguments string `json:"arguments"`
}

// ToolCall is one tool invocation from the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// --- Dispatcher ---------------------------------------------------------

// Execute dispatches a tool call. ctx cancels long-running tools (bash, study)
// on interrupt; deps carries session config (model, endpoint, window) that some
// tools need — study does; the file tools ignore both.
func (tc ToolCall) Execute(ctx context.Context, deps ToolDeps) (string, error) {
	// A tool dispatched without a session (tests, non-interactive paths) runs
	// against the nil-safe headless defaults: the shell gate fails closed, study
	// is unavailable, delete is disabled. This preserves the old behavior of the
	// nil-*CortexSession receiver now that deps is an interface (a nil interface
	// would panic on the first method call).
	if deps == nil {
		deps = headlessDeps{}
	}
	name := tc.Function.Name
	switch name {
	case FunctionReadFile:
		return tc.ReadFile(deps)
	case FunctionWriteFile:
		return tc.WriteFile()
	case FunctionEditFile:
		return tc.EditFile()
	case FunctionStudy:
		return tc.Study(ctx, deps)
	case FunctionProjectIndex:
		return tc.ProjectIndex()
	case FunctionBash:
		return tc.Bash(ctx, deps)
	case FunctionRemove:
		return tc.RemovePath(deps)
	}
	return "", fmt.Errorf(`no available tools matching name "%s"`, name)
}

// --- Helpers shared across tools ----------------------------------------

// CurationBudgetTokens is the read_file → study redirect threshold: a whole-file
// read above this is refused and redirected to study, so the coder gets a
// curated digest rather than a raw dump. Decoupled from the coder's window on
// purpose — sizing the trigger to the window let a big-window model read
// everything raw, defeating curation. (~4 bytes/token.)
const CurationBudgetTokens = 16000

// MaxToolOutput caps how much tool output we feed back into context, so a
// `cat` of a huge file (or `find` over a big tree) can't blow the window.
const MaxToolOutput = 10000

// DirStudyPasses: default deepening passes when the study target is a
// directory. A corpus boundary is far larger than one file's, so a single
// window-budget pass sees only a sliver of the tree; the curator still ends
// the loop early (DONE / exhausted), so this is a cap, not a floor.
const DirStudyPasses = 3

// printToolAction prints an indented, iconned tool-action line under the
// current cortex turn, e.g. "  ▸ read_file(go.mod)". The tool name shows in
// green; its argument list is dimmed so the verb reads first.
func printToolAction(action string) {
	name, args := action, ""
	if i := strings.IndexByte(action, '('); i >= 0 {
		name, args = action[:i], action[i:]
	}
	line := ui.Color(ui.IconTool+" "+name, ui.Green)
	if args != "" {
		line += ui.Color(args, ui.Gray)
	}
	fmt.Printf("  %s\n", line)
}

// StringArg parses Arguments (a JSON string) and pulls out one string field.
func (tc ToolCall) StringArg(name string) (string, error) {
	var m map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return "", fmt.Errorf("parse arguments %q: %w", tc.Function.Arguments, err)
		}
	}
	v, ok := m[name].(string)
	if !ok {
		return "", fmt.Errorf("missing or non-string arg %q", name)
	}
	return v, nil
}

// IntArg pulls an integer field from Arguments. JSON numbers decode as float64.
// Returns (0, false) when missing or not a number.
func (tc ToolCall) IntArg(name string) (int, bool) {
	var m map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if json.Unmarshal([]byte(s), &m) == nil {
			if v, ok := m[name].(float64); ok {
				return int(v), true
			}
		}
	}
	return 0, false
}

func (tc ToolCall) String() string {
	return fmt.Sprintf("wants %s %s %s %v", tc.ID, tc.Type, tc.Function.Name, tc.Function.Arguments)
}

// --- project_index ------------------------------------------------------

// ProjectIndex returns the project map — a recursive file tree plus per-file
// Go symbol inventory — for orientation without reading files. The path arg is
// optional (default "."); a subdirectory scopes a large repo.
func (tc ToolCall) ProjectIndex() (string, error) {
	path := "."
	if p, _ := tc.StringArg("path"); strings.TrimSpace(p) != "" {
		path = p
	}
	printToolAction(fmt.Sprintf("project_index(%s)", path))
	ix, err := projectindex.Build(path)
	if err != nil {
		return "", fmt.Errorf("index %s: %w", path, err)
	}
	return ix.Render(), nil
}

// --- study --------------------------------------------------------------

// Study runs the real study engine (internal/study) over a file and returns
// curated context: a size-adaptive, relevance-deepening digest with cited line
// ranges, or the whole file when it fits the window. Inference and curation are
// backed by an OpenAI-compatible provider pointed at the session's endpoint.
func (tc ToolCall) Study(ctx context.Context, deps ToolDeps) (string, error) {
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	goal, _ := tc.StringArg("goal") // optional
	passes := 0
	if p, ok := tc.IntArg("passes"); ok && p > 0 {
		passes = p
	}
	if passes == 0 {
		passes = defaultStudyPasses(path)
	}
	plural := ""
	if passes != 1 {
		plural = "es"
	}
	printToolAction(fmt.Sprintf("study(%s) via %s (%d pass%s)", path, deps.StudyModel(), passes, plural))

	res, err := deps.RunStudy(ctx, path, goal, passes, 0, 0, nil, 0, false)
	if err != nil {
		return "", err
	}
	return renderStudyResultWithMap(path, res), nil
}

// renderStudyResultWithMap prefixes the study digest with a structural map
// of the target when it's a directory — the map→study producer/consumer
// contract. The agent sees the terrain (file tree + symbols) before the
// analysis, so it can judge coverage gaps and decide where to deepen.
// Single-file studies skip the map: the study result already carries
// per-region citations, and project_index is the cheaper orient for one
// file. The map is bounded so it never dominates the study output.
func renderStudyResultWithMap(path string, res study.StudyLoopResult) string {
	digest := RenderStudyResult(res)
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return digest
	}
	ix, err := projectindex.Build(path)
	if err != nil {
		return digest // map is a bonus, not a gate — degrade to digest-only.
	}
	m := ix.Render()
	if len(m) > StudyMapBudget {
		m = m[:StudyMapBudget] + "\n… (map truncated; use project_index for the full tree)"
	}
	return m + "\n\n" + digest
}

// StudyMapBudget caps the structural map prefix so it never dominates the
// study digest on large trees. The map is orientation; the digest is the
// substance. project_index remains available for the unbounded view.
const StudyMapBudget = 4000

// defaultStudyPasses picks the pass count when the model didn't ask for one:
// 1 for files, DirStudyPasses for directories.
func defaultStudyPasses(path string) int {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return DirStudyPasses
	}
	return 1
}

// RenderStudyResult turns the curated study-loop result into the context string
// the harness model consumes. Read mode returns the whole file verbatim;
// otherwise it's the per-pass digests plus provenance-validated citations.
func RenderStudyResult(res study.StudyLoopResult) string {
	if res.Stopped == "read" && len(res.Passes) > 0 {
		return res.Passes[0].Response.ReadContent
	}
	var b strings.Builder
	fmt.Fprintf(&b, "coverage %.0f%%, stopped: %s\n", 100*res.CoveragePct, res.Stopped)
	for i, d := range res.Digests {
		if s := strings.TrimSpace(d); s != "" {
			fmt.Fprintf(&b, "\npass %d:\n%s\n", i+1, s)
		}
	}
	if len(res.Citations) > 0 {
		b.WriteString("\ncitations:\n")
		for _, c := range res.Citations {
			fmt.Fprintf(&b, "  %s:%d-%d  %s\n", c.RelPath, c.LineStart, c.LineEnd, c.Claim)
		}
	}
	if len(res.UncoveredFiles) > 0 {
		b.WriteString("\nuncovered files (not sampled in any pass — target these to deepen):\n")
		for _, f := range res.UncoveredFiles {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	return strings.TrimSpace(b.String())
}

// --- read_file ----------------------------------------------------------

func (tc ToolCall) ReadFile(deps ToolDeps) (string, error) {
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	// Curation budget: a whole-file read above CurationBudgetTokens is refused
	// and redirected to study, so the coder gets a CURATED digest rather than a
	// raw dump. Decoupled from the coder's window on purpose — sizing the trigger
	// to the window let a big-window model read everything raw, defeating
	// curation (2026-06-20). (~4 bytes/token.)
	if info, statErr := os.Stat(path); statErr == nil {
		if estTokens := int(info.Size()) / 4; estTokens > CurationBudgetTokens {
			// Read the map before the territory: a too-large Go file hands back
			// its declaration skeleton so the model can orient and target a
			// region (study for content, bash sed for an exact range) instead of
			// dead-ending. Non-Go (no skeleton) still redirects to study.
			if skel := goFileSkeleton(path); skel != "" {
				printToolAction(fmt.Sprintf("read_file(%s) → skeleton (~%dk tokens, too large)", path, estTokens/1000))
				return fmt.Sprintf("%s is ~%d tokens — too large to read whole. Its declaration skeleton is below; "+
					"use study(%q, goal) for curated content, or bash `sed -n 'A,Bp' %s` to read an exact line range.\n\n%s",
					path, estTokens, path, path, skel), nil
			}
			return "", fmt.Errorf("%s is %d bytes (~%d tokens) — too large to read whole; use study(%q, goal) instead",
				path, info.Size(), estTokens, path)
		}
	}
	printToolAction(fmt.Sprintf("read_file(%s)", path))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// goFileSkeleton returns the declaration skeleton of a Go file (every top-level
// func/type/const/var with line numbers), or "" for non-Go or unparseable files
// — the orientation a too-large read_file hands back in place of the content.
func goFileSkeleton(path string) string {
	if !strings.HasSuffix(path, ".go") {
		return ""
	}
	ix, err := projectindex.Build(path)
	if err != nil || len(ix.Files) == 0 || len(ix.Files[0].Symbols) == 0 {
		return ""
	}
	return ix.Render()
}

// --- write_file ---------------------------------------------------------

func (tc ToolCall) WriteFile() (string, error) {
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	content, err := tc.StringArg("content")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("write_file(%s, %d bytes)", path, len(content)))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// --- edit_file ----------------------------------------------------------

// editOp is one find/replace within an edit_file call. Several can be batched
// via the `edits` array and are applied in order to the evolving content.
type editOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// EditFile replaces text in a file. The safety property is a UNIQUE match:
// missing means the edit is wrong, ambiguous means we'd be guessing — both come
// back as observations the model can correct. Matching is exact first, then
// whitespace-tolerant (so a model that mis-indents old_string still lands the
// edit). replace_all relaxes uniqueness for renames; an `edits` array applies
// several changes atomically — if any fails the file is left untouched.
func (tc ToolCall) EditFile() (string, error) {
	var a struct {
		Path       string   `json:"path"`
		OldString  string   `json:"old_string"`
		NewString  string   `json:"new_string"`
		ReplaceAll bool     `json:"replace_all"`
		Edits      []editOp `json:"edits"`
	}
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &a); err != nil {
			return "", fmt.Errorf("parse edit_file args: %w", err)
		}
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	edits := a.Edits
	multi := len(edits) > 0
	if !multi {
		edits = []editOp{{OldString: a.OldString, NewString: a.NewString, ReplaceAll: a.ReplaceAll}}
		printToolAction(fmt.Sprintf("edit_file(%s)", a.Path))
	} else {
		printToolAction(fmt.Sprintf("edit_file(%s, %s)", a.Path, countNoun(len(edits), "edit")))
	}

	info, err := os.Stat(a.Path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", a.Path, err)
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", a.Path, err)
	}
	content := string(data)

	// Apply all edits in memory; only touch disk if every one succeeds.
	total := 0
	for i, e := range edits {
		updated, n, err := applyEdit(content, e.OldString, e.NewString, e.ReplaceAll)
		if err != nil {
			if multi {
				return "", fmt.Errorf("%s edit %d: %w", a.Path, i+1, err)
			}
			return "", fmt.Errorf("%s: %w", a.Path, err)
		}
		content = updated
		total += n
	}

	if err := os.WriteFile(a.Path, []byte(content), info.Mode()); err != nil {
		return "", fmt.Errorf("write %s: %w", a.Path, err)
	}
	if multi {
		return fmt.Sprintf("edited %s (%s, %s)", a.Path, countNoun(len(edits), "edit"), countNoun(total, "replacement")), nil
	}
	return fmt.Sprintf("edited %s (%s)", a.Path, countNoun(total, "replacement")), nil
}

// countNoun renders "1 edit" / "2 edits" — naive +s pluralization, fine for the
// nouns used here (edit, replacement).
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// applyEdit performs one find/replace on content, returning the result and the
// number of replacements. It tries an exact substring match first; only when
// that finds nothing does it fall back to whitespace-tolerant matching, so the
// fast path is unchanged and tolerance never overrides an exact hit.
func applyEdit(content, old, new string, replaceAll bool) (string, int, error) {
	if old == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	if old == new {
		return "", 0, fmt.Errorf("old_string and new_string are identical; nothing to change")
	}
	if n := strings.Count(content, old); n > 0 {
		if replaceAll {
			return strings.ReplaceAll(content, old, new), n, nil
		}
		if n > 1 {
			return "", 0, fmt.Errorf("old_string found %d times; add surrounding context to make it unique, or set replace_all", n)
		}
		return strings.Replace(content, old, new, 1), 1, nil
	}
	return tolerantEdit(content, old, new, replaceAll)
}

// tolerantEdit matches old against content line-by-line ignoring whitespace,
// then re-indents the replacement to the file's actual indentation. Tier 1
// ignores only trailing whitespace; tier 2 also ignores leading indentation —
// the safer tolerance is tried first. A match must still be unique unless
// replace_all is set.
func tolerantEdit(content, old, new string, replaceAll bool) (string, int, error) {
	fileLines := dropTrailingEmpty(strings.SplitAfter(content, "\n"))
	oldLines := dropTrailingEmpty(strings.SplitAfter(old, "\n"))
	k := len(oldLines)
	if k == 0 || k > len(fileLines) {
		return "", 0, fmt.Errorf("old_string not found%s", nearMissHint(fileLines, oldLines))
	}
	for tier := 1; tier <= 2; tier++ {
		var starts []int
		for i := 0; i+k <= len(fileLines); i++ {
			if windowMatches(fileLines[i:i+k], oldLines, tier) {
				starts = append(starts, i)
			}
		}
		if len(starts) == 0 {
			continue
		}
		if !replaceAll && len(starts) > 1 {
			return "", 0, fmt.Errorf("old_string matches %d places (ignoring whitespace); add context or set replace_all", len(starts))
		}
		return rebuildWithReplacements(fileLines, oldLines, new, starts), len(starts), nil
	}
	return "", 0, fmt.Errorf("old_string not found%s", nearMissHint(fileLines, oldLines))
}

// windowMatches reports whether a run of file lines equals the old block under
// the given tolerance tier.
func windowMatches(win, old []string, tier int) bool {
	for j := range old {
		if lineKey(win[j], tier) != lineKey(old[j], tier) {
			return false
		}
	}
	return true
}

// lineKey normalizes a line for tolerant comparison: tier 1 drops trailing
// whitespace, tier 2 drops leading and trailing whitespace.
func lineKey(line string, tier int) string {
	line = strings.TrimSuffix(line, "\n")
	if tier == 1 {
		return strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(line)
}

// rebuildWithReplacements substitutes new at each matched start (greedy,
// non-overlapping), re-indenting new from old's base indentation to the file
// region's, and preserving the trailing-newline state of the replaced span.
func rebuildWithReplacements(fileLines, oldLines []string, new string, starts []int) string {
	startSet := make(map[int]bool, len(starts))
	for _, s := range starts {
		startSet[s] = true
	}
	k := len(oldLines)
	oldBase := indentBase(oldLines)
	anchor := firstNonBlank(oldLines)
	var b strings.Builder
	for i := 0; i < len(fileLines); {
		if startSet[i] {
			fileBase := leadingWS(strings.TrimSuffix(fileLines[i+anchor], "\n"))
			repl := swapIndent(new, oldBase, fileBase)
			lastHadNL := strings.HasSuffix(fileLines[i+k-1], "\n")
			if lastHadNL && !strings.HasSuffix(repl, "\n") {
				repl += "\n"
			} else if !lastHadNL && strings.HasSuffix(repl, "\n") {
				repl = strings.TrimSuffix(repl, "\n")
			}
			b.WriteString(repl)
			i += k
			continue
		}
		b.WriteString(fileLines[i])
		i++
	}
	return b.String()
}

// swapIndent shifts new's base indentation: on each non-blank line that starts
// with oldBase, that prefix is swapped for fileBase (when oldBase is empty,
// fileBase is prepended). A no-op when the bases already match.
func swapIndent(s, oldBase, fileBase string) string {
	if oldBase == fileBase {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	for idx, ln := range lines {
		body := strings.TrimSuffix(ln, "\n")
		hadNL := strings.HasSuffix(ln, "\n")
		if strings.TrimSpace(body) == "" {
			continue // leave blank lines untouched
		}
		if strings.HasPrefix(body, oldBase) {
			body = fileBase + strings.TrimPrefix(body, oldBase)
		}
		if hadNL {
			body += "\n"
		}
		lines[idx] = body
	}
	return strings.Join(lines, "")
}

// dropTrailingEmpty removes the empty element SplitAfter leaves when the input
// ends with "\n", so line-window counts are exact.
func dropTrailingEmpty(lines []string) []string {
	if n := len(lines); n > 0 && lines[n-1] == "" {
		return lines[:n-1]
	}
	return lines
}

func firstNonBlank(lines []string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			return i
		}
	}
	return 0
}

func indentBase(lines []string) string {
	return leadingWS(strings.TrimSuffix(lines[firstNonBlank(lines)], "\n"))
}

func leadingWS(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// nearMissHint points the model at the file line most similar (by word overlap)
// to old's first meaningful line, so a failed match is fixable in one retry
// instead of looping. Returns "" when nothing is similar enough.
func nearMissHint(fileLines, oldLines []string) string {
	target := ""
	for _, l := range oldLines {
		if t := strings.TrimSpace(strings.TrimSuffix(l, "\n")); t != "" {
			target = t
			break
		}
	}
	tset := wordSet(target)
	if len(tset) == 0 {
		return ""
	}
	bestIdx, bestScore := -1, 0.0
	for i, l := range fileLines {
		body := strings.TrimSpace(strings.TrimSuffix(l, "\n"))
		if body == "" {
			continue
		}
		if s := jaccard(tset, wordSet(body)); s > bestScore {
			bestScore, bestIdx = s, i
		}
	}
	if bestIdx < 0 || bestScore < 0.5 {
		return ""
	}
	line := strings.TrimSpace(strings.TrimSuffix(fileLines[bestIdx], "\n"))
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return fmt.Sprintf(" — closest is line %d: %q (re-read the file if it changed)", bestIdx+1, line)
}

// wordSet splits text into a set of lowercased alphanumeric tokens.
func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return ('a' > r || r > 'z') && ('0' > r || r > '9')
	}) {
		set[w] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// --- remove_path --------------------------------------------------------

// RemovePath deletes a file or directory, confined to the workspace. It is the
// only deletion path (raw rm is not allowlisted): the confinement — not a
// human prompt — is the safety property, so the tool stays autonomous.
func (tc ToolCall) RemovePath(deps ToolDeps) (string, error) {
	root, allowed := deps.AllowDelete()
	if !allowed {
		return "", fmt.Errorf("remove_path is disabled")
	}
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	abs, err := confinedPath(root, path)
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("remove_path(%s)", path))
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("remove %s: %w", path, err)
	}
	return fmt.Sprintf("removed %s", path), nil
}

// confinedPath resolves p against root and verifies it stays inside it. It
// rejects absolute/`..` escapes, the root itself, the protected .git/.cortex
// trees, and symlink escapes (a path whose real parent leaves the root). The
// returned path is absolute and safe to delete.
func confinedPath(root, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	abs := filepath.Clean(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootAbs, abs)
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace (%s)", p, rootAbs)
	}
	if rel == "." {
		return "", fmt.Errorf("refusing to delete the workspace root")
	}
	top := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		top = rel[:i]
	}
	if top == ".git" || top == ".cortex" {
		return "", fmt.Errorf("refusing to delete protected path %q", rel)
	}
	// Symlink-escape guard: a lexical in-root path can still point out via a
	// symlinked parent (root/link -> /etc, then "link/x"). Re-check the real
	// parent. The final component may itself be a symlink — RemoveAll deletes
	// the link, not its target, so that's safe.
	if realParent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		rootReal, err2 := filepath.EvalSymlinks(rootAbs)
		if err2 == nil {
			if r, err := filepath.Rel(rootReal, realParent); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("path %q escapes the workspace via a symlink", p)
			}
		}
	}
	return abs, nil
}

// --- bash ---------------------------------------------------------------

func (tc ToolCall) Bash(ctx context.Context, deps ToolDeps) (string, error) {
	command, err := tc.StringArg("command")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("empty command")
	}
	// Risk gate (replaces the static allowlist). A refused/declined command
	// returns its explanation as the tool result — not an error — so the model
	// reads the reason plainly and adapts.
	if msg, ok := deps.GateShell(ctx, command); !ok {
		return msg, nil
	}
	// leadBin is the first token, used only for the grep-empty heuristic below.
	leadBin := ""
	if f := strings.Fields(command); len(f) > 0 {
		leadBin = f[0]
	}
	printToolAction(fmt.Sprintf("bash(%s)", command))

	// Full shell semantics via `bash -c`: pipes, redirects, and chaining all
	// work. The risk gate above (with its non-negotiable deny-floor) is what
	// keeps this safe now that a command is no longer a single inert binary.
	out, runErr := exec.CommandContext(ctx, "bash", "-c", command).CombinedOutput()
	result := string(out)
	// Oversized output is studied, not lost: the full output spills to
	// .cortex/shell/ and the model gets a cited digest plus the spill path
	// to study deeper. Truncation is only the no-study fallback.
	if len(result) > MaxToolOutput {
		if studied, ok := studyShellOutput(ctx, deps, command, out); ok {
			result = studied
		} else {
			result = result[:MaxToolOutput] + "\n...[output truncated]"
		}
	}
	// A non-zero exit is an observation, not a harness failure: hand the output
	// and exit error back to the model so it can react.
	if runErr != nil {
		// grep exits 1 to mean "no matches" — a normal, content-free result, not
		// an error. Reported as a bare "[exit error: exit status 1]" it gave the
		// model nothing to act on and it retried the same command in a loop
		// (2026-06-14). Name the empty-result case explicitly. Exit >=2 is a real
		// grep error and keeps its stderr (merged into result by CombinedOutput).
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 &&
			leadBin == "grep" && strings.TrimSpace(result) == "" {
			return "(no matches)", nil
		}
		return result + "\n[exit error: " + runErr.Error() + "]", nil
	}
	return result, nil
}

// bashStudyWindow is the consuming-model window the shell-output study is
// sized for, in tokens. Chosen so the engine's read-vs-study threshold
// (window/2 tokens, after sample headroom) sits BELOW MaxToolOutput:
// anything big enough to spill is always sampled into a bounded digest,
// never passed through whole — passthrough would re-create the context
// bloat the spill exists to avoid.
const bashStudyWindow = MaxToolOutput / 2

// spillShellOutput writes oversized command output under .cortex/shell/,
// content-addressed (same output → same file) so repeated runs of an
// unchanged command don't pile up copies.
func spillShellOutput(command string, out []byte) (string, error) {
	dir := filepath.Join(".cortex", "shell")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	head := "out"
	if f := strings.Fields(command); len(f) > 0 {
		head = f[0]
	}
	sum := sha256.Sum256(out)
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.txt", head, hex.EncodeToString(sum[:])[:12]))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// studyShellOutput turns oversized bash output into curated context instead
// of a truncation: the full output spills to .cortex/shell/ and the study
// engine digests it with real line citations into the spill file, which the
// model can study again (with a goal) to dig deeper. Returns ok=false on any
// failure so the caller degrades to plain truncation — losing the study is
// acceptable, losing the turn is not.
func studyShellOutput(ctx context.Context, deps ToolDeps, command string, out []byte) (string, bool) {
	spill, err := spillShellOutput(command, out)
	if err != nil {
		return "", false
	}
	printToolAction(fmt.Sprintf("output %d KB → study(%s)", len(out)/1024, spill))
	goal := fmt.Sprintf("This is the output of `%s`. What does it show? Surface errors, failures, and anomalies first.", command)
	res, err := deps.RunStudy(ctx, spill, goal, 1, 0, 0, nil, bashStudyWindow, false)
	if err != nil {
		return "", false
	}
	header := fmt.Sprintf("[%d bytes of output — studied below; full output at %s — study(path, goal) to dig deeper]\n", len(out), spill)
	return header + RenderStudyResult(res), true
}

// --- Qwen XML tool-call recovery ---------------------------------------

// Qwen3-Coder's native tool-call format is XML-ish:
//
//	<function=NAME>
//	<parameter=PNAME>
//	VALUE
//	</parameter>
//	</function>
//
// The proxy usually normalizes this into OpenAI tool_calls, but when it doesn't
// the raw XML leaks into message content with tool_calls empty. These regexes
// let us recover it.
var (
	fnRe    = regexp.MustCompile(`(?s)<function=([^>\s]+)>(.*?)</function>`)
	paramRe = regexp.MustCompile(`(?s)<parameter=([^>\s]+)>(.*?)</parameter>`)
)

// ParseXMLToolCalls extracts Qwen-native tool calls from raw content. Returns
// nil if none are present. Each call is normalized into the same ToolCall shape
// the OpenAI path produces, so it flows through Execute unchanged.
func ParseXMLToolCalls(content string) []ToolCall {
	fnMatches := fnRe.FindAllStringSubmatch(content, -1)
	if len(fnMatches) == 0 {
		return nil
	}
	var calls []ToolCall
	for i, fm := range fnMatches {
		name, body := fm[1], fm[2]
		// All current tools take string params, so a string map marshals to the
		// same JSON-string Arguments shape the wire uses. Note: TrimSpace strips
		// the framing newlines Qwen adds — fine for paths/commands, though it
		// would also trim a deliberately trailing newline in file content.
		args := map[string]string{}
		for _, pm := range paramRe.FindAllStringSubmatch(body, -1) {
			args[pm[1]] = strings.TrimSpace(pm[2])
		}
		raw, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, ToolCall{
			ID:       fmt.Sprintf("xml-%d", i+1),
			Type:     "function",
			Function: FunctionCall{Name: name, Arguments: string(raw)},
		})
	}
	return calls
}

// StripToolMarkup removes Qwen tool-call XML from content so we don't print the
// raw markup after converting it to tool calls. Any genuine prose preamble
// around the markup is preserved.
func StripToolMarkup(s string) string {
	s = fnRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "<tool_call>", "")
	s = strings.ReplaceAll(s, "</tool_call>", "")
	return strings.TrimSpace(s)
}

// ActivityLabel is the concise "tool(arg)" shown on the spinning status row
// while a tool runs — enough to tell which call is in flight without the full
// argument dump printToolAction already recorded above.
func (tc ToolCall) ActivityLabel() string {
	name := tc.Function.Name
	if p, err := tc.StringArg("path"); err == nil && p != "" {
		return name + "(" + p + ")"
	}
	if c, err := tc.StringArg("command"); err == nil && c != "" {
		first := firstLine(c)
		if len(first) > 40 {
			first = first[:40] + "…"
		}
		return name + "(" + first + ")"
	}
	return name
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
