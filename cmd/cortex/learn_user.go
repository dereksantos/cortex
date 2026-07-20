// learn_user.go — the user-scope (cross-project) promotion pass
// (docs/cross-source-learning.md piece 1's promotion half, piece 2's link
// rules, piece 3's scope generalization): the machine-wide counterpart to
// learn.go's per-project Learn pass. Two entry points, sharing one engine
// (RunLearnUserPass): `cortex learn --user` (runLearnUserCLI, one-shot
// headless) and the loop scheduler's kind:"learn"+scope:"user" firing
// (runLearnUserFiring, loop_run.go's RunLoopFiring branches to it).
//
// The pass, two stages (docs/cross-source-learning.md "Who writes it — the
// honest mechanism"):
//
//  1. Mechanical, no model (detectCandidateGroups): scan every registered
//     project's OWN memory index/notes (the already-curated per-project
//     distillate — never a project's raw journal) and connect any two notes
//     from DIFFERENT projects that share enough distinctive vocabulary
//     (recurrenceMinSharedTerms) into a candidate group. Zero groups means
//     zero model calls — the common, cheapest case.
//  2. Model-driven, one bounded LearnUser call PER candidate group (not
//     already promoted — groupAlreadyPromoted is a mechanical, no-model
//     dedup short-circuit): the model decides whether the group's notes
//     really describe the same fact and, if so, writes ONE synthesized note.
//     Every memory_write it issues is intercepted by study.go's
//     dispatcherFor and rewritten by preparePromotionWrite below — scope
//     forced to "user", wikilinks un-linked against the real user tier
//     (piece 2's link rule), and a "Promoted from: <projects>" provenance
//     line appended mechanically — so both invariants hold regardless of
//     what the live model actually produced.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
)

// recurrenceMinSharedTerms is the candidate-recurrence threshold
// (docs/cross-source-learning.md open question 1): the minimum number of
// DISTINCTIVE terms (>= recurrenceMinTermLen runes each, lowercase,
// alphanumeric, AND passing the document-frequency filter below — see
// isDistinctiveTerm) two notes from DIFFERENT projects must share before the
// deterministic detector treats them as the same recurring fact. Derek's
// recommendation on open question 1 was to start conservative — "likely
// under-promoting" — since G2's precision floor (over-promotion) punishes
// harder than a missed lift (G1) does; this value is that conservative
// starting point. A live gate run (slice-2 gate run 2) found the ORIGINAL
// version of this rule — counting ANY 5+ rune term, distinctive or not —
// let a noise project's note join an unrelated candidate group: every
// seeded note shared generic phrasing ("remember", "future", "sessions")
// that alone crossed this threshold between projects with nothing else in
// common. The document-frequency filter (distinctiveDF/isDistinctiveTerm)
// fixes that by counting only terms that are NOT common across most of the
// registry, so the threshold value itself didn't need to change — only what
// counts as a "shared term" did. TUNE FROM REAL LearnUser RECEIPTS, NOT
// PRIORS, once the ø gate (G1/G2) has live promotion data to look at — this
// is a placeholder, not a measured constant.
const recurrenceMinSharedTerms = 3

// recurrenceMinTermLen excludes short/common words from the overlap count —
// a shared "the" or "test" proves nothing; a shared 5+ letter/digit token is
// far more likely to be a real shared identifier, decision word, or concept.
const recurrenceMinTermLen = 5

// distinctiveDFFloor is the document-frequency filter's unconditional
// floor: a term appearing in notes from AT MOST this many distinct projects
// always counts as distinctive, regardless of how large the registry is.
// The base recurrence case IS exactly two projects sharing a fact, so the
// floor has to be at least 2 or the detector could never find the simplest
// case it exists to find; a third project happening to share the same term
// is exactly the boilerplate/generic-phrasing signal the filter exists to
// catch (slice-2 gate run 2's C-leak: every seeded note shared "remember
// this for future sessions" regardless of topic).
const distinctiveDFFloor = 2

// distinctiveDF computes, for every term appearing in notes' precomputed
// term sets, its PROJECT-level document frequency — the number of DISTINCT
// PROJECTS (not notes; a project with several notes repeating a term still
// counts once) whose notes contain that term. Project-level, not note-
// level, because the recurrence question is "how many different projects
// use this word", not "how many notes" — a chatty single project must not
// inflate a term's apparent commonness.
func distinctiveDF(notes []candidateNote) map[string]int {
	byTerm := map[string]map[string]bool{}
	for _, n := range notes {
		for t := range n.terms {
			if byTerm[t] == nil {
				byTerm[t] = map[string]bool{}
			}
			byTerm[t][n.Project] = true
		}
	}
	df := make(map[string]int, len(byTerm))
	for t, projects := range byTerm {
		df[t] = len(projects)
	}
	return df
}

// isDistinctiveTerm reports whether a term counts as DISTINCTIVE for the
// recurrence overlap check, given its project-level document frequency df
// and numProjects (the count of distinct projects contributing ANY note to
// this detection pass — the whole registry scan, not just the pair being
// compared). No curated stopword list: a term is judged purely by how many
// of the CONTRIBUTING projects happen to use it.
//
// Rule: distinctive if df <= distinctiveDFFloor (2), OR df is less than
// half of numProjects. The floor is what does the actual work at the
// registry sizes this pass runs against today (a handful of projects, not
// hundreds) — it's exactly "shared by the pair being compared and nobody
// else", which both catches the boilerplate case (a term shared by ALL
// contributing projects, e.g. seeding phrasing repeated verbatim by every
// note regardless of topic, has df == numProjects > 2 as soon as a third
// project exists) and preserves the simplest true-recurrence case (df == 2,
// always distinctive via the floor). The half-of-numProjects clause is what
// keeps the rule from silently degenerating as the registry grows: without
// it, a term genuinely shared by 3 projects out of 50 would be judged "too
// common" forever under a flat cap, even though 3-of-50 is a small,
// meaningfully rare minority — exactly the shape open question 1
// anticipates a real recurring fact taking once the registry is large.
func isDistinctiveTerm(df, numProjects int) bool {
	if df <= distinctiveDFFloor {
		return true
	}
	return 2*df < numProjects
}

// recurrenceMaxNotesPerProject / recurrenceMaxNoteChars bound how much of a
// project's own memory the mechanical detector reads — "capped read per
// project" (docs/cross-source-learning.md piece 1's mechanical step): a
// project with a very large note corpus must not blow the harness's own
// read budget just scanning for recurrence candidates. List() already
// returns most-recently-updated first, so a cap keeps the freshest notes.
const (
	recurrenceMaxNotesPerProject = 50
	recurrenceMaxNoteChars       = 4000
)

// termRe tokenizes lowercased note text into candidate terms for the
// recurrence overlap check — plain string comparison, no model.
var termRe = regexp.MustCompile(`[a-z0-9]+`)

func significantTerms(text string) map[string]bool {
	terms := map[string]bool{}
	for _, t := range termRe.FindAllString(strings.ToLower(text), -1) {
		if len(t) >= recurrenceMinTermLen {
			terms[t] = true
		}
	}
	return terms
}

func sharedTermCount(a, b map[string]bool) int {
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

// candidateNote is one project's memory note, read for recurrence detection.
type candidateNote struct {
	Project string
	Name    string
	Body    string
	terms   map[string]bool
}

// candidateGroup is a connected set of notes from >=2 distinct projects the
// mechanical detector flagged as the same recurring fact.
type candidateGroup struct {
	Notes []candidateNote
}

// projects returns the group's distinct source project names, sorted —
// the exact list preparePromotionWrite's provenance line and
// groupAlreadyPromoted's dedup check both key off.
func (g candidateGroup) projects() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range g.Notes {
		if !seen[n.Project] {
			seen[n.Project] = true
			out = append(out, n.Project)
		}
	}
	sort.Strings(out)
	return out
}

// sortKey gives detectCandidateGroups a deterministic output order — a set
// data structure (the union-find components) has no natural iteration
// order, and Δ1 requires the same fixtures to produce the same groups EVERY
// run, including their order.
func (g candidateGroup) sortKey() string {
	keys := make([]string, len(g.Notes))
	for i, n := range g.Notes {
		keys[i] = n.Project + "/" + n.Name
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}

// collectProjectNotes reads every registered project's OWN memory notes —
// the already-curated per-project distillate, not a raw journal
// (docs/cross-source-learning.md: "re-scanning every project's whole capture
// history from a user-level pass would blow any sane cost budget"). A
// project with no memory store yet (no .cortex/memory directory) is skipped
// without creating one — this is a read-only scan, it must not leave a side
// effect on a project that never used memory. An unreadable/missing project
// root is likewise skipped, not fatal to the whole pass — one bad registry
// entry must not block every other project's candidates.
func collectProjectNotes(reg registry.Registry) ([]candidateNote, error) {
	projects, err := reg.List()
	if err != nil {
		return nil, fmt.Errorf("learn_user: list registry: %w", err)
	}
	var notes []candidateNote
	for _, p := range projects {
		ws, err := NewWorkspace(p.Root)
		if err != nil {
			continue
		}
		memDir := filepath.Join(ws.ContextDir(), "memory")
		if _, err := os.Stat(memDir); err != nil {
			continue
		}
		mem, err := memory.New(ws.ContextDir())
		if err != nil {
			continue
		}
		metas, err := mem.List()
		if err != nil {
			continue
		}
		if len(metas) > recurrenceMaxNotesPerProject {
			metas = metas[:recurrenceMaxNotesPerProject]
		}
		for _, m := range metas {
			body, err := mem.Read(m.Name)
			if err != nil {
				continue
			}
			if len(body) > recurrenceMaxNoteChars {
				body = body[:recurrenceMaxNoteChars]
			}
			notes = append(notes, candidateNote{
				Project: p.Name,
				Name:    m.Name,
				Body:    body,
				terms:   significantTerms(m.Name + " " + body),
			})
		}
	}
	return notes, nil
}

// detectCandidateGroups is the mechanical, no-model recurrence detector
// (docs/cross-source-learning.md piece 1's step 1): unions any two notes
// from DIFFERENT projects that share >= recurrenceMinSharedTerms
// DISTINCTIVE terms (isDistinctiveTerm — generic/boilerplate vocabulary
// common across most contributing projects doesn't count, even if it's a
// 5+ rune word), then returns every connected component that spans >=2
// distinct projects as one candidate group. Deterministic — fixed note
// fixtures always produce the same groups, in the same order (Δ1).
func detectCandidateGroups(notes []candidateNote) []candidateGroup {
	n := len(notes)

	projectSet := map[string]bool{}
	for _, note := range notes {
		projectSet[note.Project] = true
	}
	numProjects := len(projectSet)

	df := distinctiveDF(notes)
	distinctiveTerms := make([]map[string]bool, n)
	for i, note := range notes {
		dt := make(map[string]bool, len(note.terms))
		for t := range note.terms {
			if isDistinctiveTerm(df[t], numProjects) {
				dt[t] = true
			}
		}
		distinctiveTerms[i] = dt
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if notes[i].Project == notes[j].Project {
				continue
			}
			if sharedTermCount(distinctiveTerms[i], distinctiveTerms[j]) >= recurrenceMinSharedTerms {
				union(i, j)
			}
		}
	}
	byRoot := map[int][]candidateNote{}
	for i, note := range notes {
		r := find(i)
		byRoot[r] = append(byRoot[r], note)
	}
	var out []candidateGroup
	for _, ns := range byRoot {
		g := candidateGroup{Notes: ns}
		if len(g.projects()) >= 2 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sortKey() < out[j].sortKey() })
	return out
}

// provenancePrefix is the fixed, grep-able provenance line's prefix
// (docs/cross-source-learning.md piece 1 decision 3) — plain text, not
// frontmatter. Always appended mechanically (preparePromotionWrite), never
// left to the model to phrase, so Δ2's "no more, no fewer" holds regardless
// of live model behavior.
const provenancePrefix = "Promoted from: "

// provenanceValue renders a candidate group's source-project list in the
// fixed, sorted, comma-joined form both preparePromotionWrite's append and
// groupAlreadyPromoted's dedup check key off — the two must agree exactly,
// or a group promoted once would never be recognized as already-promoted on
// a later firing.
func provenanceValue(projects []string) string {
	sorted := append([]string(nil), projects...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// findProvenanceLine returns the value half of body's "Promoted from: ..."
// line ("" if absent) — used both to detect whether the model already wrote
// one (preparePromotionWrite skips re-appending) and to read an existing
// user-tier note's provenance back out (groupAlreadyPromoted's dedup scan).
func findProvenanceLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), provenancePrefix); ok {
			return v
		}
	}
	return ""
}

// groupAlreadyPromoted is the mechanical, no-model dedup short-circuit
// (docs/cross-source-learning.md: "no cursor for this pass, by design ...
// idempotent via the same check-the-target-before-writing discipline Learn
// already uses"): a candidate group whose EXACT source-project set already
// appears as some existing user-tier note's provenance line is skipped
// without ever calling the model — cheaper than a bounded call, and what
// makes a full-registry rescan safe to repeat every firing.
func groupAlreadyPromoted(userMem *memory.Store, projects []string) (bool, error) {
	if userMem == nil {
		return false, nil
	}
	metas, err := userMem.List()
	if err != nil {
		return false, err
	}
	want := provenanceValue(projects)
	for _, m := range metas {
		body, err := userMem.Read(m.Name)
		if err != nil {
			continue
		}
		if findProvenanceLine(body) == want {
			return true, nil
		}
	}
	return false, nil
}

// wikiLinkRe matches a [[name]] wikilink (docs/cross-source-learning.md
// piece 2: "Plain text inside a note's body — no new file, no edge index").
var wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// unlinkAbsentTargets is piece 2's promotion un-link rule (Δ4): any
// [[name]] wikilink in content whose target is NOT present in userMem right
// now has its brackets stripped — the plain text survives, the link doesn't
// — "a bracketed reference the reader's tier can't resolve is worse than
// none." A link whose target IS present survives unchanged. Checked against
// userMem's CURRENT note list, which already reflects any note this SAME
// promotion pass wrote earlier — so two notes promoted together in one
// firing can still link each other.
func unlinkAbsentTargets(content string, userMem *memory.Store) (string, error) {
	present := map[string]bool{}
	if userMem != nil {
		metas, err := userMem.List()
		if err != nil {
			return content, err
		}
		for _, m := range metas {
			present[strings.ToLower(strings.TrimSpace(m.Name))] = true
		}
	}
	return wikiLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		sub := wikiLinkRe.FindStringSubmatch(match)
		name := strings.ToLower(strings.TrimSpace(sub[1]))
		if present[name] {
			return match
		}
		return sub[1]
	}), nil
}

// learnUserGroupKey carries the candidate group currently being promoted
// through ctx, from RunLearnUserPass's runSubagentStats call down to
// study.go's dispatcherFor / preparePromotionWrite — mirrors
// subagentDepthKey's shape (study.go), a per-call value threaded via
// context rather than a field, since dispatcherFor is shared with
// Study/Agent/Learn and must not carry LearnUser-specific state on the
// Subagent value itself.
type learnUserGroupKey struct{}

func withLearnUserGroup(ctx context.Context, g candidateGroup) context.Context {
	return context.WithValue(ctx, learnUserGroupKey{}, g)
}

func learnUserGroupFromCtx(ctx context.Context) (candidateGroup, bool) {
	g, ok := ctx.Value(learnUserGroupKey{}).(candidateGroup)
	return g, ok
}

// preparePromotionWrite is the promotion write path (docs/cross-source-
// learning.md pieces 1+2): study.go's dispatcherFor calls this on every
// memory_write ToolCall a LearnUser run issues, BEFORE the generic
// allow/confine/execute dispatch — scope is forced to "user" regardless of
// what the model passed (LearnUser only ever writes the user tier), the
// body's wikilinks are un-linked against the real user tier
// (unlinkAbsentTargets), and a "Promoted from: <projects>" line is appended
// if the model's own body doesn't already end with one — the model is
// explicitly told not to write its own (learnUserSystem), but this is
// mechanical insurance regardless. Returns the rewritten call for the
// generic dispatch path to execute normally (so printToolAction / the
// standard "saved note ... (user)" confirmation still fire).
func (cs *CortexSession) preparePromotionWrite(ctx context.Context, call ToolCall) (ToolCall, error) {
	name, err := call.StringArg("name")
	if err != nil {
		return call, err
	}
	content, err := call.StringArg("content")
	if err != nil {
		return call, err
	}
	content, err = unlinkAbsentTargets(content, cs.userMemory)
	if err != nil {
		return call, err
	}
	if group, ok := learnUserGroupFromCtx(ctx); ok && findProvenanceLine(content) == "" {
		content = strings.TrimRight(content, "\n") + "\n\n" + provenancePrefix + provenanceValue(group.projects())
	}
	args, err := json.Marshal(map[string]any{"name": name, "content": content, "scope": scopeUser})
	if err != nil {
		return call, fmt.Errorf("learn_user: marshal rewritten memory_write args: %w", err)
	}
	call.Function.Arguments = string(args)
	return call, nil
}

// learnUserSeed builds the LearnUser subagent's opening user message: the
// candidate group's full note bodies (tagged by source project) and the
// user memory index — the model's own defense-in-depth dedup check
// (mirroring learnSeed's "don't duplicate" framing), on top of the
// harness's mechanical groupAlreadyPromoted short-circuit, which already
// skips a group that's EXACTLY already promoted; the index mostly guards
// against a close-but-not-identical group the mechanical threshold let
// through.
func learnUserSeed(group candidateGroup, userIndex string) string {
	var b strings.Builder
	b.WriteString("GOAL: decide whether this candidate group describes one fact worth promoting to the user (cross-project) memory tier.\n\n")
	fmt.Fprintf(&b, "CANDIDATE GROUP (%d notes across %d projects):\n\n", len(group.Notes), len(group.projects()))
	for _, n := range group.Notes {
		fmt.Fprintf(&b, "--- [%s] %s ---\n%s\n\n", n.Project, n.Name, n.Body)
	}
	b.WriteString("USER MEMORY INDEX (already promoted — do not duplicate):\n")
	if strings.TrimSpace(userIndex) == "" {
		b.WriteString("(no notes promoted yet)\n")
	} else {
		b.WriteString(userIndex)
		b.WriteString("\n")
	}
	return b.String()
}

// LearnUserResult is one user-scope promotion pass's outcome — the CLI's
// report and the loop firing's journaled Reason are both built from it.
type LearnUserResult struct {
	CandidateGroups int
	GroupsSkipped   int // already promoted — groupAlreadyPromoted, no model call
	NotesWritten    int
}

// Report renders a short, plain, human-readable summary — `cortex learn
// --user`'s stdout line and the basis for the loop firing's journaled
// Reason.
func (r LearnUserResult) Report() string {
	if r.CandidateGroups == 0 {
		return "nothing recurring across projects"
	}
	considered := r.CandidateGroups - r.GroupsSkipped
	if r.NotesWritten == 0 {
		return fmt.Sprintf("%d candidate group(s) found (%d already promoted), nothing new promoted", r.CandidateGroups, r.GroupsSkipped)
	}
	plural := "s"
	if r.NotesWritten == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d candidate group(s) found, %d considered, %d note%s promoted", r.CandidateGroups, considered, r.NotesWritten, plural)
}

// RunLearnUserPass is the shared user-scope engine: detect candidate groups
// across every registered project's memory (no model), skip whatever's
// already promoted (no model), and hand only what's left to the LearnUser
// subagent — one bounded call per remaining group. Zero groups is the
// cheapest possible outcome (no model call at all), matching
// RunLearningPass's own bounded-cost discipline. A subagent error aborts
// the pass immediately (same fail-fast posture as RunLearningPass) — safe
// to simply retry the whole pass on the next firing, since
// groupAlreadyPromoted makes every already-succeeded group's re-detection a
// no-model no-op.
func RunLearnUserPass(ctx context.Context, cs *CortexSession, reg registry.Registry) (LearnUserResult, error) {
	notes, err := collectProjectNotes(reg)
	if err != nil {
		return LearnUserResult{}, err
	}
	groups := detectCandidateGroups(notes)
	if len(groups) == 0 {
		return LearnUserResult{}, nil
	}

	result := LearnUserResult{CandidateGroups: len(groups)}
	for _, group := range groups {
		already, err := groupAlreadyPromoted(cs.userMemory, group.projects())
		if err != nil {
			return result, err
		}
		if already {
			result.GroupsSkipped++
			continue
		}

		idx := ""
		if cs.userMemory != nil {
			idx, _ = cs.userMemory.Index()
		}
		seed := learnUserSeed(group, idx)
		groupCtx := withLearnUserGroup(ctx, group)

		before := cs.captures
		if _, _, err := cs.runSubagentStats(groupCtx, tools.LearnUser, seed); err != nil {
			return result, err
		}
		result.NotesWritten += cs.captures - before
	}
	return result, nil
}

// userFlag reports whether args carries the `--user` flag — `cortex learn
// --user` (docs/cross-source-learning.md piece 1's CLI mirror) vs. the
// existing `cortex learn [--project <name>]`.
func userFlag(args []string) bool {
	for _, a := range args {
		if a == "--user" {
			return true
		}
	}
	return false
}

// runLearnUserCLI is `cortex learn --user`: a one-shot headless user-scope
// promotion pass over every registered project's memory index. Always exits
// 0 — "nothing recurring across projects" is a normal, common outcome, not
// a failure, mirroring runLearnCLI's identical posture.
func runLearnUserCLI(args []string) {
	reg, err := registry.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "learn --user: open project registry: %v\n", err)
		os.Exit(1)
	}

	cs := NewCortexSession()
	cs.quiet = true
	cs.EnableMemory()

	// Closure so the deferred cs.Close() runs before os.Exit (which skips
	// pending defers) — mirrors runLearnCLI's identical shape.
	exitCode := func() int {
		defer cs.Close()
		result, err := RunLearnUserPass(context.Background(), cs, reg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "learn --user: %v\n", err)
			return 1
		}
		fmt.Println(result.Report())
		return 0
	}()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runLearnUserFiring is RunLoopFiring's kind:"learn"+scope:"user" branch
// (loop_run.go): same journaled-outcome contract as every other loop firing
// (exactly one loop.run event via finalizeLoopFiring, three-strike
// auto-disable on repeated failure) — but scoped to the whole machine
// rather than one project. spec.Project is NOT resolved or required here
// (RunLearnUserPass iterates reg.List() itself); it is still carried onto
// the journaled payload verbatim (Name/Project — Project may be "" for a
// pure user-scope spec) purely as a label, same field every other firing
// stamps. No `cortex change` branch (a promoted note lands under
// ~/.cortex/memory, not any project's source tree) and no NEXT/DONE marker
// (LearnUser's output is a structured result, not a free-text reply) — same
// reasoning as runLearnFiring's identical omissions.
func runLearnUserFiring(ctx context.Context, spec loops.Spec, reg registry.Registry, store loops.Store, newSession sessionFactory) error {
	payload := journal.LoopRunPayload{Name: spec.Name, Project: spec.Project}

	cs := newSession()
	strikes := cs.Config.loopAutoDisableStrikes()
	cs.EnableMemory()
	defer cs.Close()

	result, err := RunLearnUserPass(ctx, cs, reg)
	if err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("learn --user: %v", err)
		return finalizeLoopFiring(spec, store, payload, strikes)
	}

	payload.Outcome = journal.LoopOutcomeSuccess
	payload.Reason = result.Report()
	return finalizeLoopFiring(spec, store, payload, strikes)
}
