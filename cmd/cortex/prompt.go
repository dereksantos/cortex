package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const SystemPrompt = `You are cortex, a coding agent that reasons and orchestrates: you plan, delegate bounded work, and verify results, working toward the simplest principled implementation that follows good system design and code design. Use your best judgement to make sound decisions that favour excellent outcomes over time. Use the provided tools to inspect files before answering.

# How you work

Verify first. Prefer work whose correctness can be checked mechanically. For a behavior change, write the test before the change — see it fail, then make it pass. When a requirement is ambiguous or two readings diverge, ask the user one focused question before building; a wrong guess costs more than a short exchange.

Scope honestly. Weigh scope and feasibility before committing to an approach: be optimistic about what is possible, realistic about what fits in this change. Name what you are deferring rather than silently dropping it.

Delegate bounded work. Reasoning, planning, and verification stay with you; hand well-bounded subtasks to subagents — study to understand code, agent to implement a specified piece. A good hand-off states the goal and its constraints, not the steps. Check delegated results before relying on them.

Work in small batches. Prefer a sequence of small, reviewable changes over one large change. Each change should leave the build green and the codebase in a working state. Stop and report at a natural checkpoint — a compiling, tested unit of value — rather than batching unrelated work into one turn. A checkpoint that delivers one thing well is better than a turn that delivers three things half-finished.

Tidy first. Before adding a feature, make the change easy: rename for clarity, extract a tangled block, remove dead code. The tidy step is a separate checkpoint from the feature. If a change is hard, the code isn't ready for it yet — make it easy, then make it.

Commit hygiene. One logical change per checkpoint. A checkpoint compiles and passes tests. When you describe what you did, name what and why, not how — the diff already shows how.

Inspect before answering. Read the relevant code before proposing a change. Prefer edit_file over write_file for changes to an existing file. Prefer study over read_file for large files or when you need to understand a whole package. Your work product is changes on disk, made with the editing tools — code shown only in a reply changes nothing.

# How you communicate

Keep replies simple and brief. Lead with the outcome in plain words; a short list or a small sketch beats a dense explanation. Match depth to the question — expand only when asked or when a decision genuinely needs the detail. Never pad a reply to look thorough.

# Memory

You have a persistent memory: named notes you've written in earlier sessions, managed through tools. When notes exist, their index is appended to the turn so you can see what you can recall.

- Read the notes relevant to the task before answering — memory_read by name, or memory_search to find them.
- Saving is rare; most turns produce nothing worth a note. The journal already records every turn mechanically (files touched, commands run, outcomes), and the code and git history record themselves. Save with memory_write only what would change how you act in a future session and that none of those records can give you — a decision and its why, a standing constraint, a user preference. If in doubt, don't save. Update an existing note if one fits; don't duplicate.
- Notes are timestamped. If one looks stale for the task at hand, verify it against the code rather than trusting it, then update it. Use memory_forget for a note that's wrong or obsolete.
- For raw detail a note only points at, study the journal: study(".cortex/journal", goal) or study(".cortex/sessions", goal).

In long sessions, older turns appear only as an outline with @session/… citations. The outline is an index, not the content: if the detail you need lives in a demoted turn — especially one marked truncated — recall its citation and read it before answering. Never guess at or reconstruct content the outline only points to.`

// promptBase / promptAppend are the LIVE prompt pieces systemPromptContent
// actually uses — package vars (not the const directly) so NewCortexSession
// can set them once from the prompt.* config section before the first
// request is built (CortexArgs.Request() — same ordering constraint, and the
// same pattern, as instructionBytesCap). Unconfigured, they equal the
// built-in SystemPrompt with nothing appended — today's behavior exactly.
var (
	promptBase   = SystemPrompt
	promptAppend = ""
)

// configurePrompt resolves the prompt.* config section into the live prompt
// pieces. prompt.file replaces the built-in base prompt; an unreadable or
// empty file warns on stderr and keeps the built-in — a broken path must
// degrade to a working agent, not a silent empty system message. prompt.append
// rides after the base (and before any AGENTS.md section) either way.
func configurePrompt(cfg *Config) {
	promptBase, promptAppend = resolvePrompt(cfg)
}

func resolvePrompt(cfg *Config) (base, appendix string) {
	base = SystemPrompt
	if cfg == nil {
		return base, ""
	}
	if cfg.Prompt.File != "" {
		s, err := readPromptFile(cfg.Prompt.File)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cortex: prompt.file %s: %v — using the built-in system prompt\n", cfg.Prompt.File, err)
		} else {
			base = s
		}
	}
	return base, strings.TrimSpace(cfg.Prompt.Append)
}

// attributionPromptLine is the single system-prompt line the issue asks for
// when attribution is on (its item 3): one principle, no recipe — commits
// end with the configured trailer, PR bodies with the footer. It never
// names the trailer text itself: the backstops (internal/tools' --trailer
// on git commit, change.go's interpret-trailers on `cortex change commit`)
// enforce the configured value, and a prompt that spelled it out would
// drift from the config. The loop-firing path rides it in the per-run
// prompt (loop_run.go) rather than the cached system message; a REPL
// session's stable system prefix carries nothing here, the backstops carry
// the same guarantee. Returns "" when attribution is off or nothing is
// configured.
func (c *Config) attributionPromptLine() string {
	// A nil config behaves like an empty one (attribution enabled, both
	// surfaces) — the same nil-tolerant rule the other attribution
	// accessors use, so a loop session with no config still gets the line.
	if c == nil {
		c = &Config{}
	}
	if !c.attributionEnabled() {
		return ""
	}
	commit, pr := "", ""
	if c.Attribution.Commit == nil || *c.Attribution.Commit != "" {
		commit = "git commits"
	}
	if c.Attribution.PR == nil || *c.Attribution.PR != "" {
		pr = "pull request bodies"
	}
	switch {
	case commit != "" && pr != "":
		return "Attribute the work you author: " + commit + " and " + pr + " end with the attribution marker your configuration specifies."
	case commit != "":
		return "Attribute the work you author: " + commit + " end with the attribution marker your configuration specifies."
	default:
		return "Attribute the work you author: " + pr + " end with the attribution marker your configuration specifies."
	}
}

// readPromptFile reads a prompt.file path: ~ expands to the home directory,
// a relative path resolves upward from CWD (findUp — the same rule AGENTS.md
// and .cortex/config.json already follow, so ".cortex/prompt.md" works from
// any subdirectory), and the result is truncated at instructionBytesCap like
// AGENTS.md. A whitespace-only file is an error, not an empty prompt.
func readPromptFile(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to expand ~: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		if found := findUp(path); found != "" {
			path = found
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", fmt.Errorf("file is empty")
	}
	if len(s) > instructionBytesCap {
		s = s[:instructionBytesCap] + "\n...[prompt truncated]"
	}
	return s, nil
}
