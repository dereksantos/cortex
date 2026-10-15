package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/decide"
	"github.com/dereksantos/cortex/internal/journal"
)

// shadowDecider runs the parallel decider beside the coder (docs/decide.md)
// in SHADOW mode: every decision is journaled as decide.shadow with what the
// agent actually did next, and nothing is ever shown to the coder.
//
// Latency contract — the coder never waits on it:
//   - the hooks the engine calls (afterBatch, beforeSend, endTurn) take one
//     short mutex hold, build a snapshot from data already in memory, and
//     start a goroutine; they never do I/O or a model call;
//   - at most one decider call is in flight; a point that arrives while one
//     is running is recorded as "busy" and skipped, never queued;
//   - journal writes happen on one background writer goroutine; a full
//     write buffer drops the row rather than block.
//
// A decision started at a batch end can never reach the very next send (the
// engine sends it at once), so each row records RoundsLate: how many sends
// went out between the decision's start and its completion — 1 means it was
// ready for the send after the agent's next round, the earliest possible.
type shadowDecider struct {
	d        decide.Decider
	asks     []decide.Question
	session  string
	rows     chan journal.DecideShadowPayload
	writerWG sync.WaitGroup
	jobsWG   sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	turn      int
	task      string
	citations []string
	batches   []decide.Batch
	sends     int
	inflight  *shadowJob
	open      []*shadowJob // this turn's jobs not yet written
	closed    bool
}

type shadowJob struct {
	turn, round int
	final       bool
	asks        []decide.Ask
	startSends  int
	done        bool
	dec         decide.Decision
	roundsLate  int
	actual      journal.DecideActual
	actualKnown bool
}

// newShadowDecider starts the journal writer. write receives each row on the
// writer goroutine (tests capture; production appends to journalDir).
func newShadowDecider(d decide.Decider, asks []decide.Question, session string, write func(journal.DecideShadowPayload)) *shadowDecider {
	ctx, cancel := context.WithCancel(context.Background())
	s := &shadowDecider{d: d, asks: asks, session: session, rows: make(chan journal.DecideShadowPayload, 256), ctx: ctx, cancel: cancel}
	s.writerWG.Add(1)
	go func() {
		defer s.writerWG.Done()
		for r := range s.rows {
			write(r)
		}
	}()
	return s
}

// journalWriter appends decide.shadow rows under <contextDir>/journal/decide.
func journalWriter(contextDir string) func(journal.DecideShadowPayload) {
	dir := filepath.Join(contextDir, "journal", "decide")
	return func(p journal.DecideShadowPayload) {
		e, err := journal.NewDecideShadowEntry(p)
		if err != nil {
			return
		}
		w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
		if err != nil {
			return
		}
		defer w.Close()
		_, _ = w.Append(e)
	}
}

func (s *shadowDecider) asked(q decide.Question) bool { return slices.Contains(s.asks, q) }

// beginTurn resets the per-turn record. citations are the session outline's
// recall citations at turn start (recall_target's choices).
func (s *shadowDecider) beginTurn(turn int, task string, citations []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked(true) // a previous turn's leftovers: their actual is no longer knowable
	s.turn, s.task, s.citations = turn, task, citations
	s.batches, s.sends = nil, 0
}

// beforeSend counts a send. A decision's RoundsLate is the number of sends
// that went out between its start and its completion (set when it completes).
func (s *shadowDecider) beforeSend() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
}

// afterBatch records the batch that just ran, resolves the previous point's
// actual move, and starts (or skips as busy) the decision for this point.
func (s *shadowDecider) afterBatch(b decide.Batch) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.batches = append(s.batches, b)
	round := len(s.batches)
	s.resolveLocked(journal.DecideActual{Kind: "tools", Moves: decide.MovesOf(b), Tools: toolNames(b)})
	s.flushLocked(false)

	asks := s.midAsksLocked()
	if len(asks) == 0 {
		return
	}
	j := &shadowJob{turn: s.turn, round: round, asks: asks, startSends: s.sends, roundsLate: -1}
	s.open = append(s.open, j)
	if s.inflight != nil {
		j.done = true
		j.dec = decide.Decision{Backend: s.d.Name(), Status: "busy"}
		return
	}
	s.inflight = j
	s.startLocked(j, decide.BuildSnapshot(s.task, s.batches, ""))
}

// endTurn resolves the last point (finalize, or the bound that forced the
// end) and starts the finalize claim_check on the reply.
func (s *shadowDecider) endTurn(reply, stopReason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	kind := "finalize"
	switch stopReason {
	case "clean-finalize", "salvaged-finalize", "":
	default:
		kind = "forced:" + stopReason
	}
	s.resolveLocked(journal.DecideActual{Kind: kind})
	if reply != "" && s.asked(decide.ClaimCheck) {
		j := &shadowJob{turn: s.turn, round: len(s.batches) + 1, final: true,
			asks: []decide.Ask{{Q: decide.ClaimCheck, Choices: decide.ClaimChoices}}, roundsLate: -1, actualKnown: true}
		s.open = append(s.open, j)
		// The claim check is the turn's last word: it runs even when a
		// mid-turn decision is still in flight.
		s.startLocked(j, decide.BuildSnapshot(s.task, s.batches, reply))
	}
	s.flushLocked(false)
}

// drain waits up to d for decisions still in flight, then writes everything
// left and stops the writer. Called once, at session close — after the
// answer is out, so it delays only process exit.
func (s *shadowDecider) drain(d time.Duration) {
	if s == nil {
		return
	}
	waited := make(chan struct{})
	go func() { s.jobsWG.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(d):
		s.cancel()
		<-waited
	}
	s.mu.Lock()
	s.flushLocked(true)
	s.closed = true
	s.mu.Unlock()
	close(s.rows)
	s.writerWG.Wait()
}

func (s *shadowDecider) midAsksLocked() []decide.Ask {
	var asks []decide.Ask
	if s.asked(decide.NextMove) {
		asks = append(asks, decide.Ask{Q: decide.NextMove, Choices: decide.Moves})
	}
	if s.asked(decide.Looping) {
		snap := decide.BuildSnapshot(s.task, s.batches, "")
		ids := make([]string, 0, len(snap.Steps)+1)
		for _, st := range snap.Steps {
			ids = append(ids, st.ID)
		}
		asks = append(asks, decide.Ask{Q: decide.Looping, Choices: append(ids, decide.None), Multi: true})
	}
	if s.asked(decide.RecallTarget) && len(s.citations) > 0 {
		asks = append(asks, decide.Ask{Q: decide.RecallTarget, Choices: append(slices.Clone(s.citations), decide.None)})
	}
	return asks
}

func (s *shadowDecider) startLocked(j *shadowJob, snap decide.Snapshot) {
	s.jobsWG.Add(1)
	go func() {
		defer s.jobsWG.Done()
		dec := s.d.Decide(s.ctx, snap, j.asks)
		s.mu.Lock()
		defer s.mu.Unlock()
		j.done, j.dec = true, dec
		if !j.final && dec.Status == decide.StatusOK {
			j.roundsLate = s.sends - j.startSends
		}
		if s.inflight == j {
			s.inflight = nil
		}
		s.flushLocked(false)
	}()
}

// resolveLocked sets the actual move on the newest unresolved mid-turn job.
func (s *shadowDecider) resolveLocked(a journal.DecideActual) {
	for i := len(s.open) - 1; i >= 0; i-- {
		j := s.open[i]
		if j.final || j.actualKnown {
			continue
		}
		if j.round == len(s.batches)-1 || a.Kind != "tools" {
			j.actual, j.actualKnown = a, true
		}
		break
	}
}

// flushLocked queues every job that is done and resolved (or, with force,
// every job — a still-running one as a timeout) and drops them from open.
func (s *shadowDecider) flushLocked(force bool) {
	keep := s.open[:0]
	for _, j := range s.open {
		if !force && (!j.done || !j.actualKnown) {
			keep = append(keep, j)
			continue
		}
		if !j.done {
			j.dec = decide.Decision{Backend: s.d.Name(), Status: decide.StatusTimeout, Detail: "session closed first"}
		}
		s.emitLocked(j)
	}
	s.open = keep
}

func (s *shadowDecider) emitLocked(j *shadowJob) {
	if s.closed {
		return
	}
	point := "after_batch"
	if j.final {
		point = "finalize"
	}
	for _, a := range j.asks {
		p := journal.DecideShadowPayload{
			Session: s.session, Turn: j.turn, Round: j.round, Point: point,
			Question: string(a.Q), Choices: a.Choices,
			Backend: j.dec.Backend, Model: j.dec.Model, Status: j.dec.Status, Detail: j.dec.Detail,
			LatencyMs: j.dec.Latency.Milliseconds(), PromptVersion: j.dec.PromptVersion,
			RoundsLate: j.roundsLate, Actual: j.actual,
		}
		if ans, ok := j.dec.Pick(a.Q); ok {
			p.Picks, p.Confidence = ans.Picks, ans.Confidence
		}
		select {
		case s.rows <- p:
		default: // never block the caller on the journal
		}
	}
}

// startShadow builds the session's shadow decider on its first turn, from the
// USER config only (decideFromUserConfig). Off — nil — unless that config
// switches it on and the session has a workspace to journal into.
func (cs *CortexSession) startShadow() {
	if cs.shadowStarted {
		return
	}
	cs.shadowStarted = true
	if cs.workspace == nil {
		return
	}
	cfg := decideFromUserConfig(userConfigPath())
	d, note := newDeciderFromConfig(cfg)
	if note != "" {
		printStartupWarning(os.Stderr, note)
	}
	if d == nil {
		return
	}
	cs.shadowDrain = cfg.drain()
	cs.shadow = newShadowDecider(d, cfg.questions(), cs.SessionID, journalWriter(cs.workspace.ContextDir()))
}

// outlineCitations are the session outline's recall citations — the
// recall_target choices.
func (cs *CortexSession) outlineCitations() []string {
	var out []string
	for _, e := range cs.outline {
		if e.Citation != "" {
			out = append(out, e.Citation)
		}
	}
	return out
}

// shadowBatch converts one engine round into the decider's batch.
func shadowBatch(calls []ToolCall, observations []string) decide.Batch {
	b := decide.Batch{End: time.Now()}
	for i, c := range calls {
		dc := decide.Call{ID: c.ID, Tool: c.Function.Name, RawArg: c.Function.Arguments}
		_ = json.Unmarshal([]byte(c.Function.Arguments), &dc.Args)
		if i < len(observations) {
			dc.Result = observations[i]
		}
		b.Calls = append(b.Calls, dc)
	}
	return b
}

func toolNames(b decide.Batch) []string {
	out := make([]string, 0, len(b.Calls))
	for _, c := range b.Calls {
		out = append(out, c.Tool)
	}
	return out
}
