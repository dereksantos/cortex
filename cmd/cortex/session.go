package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/fslock"
)

const (
	kindMessage    = "message"
	kindCompaction = "compaction"
	kindState      = "state"
	kindContext    = "context"
	stateVersion   = 1
)

type sessionState struct {
	Version       int                  `json:"version"`
	Base          int                  `json:"base"`
	Frontier      int                  `json:"frontier"`
	TotalTurns    int                  `json:"total_turns"`
	HighWatermark int                  `json:"high_watermark"`
	LowWatermark  int                  `json:"low_watermark"`
	LastTurn      int                  `json:"last_turn"`
	Outline       []cache.OutlineEntry `json:"outline,omitempty"`
	OutlineFolded string               `json:"outline_folded,omitempty"`
}

// contextSample is one measured-vs-estimated context-fill reading, taken after
// every model round-trip (loop.go's onStatusUpdate). LastPromptTokens is the
// real usage.prompt_tokens the provider billed for that call; TailTokensEst is
// what the demotion heuristic (estTurnTokens/TailTokens, chars/4) believes the
// tail is at the same instant. The two are otherwise never compared anywhere:
// demotion fires purely off the estimate (internal/cache.WorkingSet.DemoteBatch),
// so without this sample there is no record of how far the estimate actually
// drifted from what was billed at any given moment in a turn.
type contextSample struct {
	Iteration        int `json:"iteration"`          // model round-trip number within this turn (1-based)
	LastPromptTokens int `json:"last_prompt_tokens"` // actual, from the provider's usage.prompt_tokens
	MaxTokens        int `json:"max_tokens"`         // the completion cap requested for this call
	TailTokensEst    int `json:"tail_tokens_est"`    // estTurnTokens/TailTokens heuristic, same instant
	HighWatermark    int `json:"high_watermark"`
	Window           int `json:"window"`
}

type sessionEntry struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind,omitempty"`
	Turn int       `json:"turn,omitempty"`
	Message

	From     string         `json:"from,omitempty"`
	Coverage float64        `json:"coverage,omitempty"`
	State    *sessionState  `json:"state,omitempty"`
	Context  *contextSample `json:"context,omitempty"`
}

func contextDir() string {
	root := findUp(".cortex")
	if root == "" {
		root = ".cortex"
	}
	return root
}

func sessionsDir() string { return filepath.Join(contextDir(), "sessions") }

// ContextDir returns the session's workspace .cortex directory. Sessions
// built via NewCortexSession carry a resolved Workspace (WorkspaceFromCWD,
// bit-identical to the free contextDir() above); hand-constructed sessions
// (tests build *CortexSession{} literals directly, bypassing
// NewCortexSession) fall back to the free, CWD-implicit function so their
// existing behavior is unchanged.
func (cs *CortexSession) ContextDir() string {
	if cs.workspace != nil {
		return cs.workspace.ContextDir()
	}
	return contextDir()
}

// SessionsDir returns the session's workspace sessions directory — see
// ContextDir's fallback note.
func (cs *CortexSession) SessionsDir() string {
	if cs.workspace != nil {
		return cs.workspace.SessionsDir()
	}
	return sessionsDir()
}

// openTranscript opens a session file and takes an exclusive cross-process
// lock on it (see internal/fslock). A second process that tries to open the
// same session gets a clear "session busy" error instead of silently
// interleaving appends with this one. The lock is released when the returned
// *os.File is closed.
func openTranscript(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := fslock.OpenExclusive(path, flag, perm)
	if err != nil {
		// Surface the busy case distinctly so callers (and users) can tell a
		// genuine lock collision apart from an ordinary open failure.
		if errors.Is(err, fslock.ErrBusy) {
			return nil, fmt.Errorf("session %s is busy (another process has it open): %w", filepath.Base(path), fslock.ErrBusy)
		}
		return nil, err
	}
	return f, nil
}

func (cs *CortexSession) StartTranscript() {
	dir := cs.SessionsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	base := time.Now().Format("20060102-150405")
	id := base
	var f *os.File
	for i := 2; ; i++ {
		var err error
		f, err = openTranscript(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
	cs.SessionID = id
	cs.transcript = f
	for _, m := range cs.Request.Messages {
		cs.writeTranscript(m)
	}
}

// showLoadedContext prints a human-readable summary of what context was loaded.
// Call this right after ResumeTranscript to make the loaded session visible.
func (cs *CortexSession) showLoadedContext(id string) {
	dir := cs.SessionsDir()

	// Get session info for display
	infos, _ := listSessions(dir, 1)
	var info sessionInfo
	if len(infos) > 0 {
		info = infos[0]
	}

	// Calculate demotion state
	demotedTurns := 0
	hydratedTurns := 0
	totalTurns := 0
	if cs.ws != nil {
		demotedTurns = cs.ws.Demoted()
		totalTurns = cs.ws.TotalTurns()
		hydratedTurns = totalTurns - demotedTurns
	}

	// Build context summary
	msgCount := len(cs.Request.Messages)

	// Only show demotion info if we have turns (not a fresh session)
	if totalTurns > 0 {
		fmt.Printf("%s  %d turns (%d demoted, %d hydrated tail)\n",
			withColor("context:", green),
			totalTurns, demotedTurns, hydratedTurns)
	}

	// Show message count
	fmt.Printf("%s  %d messages\n",
		withColor("messages:", green),
		msgCount)

	// Show session age if available
	if info.ModTime.IsZero() {
		fmt.Printf("%s  %s\n",
			withColor("session:", gray),
			withColor(id, cyan))
	} else {
		age := relTime(info.ModTime)
		fmt.Printf("%s  %s (%s old)\n",
			withColor("session:", gray),
			withColor(id, cyan),
			withColor(age, gray))
	}
}

func (cs *CortexSession) ResumeTranscript(id string) error {
	dir := cs.SessionsDir()
	if id == "" {
		var err error
		if id, err = latestSessionID(dir); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := openTranscript(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", path, err)
	}
	msgs, turns, state, err := loadSession(path)
	if err != nil {
		f.Close()
		return err
	}
	if len(msgs) == 0 {
		f.Close()
		return fmt.Errorf("session %s is empty", id)
	}
	cs.Request.Messages = msgs
	cs.ws, cs.turns = cs.replayWorkingSet(msgs, turns)
	cs.outline = nil
	cs.outlineFolded = ""
	if state != nil {
		// A process can stop after appending part of a turn but before its state
		// checkpoint. In that case the latest checkpoint is stale: replay the
		// transcript conservatively rather than making the session unresumable.
		_ = cs.restoreSessionState(*state)
	}
	cs.SessionID = id
	cs.transcript = f
	return nil
}

func latestSessionID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no sessions at %s: %w", dir, err)
	}
	latest := ""
	for _, e := range entries {
		if name := e.Name(); !e.IsDir() && strings.HasSuffix(name, ".jsonl") && name > latest {
			latest = name
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no sessions found in %s", dir)
	}
	return strings.TrimSuffix(latest, ".jsonl"), nil
}

type sessionInfo struct {
	ID       string
	ModTime  time.Time
	Messages int
	First    string
}

func listSessions(dir string, limit int) ([]sessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("no sessions at %s: %w", dir, err)
	}
	var out []sessionInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info := sessionInfo{ID: strings.TrimSuffix(name, ".jsonl")}
		if fi, ferr := e.Info(); ferr == nil {
			info.ModTime = fi.ModTime()
		}
		if msgs, _, _, lerr := loadSession(filepath.Join(dir, name)); lerr == nil {
			for _, m := range msgs {
				if m.Role != RoleUser && m.Role != "assistant" {
					continue
				}
				info.Messages++
				if m.Role == RoleUser && info.First == "" && strings.TrimSpace(m.Content) != "" {
					info.First = firstLine(m.Content)
				}
			}
		}
		out = append(out, info)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func invokedName() string {
	if len(os.Args) > 0 {
		if b := filepath.Base(os.Args[0]); b != "" && b != "." && b != "/" {
			return b
		}
	}
	return "cortex"
}

func loadTranscript(path string) ([]Message, []int, error) {
	msgs, turns, _, err := loadSession(path)
	return msgs, turns, err
}

func loadSession(path string) ([]Message, []int, *sessionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read session: %w", err)
	}
	var msgs []Message
	var turns []int
	var state *sessionState
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, nil, nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		if e.Kind == "" || e.Kind == kindMessage {
			msgs = append(msgs, e.Message)
			turns = append(turns, e.Turn)
		} else if e.Kind == kindState && e.State != nil {
			copy := *e.State
			state = &copy
		}
	}
	return msgs, turns, state, nil
}

func (cs *CortexSession) writeEntry(e sessionEntry) {
	if cs.transcript == nil {
		return
	}
	e.TS = time.Now()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	cs.transcript.Write(append(b, '\n'))
}

func (cs *CortexSession) writeTranscript(m Message) {
	cs.writeEntry(sessionEntry{Kind: kindMessage, Turn: cs.turnNo, Message: m})
}

func (cs *CortexSession) writeSessionState() {
	if cs.ws == nil || cs.transcript == nil {
		return
	}
	high, low := cs.ws.GetWatermarks()
	cs.writeEntry(sessionEntry{Kind: kindState, State: &sessionState{
		Version: stateVersion, Base: cs.ws.Base(), Frontier: cs.ws.Demoted(),
		TotalTurns: cs.ws.TotalTurns(), HighWatermark: high, LowWatermark: low,
		LastTurn: cs.turns, Outline: append([]cache.OutlineEntry(nil), cs.outline...),
		OutlineFolded: cs.outlineFolded,
	}})
}

// writeContextSample records one measured-vs-estimated context-fill reading.
// tailEstNow is the caller's estTurnTokens/TailTokens estimate at the same
// instant lastPromptTokens was billed — callers mid-turn must include the
// in-progress turn's own not-yet-AddTurn'd messages, since cs.ws.TailTokens()
// alone only covers turns already recorded.
func (cs *CortexSession) writeContextSample(iteration, lastPromptTokens, maxTokens, tailEstNow int) {
	if cs.transcript == nil {
		return
	}
	high := 0
	if cs.ws != nil {
		high, _ = cs.ws.GetWatermarks()
	}
	cs.writeEntry(sessionEntry{Kind: kindContext, Turn: cs.turnNo, Context: &contextSample{
		Iteration: iteration, LastPromptTokens: lastPromptTokens, MaxTokens: maxTokens,
		TailTokensEst: tailEstNow, HighWatermark: high, Window: cs.windowSize(),
	}})
}

func (cs *CortexSession) restoreSessionState(state sessionState) error {
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported version %d", state.Version)
	}
	if state.Base != cs.ws.Base() || state.TotalTurns != cs.ws.TotalTurns() {
		return fmt.Errorf("snapshot does not match transcript (base %d/%d, turns %d/%d)", state.Base, cs.ws.Base(), state.TotalTurns, cs.ws.TotalTurns())
	}
	if state.LastTurn != cs.turns {
		return fmt.Errorf("snapshot last turn %d does not match transcript %d", state.LastTurn, cs.turns)
	}
	if err := cs.ws.RestoreState(state.Frontier, state.HighWatermark, state.LowWatermark); err != nil {
		return err
	}
	cs.outline = append([]cache.OutlineEntry(nil), state.Outline...)
	cs.outlineFolded = state.OutlineFolded
	if len(cs.outline) > 0 || cs.outlineFolded != "" {
		cs.Request.OutlineBlock = cs.renderOutlineBlock()
	}
	cs.Request.PrefixEnd = cs.ws.Base()
	cs.Request.TailFrom = cs.ws.FrontierMsg()
	return nil
}

var compactSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
	return cs.SummarizeText(ctx, content, compactGoal, window)
}

func (cs *CortexSession) contextRatio() float64 {
	return float64(cs.LastPromptTokens) / float64(cs.windowSize())
}

func (cs *CortexSession) compactPrefix() (end int, content string, ok bool) {
	if cs.ws == nil {
		return 0, "", false
	}
	spans := cs.ws.TurnSpans()
	// Keep the newest completed turn verbatim. Compacting only older complete
	// turns preserves the active seam and cannot split a tool-call sequence.
	if len(spans) < 2 {
		return 0, "", false
	}
	end = spans[len(spans)-2].End
	if end <= cs.ws.Base() || end > len(cs.Request.Messages) {
		return 0, "", false
	}
	var b strings.Builder
	// Include the existing state layer (messages before base, excluding the
	// system seed) so repeated compactions fold old state into the new digest
	// instead of discarding it. Eligibility is still determined only by
	// completed working-set turns.
	for _, msg := range cs.Request.Messages[1:end] {
		b.WriteString(msg.Role)
		b.WriteString("\n")
		b.WriteString(msg.Content)
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(&b, "\n  tool %s %s", call.Function.Name, call.Function.Arguments)
		}
		b.WriteString("\n\n")
	}
	return end, b.String(), true
}

func (cs *CortexSession) Compact(ctx context.Context) error {
	if cs.transcript == nil || cs.SessionID == "" {
		return fmt.Errorf("no transcript to compact (unpersisted session)")
	}
	if cs.ws == nil {
		return fmt.Errorf("working set unavailable; nothing to compact yet")
	}
	base := cs.ws.Base()
	end, content, ok := cs.compactPrefix()
	if !ok {
		return fmt.Errorf("fewer than two completed turns; nothing to compact yet")
	}
	window := cs.windowSize() / 4
	if sw := cs.studyWindow(); sw < window {
		window = sw
	}
	digest, compressed, err := compactSummarize(ctx, cs, content, window)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	if !compressed {
		return fmt.Errorf("session fits within the %s-token digest budget; nothing to compact yet", humanK(window))
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return fmt.Errorf("compact: summarizer returned an empty digest")
	}

	from := cs.SessionID
	// Build the replacement completely before touching the live session. The
	// raw source transcript remains immutable and is linked by the compaction
	// entry, so recall citations into it remain valid.
	sys := cs.Request.Messages[0]
	summary := Message{
		Role:    RoleUser,
		Content: "[Session state — compacted summary of earlier completed turns from @session/" + from + ". Continue from this state; use study on that transcript if raw detail is needed.]\n\n" + digest,
	}
	kept := append([]Message(nil), cs.Request.Messages[end:]...)
	keptTurns := make([]int, len(kept))
	for i := range keptTurns {
		keptTurns[i] = cs.turns
	}
	coverage := float64(end-base) / float64(len(cs.Request.Messages)-base)
	cs.transcript.Close()
	cs.transcript = nil
	cs.Request.Messages = []Message{sys, summary}
	cs.StartTranscript()
	cs.writeEntry(sessionEntry{Kind: kindCompaction, From: from, Coverage: coverage})
	for i, msg := range kept {
		cs.turnNo = keptTurns[i]
		cs.Append(msg)
	}
	cs.turnNo = 0
	cs.ws, _ = cs.replayWorkingSet(cs.Request.Messages, append([]int{0, 0}, keptTurns...))
	cs.outline = nil
	cs.outlineFolded = ""
	cs.Request.OutlineBlock = ""
	cs.Request.PrefixEnd = cs.ws.Base()
	cs.Request.TailFrom = cs.ws.FrontierMsg()
	cs.LastPromptTokens = cs.currentContextSize()
	cs.writeSessionState()
	return nil
}

func (cs *CortexSession) printSessions() {
	infos, err := listSessions(cs.SessionsDir(), 15)
	if err != nil || len(infos) == 0 {
		fmt.Println(withColor("no sessions found", gray))
		return
	}
	for _, s := range infos {
		marker := "  "
		if s.ID == cs.SessionID {
			marker = withColor("✦ ", green)
		}
		preview := s.First
		if preview == "" {
			preview = "(no prompt)"
		}
		if r := []rune(preview); len(r) > 60 {
			preview = string(r[:60]) + "…"
		}
		fmt.Printf("%s%s  %-8s  %2d msgs  %s\n", marker, s.ID, relTime(s.ModTime), s.Messages, preview)
	}
	fmt.Println(withColor(fmt.Sprintf("resume at startup: %s resume <id>", invokedName()), gray))
}

func (cs *CortexSession) Clear() {
	if cs.transcript != nil {
		cs.transcript.Close()
		cs.transcript = nil
	}
	old := cs.Request
	cs.Request = (CortexArgs{}).Request()
	cs.Request.Model = old.Model
	cs.Request.BaseURL = old.BaseURL
	cs.Request.APIKey = old.APIKey
	cs.Request.ChatTemplateKwargs = old.ChatTemplateKwargs
	cs.Request.Reasoning = old.Reasoning
	cs.Request.Dialect = old.Dialect
	cs.Request.Effort = old.Effort
	cs.Request.MaxTokens = old.MaxTokens
	cs.ws = cs.newWorkingSet(1)
	cs.outline = nil
	cs.outlineFolded = ""
	cs.Request.OutlineBlock = ""
	cs.Request.PrefixEnd = 0
	cs.Request.TailFrom = 0
	// A cleared session is a fresh conversation: stamp its transcript from 1.
	cs.turns = 0
	cs.LastPromptTokens = 0
	cs.StartTranscript()
}
