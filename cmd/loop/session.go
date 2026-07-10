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
)

const (
	kindMessage    = "message"
	kindCompaction = "compaction"
	kindState      = "state"
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

type sessionEntry struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind,omitempty"`
	Turn int       `json:"turn,omitempty"`
	Message

	From     string        `json:"from,omitempty"`
	Coverage float64       `json:"coverage,omitempty"`
	State    *sessionState `json:"state,omitempty"`
}

func contextDir() string {
	root := findUp(".cortex")
	if root == "" {
		root = ".cortex"
	}
	return root
}

func sessionsDir() string { return filepath.Join(contextDir(), "sessions") }

func (cs *CortexSession) StartTranscript() {
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	base := time.Now().Format("20060102-150405")
	id := base
	var f *os.File
	for i := 2; ; i++ {
		var err error
		f, err = os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
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
	dir := sessionsDir()

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
	dir := sessionsDir()
	if id == "" {
		var err error
		if id, err = latestSessionID(dir); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, id+".jsonl")
	msgs, turns, state, err := loadSession(path)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return fmt.Errorf("session %s is empty", id)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", path, err)
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
	return "loop"
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

var compactSummarize = func(ctx context.Context, cs *CortexSession, path string, window int) (string, bool, error) {
	return cs.Summarize(ctx, path, compactGoal, window)
}

func (cs *CortexSession) contextRatio() float64 {
	return float64(cs.LastPromptTokens) / float64(cs.windowSize())
}

func (cs *CortexSession) Compact(ctx context.Context) error {
	if cs.transcript == nil || cs.SessionID == "" {
		return fmt.Errorf("no transcript to compact (unpersisted session)")
	}
	path := filepath.Join(sessionsDir(), cs.SessionID+".jsonl")
	window := cs.windowSize() / 4
	if sw := cs.studyWindow(); sw < window {
		window = sw
	}
	digest, compressed, err := compactSummarize(ctx, cs, path, window)
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

	sys := cs.Request.Messages[0]
	summary := Message{
		Role:    RoleUser,
		Content: "[Compacted session — summary of the conversation so far. Continue from this state.]\n\n" + digest,
	}
	from := cs.SessionID
	cs.transcript.Close()
	cs.transcript = nil
	cs.Request.Messages = []Message{sys}
	cs.StartTranscript()
	cs.writeEntry(sessionEntry{Kind: kindCompaction, From: from})
	cs.Append(summary)
	cs.ws = cs.newWorkingSet(2)
	cs.outline = nil
	cs.outlineFolded = ""
	cs.Request.OutlineBlock = ""
	cs.Request.PrefixEnd = 0
	cs.Request.TailFrom = 0
	cs.LastPromptTokens = 0
	return nil
}

func (cs *CortexSession) printSessions() {
	infos, err := listSessions(sessionsDir(), 15)
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
