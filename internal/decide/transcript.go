package decide

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// The replay side: read a saved session transcript (.cortex/sessions/<id>.jsonl)
// and cut it into the decision points a live decider would have seen — one
// after every tool batch, plus the final reply — each paired with what the
// agent actually did next. A Decider is a pure function of a Snapshot, so the
// same questions can be asked of any stored run without re-running the agent.

// Snapshot clipping bounds. The decider sees a digest, never the full context.
const (
	maxTaskChars   = 1500
	maxArgsChars   = 200
	maxResultChars = 300
	maxReplyChars  = 2500
	maxCommandChar = 160
	maxLastLine    = 120
	// RecentSteps is how many trailing steps a Snapshot carries verbatim.
	RecentSteps = 12
)

// transcriptLine is the subset of a session JSONL line the replay reads.
type transcriptLine struct {
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	Turn       int       `json:"turn"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	ToolCallID string    `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// Call is one tool call with its observation, as recorded.
type Call struct {
	ID     string
	Tool   string
	Args   map[string]any
	RawArg string
	Result string
}

// Batch is one model round that asked for tools. End is when its last tool
// result was recorded — the moment a live decider would start, and (by the
// engine's construction) the moment the coder's next send goes out.
type Batch struct {
	Calls []Call
	End   time.Time
}

// TurnRecord is one turn of a transcript: the task, its tool batches in
// order, and the final reply ("" when the turn never answered).
type TurnRecord struct {
	Turn    int
	Task    string
	Batches []Batch
	Reply   string
	ReplyAt time.Time
}

// ReadTranscript parses a session transcript into turns.
func ReadTranscript(path string) ([]TurnRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer f.Close()
	return parseTranscript(f)
}

func parseTranscript(r io.Reader) ([]TurnRecord, error) {
	var turns []TurnRecord
	var cur *TurnRecord
	pending := map[string]*Call{} // tool_call_id → call awaiting its result
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ln transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue // a torn or foreign line is skipped, never fatal
		}
		if ln.Kind != "message" || ln.Turn == 0 {
			continue
		}
		if cur == nil || ln.Turn != cur.Turn {
			turns = append(turns, TurnRecord{Turn: ln.Turn})
			cur = &turns[len(turns)-1]
			clear(pending)
		}
		switch ln.Role {
		case "user":
			if cur.Task == "" {
				cur.Task = ln.Content // later user lines are harness notes
			}
		case "assistant":
			if len(ln.ToolCalls) == 0 {
				cur.Reply, cur.ReplyAt = ln.Content, ln.TS
				continue
			}
			b := Batch{End: ln.TS}
			for _, tc := range ln.ToolCalls {
				c := Call{ID: tc.ID, Tool: tc.Function.Name, RawArg: tc.Function.Arguments}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &c.Args)
				b.Calls = append(b.Calls, c)
			}
			cur.Batches = append(cur.Batches, b)
			last := &cur.Batches[len(cur.Batches)-1]
			for i := range last.Calls {
				pending[last.Calls[i].ID] = &last.Calls[i]
			}
		case "tool":
			if c, ok := pending[ln.ToolCallID]; ok {
				c.Result = ln.Content
				delete(pending, ln.ToolCallID)
				if n := len(cur.Batches); n > 0 && ln.TS.After(cur.Batches[n-1].End) {
					cur.Batches[n-1].End = ln.TS
				}
			}
		}
	}
	return turns, sc.Err()
}

// Point is one decision point cut from a turn.
type Point struct {
	Turn int
	// Round is the 1-based index of the batch the point follows; for the
	// finalize point it is len(Batches)+1.
	Round    int
	Final    bool // the finalize point (claim_check)
	Snapshot Snapshot
	// Actual is what the agent did next: the moves of the next batch's calls,
	// or [finalize] when it answered. Empty for the finalize point.
	Actual      []string
	ActualTools []string
	// Window is how long a decision started at this point has before the
	// first send that could carry it. The engine sends the very next request
	// the moment a batch ends, so a decision can never reach that one; the
	// earliest it can reach is the send after the agent's next round — Window
	// is that round's length (next batch end − this batch end), or the time to
	// the reply when the next round is the answer. Zero when unknown (no
	// timestamps, or the finalize point).
	Window time.Duration
}

// Points cuts a turn into decision points: one after each batch (the agent's
// next round is Actual), then the finalize point when the turn answered.
func Points(t TurnRecord) []Point {
	var pts []Point
	var steps []Step
	for i, b := range t.Batches {
		for _, c := range b.Calls {
			steps = append(steps, stepOf(len(steps)+1, c))
		}
		p := Point{Turn: t.Turn, Round: i + 1, Snapshot: snapshotOf(t.Task, steps, t.Batches[:i+1], "")}
		if i+1 < len(t.Batches) {
			for _, c := range t.Batches[i+1].Calls {
				p.Actual = appendUnique(p.Actual, MoveOf(c))
				p.ActualTools = append(p.ActualTools, c.Tool)
			}
			p.Window = window(b.End, t.Batches[i+1].End)
		} else if t.Reply != "" {
			p.Actual = []string{MoveFinalize}
			p.Window = window(b.End, t.ReplyAt)
		}
		pts = append(pts, p)
	}
	if t.Reply != "" {
		pts = append(pts, Point{
			Turn:     t.Turn,
			Round:    len(t.Batches) + 1,
			Final:    true,
			Snapshot: snapshotOf(t.Task, steps, t.Batches, t.Reply),
		})
	}
	return pts
}

func window(from, to time.Time) time.Duration {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0
	}
	return to.Sub(from)
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

// BuildSnapshot is the live path's snapshot: the same digest a replay point
// carries, built from a turn's task, its tool batches so far, and (at
// finalize) the reply. Cheap enough to build synchronously at a batch end.
func BuildSnapshot(task string, batches []Batch, reply string) Snapshot {
	var steps []Step
	for _, b := range batches {
		for _, c := range b.Calls {
			steps = append(steps, stepOf(len(steps)+1, c))
		}
	}
	return snapshotOf(task, steps, batches, reply)
}

// MovesOf maps a batch onto the next_move vocabulary (distinct, in order).
func MovesOf(b Batch) []string {
	var out []string
	for _, c := range b.Calls {
		out = appendUnique(out, MoveOf(c))
	}
	return out
}

func snapshotOf(task string, steps []Step, batches []Batch, reply string) Snapshot {
	recent := steps
	if len(recent) > RecentSteps {
		recent = recent[len(recent)-RecentSteps:]
	}
	return Snapshot{
		Task:      clip(task, maxTaskChars),
		Steps:     slices.Clone(recent),
		StepsSeen: len(steps),
		Ledger:    ledgerOf(batches),
		Reply:     clip(reply, maxReplyChars),
	}
}

func stepOf(n int, c Call) Step {
	return Step{
		ID:     fmt.Sprintf("s%d", n),
		Tool:   c.Tool,
		Args:   clip(oneLine(c.RawArg), maxArgsChars),
		Result: clipHeadTail(oneLine(c.Result), maxResultChars),
		Err:    IsFailure(c.Result),
	}
}

func ledgerOf(batches []Batch) Ledger {
	l := Ledger{ToolCounts: map[string]int{}}
	for _, b := range batches {
		for _, c := range b.Calls {
			l.ToolCounts[c.Tool]++
			failed := IsFailure(c.Result)
			if failed {
				l.Errors++
			}
			if MoveOf(c) == MoveEdit && !failed {
				if p := argString(c.Args, "path", "file_path"); p != "" {
					l.FilesEdited = appendUnique(l.FilesEdited, p)
				}
			}
			if c.Tool == "bash" {
				cmd := clip(oneLine(argString(c.Args, "command")), maxCommandChar)
				if failed {
					cmd += "  [did not run or failed]"
				} else if last := lastLine(c.Result); last != "" {
					cmd += "  => " + clip(last, maxLastLine)
				}
				l.Commands = append(l.Commands, cmd)
			}
		}
	}
	return l
}

// IsFailure reports whether an observation is a failed or refused step: the
// engine's own error prefix, a shell-gate refusal, or a nonzero shell exit.
func IsFailure(obs string) bool {
	s := strings.TrimSpace(obs)
	for _, p := range []string{"Error:", "error:", "blocked (", "refused", "declined"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return strings.Contains(s, "[exit error")
}

// MoveOf maps a recorded tool call onto the next_move vocabulary.
func MoveOf(c Call) string {
	switch c.Tool {
	case "read_file":
		return MoveRead
	case "outline", "grep", "study", "scan_landscape", "web_search", "fetch_url":
		return MoveSearch
	case "edit_file", "write_file", "apply_patch", "remove_path", "agent":
		return MoveEdit
	case "recall", "memory_read", "memory_search":
		return MoveRecall
	case "bash":
		return shellMove(argString(c.Args, "command"))
	}
	return MoveOther
}

// shellMove classifies a shell command: an inspection command (by its first
// word, after any leading `cd <dir>`) is read or search; otherwise a command
// any of whose words names a test or check is test; anything else is run.
// Ecosystem-agnostic by construction: it looks at word shapes, not at any one
// tool — and only whole-word shapes, so a path like /workspace/testdata is not
// a test run.
func shellMove(cmd string) string {
	fields := strings.FieldsFunc(strings.ToLower(cmd), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '|' || r == '&' || r == '/' || r == '"' || r == '\''
	})
	for len(fields) >= 2 && fields[0] == "cd" {
		fields = fields[2:]
	}
	if len(fields) == 0 {
		return MoveRun
	}
	switch fields[0] {
	case "cat", "head", "tail", "sed", "less", "nl", "wc":
		return MoveRead
	case "grep", "rg", "find", "ls", "tree", "fd", "ag", "git":
		return MoveSearch
	}
	for _, f := range fields {
		if isTestWord(f) {
			return MoveTest
		}
	}
	return MoveRun
}

func isTestWord(f string) bool {
	switch f {
	case "test", "tests", "check", "spec", "tox":
		return true
	}
	return strings.Contains(f, "pytest") || strings.Contains(f, "runtests") ||
		strings.HasPrefix(f, "test_") || strings.HasPrefix(f, "test.") ||
		strings.HasSuffix(f, "_test") || strings.Contains(f, "_test.") ||
		strings.HasSuffix(f, "_spec") || strings.Contains(f, "_spec.")
}

func argString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok {
			return v
		}
	}
	return ""
}

// clipHeadTail keeps the start and the end of s: tool output states what ran
// first and its verdict (a summary, an exit status) last.
func clipHeadTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head := clip(s, n/3)
	tail := s[len(s)-(n-n/3):]
	for len(tail) > 0 && !utf8RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return head + " … " + tail
}

// lastLine is the last non-blank line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, " \t\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
