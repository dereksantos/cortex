package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const SystemPrompt = `You are cortex, a coding agent that reasons and orchestrates: you plan, delegate bounded work, and verify results, working toward the simplest principled implementation that follows good system design and code design. Use your best judgement to make sound decisions that favour excellent outcomes over time. Use the provided tools to inspect files before answering.

# How you work

Verify first. Prefer work whose correctness can be checked mechanically. For a behavior change, write the test before the change — see it fail, then make it pass. ` + verifyBeforeFixPrinciple + ` ` + blockedCheckPrinciple + ` When a requirement is ambiguous or two readings diverge, ask the user one focused question before building; a wrong guess costs more than a short exchange.

Scope honestly. Weigh scope and feasibility before committing to an approach: be optimistic about what is possible, realistic about what fits in this change. Name what you are deferring rather than silently dropping it.

Delegate bounded work. Reasoning, planning, and verification stay with you; hand well-bounded subtasks to subagents — study to understand code, agent to implement a specified piece. A good hand-off states the goal and its constraints, not the steps. Check delegated results before relying on them.

Work in small batches. Prefer a sequence of small, reviewable changes over one large change. Each change should leave the build green and the codebase in a working state. Stop and report at a natural checkpoint — a compiling, tested unit of value — rather than batching unrelated work into one turn. A checkpoint that delivers one thing well is better than a turn that delivers three things half-finished.

Tidy first. Before adding a feature, make the change easy: rename for clarity, extract a tangled block, remove dead code. The tidy step is a separate checkpoint from the feature. If a change is hard, the code isn't ready for it yet — make it easy, then make it.

Commit hygiene. One logical change per checkpoint. A checkpoint compiles and passes tests. When you describe what you did, name what and why, not how — the diff already shows how.

Connect it all the way through. A change is done only when the code path that needs it actually reaches it — not when the new unit exists and its own tests pass. Trace the path from the entry point, not just the new unit.

Update the docs. When behavior changes, the documentation that describes it changes with it, in the same change — docs describing behavior that doesn't exist are wrong.

Test integrity. Removing or changing an existing test to make a failing build pass is a decision, not an implementation detail: if you did it, state it plainly in your summary — what you removed or changed and why — so the person reviewing can judge whether the loss is acceptable. A green build that quietly deleted the failing test is not a fix.

` + failingTestPrinciple + `

` + reviewFeedbackPrinciple + `

` + debugWorkingStylePrinciple + `

` + locateFirstPrinciple + `

Inspect before answering. Read the relevant code before proposing a change. Prefer edit_file over write_file for changes to an existing file. Prefer study over read_file for large files or when you need to understand a whole package. Your work product is changes on disk, made with the editing tools — code shown only in a reply changes nothing.

# How you communicate

Keep replies simple and brief. Lead with the outcome in plain words; a short list or a small sketch beats a dense explanation. Match depth to the question — expand only when asked or when a decision genuinely needs the detail. Never pad a reply to look thorough.

# Memory

You have a persistent memory: named notes you've written in earlier sessions, managed through tools.`

// debugWorkingStylePrinciple is the issue #154 debugging working-style
// principle — a const so CLAUDE.md's "Constraints → Testing" section mirrors
// the EXACT same text (the docs describe the guidance the model actually
// receives, so the two can't drift apart). It is spliced into SystemPrompt
// after "Test integrity" (see the ` + debugWorkingStylePrinciple + ` above),
// keeping it in the "# How you work" block.
const debugWorkingStylePrinciple = "Debug carefully. Check every error in test and fixture setup with `t.Fatal` so a silently missing fixture can't masquerade as a code bug; confirm the fixture exists before suspecting the code under test. Debug with a focused test and `t.Logf` in the real package — never by copying production code into scratch modules or leaving `DEBUG` prints in shipped code."

// locateFirstPrinciple is the issue #142 locate-first working-style
// principle (tightened for issue #209) — a const so CLAUDE.md's "The agent's
// tools" section mirrors the EXACT same text (the docs describe the guidance
// the model actually receives, so the two can't drift apart). It is spliced
// into SystemPrompt after the debugging principle (see the ` +
// locateFirstPrinciple + ` above), keeping it in the "# How you work" block.
// The bash ban covers both READS (cat/sed/head) and CREATES (cat > f,
// heredocs, tee, /tmp scratch), steering to the dedicated tools.
const locateFirstPrinciple = "Locate first. Outline or grep a path to find exactly where the content lives, then read_file only the spans you need — never read whole files you haven't outlined, never invent or guess file paths (work only from paths outline/grep actually returned), never re-read content already present in context (already-read spans, earlier tool output, the outline), and never use bash to read or create files — never `cat`/`sed`/`head` (or similar) to read them, and never `cat > f`/`<<` heredocs/`tee`/`/tmp` scratch to create them — read_file/outline/grep are your readers and write_file/edit_file are your writers."

// verifyBeforeFixPrinciple is the issue #178 verify-before-fix principle —
// a const so every surface that restates the same idea (the planning
// instruction and each step prompt in plan_mode.go) carries the SAME text:
// one principle, no recipe. It is spliced into SystemPrompt inside the
// "Verify first" line (see the ` + verifyBeforeFixPrinciple + ` above), so
// EVERY turn sees it — REPL turns, headless turns, plan-mode turns, and the
// self-dev loop's own ordinary step turns, which never go through
// TurnWithPlan's prompts (the scenario in #178).
const verifyBeforeFixPrinciple = "Confirm a problem exists before fixing it. When a reported problem doesn't reproduce, saying so with the evidence is the finished result; a fix for a problem you haven't observed is not."

// blockedCheckPrinciple is the issue #200 blocked-check principle — a const
// so CLAUDE.md's "Constraints → Testing" section mirrors the EXACT same text
// (the docs describe the guidance the model actually receives, so the two
// can't drift apart — the same mirror pattern as failingTestPrinciple and
// debugWorkingStylePrinciple). It is spliced into SystemPrompt inside the
// "Verify first" line, right after verifyBeforeFixPrinciple, so every turn
// sees it at the exact moment a bash refusal could land: a blocked, refused,
// or declined check leaves its result unknown — don't guess it, and a check
// of something else doesn't stand in for it; failing that, mark the claim
// unverified wherever it is stated. (The shellrisk refusal messages carry
// the short per-incident version of the same instruction; this is the
// standing principle.) Deliberately a principle, not a recipe: no tool names,
// no paths, no list of incident surfaces — the PR #196/#197 specifics (a
// focused t.Logf test, in-tree files vs /tmp, comments/goldens/commit
// summaries) were the incident, not the principle.
const blockedCheckPrinciple = "A check that was blocked, refused, or declined leaves its result unknown. Don't guess it, and a check of something else doesn't stand in for it. Look for another safe way to observe the same thing; failing that, mark the claim unverified wherever you state it."

// failingTestPrinciple is the issue #177 failing-test working-style principle
// — a const so CLAUDE.md's "Constraints → Testing" section mirrors the EXACT
// same text (the docs describe the guidance the model actually receives, so
// the two can't drift apart; see TestFailingTestPrincipleMirroredInClaudeMD).
// It is spliced into SystemPrompt right after "Test integrity" (see the
// ` + failingTestPrinciple + ` above), keeping it in the "# How you work"
// block, before debugWorkingStylePrinciple.
const failingTestPrinciple = "Tests are evidence. An existing test's expected value records what someone decided correct behavior is; when it disagrees with your change, the burden of proof is on your change. Rewriting an expectation to match output you just produced is never a fix — it turns a bug into the specification."

// reviewFeedbackPrinciple is the issue #162 review-feedback principle — a
// const so CLAUDE.md's "Constraints" section mirrors the EXACT same text
// (the docs describe the guidance the model actually receives, so the two
// can't drift apart — the same mirror pattern as blockedCheckPrinciple,
// failingTestPrinciple, and debugWorkingStylePrinciple). It is spliced into
// SystemPrompt as its own paragraph after failingTestPrinciple and before
// debugWorkingStylePrinciple, so every turn sees it: REPL turns, headless
// turns, plan-mode turns, and the self-dev loop's ordinary step turns, which
// never go through TurnWithPlan's prompts (the same delivery reasoning as
// verifyBeforeFixPrinciple).
//
// It exists because review rounds burned whole extra cycles on four distinct
// ways of not applying what a reviewer asked (three PRs across four ticks, the
// self-dev loop's tick 20261001T013107Z): PR #158 left a docs placeholder
// uncorrected for two rounds after every review named it, and in its final
// round put a deferral note in a doc file instead of the commit message the
// reviewer had asked for; PR #145 followed BOTH options a reviewer had offered
// as alternatives, so a complying model printed its summary twice; PR #143
// patched exactly the flag spellings each previous review listed, adding one
// spelling per round instead of closing the class of bad spellings. Each
// failure is a finding that was silently dropped rather than consciously
// declined — the cost is not the drop itself but that nobody could see it,
// which is why the principle asks for an explicit disposition per finding.
//
// Deliberately a principle, not a recipe: no file paths, no list of incident
// PRs, no checklist format — the tick specifics (a '~len/4 tokens'
// placeholder, a denylist of flag spellings) were the incidents, not the
// guidance. It extends the per-item accounting idea of #128 from issue
// requirements to review findings, and stays silent on how to record the
// dispositions, because the shape belongs to whoever drives the round.
const reviewFeedbackPrinciple = "Every finding a review raises is owed an explicit disposition: addressed, deferred with a reason, or disputed. A finding left with none of these is a finding you dropped, and a dropped finding is invisible to the next round, so it comes back. When a reviewer offers several options as alternatives, pick one and say which — applying all of them is not thoroughness, it stacks behaviour the reviewer meant as a choice. When a reviewer gives example instances, name and fix the underlying class rather than only the instances listed: a fix that covers the examples and not the class needs another round for the next example. Where the reviewer asked for something to live, put it there — a note the reviewer asked to keep belongs in the place they named, not in a nearby file that happens to be open."

// memoryPromptSection is the full memory guidance — the four bullets plus the
// outline/recall paragraph — appended to the system prompt only when there's
// something to use it on (notes exist, or the outline has demoted turns).
// Kept as a separate const so the always-present base prompt stays small for
// small local models; the short line in SystemPrompt above always appears, and
// this section rides on top per turn — delivered through the ephemeral wire
// slot (turn.go's memorySectionFor), never through the stored system message.
const memoryPromptSection = `
When notes exist, their index is appended to the turn so you can see what you can recall.

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
	// promptAttribution is the attribution line (configureAttributionPrompt);
	// "" until a session configures it, so an unconfigured prompt is the
	// built-in one verbatim.
	promptAttribution = ""
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

// memorySectionFor returns the full memory guidance (memoryPromptSection)
// when there's something to use it on — memory notes exist or demoted turns
// are visible to the model (live outline entries or the folded digest) — and
// "" otherwise. Pure and per request: turn.go calls it once per turn with
// that turn's memory index, outline-present state, and whether the session's
// base prompt is the built-in one (builtinBase — promptBase == SystemPrompt),
// so concurrent sessions in one process (cortex serve, cortex discord) never
// share mutable state.
//
// builtinBase is load-bearing: prompt.file is documented to REPLACE the
// built-in base prompt (docs/configuration.md), so a custom prompt fully
// controls its own memory guidance — the built-in section must not ride on
// top of it. The section's opening line ("When notes exist, their index is
// appended to the turn…") also assumes the built-in prompt's short memory
// line ("You have a persistent memory…"), which a custom prompt need not
// carry. A missing or empty prompt.file falls back to the built-in (base is
// still SystemPrompt), so its sessions keep the section exactly as before.
//
// The outline-present condition mirrors turn.go's outline-block condition
// (len(cs.outline) > 0 || cs.outlineFolded != ""): once context_evict has
// removed every live entry while the folded digest's @session citations are
// still on the wire, the recall guidance must not be dropped along with the
// entries. The skills index deliberately plays no part: a project with Agent
// Skills but zero memory notes still gets no memory section.
func memorySectionFor(memIndex string, outlinePresent, builtinBase bool) string {
	if !builtinBase {
		return ""
	}
	if memIndex != "" || outlinePresent {
		return memoryPromptSection
	}
	return ""
}

// attributionPromptLine is the system-prompt line the attribution config
// contributes when it is on: one principle, no recipe — commit messages the
// agent authors end with the configured trailer, pull request bodies with
// the configured footer, both spelled out verbatim so the model can write
// them. model is substituted into the trailer by attributionCommit's rules.
// Returns "" when attribution is disabled or both surfaces are "". The PR
// footer has no mechanical backstop (Cortex never composes a PR body
// itself), so this line is its only delivery; commits are also covered by
// the bash tool's --trailer backstop and change.go's interpret-trailers path.
func (c *Config) attributionPromptLine(model string) string {
	commit, pr := c.attributionCommit(model), c.attributionPR()
	switch {
	case commit != "" && pr != "":
		return fmt.Sprintf("Attribution: end every git commit message you author with the trailer line %q, and end every pull request body you write with the line %q.", commit, pr)
	case commit != "":
		return fmt.Sprintf("Attribution: end every git commit message you author with the trailer line %q.", commit)
	case pr != "":
		return fmt.Sprintf("Attribution: end every pull request body you write with the line %q.", pr)
	default:
		return ""
	}
}

// configureAttributionPrompt sets the attribution line systemPromptContent
// appends. NewCortexSession calls it once, after the code model is resolved
// and before the first request is built, so every coder session (REPL,
// `cortex turn`, serve/web, discord, loop firings) carries it in its stable
// system prefix rather than per turn.
func configureAttributionPrompt(cfg *Config, model string) {
	promptAttribution = cfg.attributionPromptLine(model)
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
