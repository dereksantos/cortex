// learn.go — the learning-loop trigger surface (docs/learning-loop.md): the
// smallest honest slice of the historical Think/Dream design
// (docs/think-dream-eval.md), gated on that doc's Δ/ø bar unchanged. One
// engine (RunLearningPass), two entry points sharing it: `cortex learn`
// (runLearnCLI, one-shot headless) and the loop scheduler's kind:"learn"
// firing (runLearnFiring, loop_run.go's RunLoopFiring branches to it).
//
// The pass: read the project journal's "capture" writer-class (one entry per
// foreground turn, cmd/cortex/session_runtime.go's captureTurn) since the
// last learn cursor, hand the window to the Learn subagent
// (internal/tools.Learn) alongside the memory index, and let it decide what
// (if anything) is worth a memory_write. The mechanical step is windowing
// the journal and advancing the cursor — same discipline as
// memory-tools.md's index injection: the model decides what's worth saving,
// the harness only decides what it gets to look at. One exception to
// "windowing only": formatLearnEntry/learnFullTurnText recover a truncated
// entry's full turn text from the session transcript before handing it to
// the model — still mechanical (no model call, no judgment about what's
// worth saving), just making sure a durable fact stated past the capture
// class's own truncation points is actually visible to look at.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/events"
)

// learnDigestCapChars bounds the seed's TURNS block regardless of how big the
// unscanned backlog is (e.g. the very first learn pass over a long-lived
// project) — G4's bounded-cost gate is a property of the whole pass, not
// just the subagent's own Bounds, so the harness must not hand it an
// unbounded seed in the first place.
const learnDigestCapChars = 12_000

// learnPromptCapChars / learnResultCapChars bound one turn's contribution to
// the digest so a single verbose turn can't crowd out the rest of the
// window.
const (
	learnPromptCapChars = 200
	learnResultCapChars = 300
)

// captureClassDir is the journal writer-class Learn reads: one capture.event
// per foreground turn (session_runtime.go's captureTurn).
func captureClassDir(cs *CortexSession) string {
	return filepath.Join(cs.ContextDir(), "journal", "capture")
}

// learnCursorDir is where the learn pass's own cursor lives (journal.Cursor,
// "<dir>/.cursor") — reusing internal/journal's Cursor machinery, but
// pointed at a directory Learn owns exclusively rather than the "capture"
// class directory it reads. Learn is one consumer of the capture class, not
// its indexer (internal/journal/indexer.go's Indexer, today dormant, is
// reserved for a future storage projection over the SAME class and would
// want its own independent cursor) — so it gets its own cursor file.
func learnCursorDir(cs *CortexSession) string {
	return filepath.Join(cs.ContextDir(), "journal", "learn")
}

// learnEntry is one capture.event turn reduced to what the digest needs.
// SessionID/TurnNo are the coordinate pair back into that turn's session
// transcript (session_runtime.go's captureTurn stamps Context.SessionID and
// Metadata["turn"]); either may be zero-value for an older or hand-seeded
// capture entry that predates this, in which case learnFullTurnText simply
// can't recover anything extra for it.
type learnEntry struct {
	Offset    journal.Offset
	TS        time.Time
	Prompt    string
	Result    string
	SessionID string
	TurnNo    int
}

// scanCaptureWindow reads every capture.event entry past the learn cursor, in
// journal order, plus the highest offset seen (0 if none past the cursor).
// The caller advances the cursor to that offset after a successful pass —
// whether or not anything was worth saving — so a NO_INSIGHT run never
// re-scans the same window (docs/learning-loop.md's non-duplication
// invariant). An entry whose type isn't "capture.event" (unexpected, since
// this class only ever holds those) is skipped but still advances the
// returned tail, mirroring internal/journal.Indexer's UnknownLogAndSkip
// posture rather than getting stuck on it. A malformed payload is likewise
// skipped, not fatal to the whole pass.
func scanCaptureWindow(cs *CortexSession) ([]learnEntry, journal.Offset, error) {
	cursor := journal.OpenCursor(learnCursorDir(cs))
	last, err := cursor.Get()
	if err != nil {
		return nil, 0, fmt.Errorf("learn: cursor get: %w", err)
	}
	r, err := journal.NewReader(captureClassDir(cs))
	if err != nil {
		return nil, 0, fmt.Errorf("learn: open capture journal: %w", err)
	}
	defer r.Close()

	tail := last
	var entries []learnEntry
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("learn: read capture journal: %w", err)
		}
		if e.Offset <= last {
			continue
		}
		tail = e.Offset
		if e.Type != "capture.event" {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal(e.Payload, &ev); err != nil {
			continue
		}
		prompt, _ := ev.ToolInput["user_prompt"].(string)
		turnNo := 0
		if n, ok := ev.Metadata["turn"].(float64); ok { // json.Unmarshal decodes numbers as float64
			turnNo = int(n)
		}
		entries = append(entries, learnEntry{
			Offset: e.Offset, TS: e.TS, Prompt: prompt, Result: ev.ToolResult,
			SessionID: ev.Context.SessionID, TurnNo: turnNo,
		})
	}
	return entries, tail, nil
}

// truncateLearnField trims s and clamps it to n runes-worth of bytes for the
// digest — good enough for the ASCII/near-ASCII prose these fields hold; an
// exact rune boundary isn't worth the complexity here.
func truncateLearnField(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// learnFullTurnCapChars bounds one turn's full-transcript pull
// (learnFullTurnText) when the capture-summary line would otherwise
// truncate it. Generous relative to the capture/digest caps (280/200/300)
// so a durable fact stated anywhere in a conversational turn survives, but
// still bounded — a single verbose turn must not be able to blow the
// digest budget (learnDigestCapChars) on its own.
const learnFullTurnCapChars = 3000

// learnFullTurnText renders the verbatim messages of one turn straight from
// its session transcript — the same durable source tool_deps.go's Recall
// resolves citations against — bounded to learnFullTurnCapChars. It exists
// because the capture class's own fields are already lossy by the time
// they reach here: session_runtime.go's captureTurn caps the assistant
// excerpt at capture time (captureExcerptCapChars, default 280), and even
// the untruncated user prompt it stores gets cut a second, harsher time by
// formatLearnEntry's digest caps (learnPromptCapChars/learnResultCapChars)
// — a durable fact stated partway through an otherwise-ordinary turn (e.g.
// after ~130 chars of unrelated preamble) can fall past either cut. Pulling
// the raw transcript recovers what the capture summary can't.
//
// ok is false when the transcript isn't resolvable (no session id/turn
// number on the entry — e.g. a hand-seeded test fixture or a capture event
// that predates this metadata — an unreadable session file, or a turn
// number not present in it); the caller falls back to the capture-summary
// line in that case.
func learnFullTurnText(sessionsDir, sessionID string, turnNo int) (string, bool) {
	if sessionID == "" || turnNo <= 0 {
		return "", false
	}
	msgs, turns, err := loadTranscript(filepath.Join(sessionsDir, sessionID+".jsonl"))
	if err != nil {
		return "", false
	}
	var b strings.Builder
	found := false
	for i, m := range msgs {
		if i >= len(turns) || turns[i] != turnNo {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		found = true
		fmt.Fprintf(&b, "%s: %s\n", m.Role, content)
	}
	if !found {
		return "", false
	}
	return truncateLearnField(b.String(), learnFullTurnCapChars), true
}

// formatLearnEntry renders one turn as a single digest line. When the
// capture-summary fields would be truncated by the digest caps, it prefers
// the turn's full text pulled straight from the session transcript
// (learnFullTurnText) — "prefer the turns the digest flags": the transcript
// read only happens for turns that actually need it, not on every entry, so
// the common case (a short turn that already fits) costs nothing extra.
func formatLearnEntry(sessionsDir string, e learnEntry) string {
	ts := e.TS.UTC().Format(time.RFC3339)
	if len(e.Prompt) > learnPromptCapChars || len(e.Result) > learnResultCapChars {
		if full, ok := learnFullTurnText(sessionsDir, e.SessionID, e.TurnNo); ok {
			return fmt.Sprintf("- [%s] %s\n", ts, full)
		}
	}
	prompt := truncateLearnField(e.Prompt, learnPromptCapChars)
	result := truncateLearnField(e.Result, learnResultCapChars)
	if result == "" {
		return fmt.Sprintf("- [%s] %s\n", ts, prompt)
	}
	return fmt.Sprintf("- [%s] %s → %s\n", ts, prompt, result)
}

// learnSeed builds the Learn subagent's opening user message: the extraction
// goal, the memory index (what's already saved, so Learn doesn't duplicate
// it), and the capped digest of unscanned turns. focus is an optional extra
// hint (a loop spec's Prompt, when Kind is "learn"); empty is the normal
// case. sessionsDir is where formatLearnEntry looks up a truncated entry's
// full turn text (learnFullTurnText); "" is fine — entries without a
// resolvable session/turn just fall back to their capture-summary line.
//
// userIndex is the user-tier memory index (docs/cross-source-learning.md
// piece 1's groundwork for slice 2's cross-project promotion): Learn stays
// project-scoped in THIS slice — it still only ever calls memory_write
// against the project store (its system prompt never mentions a scope arg,
// so an unset scope always resolves to "project") — but seeing the user
// index lets it avoid re-saving, at the project tier, a fact that's already
// promoted cross-project. "" (no user store, or an empty one) omits the
// section entirely rather than printing a hollow header.
func learnSeed(memoryIndex, userIndex string, entries []learnEntry, focus string, sessionsDir string) string {
	var b strings.Builder
	b.WriteString("GOAL: find durable decisions, patterns, constraints, or corrections in the turns below that are not already saved in the memory index.\n")
	if strings.TrimSpace(focus) != "" {
		fmt.Fprintf(&b, "FOCUS: %s\n", strings.TrimSpace(focus))
	}
	if strings.TrimSpace(userIndex) != "" {
		b.WriteString("\nUSER MEMORY INDEX (already saved cross-project — do not duplicate at the project tier):\n")
		b.WriteString(userIndex)
		b.WriteString("\n")
	}
	b.WriteString("\nMEMORY INDEX (already saved — do not duplicate; update instead if a fact fits):\n")
	if strings.TrimSpace(memoryIndex) == "" {
		b.WriteString("(no notes saved yet)\n")
	} else {
		b.WriteString(memoryIndex)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nTURNS since the last learning pass (%d):\n", len(entries))
	used, shown := 0, 0
	for _, e := range entries {
		line := formatLearnEntry(sessionsDir, e)
		if used+len(line) > learnDigestCapChars && shown > 0 {
			fmt.Fprintf(&b, "… %d more turns omitted (over the digest budget)\n", len(entries)-shown)
			break
		}
		b.WriteString(line)
		used += len(line)
		shown++
	}
	return b.String()
}

// LearnResult is one learn pass's outcome — the CLI's report and the loop
// firing's journaled Reason are both built from it.
type LearnResult struct {
	EntriesScanned int
	NotesWritten   int
	Digest         string // the Learn subagent's own final reply; "" when skipped
}

// Report renders a short, plain, human-readable summary — `cortex learn`'s
// stdout line and the basis for the loop firing's journaled Reason.
func (r LearnResult) Report() string {
	if r.EntriesScanned == 0 {
		return "nothing new since the last learning pass (0 entries scanned)"
	}
	if r.NotesWritten == 0 {
		return fmt.Sprintf("%d entries scanned, nothing worth saving", r.EntriesScanned)
	}
	plural := "s"
	if r.NotesWritten == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d entries scanned, %d note%s written", r.EntriesScanned, r.NotesWritten, plural)
}

// RunLearningPass is the shared learning-loop engine: window the journal
// since the last cursor, and — only if there's anything new — hand it to the
// Learn subagent. Returns a zero-entries LearnResult (no error, no subagent
// call) when there's nothing new to scan, which is both the common repeat-run
// case and the cheapest possible outcome (G4's bounded-cost gate: no model
// call at all beats a bounded one). The cursor only advances on a
// SUCCESSFUL pass (including a NO_INSIGHT one) — a subagent error leaves the
// cursor where it was, so that window is retried next time rather than
// silently dropped.
func RunLearningPass(ctx context.Context, cs *CortexSession, focus string) (LearnResult, error) {
	entries, tail, err := scanCaptureWindow(cs)
	if err != nil {
		return LearnResult{}, err
	}
	if len(entries) == 0 {
		return LearnResult{}, nil
	}

	idx := ""
	if cs.memory != nil {
		idx, _ = cs.memory.Index()
	}
	userIdx := ""
	if cs.userMemory != nil {
		userIdx, _ = cs.userMemory.Index()
	}
	seed := learnSeed(idx, userIdx, entries, focus, cs.SessionsDir())

	before := cs.captures
	digest, _, err := cs.runSubagentStats(ctx, tools.Learn, seed)
	if err != nil {
		return LearnResult{EntriesScanned: len(entries)}, err
	}
	written := cs.captures - before

	cursor := journal.OpenCursor(learnCursorDir(cs))
	if err := cursor.Set(tail); err != nil {
		return LearnResult{}, fmt.Errorf("learn: advance cursor: %w", err)
	}

	return LearnResult{EntriesScanned: len(entries), NotesWritten: written, Digest: digest}, nil
}

// runLearnCLI is `cortex learn [--project <name>]`: a one-shot headless
// learning pass over the current (or named) project's journal since the
// last learn cursor. Always exits 0 — "nothing worth saving" is a normal,
// common outcome, not a failure (docs/learning-loop.md). `cortex learn
// --user` (learn_user.go's userFlag/runLearnUserCLI,
// docs/cross-source-learning.md piece 1) instead runs the machine-wide
// cross-project promotion pass over every registered project's memory
// index, ignoring --project (there is no single project to scope it to).
func runLearnCLI(args []string) {
	if userFlag(args) {
		runLearnUserCLI(args)
		return
	}
	project, _ := parseProjectFlag(args)

	cs := NewCortexSession()
	if err := applyProjectFlag(cs, project); err != nil {
		fmt.Fprintf(os.Stderr, "project %s: %v\n", project, err)
		os.Exit(1)
	}
	cs.quiet = true
	cs.EnableMemory()

	// Closure so the deferred cs.Close() runs before os.Exit (which skips
	// pending defers) — mirrors runTurnCLI's identical shape (cli.go).
	exitCode := func() int {
		defer cs.Close()
		result, err := RunLearningPass(context.Background(), cs, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "learn: %v\n", err)
			return 1
		}
		fmt.Println(result.Report())
		return 0
	}()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// runLearnFiring is RunLoopFiring's kind:"learn" branch (loop_run.go): same
// journaled-outcome contract (exactly one loop.run event on every exit path,
// three-strike auto-disable on repeated failure) as the coder-turn path, but
// running RunLearningPass instead of a Turn. There is no `cortex change`
// branch here (Learn writes memory notes under .cortex/memory, which is
// git-ignored — see CLAUDE.md's ".cortex/ in .gitignore" invariant — not
// source files, so there's nothing to land as a reviewable change) and no
// NEXT/DONE self-pacing marker (Learn's output is a structured result, not a
// free-text reply with a tail marker to parse) — the spec's own
// IntervalMinutes always governs a learn loop's cadence.
func runLearnFiring(ctx context.Context, spec loops.Spec, reg registry.Registry, store loops.Store, newSession sessionFactory) error {
	payload := journal.LoopRunPayload{Name: spec.Name, Project: spec.Project}

	if _, err := reg.Lookup(spec.Project); err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("resolve project %q: %v", spec.Project, err)
		return finalizeLoopFiring(spec, store, payload, 0)
	}

	cs := newSession()
	strikes := cs.Config.loopAutoDisableStrikes()
	if err := applyProjectByName(cs, reg, spec.Project); err != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("apply project: %v", err)
		return finalizeLoopFiring(spec, store, payload, strikes)
	}
	cs.EnableMemory()
	defer cs.Close()

	result, learnErr := RunLearningPass(ctx, cs, spec.Prompt)
	if learnErr != nil {
		payload.Outcome = journal.LoopOutcomeFailed
		payload.Reason = fmt.Sprintf("learn: %v", learnErr)
		return finalizeLoopFiring(spec, store, payload, strikes)
	}

	payload.Outcome = journal.LoopOutcomeSuccess
	payload.Reason = result.Report()
	return finalizeLoopFiring(spec, store, payload, strikes)
}
