package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
const locateFirstPrinciple = "Locate first. Outline or grep a path to find exactly where the content lives, then read_file only the spans you need — never read whole files you haven't outlined, never invent or guess file paths (work only from paths outline/grep actually returned), never re-read content already present in context (already-read spans, earlier tool output, the outline), and never use bash to read or create files — never `cat`/`sed`/`head` (or similar) to read them, and never `cat > f`/heredocs/`tee`/`/tmp` scratch to create them — read_file/outline/grep are your readers and write_file/edit_file are your writers."

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

// checklistAccountingPrinciple is the issue #220 per-item checklist
// accounting principle (part 2 of #128, "completion based on facts") — a
// const so every surface that states the same idea carries the SAME text,
// the one-principle-no-recipe pattern verifyBeforeFixPrinciple and
// reviewFeedbackPrinciple set. It is spliced into the model-facing task
// prompt (taskPrompt, below) ONLY when taskChecklistItems finds at least
// one `- [ ]` item in the task — a task with no checklist gets nothing,
// and a standing principle in the base system prompt would tax every
// checklist-free turn for the one shape this issue is about. It tells the
// model WHAT to conclude (each item accounted for: done, with the evidence
// — a file and line, or a command and its result — or not done) but says
// nothing about WHERE to record the accounting: the output shape belongs to
// whoever drives the turn (plan mode's step prompts, the #219 receipt, …),
// so the principle stays reusable. It is deliberately silent on the word
// "all": the accounting it demands is per item, and an aggregate "all met"
// that skips an item is the failure mode the issue exists to close.
const checklistAccountingPrinciple = "A task that lists checklist items is accounted for item by item in your final answer: every `- [ ]` item is reported as done — with the evidence (a file and line, or a command and its result) — or as not done. A summary that covers the items in the aggregate, without each one named and evidenced, is not an account of them."

// taskChecklistLineRe matches one task checklist item: a line whose leading
// markdown checkbox ("- [ ] " or "- [x] ", either case, `*`/`+` bullets
// tolerated) is followed by item text, captured in group 1 — the text may
// itself contain brackets or dashes ("[x] handle - [ ] markers"), so it is
// taken verbatim to end of line. Fenced code blocks are NOT handled by the
// regex: taskChecklistItems skips lines inside ``` or ~~~ fences, so a task
// that merely shows checkbox syntax in an example block lists no items.
var taskChecklistLineRe = regexp.MustCompile(`^ {0,3}[-*+]\s*\[[ xX]\]\s+(\S.*)$`)

// taskFenceLineRe reports which fence character (backtick or tilde) a line
// uses, if any. CommonMark allows up to three spaces of indentation on a
// fence line. taskChecklistItems tracks the opener's character so that a
// fence only closes on the same character — tildes and backticks cannot be
// mixed (a ~~~ line does not close a ``` block).
var taskFenceLineRe = regexp.MustCompile(`^ {0,3}(` + "```" + `|~~~)\S*`)

// taskChecklistItems extracts the task's checklist items — the lines that
// start a markdown checkbox (either state, `- [ ]` or `- [x]`, `*`/`+`
// bullets tolerated) — in order, with the item text trimmed. Fenced code
// blocks (``` or ~~~) are skipped: a task that shows checkbox syntax inside
// a code example ("add a status line like `- [ ] foo`") is not a task WITH
// a checklist, and its example lines must not become items the turn is
// owed an account for. Returns nil when the task has no checklist items —
// callers treat that as "the principle does not apply to this task" and
// splice nothing into the prompt. Pure: no session, no config, safe under
// concurrent turns.
func taskChecklistItems(task string) []string {
	var items []string
	fenceChar := rune(0)
	for _, line := range strings.Split(task, "\n") {
		if m := taskFenceLineRe.FindStringSubmatch(line); m != nil {
			c := rune(m[1][0])
			if fenceChar == 0 {
				fenceChar = c
			} else if c == fenceChar {
				fenceChar = 0
			}
			continue
		}
		if fenceChar != 0 {
			continue
		}
		m := taskChecklistLineRe.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
		if m == nil {
			continue
		}
		if item := strings.TrimSpace(m[1]); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// taskPrompt renders the model-facing prompt for a turn's task: the task
// text, and — only when the task actually carries a checklist (see
// taskChecklistItems) — a separator, the extracted items each rendered as
// `- [ ] item` (the checkbox state the task used is not load-bearing; the
// account is owed per item either way), and the per-item accounting
// principle (checklistAccountingPrinciple). A task with no checklist
// returns its text UNCHANGED — the common case keeps the wire bytes
// identical to the pre-#220 behavior, and the transcript records the prompt
// as the model sees it (cs.Append persists what turn.go sends).
// Pure: turn.go calls it exactly once per turn, right before Append.
func taskPrompt(task string) string {
	items := taskChecklistItems(task)
	if len(items) == 0 {
		return task
	}
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\nTask checklist (account for each item in your final answer):\n")
	for _, item := range items {
		b.WriteString("- [ ] ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(checklistAccountingPrinciple)
	return b.String()
}

// checklistStopWords are the words an item's significant content never
// carries: articles, prepositions, and other function words. checklistItem
// Present drops them from the ITEM before requiring the reply to name the
// item's words — they carry no meaning an item could fail on ("add the
// handler" is accounted for by "added the handler" whether or not the
// reply kept the "the"). The reply is never stop-worded: an item word that
// happens to be in this list ("the tests") is still required verbatim, so
// the list only ever makes matching LOOSER on the item side, never on the
// reply side.
var checklistStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "in": true, "into": true,
	"is": true, "it": true, "its": true, "of": true, "on": true, "or": true,
	"so": true, "than": true, "that": true, "the": true, "their": true,
	"then": true, "there": true, "these": true, "this": true, "to": true,
	"up": true, "with": true,
}

// checklistItemWords returns item's significant words: lowercased, with
// punctuation stripped (only ASCII letters and digits survive — "handler."
// and `handler` are one word), with checklistStopWords dropped, and with
// the leading "add " form-verb prefix stripped so the form of the verb does
// not matter ("add the helper" is named by "the helper"). Empty or
// stop-word-only items yield nil — an empty item (taskChecklistItems never
// yields one) is then always present, so it can never dangle on a receipt.
func checklistItemWords(item string) []string {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(item)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		w = b.String()
		if w == "" || checklistStopWords[w] {
			continue
		}
		if len(words) == 0 && w == "add" {
			// A leading form-verb: "add the helper" == "the helper". A
			// mid-item "add" ("re-add the flag") stays significant.
			continue
		}
		words = append(words, w)
	}
	return words
}

// checklistItemPresent reports whether the reply accounts for the item:
// every significant word of the item (checklistItemWords) must appear in
// the reply — case-insensitive, punctuation-stripped — as a PREFIX of some
// reply word ("add" matches "added", "test" matches "tests", "wire" matches
// "wired"/"wires"). The item's words need not be adjacent or in order, so
// an item named in the reply's own words ("I added the helper" for "add the
// helper"; "The tests are deferred" for "add the tests") is accounted for,
// and an item the reply never names is not. An empty item (or one with no
// significant words) is treated as present so it can never dangle on a
// receipt. Pure: no session, no config; O(len(reply)) per item, and the
// receipt's single pass over the items is the only caller.
func checklistItemPresent(reply, item string) bool {
	itemWords := checklistItemWords(item)
	if len(itemWords) == 0 {
		return true
	}
	var replyWords []string
	for _, w := range strings.Fields(strings.ToLower(reply)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if s := b.String(); s != "" {
			replyWords = append(replyWords, s)
		}
	}
	for _, iw := range itemWords {
		found := false
		for _, rw := range replyWords {
			if rw == iw || strings.HasPrefix(rw, iw) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// checklistMissingItems returns the task's checklist items (taskChecklistItems,
// in order) that the reply does NOT account for — the receipt's
// "checklist:" fact (issue #220 step 2). Matching is per item through
// checklistItemPresent: every significant word of the item must appear in
// the reply, case-insensitive, punctuation-stripped, with a word-prefix
// match ("add" matches "added"), and the reply's own words — an explicit
// "not done" is an account of the item, too. Returns nil in two cases a
// caller reads as "nothing to measure": the task has no checklist at all
// (taskChecklistItems nil), and the reply accounts for every item (the
// empty-slice case is the same nil — a turn that met its checklist has no
// missing-item fact). Pure: no session, no config, safe under concurrent
// turns.
func checklistMissingItems(task, reply string) []string {
	items := taskChecklistItems(task)
	if len(items) == 0 {
		return nil
	}
	var missing []string
	for _, item := range items {
		if !checklistItemPresent(reply, item) {
			missing = append(missing, item)
		}
	}
	return missing
}

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
