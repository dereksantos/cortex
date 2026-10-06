package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/projectcmd"
	"github.com/dereksantos/cortex/internal/redact"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

const studyFallbackWindow = 8192

var learnedWindows = map[string]int{}

var ctxSizeRe = regexp.MustCompile(`context size \((\d+) tokens\)`)

func parseCtxSize(s string) int {
	if m := ctxSizeRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// learnWindow records an observed context-overflow limit for the code
// model into the shared learnedWindows map (C2: extends the study-only
// calibration in studyWindow() to the coder path) and updates cs.Window as
// the session-local fallback so windowSize() is correct even if the model
// name later changes underneath it (e.g. /model). Callers pair this with a
// compaction (main.go's REPL loop, discord.go's boundSession) so the
// working set's baked-in watermarks — which only recompute at a session
// boundary — catch up to the learned value in the same breath it's learned.
func (cs *CortexSession) learnWindow(real int) {
	learnedWindows[cs.Request.Model] = real
	cs.Window = real
}

func (cs *CortexSession) studyWindow() int {
	if v := os.Getenv("CORTEX_LOOP_STUDY_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if w, ok := learnedWindows[cs.Study.Model]; ok {
		return w
	}
	if cs.Study.Window > 0 {
		return cs.Study.Window
	}
	return studyFallbackWindow
}

var toolSet = tools.All

type ToolCall = tools.ToolCall
type FunctionCall = tools.FunctionCall

var parseXMLToolCalls = tools.ParseXMLToolCalls
var parseToolCallsFromContent = tools.ToolCallsFromContent
var stripToolMarkup = tools.StripToolMarkup

// effortOffKwargs is pinned (not routed through cs.Study.TemplateKwargs) at
// the fast-role sub-LLM call sites below — docs/thinking-models.md's third
// open decision, resolved yes: the summarizer and shell-risk judge are
// formatting/classification asks, not the study role's own deliberate work,
// so they must never inherit the study binding's "on" default. This is the
// 37.8s-incident fix (pkg/llm/provider_factory.go's factory-routed calls ran
// a hybrid model with thinking left on and burned a 30s deadline on
// reasoning_content) applied at cmd/cortex's two analogous call sites.
var effortOffKwargs, _ = llm.Translate(llm.DialectTemplateKwargs, llm.Effort{Level: llm.EffortOff})

func (cs *CortexSession) newStudyProvider(maxTokens int) *llm.OpenAICompatClient {
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "study",
		BaseURL:            base,
		APIKey:             resolveKey(cs.Study),
		ChatTemplateKwargs: effortOffKwargs,
		// P1: previously hardcoded 10*time.Minute, which bypassed
		// CORTEX_COMPAT_TIMEOUT_SEC entirely. Now resolved the same way
		// every other transport timeout in this audit is: an explicit
		// models.study.request_timeout_sec wins, then the env var, then
		// this 10-minute default — unchanged for anyone touching neither.
		Timeout: cs.Study.timeout(10 * time.Minute),
	})
	p.SetModel(cs.Study.Model)
	p.SetTemperature(cs.Study.temperature(defaultTemperature))
	p.SetMaxTokens(maxTokens)
	return p
}

func (cs *CortexSession) reasoner() *llm.OpenAICompatClient {
	base := strings.TrimRight(cs.Study.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	p := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:               "shell-classifier",
		BaseURL:            base,
		APIKey:             resolveKey(cs.Study),
		ChatTemplateKwargs: effortOffKwargs,
		// P1: see newStudyProvider's identical comment above.
		Timeout: cs.Study.timeout(10 * time.Minute),
	})
	p.SetModel(cs.Study.Model)
	p.SetTemperature(cs.Study.temperature(defaultTemperature))
	return p
}

func (cs *CortexSession) GateShell(ctx context.Context, command string) (string, bool) {
	return cs.gateShell(ctx, command)
}

func (cs *CortexSession) AllowDelete() (string, bool) { return cs.deleteRoot, cs.allowDelete }

func (cs *CortexSession) Quiet() bool { return cs.quiet }

// Workdir implements tools.Workdirer: an explicit-root workspace (--project,
// serve, loop firings) anchors relative tool paths and the shell's working
// directory to ITS project. CWD-derived workspaces return "" so the REPL's
// CWD-relative tool behavior stays byte-identical.
func (cs *CortexSession) Workdir() string {
	if cs.workspace != nil && cs.workspace.Explicit {
		return cs.workspace.Root
	}
	return ""
}

// ProjectCommands implements tools.ProjectCommands: the resolved command set
// (discovered from the workspace's manifests, overridden by config and
// AGENTS.md) that the write_file/edit_file post-edit hook runs. It is the
// value cs.projectCommands, computed once at construction / project
// targeting — a session that never resolved a project (or one with no
// recognized manifest) returns the zero Commands, so the hook is a no-op.
func (cs *CortexSession) ProjectCommands() projectcmd.Commands {
	return cs.projectCommands
}

// WorkspaceTrusted implements tools.WorkspaceTrust (issue #129's trust
// gate — the ONLY gate for the post-edit hook): whether THIS workspace's
// root is on the operator's USER-level trust list. On an untrusted
// workspace the hook runs nothing. The config consulted is cs.Config —
// the MERGED config, whose Project.Trusted is the user-level list
// (mergeProject drops the project-level copy), so a repository's own
// .cortex/config.json can never put its own workspace on the trust list.
// A session with no resolved workspace or no config is untrusted: the
// safe default.
func (cs *CortexSession) WorkspaceTrusted() bool {
	if cs == nil || cs.workspace == nil || cs.Config == nil {
		return false
	}
	return cs.Config.WorkspaceTrusted(cs.workspace.Root)
}

// HookState implements tools.HookStateProvider: the session-scoped state
// of the post-edit hook (the "hook inactive on an untrusted workspace"
// note fires once per session — see tools.PostEditHookState). The state
// lives on the session, so "once per session" is exactly that: the REPL's
// session, a served session, or a loop firing, each with their own. A nil
// state (a session that implements the capability but never allocated
// state — every hand-built test session) is a no-op: the hook has no
// per-session slot to announce into, so the untrusted note is not
// surfaced.
func (cs *CortexSession) HookState() *tools.PostEditHookState {
	return cs.hookState
}

// citationRe parses the outline citation coordinate: @session/<id>#m<start>-<end>,
// a half-open message-index range into that session transcript.
var citationRe = regexp.MustCompile(`^@session/([A-Za-z0-9-]+)#m(\d+)-(\d+)$`)

const memUnavailable = "memory is unavailable in this session (no .cortex workspace)"

// scopeProject / scopeUser are the two memory tiers a scope arg can name
// (docs/cross-source-learning.md piece 1). scopeProject is also the default
// for write/forget (an unspecified scope never writes or deletes cross-
// project by accident); memory_read's no-scope path shadows project over
// user instead (see MemoryRead), and memory_search's no-scope path searches
// both (see MemorySearch) — each tool's own default is documented at its
// call site since the three differ by design, not by omission.
const (
	scopeProject = "project"
	scopeUser    = "user"
)

// normalizeScope maps a tool call's optional scope argument to one of the two
// tiers, defaulting unset/unrecognized input to scopeProject — the safe
// default for a write or delete (never touches the wrong tier silently).
func normalizeScope(scope string) string {
	if strings.ToLower(strings.TrimSpace(scope)) == scopeUser {
		return scopeUser
	}
	return scopeProject
}

// storeFor resolves the memory.Store + tier label for an explicit scope
// (scopeProject or scopeUser, via normalizeScope). Read and search have
// their own tier-spanning defaults for an UNSET scope and call the
// underlying stores directly instead of going through this helper.
func (cs *CortexSession) storeFor(scope string) (*memory.Store, string) {
	if normalizeScope(scope) == scopeUser {
		return cs.userMemory, scopeUser
	}
	return cs.memory, scopeProject
}

func (cs *CortexSession) MemoryWrite(name, content, scope string) (string, error) {
	store, tier := cs.storeFor(scope)
	if store == nil {
		return fmt.Sprintf("%s memory is unavailable in this session", tier), nil
	}
	// Issue #103: redact the note's content right at the write seam, before
	// it is persisted to the on-disk note, so a secret the agent or the
	// learn-loop captured never reaches .cortex/memory/*.md. MemoryWrite is
	// the single choke point for model- and learn-loop-driven writes (both
	// call into this method); memory_read of the note returns the same
	// redacted body the store holds. The per-turn redaction counter is folded
	// in with the transcript's (cs.redactions, reset per turn in turn.go) so a
	// caller's TurnResult.Redactions reflects every surface that persisted a
	// secret this turn — transcript, journal, and now memory.
	redacted, n := redact.Redact(content)
	cs.redactions += n
	saved, err := store.Write(name, redacted, time.Now())
	if err != nil {
		return "", err
	}
	cs.captures++
	return fmt.Sprintf("saved note %q (%s)", saved, tier), nil
}

// MemoryRead resolves a name against an explicit tier when scope is set; with
// scope unset it shadows project over user (docs/cross-source-learning.md:
// "project shadows user" — a project note of the same name wins silently, so
// a project can locally override a cross-project fact without deleting the
// promoted one). A miss on the checked tier(s) is a friendly observation, not
// an error, matching the pre-existing single-tier behavior.
func (cs *CortexSession) MemoryRead(name, scope string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(scope))
	if s == "" {
		if cs.memory == nil && cs.userMemory == nil {
			return memUnavailable, nil
		}
		if cs.memory != nil {
			body, err := cs.memory.Read(name)
			if err == nil {
				return body, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
		}
		if cs.userMemory != nil {
			body, err := cs.userMemory.Read(name)
			if err == nil {
				return body, nil
			}
			if !os.IsNotExist(err) {
				return "", err
			}
		}
		return fmt.Sprintf("no note named %q (check the memory index, or memory_search for it)", name), nil
	}

	store, tier := cs.storeFor(s)
	if store == nil {
		return fmt.Sprintf("%s memory is unavailable in this session", tier), nil
	}
	body, err := store.Read(name)
	if os.IsNotExist(err) {
		return fmt.Sprintf("no note named %q in %s memory (check the memory index, or memory_search for it)", name, tier), nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

// MemorySearch searches an explicit tier when scope is set; with scope unset
// it searches BOTH tiers and tags every hit ([project]/[user]) so a same-name
// collision that MemoryRead's shadowing would hide is still visible here
// (docs/cross-source-learning.md piece 1).
func (cs *CortexSession) MemorySearch(query, scope string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(scope))
	if s != "" {
		store, tier := cs.storeFor(s)
		if store == nil {
			return fmt.Sprintf("%s memory is unavailable in this session", tier), nil
		}
		hits, err := store.Search(query)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return fmt.Sprintf("no notes match %q", query), nil
		}
		return renderMemoryHits(hits, tier), nil
	}

	if cs.memory == nil && cs.userMemory == nil {
		return memUnavailable, nil
	}
	var lines []string
	if cs.memory != nil {
		hits, err := cs.memory.Search(query)
		if err != nil {
			return "", err
		}
		if rendered := renderMemoryHits(hits, scopeProject); rendered != "" {
			lines = append(lines, rendered)
		}
	}
	if cs.userMemory != nil {
		hits, err := cs.userMemory.Search(query)
		if err != nil {
			return "", err
		}
		if rendered := renderMemoryHits(hits, scopeUser); rendered != "" {
			lines = append(lines, rendered)
		}
	}
	if len(lines) == 0 {
		return fmt.Sprintf("no notes match %q", query), nil
	}
	return strings.Join(lines, "\n"), nil
}

func (cs *CortexSession) MemoryForget(name, scope string) (string, error) {
	store, tier := cs.storeFor(scope)
	if store == nil {
		return fmt.Sprintf("%s memory is unavailable in this session", tier), nil
	}
	removed, err := store.Forget(name)
	if err != nil {
		return "", err
	}
	if !removed {
		return fmt.Sprintf("no note named %q to forget in %s memory", name, tier), nil
	}
	return fmt.Sprintf("forgot note %q (%s)", name, tier), nil
}

// memoryIndexCap is the historical (project-tier) memory-index truncation
// default. Config-overridable via limits.memory_index_cap_chars — see
// Config.memoryIndexCapChars.
const memoryIndexCap = 4000

// userMemoryIndexCap is the user-tier memory index's truncation default —
// independently of memoryIndexCap, and deliberately smaller (see
// LimitsConfig.UserMemoryIndexCapChars's doc comment). Config-overridable via
// limits.user_memory_index_cap_chars — see Config.userMemoryIndexCapChars.
const userMemoryIndexCap = 1500

// workspaceRootForNote resolves the workspace root the turn-start workspace
// note states (issue #142): the session's resolved Workspace when set, else
// the current working directory. "" only when neither is resolvable. Shared
// by workspaceNote (the injection) and workspaceLegendDetail (/context's
// legend row) so the wire and the legend can never disagree.
func (cs *CortexSession) workspaceRootForNote() string {
	if cs.workspace != nil && cs.workspace.Root != "" {
		return cs.workspace.Root
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// workspaceNote renders the turn-start workspace injection (issue #142):
// one line naming the absolute workspace root so the model never falls back
// on remembered layouts from other environments (/testbed, /go/src/…, cd
// /Users/…) and never has to guess where it stands. It is injected for
// EVERY turn (not gated on notes or outline, like the memory index) because
// a fresh session's first tool calls are exactly where path guessing bites.
// "" only when no workspace is resolvable at all (workspaceRootForNote's ""
// case) — there is no root to state, and the CWD is what every tool
// resolves relative to anyway.
func (cs *CortexSession) workspaceNote() string {
	root := cs.workspaceRootForNote()
	if root == "" {
		return ""
	}
	return fmt.Sprintf("Workspace: %s is this session's workspace root (absolute path). All tool paths are relative to it — never guess other absolute paths (e.g. /testbed, /go/src/...) or cd elsewhere; use outline or grep to find what you need.", root)
}

// memoryIndexNote renders the turn-start memory injection: the user tier
// FIRST, then the project tier (docs/cross-source-learning.md piece 1) — a
// cross-project fact is the more load-bearing one to keep visible, so it
// leads. Each tier is capped INDEPENDENTLY (project: memoryIndexCap /
// limits.memory_index_cap_chars; user: userMemoryIndexCap /
// limits.user_memory_index_cap_chars) and clearly labeled so the model can
// tell which tier a note belongs to. Either section is omitted when that
// tier has no notes (or no store); the whole note is "" when both are empty,
// preserving the pre-existing "no injection" behavior for a project with no
// memory at all.
func (cs *CortexSession) memoryIndexNote() string {
	var sections []string
	if cs.userMemory != nil {
		if idx, err := cs.userMemory.Index(); err == nil && strings.TrimSpace(idx) != "" {
			idx = capIndex(idx, cs.Config.userMemoryIndexCapChars())
			sections = append(sections, "## User memory (every project on this machine)\n\n"+idx)
		}
	}
	if cs.memory != nil {
		if idx, err := cs.memory.Index(); err == nil && strings.TrimSpace(idx) != "" {
			idx = capIndex(idx, cs.Config.memoryIndexCapChars())
			sections = append(sections, "## Project memory (this codebase)\n\n"+idx)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return "These are notes you saved in earlier sessions. Read the relevant ones with " +
		"memory_read before answering.\n\n" + strings.Join(sections, "\n\n")
}

// capIndex truncates a rendered index to cap chars with a visible "truncated"
// note pointing at memory_search — shared by both tiers' independent caps.
func capIndex(idx string, cap int) string {
	if len(idx) > cap {
		return idx[:cap] + "\n… (index truncated; memory_search to find the rest)"
	}
	return idx
}

// renderMemoryHits renders search hits with a leading tier tag ([project] /
// [user]) on each line, so a cross-tier search result is unambiguous even
// though memory_read's shadowing hides the same collision. "" for no hits.
func renderMemoryHits(hits []memory.NoteMeta, tier string) string {
	var b strings.Builder
	for _, m := range hits {
		when := ""
		if !m.Updated.IsZero() {
			when = " (updated " + m.Updated.UTC().Format("2006-01-02") + ")"
		}
		fmt.Fprintf(&b, "- [%s] %s — %s%s\n", tier, m.Name, m.Hook, when)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Recall resolves an outline citation to the verbatim transcript messages it
// stands for — the recovery path that makes demotion lossless
// (docs/context-architecture.md). Deterministic: no model call.
func (cs *CortexSession) Recall(citation string) (string, error) {
	// Outline renders citations in brackets (e.g., "[@session/...]"), so strip them.
	trimmed := strings.Trim(strings.TrimSpace(citation), "[]")
	m := citationRe.FindStringSubmatch(trimmed)
	if m == nil {
		return fmt.Sprintf("unrecognized citation %q (expected @session/<id>#m<start>-<end>, as shown in the session outline)", citation), nil
	}

	id := m[1]
	start, err := strconv.Atoi(m[2])
	if err != nil {
		return "", fmt.Errorf("recall %s: invalid start index: %w", citation, err)
	}
	end, err := strconv.Atoi(m[3])
	if err != nil {
		return "", fmt.Errorf("recall %s: invalid end index: %w", citation, err)
	}

	msgs, _, err := loadTranscript(filepath.Join(cs.SessionsDir(), id+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("recall %s: %w", citation, err)
	}

	if start < 0 || end > len(msgs) || start >= end {
		return fmt.Sprintf("citation %s is out of range for that transcript (%d messages)", citation, len(msgs)), nil
	}

	var b strings.Builder
	for i, msg := range msgs[start:end] {
		abs := start + i
		b.WriteString(msg.Role)
		b.WriteString("\n")
		b.WriteString(msg.Content)
		if strings.HasPrefix(msg.Content, tools.ImageObservationMarker()) {
			// An image result demoted as its short marker text (#217): the
			// bytes live on disk beside the transcript (the dispatcher's
			// side-car, one file per message index) — recall names the file
			// so the model can read it again (or hand it to a tool) instead
			// of getting a marker with no image behind it.
			if p, ok := imagePathForMsg(cs.SessionsDir(), id, abs); ok {
				b.WriteString(" (image bytes on disk: " + p + ")")
			} else {
				b.WriteString(" (image bytes no longer on disk)")
			}
		}
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				b.WriteString("\n  ▸ ")
				b.WriteString(call.ActivityLabel())
			}
		}
		// A resumed in-memory message can still hold wire Parts (a turn
		// demoted in this very session): name the actual part count and the
		// byte size so recall of a not-yet-resumed image says what was
		// there, not just that a marker line existed (#217).
		if imgs := llmImageParts(msg.Parts); len(imgs) > 0 {
			imgBytes := 0
			for _, p := range imgs {
				imgBytes += imageDataURIRawBytes(p.ImageURL)
			}
			fmt.Fprintf(&b, "\n  ▸ %d image part(s) attached (%d bytes)", len(imgs), imgBytes)
		}
		b.WriteString("\n\n")
	}

	// Gate at the curation budget, scaled down to the tail's low (drain)
	// watermark on small windows: a recall bigger than the tail's drain
	// target would flood the hydrated tail and immediately re-demote. Uses
	// the resolved drain watermark (context.tail_drain_fraction, default
	// W/3 — docs/configuration.md, cs.Config.tailDrainWatermark) rather than
	// windowSize()/3 directly, so this gate always tracks whatever
	// newWorkingSet actually built the session's working set with.
	gate := cs.Config.toolLimits().CurationBudgetTokens
	if w := cs.Config.tailDrainWatermark(cs.windowSize()); w < gate {
		gate = w
	}
	rendered := b.String()
	if len(rendered)/4 > gate {
		return fmt.Sprintf("recall of %s is ~%d tokens — over the %d-token recall budget. Study the transcript instead: study(%s, <your question>)", citation, len(rendered)/4, gate, filepath.Join(cs.SessionsDir(), id+".jsonl")), nil
	}

	return rendered, nil
}

// shellApproval is one session-scoped bash approval (issue #107): the kind
// chosen at the risky-command prompt ("exact" or "prefix") plus the stored
// pattern. The kind rides with the pattern on purpose: an EXACT approval
// whose command ends in "*" (e.g. "rm -f build/*") must stay a byte-for-byte
// exact match, and the matcher (shellApprovalMatches) keys on the kind,
// never on the pattern string's shape.
type shellApproval struct {
	Kind    string // "exact" or "prefix"
	Pattern string
}

// ApproveShell records a session-scoped bash approval (issue #107) — the
// user chose "always this session" at the risky-command prompt — and
// journals the event so a review of the session shows what was auto-allowed.
// kind is "exact" or "prefix" and rides with the pattern (shellApproval),
// so the matcher never infers prefix-ness from the pattern string: an
// EXACT approval of a command that ends in "*" (e.g. "rm -f build/*")
// compares byte for byte and never widens into a word prefix. For "exact",
// pattern is the exact command as approved; for "prefix", it is the derived
// word prefix — commandPrefixPattern(command), the program plus one
// subcommand + "*", e.g. "make test*" for "make test ./pkg/..." — shown to
// the user in the prompt before they choose p, so what is approved is what
// is stored. command is the exact command that earned the approval; reason
// is the classifier's reason for it.
func (cs *CortexSession) ApproveShell(kind, pattern, command, reason string) {
	if cs == nil || kind == "" || pattern == "" {
		return
	}
	cs.shellApprovals = append(cs.shellApprovals, shellApproval{Kind: kind, Pattern: pattern})
	cs.journalShellApproval(kind, pattern, command, reason)
}

// commandPrefixPattern derives the stored prefix pattern for a command the
// user approved "always this session" by prefix (issue #107): the program
// plus one subcommand + "*" — "make test*" for "make test ./pkg/..." (the
// issue's example), "git*" for bare "git". The single-token form is a bare
// "prog*" (not "prog *") so it matches the command itself as well as the
// command with further arguments — prefixMatches' word boundary still keeps
// "git*" from matching "github" (a longer word glued onto the prefix). An
// empty command yields "*", whose prefix is empty: prefixMatches treats an
// empty prefix as no-match, so a bare "*" approval auto-runs nothing.
func commandPrefixPattern(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "*"
	}
	if len(fields) == 1 {
		return fields[0] + "*"
	}
	return fields[0] + " " + fields[1] + "*"
}

// journalShellApproval appends one shell.approval event to the project
// journal's "shell" class dir (issue #107's "approvals are journaled"
// acceptance item). Mirrors appendModelSubstitution's write path —
// journal.NewWriter with FsyncPerBatch, best-effort: a journal failure
// never affects the approval itself, only the record of it. A session with
// no workspace (cs.workspace == nil — hand-built test sessions) records
// the approval in memory only: there is no ContextDir to journal to, and
// the CWD-implicit fallback would write into whatever directory the test
// happens to run from.
func (cs *CortexSession) journalShellApproval(kind, pattern, command, reason string) {
	if cs.workspace == nil {
		return
	}
	dir := filepath.Join(cs.workspace.ContextDir(), "journal", "shell")
	entry, err := journal.NewShellApprovalEntry(journal.ShellApprovalPayload{
		Kind:    kind,
		Pattern: pattern,
		Command: command,
		Reason:  reason,
		Turn:    cs.turns,
	})
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		return
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		return
	}
}

// shellApprovalPatterns returns the session's current approval records
// (issue #107): each carries the KIND chosen at the prompt alongside the
// pattern, so matching keys on the kind, never on the pattern string. Empty
// when none were chosen.
func (cs *CortexSession) shellApprovalPatterns() []shellApproval {
	if cs == nil {
		return nil
	}
	return cs.shellApprovals
}

// shellApprovalMatches reports whether command matches any of the session's
// approval records (issue #107). An EXACT record must equal the command
// byte for byte — even when it ends in "*" (e.g. "rm -f build/*"): the
// kind, not the pattern's shape, decides, so an exact approval never
// re-interprets as a prefix. A PREFIX record is a WORD prefix — it matches
// the prefix itself or the prefix followed by whitespace, and NEVER a
// longer word glued onto the prefix ("make test*" does not match "make
// testX") or a command that chains, pipes, redirects, or substitutes after
// it ("make test; rm -rf x", "make test | cat", "make test$(evil)"): the
// remainder after the prefix must start with whitespace and contain no
// shell control character (; & | ` $ ( < >), newline, or quote. A bare
// HasPrefix is not safe enough — approving "make test" must not auto-run
// "make test && git push --force".
func shellApprovalMatches(records []shellApproval, command string) bool {
	for _, r := range records {
		if r.Kind == "prefix" {
			if strings.HasSuffix(r.Pattern, "*") && prefixMatches(r.Pattern[:len(r.Pattern)-1], command) {
				return true
			}
			continue
		}
		if r.Pattern == command {
			return true
		}
	}
	return false
}

// prefixMatches reports whether command matches the word prefix: command
// starts with prefix and the remainder is empty, starts with whitespace, and
// carries no shell control or substitution syntax (see
// shellApprovalMatches). An empty prefix matches nothing — a bare "*"
// approval would auto-run any Risky command, so it is not a match. The
// "remainder is empty" arm is what makes a single-token prefix ("git*")
// match the bare command it was granted for.
func prefixMatches(prefix, command string) bool {
	if prefix == "" || !strings.HasPrefix(command, prefix) {
		return false
	}
	rest := command[len(prefix):]
	if rest == "" {
		return true
	}
	if !unicode.IsSpace(rune(rest[0])) {
		return false // glued onto the prefix ("make testX") — not a match
	}
	return !containsShellControl(rest)
}

// containsShellControl reports whether s contains shell control or
// substitution syntax that would let a prefix approval silently widen into a
// chained command: ; & | ` $( < >, a newline, or any quote.
func containsShellControl(s string) bool {
	return strings.ContainsAny(s, ";&|`<>\"'\n") || strings.Contains(s, "$(")
}

func (cs *CortexSession) gateShell(ctx context.Context, command string) (string, bool) {
	// Per-turn same-action gate (issue #169): if this command's effect
	// class was already Blocked in the current turn, refuse it before
	// classification. This is the mechanical backstop to the reworded
	// blocked message — a model that re-issues a same-effect variant
	// (e.g. `git commit-tree` after a blocked `git commit`, or
	// `--no-verify` after a blocked `git commit`) is refused without
	// reaching the risk gate, and the refusal names the class so it
	// knows what is barred.
	if cls := shellrisk.EffectClass(command); cls != "" && cs.sameActionBlockedInTurn(cls) {
		return shellrisk.SameActionBlockedMessage(cls), false
	}
	var fn shellrisk.ClassifyFn
	if cs != nil {
		fn = cs.classifyShell
		if fn == nil {
			fn = cs.riskJudge()
		}
	}
	// Issue #102: the taint's source list rides into the classifier as the
	// untrustedContent note, so the intent judge can never wave a command
	// through on a fetched page's say-so — it sees that the task context and
	// the request the command serves may have been steered by web content.
	// (The judge's own teeth are advisory; the mechanical ones — the
	// re-examination below and the git-push floor — are the harness's.)
	note := cs.untrustedClassifierNote()
	v := shellrisk.Classify(ctx, command, note, fn)
	// Issue #102 (taint): once untrusted web content entered the turn, a
	// gray-zone verdict held only by the judge's judgement no longer waves a
	// command through — that judge may have been shaped by the injected
	// content itself (its task context is the turn intent a poisoned page
	// could have steered; its own verdict, in the worst case, is text the
	// page dictated upstream). So mechanically: on a tainted turn every
	// "classified" Safe verdict is RAISED to Risky, keeping the judge's own
	// reason and naming the taint source. Nothing is re-asked — a second call
	// to the same judge with the same input answers the same way, so it would
	// only double the classifier's latency and cost while changing nothing.
	// Safe-PATH verdicts (an allowlisted `ls`, `git status`: a different tier,
	// not "classified") are untouched, and deny-floor Blocked is never
	// re-consulted — its refusal stands below. Interactive sessions then
	// prompt and headless ones block with the taint message.
	//   `tainted` is sampled once and shared by the floor above and the
	// approval wording below (the gate is single-goroutine within the turn's
	// tool batch; naming the fact once keeps the classify-time and gate-time
	// halves of the rule reading as one decision).
	tainted := cs.untrustedContentActive()
	// The taint-only git-push floor (issue #102): after untrusted web
	// content, a push publishes whatever the tainted turn produced — a
	// page that steered the turn must not get an outbound publish waved
	// through by the judge. Any Safe verdict on a git-push command
	// (safe-path or gray-zone alike) is RAISED to Risky here: never
	// Blocked, so the push may still run on a human's explicit approval,
	// but no push is ever auto-approved after untrusted content. A
	// classifier-flagged Risky push needs no raising; deny-floor Blocked
	// stands untouched below. Placed before the re-examination so the
	// floor's reason wins over the judge's for the prompt line.
	if tainted && v.Level == shellrisk.Safe && shellrisk.IsGitPush(command) {
		v = shellrisk.Verdict{Level: shellrisk.Risky, Reason: shellrisk.GitPushTaintReason, Tier: "taint-floor"}
	}
	if tainted && v.Tier == "classified" && v.Level == shellrisk.Safe {
		v = shellrisk.Verdict{Level: shellrisk.Risky, Reason: v.Reason + "; " + cs.taintSourceNote(), Tier: "classified-tainted"}
	}
	switch v.Level {
	case shellrisk.Safe:
		return "", true
	case shellrisk.Blocked:
		cs.recordSameActionBlock(command)
		return shellrisk.RefusedMessage(v.Reason), false
	default:
		// The single shared source for the reworded blocked message
		// (internal/shellrisk.BlockedMessage, issue #169): a Risky command with
		// no interactive approver, a Risky command inside a subagent, and a
		// Risky command whose approver timed out all read identically to the
		// model. Under a taint (issue #102) the no-approver refusal is the
		// taint-specific shared message instead — the headless Blocked stance
		// is unchanged, the wording says why the bar is raised this turn.
		blocked := shellrisk.BlockedMessage(v.Reason)
		reason := v.Reason
		if tainted {
			blocked = shellrisk.TaintBlockedMessage(strings.Join(cs.taintSources(), ", "))
			reason = v.Reason + "; " + cs.taintSourceNote()
			// Telemetry (issue #102): the raised bar engaged — a Risky
			// command reached the gate on a tainted turn. One receipt per
			// turn regardless of how many commands or which approval path
			// (prompt, decline, headless block) answers it; the gate is
			// recorded BEFORE the decision so a decline counts too.
			cs.recordRiskyGateUnderTaint()
		}
		// A subagent (depth >= 1) has no human operator mid-loop — Risky is
		// treated as Blocked, same as a headless session. Only the coder's own
		// top-level bash call (depth 0) gets an interactive approval path —
		// and this check stands BEFORE the session-approval match below, so a
		// subagent never auto-runs on a stored approval either.
		if subagentDepth(ctx) != 0 || cs == nil {
			cs.recordSameActionBlock(command)
			return blocked, false
		}
		// Session-scoped approvals (issue #107): a Risky command the user
		// already chose "always this session" for — exact (a, byte for
		// byte, even when the command ends in "*") or by prefix (p, the
		// derived word prefix shown in the prompt) — runs without
		// re-prompting. Checked HERE, after the Blocked arm of the switch,
		// on purpose: a Blocked command takes the arm above and never
		// reaches this line, so no approval can ever override a Blocked
		// verdict (Blocked stays Blocked).
		// Not on a tainted turn (issue #102): untrusted content entered this
		// turn, so every Risky command needs an explicit answer for the rest
		// of it — a stored session approval would let injected text ride an
		// earlier "always" (e.g. a `git push` prefix) past the raised bar.
		if !tainted && shellApprovalMatches(cs.shellApprovalPatterns(), command) {
			return "", true
		}
		if !cs.quiet && cs.confirmRisky != nil {
			// The question carries the classifier's reason on its own line
			// above the command (issue #107's "show why it was flagged")
			// and ends with the choices: y once, n, a always this session
			// (this exact command), p always this session (this command's
			// prefix — the derived pattern, shown, not just named: what the
			// user sees is what ApproveShell stores). The body lines scroll
			// away; the ask line stays on the status row. Non-TTY /
			// CORTEX_LOOP_RENDER=0 sessions take the confirmRisky hook's
			// ReadLine fallback with the SAME question — plain text, the
			// pre-change path unchanged.
			prefix := commandPrefixPattern(command)
			q := fmt.Sprintf("\nrisky: %s\n    %s\n  run it? [y once | a this command | p always %q | n] ", reason, command, prefix)
			choice := cs.confirmRisky(q)
			switch choice {
			case lineedit.ConfirmAlwaysExact:
				cs.ApproveShell("exact", command, command, v.Reason)
				return "", true
			case lineedit.ConfirmAlwaysPrefix:
				cs.ApproveShell("prefix", prefix, command, v.Reason)
				return "", true
			case lineedit.ConfirmYes:
				return "", true
			default:
				cs.recordSameActionBlock(command)
				return shellrisk.DeclinedMessage(), false
			}
		}
		// approveRisky is Discord's non-terminal-but-human-present approval
		// path (docs/cortex-web.md Phase 7) — checked independently of
		// cs.quiet so a quiet served/headless session stays exactly as
		// blocked as it is today unless it explicitly wires an approver.
		if cs.approveRisky != nil {
			approved, timedOut := cs.approveRisky(ctx, reason, command)
			if approved {
				return "", true
			}
			if timedOut {
				cs.recordSameActionBlock(command)
				return blocked, false
			}
			cs.recordSameActionBlock(command)
			return shellrisk.DeclinedMessage(), false
		}
		cs.recordSameActionBlock(command)
		return blocked, false
	}
}

// riskJudge resolves the tier-3 classifier exactly as gateShell builds it
// inline (the injected classifyShell seam, else the provider-backed one over
// the reasoner) — factored out so the tainted re-check in gateShell uses the
// SAME judge rather than duplicating its wiring.
func (cs *CortexSession) riskJudge() shellrisk.ClassifyFn {
	if cs.classifyShell != nil {
		return cs.classifyShell
	}
	return shellrisk.ProviderClassifierWithLimit(cs.reasoner(), cs.turnIntent, cs.Config.maxTaskContextChars())
}

// untrustedClassifierNote is the classifier-facing taint note for the
// current turn (issue #102): shellrisk.TaintNote over this turn's sources,
// "" when the turn is clean. Threaded through every classifier call so
// the intent judge can never wave a command through on a fetched page's
// say-so — it sees that the task context itself may have been steered.
func (cs *CortexSession) untrustedClassifierNote() string {
	if !cs.untrustedContentActive() {
		return ""
	}
	return shellrisk.TaintNote(cs.taintSources())
}

// sameActionBlockedInTurn reports whether effectClass was already Blocked
// in the current turn (issue #169). The check spans the command's WHOLE
// barred group (shellrisk.EffectClasses): a blocked `git commit` bars
// `git commit --no-verify` too — the hook-disabling variant is a same-
// effect spelling of the same route-around — so the two git classes act as
// one group for the ledger. Outside a turn (cs.turnNo == 0) the ledger is
// inert: the gate still runs, but nothing is refused on the same-action
// rule.
func (cs *CortexSession) sameActionBlockedInTurn(effectClass string) bool {
	if cs == nil || cs.turnNo == 0 {
		return false
	}
	for _, c := range shellrisk.EffectClasses(effectClass) {
		if cs.sameActionBlocked[c] {
			return true
		}
	}
	return false
}

// recordSameActionBlock records command's effect class in the per-turn
// same-action ledger (issue #169), so a later same-effect command is
// refused before classification. The record spans the command's WHOLE
// barred group (shellrisk.EffectClasses) — a blocked `git commit` records
// BOTH git-history-write and hook-disabling, so the issue's commit →
// --no-verify / -c core.hooksPath=… sequence is refused by the ledger, not
// re-classified. Commands with no effect class (the common case — most
// commands are not in a tracked class) are no-ops. Outside a turn
// (cs.turnNo == 0) the record is dropped: there is no current turn to
// attach it to, and a stale record from a prior turn must not leak into the
// next one.
func (cs *CortexSession) recordSameActionBlock(command string) {
	if cs == nil || cs.turnNo == 0 {
		return
	}
	classes := shellrisk.EffectClasses(shellrisk.EffectClass(command))
	if len(classes) == 0 {
		return
	}
	if cs.sameActionBlocked == nil {
		cs.sameActionBlocked = make(map[string]bool)
	}
	for _, cls := range classes {
		cs.sameActionBlocked[cls] = true
	}
}
