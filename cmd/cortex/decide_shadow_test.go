package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/decide"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/tools"
)

// fakeDecider answers every ask with its first choice; when gate is non-nil
// it blocks until gate closes or ctx ends.
type fakeDecider struct {
	gate  chan struct{}
	mu    sync.Mutex
	calls int
}

func (f *fakeDecider) Name() string { return "fake" }

func (f *fakeDecider) Decide(ctx context.Context, _ decide.Snapshot, asks []decide.Ask) decide.Decision {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return decide.Decision{Backend: "fake", Status: decide.StatusTimeout}
		}
	}
	d := decide.Decision{Backend: "fake", Model: "fm", Status: decide.StatusOK, Latency: time.Millisecond}
	for _, a := range asks {
		d.Answers = append(d.Answers, decide.Answer{Q: a.Q, Picks: a.Choices[:1]})
	}
	return d
}

type rowSink struct {
	mu   sync.Mutex
	rows []journal.DecideShadowPayload
}

func (r *rowSink) write(p journal.DecideShadowPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, p)
}

func (r *rowSink) find(round int, q decide.Question) []journal.DecideShadowPayload {
	var out []journal.DecideShadowPayload
	for _, p := range r.rows {
		if p.Round == round && p.Question == string(q) {
			out = append(out, p)
		}
	}
	return out
}

func batchOf(tool, args, obs string) decide.Batch {
	return shadowBatch([]ToolCall{{ID: "x", Function: FunctionCall{Name: tool, Arguments: args}}}, []string{obs})
}

// TestShadowJournalsDecisionsWithActual drives the hooks in the order the
// engine calls them and checks each row pairs the answer with what the agent
// did next, and when the decision became usable.
func TestShadowJournalsDecisionsWithActual(t *testing.T) {
	sink := &rowSink{}
	s := newShadowDecider(&fakeDecider{}, decideDefaultQuestions, "sess", sink.write)
	s.beginTurn(1, "fix it", nil)
	s.beforeSend() // send 1 → the model asks for round 1
	s.afterBatch(batchOf("grep", `{"pattern":"x"}`, "a.txt:1: x"))
	s.beforeSend()  // send 2 goes out at once — decision 1 can never ride it
	s.jobsWG.Wait() // decision 1 completes during send 2 → ready for send 3
	s.afterBatch(batchOf("edit_file", `{"path":"a.txt"}`, "edited a.txt"))
	s.beforeSend() // send 3 → the model answers
	s.jobsWG.Wait()
	s.endTurn("Changed a.txt.", "clean-finalize")
	s.drain(time.Second)

	r1 := sink.find(1, decide.NextMove)
	if len(r1) != 1 {
		t.Fatalf("round 1 next_move rows = %d, want 1 (all rows: %+v)", len(r1), sink.rows)
	}
	if r1[0].Actual.Kind != "tools" || !slices.Equal(r1[0].Actual.Moves, []string{decide.MoveEdit}) || r1[0].Status != "ok" || r1[0].RoundsLate != 1 {
		t.Errorf("round 1 row = %+v, want actual tools/[edit], ok, rounds_late 1", r1[0])
	}
	if len(sink.find(1, decide.Looping)) != 1 {
		t.Errorf("round 1 has no looping row")
	}
	r2 := sink.find(2, decide.NextMove)
	if len(r2) != 1 || r2[0].Actual.Kind != "finalize" {
		t.Fatalf("round 2 rows = %+v, want one with actual finalize", r2)
	}
	if r2[0].RoundsLate != 1 {
		t.Errorf("round 2 rounds_late = %d, want 1 (ready by send 3)", r2[0].RoundsLate)
	}
	cc := sink.find(3, decide.ClaimCheck)
	if len(cc) != 1 || cc[0].Point != "finalize" || cc[0].RoundsLate != -1 {
		t.Errorf("claim_check rows = %+v, want one finalize row", cc)
	}
}

// TestShadowNeverBlocksTheCoder: with the decider stuck, the hooks return at
// once, a second point is recorded busy (one call in flight at most), and
// drain cuts the stuck call off at its deadline.
func TestShadowNeverBlocksTheCoder(t *testing.T) {
	sink := &rowSink{}
	f := &fakeDecider{gate: make(chan struct{})}
	s := newShadowDecider(f, decideDefaultQuestions, "sess", sink.write)
	s.beginTurn(1, "task", nil)
	start := time.Now()
	s.beforeSend()
	s.afterBatch(batchOf("read_file", `{"path":"a"}`, "ok"))
	s.beforeSend()
	s.afterBatch(batchOf("read_file", `{"path":"b"}`, "ok"))
	s.endTurn("done", "max-iter")
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("hooks took %v with the decider stuck — the coder waited", el)
	}
	s.drain(20 * time.Millisecond)
	if f.calls != 2 { // round 1 + the finalize claim check; round 2 was busy
		t.Errorf("decider calls = %d, want 2", f.calls)
	}
	busy := sink.find(2, decide.NextMove)
	if len(busy) != 1 || busy[0].Status != "busy" || busy[0].Actual.Kind != "forced:max-iter" {
		t.Errorf("round 2 = %+v, want busy with actual forced:max-iter", busy)
	}
	r1 := sink.find(1, decide.NextMove)
	if len(r1) != 1 || r1[0].Status != decide.StatusTimeout || r1[0].RoundsLate != -1 {
		t.Errorf("round 1 = %+v, want timeout, never ready", r1)
	}
}

func TestShadowNilIsInert(t *testing.T) {
	var s *shadowDecider
	s.beginTurn(1, "t", nil)
	s.beforeSend()
	s.afterBatch(decide.Batch{})
	s.endTurn("x", "clean-finalize")
	s.drain(time.Millisecond)
}

// TestRunLoopCallsAfterBatch: the engine hands every round's calls and their
// observations to AfterBatch, once per round, in order.
func TestRunLoopCallsAfterBatch(t *testing.T) {
	req := &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "s"}}}
	appendMsg := func(m Message) { req.Messages = append(req.Messages, m) }
	round := 0
	send := SenderFunc(func(_ context.Context, r *AgentRequest) (*AgentResponse, bool, error) {
		round++
		if round == 3 {
			return fakeResp("done", nil, 1, 1), false, nil
		}
		return fakeResp("", []ToolCall{readCall("a", "f1"), readCall("b", "f2")}, 1, 1), false, nil
	})
	disp := DispatchFunc(func(_ context.Context, c ToolCall) string { return "obs-" + c.ID })
	var got [][]string
	ts := Toolset{Tools: []Tool{tools.ReadFile}, Dispatch: disp,
		AfterBatch: func(calls []ToolCall, obs []string) {
			if len(calls) != len(obs) {
				t.Errorf("calls %d vs observations %d", len(calls), len(obs))
			}
			got = append(got, obs)
		}}
	if _, _, err := runLoop(context.Background(), send, req, ts, Bounds{MaxTokens: 100, MaxIter: 10}, nil, appendMsg, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(got[0], []string{"obs-a", "obs-b"}) {
		t.Errorf("AfterBatch saw %v, want two rounds of [obs-a obs-b]", got)
	}
}

func TestValidateDecideConfig(t *testing.T) {
	tests := []struct {
		name    string
		c       DecideConfig
		wantErr string
	}{
		{"off by default", DecideConfig{}, ""},
		{"null", DecideConfig{Backend: "null"}, ""},
		{"chat ok", DecideConfig{Backend: "chat", Endpoint: "https://e", Model: "m", Questions: []string{"looping", "recall_target"}}, ""},
		{"chat needs endpoint", DecideConfig{Backend: "chat", Model: "m"}, "decide.endpoint"},
		{"chat needs model", DecideConfig{Backend: "chat", Endpoint: "https://e"}, "decide.model"},
		{"logprob not built", DecideConfig{Backend: "logprob"}, "not implemented"},
		{"unknown backend", DecideConfig{Backend: "jev"}, "decide.backend"},
		{"unknown question", DecideConfig{Backend: "chat", Endpoint: "e", Model: "m", Questions: []string{"vibes"}}, "unknown question"},
		{"negative calls", DecideConfig{Backend: "chat", Endpoint: "e", Model: "m", MaxCalls: -1}, "decide.max_calls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDecideConfig(tt.c)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestDecideHonoredOnlyFromUserConfig: the decider is a new outbound path, so
// only the user's own config can switch it on — never a repository's.
func TestDecideHonoredOnlyFromUserConfig(t *testing.T) {
	dir := t.TempDir()
	on := `{"decide":{"backend":"chat","endpoint":"https://e","model":"m"}}`
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	userOff := write("user-off.json", `{"backend":{"type":"openrouter"}}`)
	userOn := write("user-on.json", on)
	project := write("project.json", on)
	tests := []struct {
		name string
		user string
		want bool
	}{
		{"user enables", userOn, true},
		{"user silent, project enables", userOff, false},
		{"no user config, project enables", filepath.Join(dir, "missing.json"), false},
		{"malformed user config", write("bad.json", `{"decide":`), false},
		{"invalid user block", write("inv.json", `{"decide":{"backend":"chat"}}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The merged config may well carry the project's block; the
			// gate must not read it. (An invalid user block is skipped
			// here: loadMergedConfig exits on any invalid config, the
			// decide block included — startup refuses it outright.)
			if !strings.Contains(tt.name, "invalid") {
				_ = loadMergedConfig(tt.user, project)
			}
			if got := decideFromUserConfig(tt.user).enabled(); got != tt.want {
				t.Errorf("enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeciderRefusedUnderLocalOnly(t *testing.T) {
	c := DecideConfig{Backend: "chat", Endpoint: "https://e", Model: "m"}
	t.Setenv("CORTEX_LOCAL_ONLY", "1")
	if d, note := newDeciderFromConfig(c); d != nil || !strings.Contains(note, "CORTEX_LOCAL_ONLY") {
		t.Errorf("decider = %v, note %q; want off with the reason", d, note)
	}
	t.Setenv("CORTEX_LOCAL_ONLY", "")
	if d, note := newDeciderFromConfig(c); d == nil || !strings.Contains(note, "https://e") {
		t.Errorf("decider = %v, note %q; want on, naming the destination", d, note)
	}
	if d, _ := newDeciderFromConfig(DecideConfig{}); d != nil {
		t.Errorf("an empty block built a decider")
	}
}
