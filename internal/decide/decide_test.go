package decide

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var testAsks = []Ask{
	{Q: NextMove, Choices: Moves},
	{Q: Looping, Choices: []string{"s1", "s2", "s3", None}, Multi: true},
}

func TestParseAnswers(t *testing.T) {
	tests := []struct {
		name    string
		reply   string
		want    map[Question][]string
		wantErr string
	}{
		{name: "plain", reply: `{"next_move":"test","looping":["none"]}`,
			want: map[Question][]string{NextMove: {"test"}, Looping: {"none"}}},
		{name: "fenced with prose", reply: "Thinking...\n```json\n{\"next_move\": \"edit\", \"looping\": [\"s2\",\"s3\"]}\n```",
			want: map[Question][]string{NextMove: {"edit"}, Looping: {"s2", "s3"}}},
		{name: "last object wins", reply: `example {"next_move":"read"} answer {"next_move":"finalize","looping":"none"}`,
			want: map[Question][]string{NextMove: {"finalize"}, Looping: {"none"}}},
		{name: "brace inside string", reply: `{"next_move":"run","looping":["none"],"note":"a } here"}`,
			want: map[Question][]string{NextMove: {"run"}, Looping: {"none"}}},
		{name: "off menu", reply: `{"next_move":"refactor","looping":["none"]}`, wantErr: "not one of the choices"},
		{name: "missing question", reply: `{"next_move":"read"}`, wantErr: "missing answer for looping"},
		{name: "two picks for single", reply: `{"next_move":["read","edit"],"looping":["none"]}`, wantErr: "want one choice"},
		{name: "none mixed", reply: `{"next_move":"read","looping":["s1","none"]}`, wantErr: "mixed"},
		{name: "empty list", reply: `{"next_move":"read","looping":[]}`, wantErr: "empty answer"},
		{name: "no object", reply: "I think it should test.", wantErr: "no JSON object"},
		{name: "wrong type", reply: `{"next_move":3,"looping":["none"]}`, wantErr: "neither a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAnswers(tt.reply, testAsks)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, a := range got {
				if !slices.Equal(a.Picks, tt.want[a.Q]) {
					t.Errorf("%s = %v, want %v", a.Q, a.Picks, tt.want[a.Q])
				}
			}
		})
	}
}

func TestChatDecide(t *testing.T) {
	okReply := `{"next_move":"test","looping":["none"]}`
	tests := []struct {
		name       string
		content    string
		reasoning  string
		err        error
		block      bool // the completer waits for ctx to end
		maxCalls   int
		priorCalls int
		wantStatus string
	}{
		{name: "ok", content: okReply, wantStatus: StatusOK},
		{name: "answer in reasoning field", reasoning: "let me see... " + okReply, wantStatus: StatusOK},
		{name: "rate limited", err: errors.New("decide (429): Provider returned error"), wantStatus: StatusRateLimited},
		{name: "timeout", block: true, wantStatus: StatusTimeout},
		{name: "transport error", err: errors.New("connection refused"), wantStatus: StatusError},
		{name: "off menu", content: `{"next_move":"think","looping":["none"]}`, wantStatus: StatusParseError},
		{name: "over budget", content: okReply, maxCalls: 1, priorCalls: 1, wantStatus: StatusBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := CompleterFunc(func(ctx context.Context, prompt, system string) (string, string, error) {
				calls++
				if system != SystemPrompt {
					t.Errorf("system prompt not passed through")
				}
				if tt.block {
					<-ctx.Done()
					return "", "", ctx.Err()
				}
				return tt.content, tt.reasoning, tt.err
			})
			d := NewChatWith(c, ChatConfig{Model: "m", Timeout: 20 * time.Millisecond, MaxCalls: tt.maxCalls})
			d.calls.Store(int64(tt.priorCalls))
			got := d.Decide(context.Background(), Snapshot{Task: "t"}, testAsks)
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Detail, tt.wantStatus)
			}
			if got.Status == StatusOK {
				if a, ok := got.Pick(NextMove); !ok || a.Picks[0] != "test" {
					t.Errorf("next_move = %v, want test", a.Picks)
				}
			} else if len(got.Answers) != 0 {
				t.Errorf("non-ok decision carries answers: %v", got.Answers)
			}
			if tt.wantStatus == StatusBudget && calls != 0 {
				t.Errorf("over budget made %d calls, want 0", calls)
			}
			if got.Model != "m" || got.Backend != "chat" || got.PromptVersion == "" {
				t.Errorf("decision missing identity: %+v", got)
			}
		})
	}
}

func TestNullAndLogprobNeverAnswer(t *testing.T) {
	for _, d := range []Decider{Null{}, Logprob{}} {
		t.Run(d.Name(), func(t *testing.T) {
			got := d.Decide(context.Background(), Snapshot{}, testAsks)
			if got.Status == StatusOK || len(got.Answers) != 0 {
				t.Errorf("%s answered: %+v", d.Name(), got)
			}
		})
	}
}

func TestShellMove(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"cat /workspace/testbed/a.txt", MoveRead},
		{"cd /testbed && cat x", MoveRead},
		{"find /testbed -name x | head", MoveSearch},
		{"grep -rn foo src", MoveSearch},
		{"python -m pytest tests/unit -x", MoveTest},
		{"cd /testbed && make test", MoveTest},
		{"./runtests.sh", MoveTest},
		{"node test_repro.js", MoveTest},
		{"python /testbed/reproduce.py", MoveRun},
		{"pip install -e .", MoveRun},
		{"", MoveRun},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := shellMove(tt.cmd); got != tt.want {
				t.Errorf("shellMove(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

const sampleTranscript = `{"kind":"message","role":"system","content":"sys"}
{"kind":"message","turn":1,"role":"user","content":"Fix the parser bug."}
{"kind":"context","turn":1,"context":{"iteration":1}}
{"kind":"message","turn":1,"role":"assistant","content":"","tool_calls":[{"id":"a","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"parse\"}"}}]}
{"ts":"2026-10-14T00:00:02Z","kind":"message","turn":1,"role":"tool","tool_call_id":"a","content":"src/p.txt:3: parse"}
{"kind":"message","turn":1,"role":"assistant","content":"","tool_calls":[{"id":"b","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"src/p.txt\",\"old_string\":\"x\",\"new_string\":\"y\"}"}},{"id":"c","type":"function","function":{"name":"bash","arguments":"{\"command\":\"make test\"}"}}]}
{"kind":"message","turn":1,"role":"tool","tool_call_id":"b","content":"edited src/p.txt"}
{"ts":"2026-10-14T00:00:09Z","kind":"message","turn":1,"role":"tool","tool_call_id":"c","content":"blocked (risk: classifier unavailable)"}
{"kind":"message","turn":1,"role":"user","content":"Harness note: wrap up."}
{"ts":"2026-10-14T00:00:12Z","kind":"message","turn":1,"role":"assistant","content":"Fixed the bug and all tests pass."}
{"kind":"state","state":{}}
not json at all
`

func TestTranscriptPoints(t *testing.T) {
	turns, err := parseTranscript(strings.NewReader(sampleTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	tr := turns[0]
	if tr.Task != "Fix the parser bug." {
		t.Errorf("task = %q (a harness note must not replace it)", tr.Task)
	}
	pts := Points(tr)
	if len(pts) != 3 {
		t.Fatalf("points = %d, want 2 batches + finalize", len(pts))
	}
	if !slices.Equal(pts[0].Actual, []string{MoveEdit, MoveTest}) {
		t.Errorf("point 1 actual = %v, want [edit test]", pts[0].Actual)
	}
	if !slices.Equal(pts[1].Actual, []string{MoveFinalize}) {
		t.Errorf("point 2 actual = %v, want [finalize]", pts[1].Actual)
	}
	if pts[0].Window != 7*time.Second || pts[1].Window != 3*time.Second || pts[2].Window != 0 {
		t.Errorf("windows = %v %v %v, want 7s 3s 0", pts[0].Window, pts[1].Window, pts[2].Window)
	}
	final := pts[2]
	if !final.Final || final.Snapshot.Reply == "" {
		t.Fatalf("last point is not the finalize point: %+v", final)
	}
	l := final.Snapshot.Ledger
	if !slices.Equal(l.FilesEdited, []string{"src/p.txt"}) || l.Errors != 1 || len(l.Commands) != 1 ||
		!strings.Contains(l.Commands[0], "did not run") {
		t.Errorf("ledger = %+v", l)
	}
	if s := final.Snapshot.Steps; len(s) != 3 || !s[2].Err || s[0].ID != "s1" {
		t.Errorf("steps = %+v", s)
	}
	prompt := RenderPrompt(final.Snapshot, []Ask{{Q: ClaimCheck, Choices: ClaimChoices}})
	for _, want := range []string{"Fix the parser bug.", "FAILED", "files changed: src/p.txt", "all tests pass", "supported, unsupported, no_claims"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSnapshotKeepsOnlyRecentSteps(t *testing.T) {
	var b []Batch
	for i := 0; i < RecentSteps+5; i++ {
		b = append(b, Batch{Calls: []Call{{ID: "x", Tool: "read_file", RawArg: `{"path":"a"}`, Result: "ok"}}})
	}
	pts := Points(TurnRecord{Turn: 1, Task: "t", Batches: b})
	last := pts[len(pts)-1].Snapshot
	if len(last.Steps) != RecentSteps || last.StepsSeen != RecentSteps+5 {
		t.Errorf("steps = %d seen = %d", len(last.Steps), last.StepsSeen)
	}
	if !strings.Contains(RenderPrompt(last, testAsks), "the last 12 of 17 steps") {
		t.Errorf("prompt does not say the record is a suffix")
	}
}

// TestEvalCasesWellFormed: every expected pick is offered, and a decider that
// answers exactly the expectation scores perfectly (and "none" for a loop
// case scores wrong).
func TestEvalCasesWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range EvalCases() {
		t.Run(c.ID, func(t *testing.T) {
			if seen[c.ID] {
				t.Fatalf("duplicate id")
			}
			seen[c.ID] = true
			var answers []Answer
			for _, a := range c.Asks {
				if a.Q == Looping {
					pick := []string{None}
					if c.Loop == "yes" {
						pick = []string{c.Snapshot.Steps[len(c.Snapshot.Steps)-1].ID}
					} else if c.Loop != "no" {
						t.Fatalf("looping ask without a Loop expectation")
					}
					answers = append(answers, Answer{Q: a.Q, Picks: pick})
					continue
				}
				acc := c.Accept[a.Q]
				if len(acc) == 0 {
					t.Fatalf("%s has no accepted picks", a.Q)
				}
				for _, p := range acc {
					if !slices.Contains(a.Choices, p) {
						t.Fatalf("%s accepts %q, which is not offered", a.Q, p)
					}
				}
				answers = append(answers, Answer{Q: a.Q, Picks: acc[:1]})
			}
			for q, ok := range c.Score(Decision{Status: StatusOK, Answers: answers}) {
				if !ok {
					t.Errorf("perfect answer scored wrong on %s", q)
				}
			}
		})
	}
	if len(seen) < 10 {
		t.Errorf("eval set has %d cases, want at least 10", len(seen))
	}
}

func TestClipKeepsVerdict(t *testing.T) {
	out := "collecting ... " + strings.Repeat("noise ", 200) + "=== 12 passed in 0.31s ==="
	got := clipHeadTail(out, maxResultChars)
	if len(got) > maxResultChars+10 || !strings.HasPrefix(got, "collecting") || !strings.HasSuffix(got, "12 passed in 0.31s ===") {
		t.Errorf("clipHeadTail lost the head or the verdict: %q", got)
	}
	l := ledgerOf([]Batch{{Calls: []Call{{Tool: "bash", Args: map[string]any{"command": "make check"}, Result: "building\n\nall 48 checks passed\n"}}}})
	if len(l.Commands) != 1 || !strings.HasSuffix(l.Commands[0], "=> all 48 checks passed") {
		t.Errorf("ledger command lacks its last output line: %v", l.Commands)
	}
}
