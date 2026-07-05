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
)

const (
	kindMessage    = "message"
	kindCompaction = "compaction"
)

type sessionEntry struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind,omitempty"`
	Turn int       `json:"turn,omitempty"`
	Message

	From     string  `json:"from,omitempty"`
	Coverage float64 `json:"coverage,omitempty"`
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

func (cs *CortexSession) ResumeTranscript(id string) error {
	dir := sessionsDir()
	if id == "" {
		var err error
		if id, err = latestSessionID(dir); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, id+".jsonl")
	msgs, turns, err := loadTranscript(path)
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
		if msgs, _, lerr := loadTranscript(filepath.Join(dir, name)); lerr == nil {
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read session: %w", err)
	}
	var msgs []Message
	var turns []int
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, nil, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		if e.Kind == "" || e.Kind == kindMessage {
			msgs = append(msgs, e.Message)
			turns = append(turns, e.Turn)
		}
	}
	return msgs, turns, nil
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
	cs.Request.TailFrom = 0
	cs.LastPromptTokens = 0
	cs.StartTranscript()
}
