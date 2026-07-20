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

	"github.com/dereksantos/cortex/internal/memory"
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
	saved, err := store.Write(name, content, time.Now())
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
	for _, msg := range msgs[start:end] {
		b.WriteString(msg.Role)
		b.WriteString("\n")
		b.WriteString(msg.Content)
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				b.WriteString("\n  ▸ ")
				b.WriteString(call.ActivityLabel())
			}
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

func (cs *CortexSession) gateShell(ctx context.Context, command string) (string, bool) {
	var fn shellrisk.ClassifyFn
	if cs != nil {
		fn = cs.classifyShell
		if fn == nil {
			fn = func(ctx context.Context, command string) (shellrisk.Level, string, error) {
				return shellrisk.ProviderClassifierWithLimit(cs.reasoner(), cs.turnIntent, cs.Config.maxTaskContextChars())(ctx, command)
			}
		}
	}
	v := shellrisk.Classify(ctx, command, fn)
	switch v.Level {
	case shellrisk.Safe:
		return "", true
	case shellrisk.Blocked:
		return fmt.Sprintf("refused by the safety gate (%s). This command will not run; choose a safer approach.", v.Reason), false
	default:
		blocked := fmt.Sprintf("blocked (risk: %s). No interactive approval is available in this session — re-issue a safer command, or ask the user to run it.", v.Reason)
		// A subagent (depth >= 1) has no human operator mid-loop — Risky is
		// treated as Blocked, same as a headless session. Only the coder's own
		// top-level bash call (depth 0) gets an interactive approval path.
		if subagentDepth(ctx) != 0 || cs == nil {
			return blocked, false
		}
		if !cs.quiet && cs.confirmRisky != nil {
			q := fmt.Sprintf("\nrisky: %s\n    %s\n  run it? [y/N] ", v.Reason, command)
			if cs.confirmRisky(q) {
				return "", true
			}
			return "declined by the user; not run. Ask before retrying, or use a safer command.", false
		}
		// approveRisky is Discord's non-terminal-but-human-present approval
		// path (docs/cortex-web.md Phase 7) — checked independently of
		// cs.quiet so a quiet served/headless session stays exactly as
		// blocked as it is today unless it explicitly wires an approver.
		if cs.approveRisky != nil {
			approved, timedOut := cs.approveRisky(ctx, v.Reason, command)
			if approved {
				return "", true
			}
			if timedOut {
				return blocked, false
			}
			return "declined by the user; not run. Ask before retrying, or use a safer command.", false
		}
		return blocked, false
	}
}
