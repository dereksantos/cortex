// Package tools holds the tool surface the loop exposes to the model: the
// tool declarations, the ToolCall dispatcher, and each tool's implementation.
//
// The tools are extracted from cmd/cortex/main.go as a pure move — no behavior
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

	"github.com/dereksantos/cortex/internal/agent"
	"github.com/dereksantos/cortex/internal/outline"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

// The tool-call vocabulary lives in internal/agent (the leaf the engine and this
// package share); aliased here so the declarations and dispatch read unchanged.
// The dependency arrow is internal/tools → internal/agent.
type (
	Tool         = agent.Tool
	ToolFunction = agent.ToolFunction
	ToolCall     = agent.ToolCall
	FunctionCall = agent.FunctionCall
)

// The tool deps are segmented into narrow, consumer-owned role-interfaces
// (Interface Segregation): each tool depends only on the slice it uses, and the
// fat ToolDeps that Execute's switch consumes is assembled by EMBEDDING the
// parts — not hand-listed. *CortexSession satisfies every one of them
// structurally (asserted at the composition root in main.go).

// MemoryStore is the model-driven note surface (memory_* tools). See
// internal/memory and docs/memory-tools.md.
type MemoryStore interface {
	// MemoryWrite creates or updates a named note and returns a confirmation
	// (the normalized name).
	MemoryWrite(name, content string) (result string, err error)
	// MemoryRead returns one note's body in full.
	MemoryRead(name string) (body string, err error)
	// MemorySearch returns notes matching the query as a formatted list (name —
	// hook — updated date), ready to hand back as the tool result.
	MemorySearch(query string) (results string, err error)
	// MemoryForget removes a note and returns a confirmation.
	MemoryForget(name string) (result string, err error)
}

// Recaller resolves an outline citation back to the raw transcript messages
// it stands for (docs/context-architecture.md: demotion is recoverable).
type Recaller interface {
	Recall(citation string) (string, error)
}

// Summarizer reduces a free-text file (no structural map) to a digest via
// sequential chunk-and-fold. Used by oversized shell-output study; permanent,
// unrelated to the study subagent. The compressed flag is unused there.
type Summarizer interface {
	// Summarize reduces a file to a digest toward goal.
	Summarize(ctx context.Context, path, goal string, window int) (digest string, compressed bool, err error)
	// SummarizeText reduces content (already in memory) to a digest toward goal.
	SummarizeText(ctx context.Context, content, goal string, window int) (digest string, compressed bool, err error)
}

// Outliner renders the structural map of a path (the study tool's seed + the
// outline tool). Satisfied by *CortexSession via internal/outline.
type Outliner interface {
	Outline(path string, budget int) (rendered string, err error)
}

// SubAgentRunner runs a Subagent profile on the shared engine. Satisfied by
// *CortexSession (RunSubagent). The study tool is the only caller today.
type SubAgentRunner interface {
	RunSubagent(ctx context.Context, sa Subagent, seed string) (digest string, err error)
}

// ShellGate runs the shell-risk gate. Returns (message, ok); ok=false means the
// command must not run and message explains why.
type ShellGate interface {
	GateShell(ctx context.Context, command string) (string, bool)
}

// DeleteGate reports whether remove_path is enabled, and the workspace root it's
// confined to.
type DeleteGate interface {
	AllowDelete() (root string, allowed bool)
}

// ConfigProvider exposes tool enable/disable configuration.
// Tools can query this to check if they are enabled via config.
type ConfigProvider interface {
	IsToolEnabled(toolName string) bool
}

// Validator provides optional validation for tool calls beyond config enable/disable.
// If ValidateToolCall returns (false, msg), the tool call is rejected with msg as the observation.
// This is for dynamic validation (e.g., watermark deltas must be within bounds).
type Validator interface {
	ValidateToolCall(tc ToolCall) (bool, string)
}

// OutlineModifier provides methods to modify the outline.
// This is implemented by *CortexSession.
type OutlineModifier interface {
	RemoveOutlineEntry(citation string) bool
	// MergeOutlineEntries replaces the contiguous outline entries from
	// startCitation through endCitation with one merged entry and returns its
	// spanning citation (which recall resolves to all the original messages).
	MergeOutlineEntries(startCitation, endCitation string) (string, error)
	OutlineLen() int
}

// WatermarkAdjuster provides methods to adjust working set watermarks.
// This is implemented by *CortexSession.
type WatermarkAdjuster interface {
	AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error)
}

// ToolDeps is the union Execute's big switch consumes — assembled from the parts
// by embedding, not hand-listed. A pure tool (read_file body, edit_file, grep,
// outline) takes none of these; a memory tool depends only on MemoryStore; the
// study tool needs Outliner (to seed) + SubAgentRunner (to run). The implementors
// are concrete types that satisfy it structurally — *CortexSession (production,
// asserted at the composition root in main.go) and headlessDeps (the nil-safe stub
// below) — so the interface is never constructed; the concretes are.
type ToolDeps interface {
	MemoryStore
	Summarizer
	Outliner
	SubAgentRunner
	ShellGate
	DeleteGate
	// Quiet reports whether terminal emission is suppressed (headless mode).
	Quiet() bool
	// Recaller resolves an outline citation back to the raw transcript messages
	// it stands for (docs/context-architecture.md: demotion is recoverable).
	Recaller
	// ConfigProvider exposes tool enable/disable configuration.
	ConfigProvider
	// Validator provides optional validation for tool calls beyond config.
	Validator
	// OutlineModifier provides methods to modify the outline.
	OutlineModifier
	// WatermarkAdjuster provides methods to adjust working set watermarks.
	WatermarkAdjuster
}

// headlessDeps is the nil-safe ToolDeps substituted by Execute when a tool is
// dispatched without a session. It mirrors the old nil-*CortexSession path:
// the shell gate fails closed through shellrisk with no classifier and no
// interactive approver, study is unavailable (oversized output truncates), and
// delete is disabled.
type headlessDeps struct{}

func (headlessDeps) Outline(string, int) (string, error) {
	return "", errors.New("outline unavailable: no session")
}
func (headlessDeps) RunSubagent(context.Context, Subagent, string) (string, error) {
	return "", errors.New("study unavailable: no session")
}
func (headlessDeps) Summarize(context.Context, string, string, int) (string, bool, error) {
	return "", false, errors.New("summarize unavailable: no session")
}
func (headlessDeps) SummarizeText(context.Context, string, string, int) (string, bool, error) {
	return "", false, errors.New("summarize unavailable: no session")
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
func (headlessDeps) MemoryWrite(string, string) (string, error) {
	return "", errors.New("memory unavailable: no session")
}
func (headlessDeps) MemoryRead(string) (string, error) {
	return "", errors.New("memory unavailable: no session")
}
func (headlessDeps) MemorySearch(string) (string, error) {
	return "", errors.New("memory unavailable: no session")
}
func (headlessDeps) MemoryForget(string) (string, error) {
	return "", errors.New("memory unavailable: no session")
}
func (headlessDeps) Recall(string) (string, error) {
	return "", errors.New("recall unavailable: no session")
}
func (headlessDeps) IsToolEnabled(string) bool                   { return true }
func (headlessDeps) ValidateToolCall(tc ToolCall) (bool, string) { return true, "" }
func (headlessDeps) RemoveOutlineEntry(string) bool              { return false }
func (headlessDeps) OutlineLen() int                             { return 0 }
func (headlessDeps) MergeOutlineEntries(string, string) (string, error) {
	return "", errors.New("outline unavailable: no session")
}
func (headlessDeps) AdjustWatermarks(int, int) (int, int, int, int, error) {
	return 0, 0, 0, 0, errors.New("watermark adjustment unavailable: no session")
}

// Tool names — the canonical identifiers on the wire and in the dispatcher.
const (
	FunctionReadFile     = "read_file"
	FunctionWriteFile    = "write_file"
	FunctionEditFile     = "edit_file"
	FunctionStudy        = "study"
	FunctionBash         = "bash"
	FunctionRemove       = "remove_path"
	FunctionOutline      = "outline"
	FunctionMemoryWrite  = "memory_write"
	FunctionMemoryRead   = "memory_read"
	FunctionMemorySearch = "memory_search"
	FunctionMemoryForget = "memory_forget"
	FunctionRecall       = "recall"
	FunctionFetchURL     = "fetch_url"
	FunctionWebSearch    = "web_search"
)

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
	"Read a file, or an exact line range of one. With just a path: the whole "+
		"file, unless it's too large — then its structural outline (declarations/"+
		"headings/sections with line spans, whatever the file type supports) comes "+
		"back instead. With start/end: exactly those 1-indexed lines, which bypasses "+
		"the size limit — the precise way to pull one declaration after a "+
		"skeleton/outline/study points you at its line span.",
	objectSchema(map[string]any{
		"path":  stringProp("Path to the file to read, relative to the working directory."),
		"start": map[string]any{"type": "integer", "description": "Optional: 1-indexed first line to read. When set, only the line range is returned (the size limit does not apply)."},
		"end":   map[string]any{"type": "integer", "description": "Optional: 1-indexed last line (inclusive). Defaults to a bounded window after start when omitted."},
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
	"Study a file or directory against a goal and get back a digest. A read-only "+
		"subagent is handed a structural map of the target plus your goal; it reads the "+
		"relevant parts itself and reports what it found — you don't get the raw file. "+
		"Prefer this over read_file for large files, for whole packages/directories, or "+
		"whenever you want code explained relative to a goal. The more specific the "+
		"goal, the tighter the subagent reads.",
	objectSchema(map[string]any{
		"path": stringProp("Path to the file or directory to study."),
		"goal": stringProp("What you want to learn — the subagent reads the parts relevant to this and answers it."),
	}, "path"))

var OutlineTool = newTool(FunctionOutline,
	"Map a path structurally without reading its contents, filled breadth-first to "+
		"a token budget. A directory lists its files/subdirs; a file lists its "+
		"top-level units (funcs, types, sections) each with a line span — so you can "+
		"read_file exactly the part you need. Cheap, high-signal orientation; outline "+
		"a path before reading it. Respects .gitignore and skips vendor/build dirs "+
		"and secrets.",
	objectSchema(map[string]any{
		"path":   stringProp("Directory or file to outline, relative to the working directory. Default: the whole project ('.')."),
		"budget": map[string]any{"type": "integer", "description": "Optional token budget for how much structure to expand (default a few thousand). A bigger budget expands more files/subdirs."},
	}, "path"))

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

// --- memory tools -------------------------------------------------------
//
// The four model-driven memory tools over the free-form note store
// (internal/memory). The model curates its own durable notes; the only
// mechanical seam is the index injected at turn start. See docs/memory-tools.md.

var MemoryWriteTool = newTool(FunctionMemoryWrite,
	"Save a durable note — only for what would change how you act in a future session "+
		"and that the code, git history, and journal don't already record: a decision and "+
		"its why, a standing constraint, a user preference. Rare by design; most turns "+
		"produce nothing worth saving. Pick a short kebab-case name; writing an existing "+
		"name updates that note (don't duplicate). Notes are timestamped automatically.",
	objectSchema(map[string]any{
		"name":    stringProp("Short kebab-case identifier for the note, e.g. 'auth-token-rotation'. Reusing a name updates that note."),
		"content": stringProp("The note body — free-form prose. State the fact and, where it helps, a pointer to the source (a file path, a journal turn)."),
	}, "name", "content"))

var MemoryReadTool = newTool(FunctionMemoryRead,
	"Read one memory note in full by name (names are listed in the memory index).",
	objectSchema(map[string]any{
		"name": stringProp("The note's name, as shown in the memory index."),
	}, "name"))

var MemorySearchTool = newTool(FunctionMemorySearch,
	"Search memory notes by keyword and get back matching names with one-line hooks "+
		"and their last-updated date. Use it to find a note when the index is long; "+
		"then memory_read the ones that look relevant.",
	objectSchema(map[string]any{
		"query": stringProp("Keywords to match against note names and bodies."),
	}, "query"))

var MemoryForgetTool = newTool(FunctionMemoryForget,
	"Delete a memory note by name — use when a note is wrong or obsolete and should "+
		"no longer be recalled.",
	objectSchema(map[string]any{
		"name": stringProp("The note's name, as shown in the memory index."),
	}, "name"))

var RecallTool = newTool(FunctionRecall,
	"Fetch the verbatim messages behind a session-outline citation (the [@session/…#m…-…] coordinates on demoted turns). Use it when the outline line is not enough and you need the raw detail of an older turn. Pass budget to get a compact digest instead of the raw messages when the gist is enough.",
	objectSchema(map[string]any{
		"citation": stringProp("The citation exactly as it appears in the outline, e.g. @session/20260701-143210#m12-19."),
		"budget":   map[string]any{"type": "integer", "description": "Optional: return a digest at roughly this many tokens instead of the raw messages."},
		"goal":     stringProp("Optional, with budget: what the digest should focus on. Default: the turn's key facts and decisions."),
	}, "citation"))

var FetchURLTool = newTool(FunctionFetchURL,
	"Fetch readable text from a public HTTP(S) URL. Responses are size-bounded; HTML scripts and styles are removed. Private and local network addresses are refused.",
	objectSchema(map[string]any{
		"url": stringProp("The public http or https URL to fetch."),
	}, "url"))

var WebSearchTool = newTool(FunctionWebSearch,
	"Search the public web and return compact ranked results with titles, URLs, and snippets. Use fetch_url to read a promising result in full.",
	objectSchema(map[string]any{
		"query":       stringProp("Search terms."),
		"max_results": map[string]any{"type": "integer", "description": "Maximum results to return (default 5, maximum 10)."},
	}, "query"))

// All is the coder's full tool set, in declaration order. project_index is gone
// — outline (the structural map) and grep (the content locator) replace it.
var All = []Tool{ReadFile, WriteFile, EditFile, Study.AsTool(), OutlineTool, GrepTool, Bash, RemoveTool,
	MemoryWriteTool, MemoryReadTool, MemorySearchTool, MemoryForgetTool, RecallTool, WebSearchTool, FetchURLTool,
	ContextEvictTool, ContextMergeTool, ContextAdjustWatermarksTool, ScanLandscapeTool}

// Study is the one subagent profile today (the profile shape + runner live in
// study.go). Read-only: outline/grep/read_file only — no write/edit/bash/remove,
// no study (no recursion), no memory_write (writing notes is the coder's job, not
// a read-only researcher's). MaxTokens is mandatory (the runaway backstop); a
// profile that left it unset would reintroduce the 2026-06-28 north runaway.
var Study = Subagent{
	Name:   "study",
	Role:   "study",
	System: studySystem,
	Tools:  []Tool{OutlineTool, GrepTool, ReadFile},
	// MaxTokens is task-fit, not the model max. Keep it low enough that a
	// reasoning-model spiral reaches the clamp quickly and leaves wall-clock room
	// for runLoop's salvage re-ask, while still clearing the normal concise digest
	// path (~1–3k output in the live eval).
	Bounds:      agent.Bounds{MaxTokens: 8_192, MaxIter: 12, ReadBudgetBytes: 96_000},
	Declaration: StudyTool,
}

// init registers Study on the shared subagent registry so Execute's generic
// dispatch (see Lookup in Execute) resolves the "study" tool name back to this
// profile — the same path any future inheritor (reflect, dream) will use.
func init() {
	Register(Study)
}

// --- Dispatcher ---------------------------------------------------------

// Execute dispatches a tool call. ctx cancels long-running tools (bash, study)
// on interrupt; deps carries session config (model, endpoint, window) that some
// tools need — study does; the file tools ignore both. It is a function (not a
// method) because ToolCall now lives in internal/agent and methods cannot be
// added to a type from another package.
func Execute(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
	// A tool dispatched without a session (tests, non-interactive paths) runs
	// against the nil-safe headless defaults: the shell gate fails closed, study
	// is unavailable, delete is disabled. This preserves the old behavior of the
	// nil-*CortexSession receiver now that deps is an interface (a nil interface
	// would panic on the first method call).
	if deps == nil {
		deps = headlessDeps{}
	}

	// Check if tool is enabled via config
	if !deps.IsToolEnabled(tc.Function.Name) {
		return fmt.Sprintf("%s is disabled in .cortex/config.json", tc.Function.Name), nil
	}

	// Validate tool call (dynamic checks beyond config)
	if deps != nil {
		if ok, msg := deps.ValidateToolCall(tc); !ok {
			return msg, nil
		}
	}

	name := tc.Function.Name
	switch name {
	case FunctionReadFile:
		return readFile(tc, deps)
	case FunctionWriteFile:
		return writeFile(tc)
	case FunctionEditFile:
		return editFile(tc)
	case FunctionOutline:
		return outlineTool(tc)
	case FunctionGrep:
		return grep(ctx, tc)
	case FunctionBash:
		return bash(ctx, tc, deps)
	case FunctionRemove:
		return removePath(tc, deps)
	case FunctionMemoryWrite:
		return memoryWrite(tc, deps)
	case FunctionMemoryRead:
		return memoryRead(tc, deps)
	case FunctionMemorySearch:
		return memorySearch(tc, deps)
	case FunctionMemoryForget:
		return memoryForget(tc, deps)
	case FunctionRecall:
		return recall(ctx, tc, deps)
	case FunctionFetchURL:
		return fetchURL(ctx, tc)
	case FunctionWebSearch:
		return webSearch(ctx, tc)
	case FunctionContextEvict:
		return contextEvict(tc, deps)
	case FunctionContextMerge:
		return contextMerge(tc, deps)
	case FunctionContextAdjustWatermarks:
		return contextAdjustWatermarks(tc, deps)
	case FunctionScanLandscape:
		return scanLandscape(deps)
	}
	// Any registered subagent tool (study, and future inheritors reflect/dream)
	// shares this one dispatch path via the name→profile registry.
	if sa, ok := Lookup(name); ok {
		return runSubagent(ctx, tc, deps, sa)
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

// printToolAction prints an indented, iconned tool-action line under the
// current cortex turn, e.g. "  ▸ read_file(go.mod)". The tool name shows in
// green; its argument list is dimmed so the verb reads first.
func printToolAction(action string) {
	name, args := action, ""
	if i := strings.IndexByte(action, '('); i >= 0 {
		name, args = action[:i], action[i:]
	}
	line := Color(IconTool+" "+name, Green)
	if args != "" {
		line += Color(args, Gray)
	}
	fmt.Printf("  %s\n", line)
}

// --- outline ------------------------------------------------------------

// outlineDefaultBudget is the token budget the outline tool uses when the model
// omits one — a few thousand tokens of structure, enough to orient.
const outlineDefaultBudget = 4000

// outlineTool renders the structural map of a path (internal/outline). The path
// arg is optional (default "."); budget controls how much structure to expand.
func outlineTool(tc ToolCall) (string, error) {
	path := "."
	if p, _ := tc.StringArg("path"); strings.TrimSpace(p) != "" {
		path = p
	}
	budget := outlineDefaultBudget
	if n, ok := tc.IntArg("budget"); ok && n > 0 {
		budget = n
	}
	printToolAction(fmt.Sprintf("outline(%s)", path))
	return outline.Render(path, budget)
}

// --- subagent tools (study, and future inheritors reflect/dream) --------

// runSubagent is the shared entry point for any registered Subagent tool:
// seed the profile with the goal + an outline of the target, then run it on
// the shared engine via SubAgentRunner. An empty digest is reported explicitly
// so the caller never gets a silent "".
func runSubagent(ctx context.Context, tc ToolCall, deps ToolDeps, sa Subagent) (string, error) {
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	goal, _ := tc.StringArg("goal") // optional
	ol, err := deps.Outline(path, StudySeedBudget)
	if err != nil {
		return "", err
	}
	digest, err := deps.RunSubagent(ctx, sa, StudySeed(goal, path, ol))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(digest) == "" {
		return fmt.Sprintf("%s produced no digest for %s", sa.Name, path), nil
	}
	return digest, nil
}

// --- memory -------------------------------------------------------------
//
// Thin dispatchers: argument parsing + the action line, then delegate to the
// session-backed store via ToolDeps. The session formats the result (so the
// tools package needn't import the memory types).

func memoryWrite(tc ToolCall, deps ToolDeps) (string, error) {
	name, err := tc.StringArg("name")
	if err != nil {
		return "", err
	}
	content, err := tc.StringArg("content")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("memory_write(%s)", name))
	return deps.MemoryWrite(name, content)
}

func memoryRead(tc ToolCall, deps ToolDeps) (string, error) {
	name, err := tc.StringArg("name")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("memory_read(%s)", name))
	return deps.MemoryRead(name)
}

func memorySearch(tc ToolCall, deps ToolDeps) (string, error) {
	query, err := tc.StringArg("query")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("memory_search(%s)", query))
	return deps.MemorySearch(query)
}

func memoryForget(tc ToolCall, deps ToolDeps) (string, error) {
	name, err := tc.StringArg("name")
	if err != nil {
		return "", err
	}
	printToolAction(fmt.Sprintf("memory_forget(%s)", name))
	return deps.MemoryForget(name)
}

// recall resolves an outline citation to its verbatim messages. With a budget
// it returns a summarizer digest at roughly that many tokens instead — a
// compressed recall for when the gist is enough (this subsumed the separate
// context_summarize tool). The citation always survives the digest.
func recall(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
	citation, err := tc.StringArg("citation")
	if err != nil {
		return "", err
	}
	budget, _ := tc.IntArg("budget")
	if budget <= 0 {
		printToolAction(fmt.Sprintf("recall(%s)", citation))
		return deps.Recall(citation)
	}

	printToolAction(fmt.Sprintf("recall(%s, budget=%d)", citation, budget))
	raw, err := deps.Recall(citation)
	if err != nil {
		return "", err
	}
	// Already within budget: nothing to compress (this also passes through
	// Recall's friendly not-found messages untouched).
	if len(raw)/4 <= budget {
		return raw, nil
	}
	goal, _ := tc.StringArg("goal")
	if goal == "" {
		goal = "What are the key facts and decisions in this turn?"
	}
	digest, _, err := deps.SummarizeText(ctx, raw, goal, budget)
	if err != nil {
		return "", fmt.Errorf("summarize failed: %w", err)
	}
	if !strings.Contains(digest, citation) {
		digest += fmt.Sprintf("\n\n[%s]", citation)
	}
	return fmt.Sprintf("digest of %s (~%d-token budget; recall without budget for the raw messages):\n\n%s",
		citation, budget, digest), nil
}

// --- read_file ----------------------------------------------------------

// DefaultRangeLines is the window read_file returns when start is given without
// end — enough to see a declaration plus context without the model having to
// name an exact end. A range is bounded by construction, so it bypasses the
// whole-file size gate.
const DefaultRangeLines = 200

// MaxRangeLines caps a single ranged read so a model can't ask for lines
// 1–1000000 and pull a whole large file back through the gate it bypassed.
const MaxRangeLines = 800

func readFile(tc ToolCall, deps ToolDeps) (string, error) {
	path, err := tc.StringArg("path")
	if err != nil {
		return "", err
	}
	// Ranged read: exact 1-indexed lines, bypassing the size gate (a range is
	// bounded). This is the navigator's precise pull — project_index/study hands
	// back a line span, and read_file(path, start, end) reads exactly it.
	if start, ok := tc.IntArg("start"); ok && start > 0 {
		end, hasEnd := tc.IntArg("end")
		if !hasEnd || end < start {
			end = start + DefaultRangeLines - 1
		}
		return readRange(path, start, end)
	}
	// Curation budget: a whole-file read above CurationBudgetTokens is refused
	// and redirected to study, so the coder gets a CURATED digest rather than a
	// raw dump. Decoupled from the coder's window on purpose — sizing the trigger
	// to the window let a big-window model read everything raw, defeating
	// curation (2026-06-20). (~4 bytes/token.)
	if info, statErr := os.Stat(path); statErr == nil {
		if estTokens := int(info.Size()) / 4; estTokens > CurationBudgetTokens {
			// Read the map before the territory: a too-large file (any language —
			// outline.Render's regex/prose/positional tiers cover what go/ast
			// doesn't) hands back its structural skeleton so the model can orient
			// and target a region (read_file(start,end), study for curated
			// content, or bash sed for an exact range) instead of dead-ending.
			if skel := fileSkeleton(path); skel != "" {
				printToolAction(fmt.Sprintf("read_file(%s) → skeleton (~%dk tokens, too large)", path, estTokens/1000))
				return fmt.Sprintf("%s is ~%d tokens — too large to read whole. Its structural outline is below; "+
					"read_file(%q, start, end) an exact span, study(%q, goal) for curated content, or bash `sed -n 'A,Bp' %s` for a raw range.\n\n%s",
					path, estTokens, path, path, path, skel), nil
			}
			// outline.Render itself failed (e.g. an unreadable path) — the same
			// error readFile would hit trying to read it directly.
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

// readRange returns lines [start,end] (1-indexed, inclusive) of a file, capped
// at MaxRangeLines. The output carries a "@path:start-end" header so the model
// sees exactly which lines it got (and a truncation note when the request was
// clamped). Lines beyond EOF are silently dropped — asking past the end yields
// what exists, not an error.
func readRange(path string, start, end int) (string, error) {
	if end-start+1 > MaxRangeLines {
		end = start + MaxRangeLines - 1
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	// A file ending in "\n" splits to a trailing "" that isn't a real line; drop
	// it so line numbers match the file's (and an EOF clamp lands correctly).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if start > len(lines) {
		return "", fmt.Errorf("%s has %d lines; start %d is past the end", path, len(lines), start)
	}
	hi := end
	if hi > len(lines) {
		hi = len(lines)
	}
	printToolAction(fmt.Sprintf("read_file(%s:%d-%d)", path, start, hi))
	body := strings.Join(lines[start-1:hi], "\n")
	// Byte ceiling: the line clamp alone doesn't bound a span of VERY long lines
	// (minified JSON, journal JSONL — ~2.6 KB/line), which could return hundreds of
	// KB and blow up the context. Cap the bytes too, truncating at a line boundary
	// with a visible note so the model greps for what it needs instead.
	note := ""
	if len(body) > maxReadBytes {
		cut := maxReadBytes
		if nl := strings.LastIndexByte(body[:maxReadBytes], '\n'); nl > 0 {
			cut = nl
		}
		body = body[:cut]
		note = fmt.Sprintf("\n… [truncated at %d bytes — lines here are very long; grep for the specific text you need]", maxReadBytes)
	}
	return fmt.Sprintf("@%s:%d-%d\n%s%s", path, start, hi, body, note), nil
}

// maxReadBytes is the per-read byte ceiling — a span of very long lines can't
// return more than this regardless of its line count, so a single read can't
// explode the context. ~24 KB ≈ 6k tokens, ample for a real code span.
const maxReadBytes = 24000

// fileSkeleton returns the structural outline of any file (outline.Render:
// go/ast for Go, a declaration/heading regex for other languages, a prose
// paragraph split, or a positional line-window floor when nothing else
// applies) — the orientation a too-large read_file hands back in place of the
// content. "" only if outline.Render itself errors (unreadable path); the
// positional floor means a real file otherwise always yields something.
func fileSkeleton(path string) string {
	skel, err := outline.Render(path, 8000)
	if err != nil || strings.TrimSpace(skel) == "" {
		return ""
	}
	return skel
}

// --- write_file ---------------------------------------------------------

func writeFile(tc ToolCall) (string, error) {
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
func editFile(tc ToolCall) (string, error) {
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
func removePath(tc ToolCall, deps ToolDeps) (string, error) {
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

func bash(ctx context.Context, tc ToolCall, deps ToolDeps) (string, error) {
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

// studyShellOutput turns oversized bash output into curated context instead of
// a truncation: the full output spills to .cortex/shell/ and the free-text
// summarizer folds it into a digest (errors first), with the spill path so the
// model can study it again to dig deeper. Command output has no structural map,
// so this is the summarizer path, not the navigator/engine. Returns ok=false on
// any failure so the caller degrades to plain truncation — losing the digest is
// acceptable, losing the turn is not.
func studyShellOutput(ctx context.Context, deps ToolDeps, command string, out []byte) (string, bool) {
	spill, err := spillShellOutput(command, out)
	if err != nil {
		return "", false
	}
	printToolAction(fmt.Sprintf("output %d KB → summarize(%s)", len(out)/1024, spill))
	goal := fmt.Sprintf("This is the output of `%s`. What does it show? Surface errors, failures, and anomalies first.", command)
	digest, _, err := deps.Summarize(ctx, spill, goal, bashStudyWindow)
	if err != nil {
		return "", false
	}
	header := fmt.Sprintf("[%d bytes of output — summarized below; full output at %s — study(path, goal) to dig deeper]\n", len(out), spill)
	return header + digest, true
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
