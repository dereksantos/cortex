// serve_scheduler.go — M6.8 (GOAL.md §6, owner amendment A4): the
// serve-resident scheduler docs/cortex-web.md Phase 6 asks for
// ("Scheduler in the serve process — ticks while cortex serve runs").
// Composes internal/loops.Scheduler.Due (M6.2) with RunLoopFiring
// (loop_run.go, M6.3) on an injected tick channel — mirrors M6.2's own
// injected-Clock seam so this file's tests never sleep either: production
// (runServeCLI, serve.go) wires a real time.Ticker's C; tests send
// directly on a channel they control, or close it, to drive ticks
// deterministically.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// runningSet is the scheduler's in-memory overlap guard: which loop names
// currently have a RunLoopFiring in flight. Serve owns no canonical state
// (GOAL.md §1 pillar 2) — this tracks only a live-process fact, never
// persisted; a restart naturally clears it, which is correct (nothing is
// genuinely "running" across a restart).
type runningSet struct {
	mu   sync.Mutex
	name map[string]bool
}

func newRunningSet() *runningSet { return &runningSet{name: make(map[string]bool)} }

func (rs *runningSet) Check(name string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.name[name]
}

func (rs *runningSet) start(name string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.name[name] = true
}

func (rs *runningSet) finish(name string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.name, name)
}

// loopScheduler wires internal/loops.Scheduler (Due) to RunLoopFiring: each
// tick lists specs, asks Due which are due, and fires each due spec on its
// own goroutine (tracked by wg) so a slow firing can't stall the tick loop
// — and so a genuine overlap is observable by the NEXT tick's Due call
// (the overlap guard is only meaningful if a firing can still be in flight
// when the next tick arrives).
type loopScheduler struct {
	store      loops.Store
	reg        registry.Registry
	newSession sessionFactory
	sched      *loops.Scheduler
	running    *runningSet
	wg         sync.WaitGroup
}

// newLoopScheduler builds a loopScheduler over store/reg/newSession with
// clock as the Scheduler's injected Clock. LastRun and the overlap-skip
// journal write are wired to the real user-level loop.run journal
// (loops.JournalLastRun, M6.6; journal.AppendLoopRun, the same sink M6.3's
// RunLoopFiring itself writes to) — Running is wired to this scheduler's
// own in-memory running set, the only piece of new state M6.8 introduces.
func newLoopScheduler(store loops.Store, reg registry.Registry, newSession sessionFactory, clock loops.Clock) *loopScheduler {
	running := newRunningSet()
	ls := &loopScheduler{store: store, reg: reg, newSession: newSession, running: running}
	ls.sched = &loops.Scheduler{
		Clock:   clock,
		LastRun: loops.JournalLastRun,
		Running: running.Check,
		OnSkip: func(spec loops.Spec, reason string) error {
			return journal.AppendLoopRun(journal.LoopRunPayload{
				Name:    spec.Name,
				Project: spec.Project,
				Outcome: journal.LoopOutcomeSkipped,
				Reason:  reason,
			})
		},
	}
	return ls
}

// tick runs one due-check pass: list specs, ask Scheduler.Due, then spawn
// each due spec's firing on its own goroutine. running.start marks a spec
// in-flight SYNCHRONOUSLY, before its goroutine is spawned — so by the time
// tick() returns, a second tick() call already observes the correct
// overlap state via Scheduler.Due's Running check, with no dependency on
// how far the spawned goroutine has actually gotten. Returns an error only
// for a failure to list specs or compute Due (an infra problem); a
// firing's own outcome is journaled by RunLoopFiring itself and never
// propagated here — firings run to completion independently of the tick
// that spawned them.
func (ls *loopScheduler) tick(ctx context.Context) error {
	specs, err := ls.store.List()
	if err != nil {
		return fmt.Errorf("failed to list loop specs: %w", err)
	}
	due, err := ls.sched.Due(specs)
	if err != nil {
		return fmt.Errorf("failed to compute due loops: %w", err)
	}
	for _, spec := range due {
		ls.running.start(spec.Name)
		ls.wg.Add(1)
		go func(spec loops.Spec) {
			defer ls.wg.Done()
			defer ls.running.finish(spec.Name)
			_ = RunLoopFiring(ctx, spec, ls.reg, ls.newSession)
		}(spec)
	}
	return nil
}

// Start begins the tick loop: each value received on ticks triggers one
// tick() pass. The loop exits — closing the returned channel — when ctx is
// canceled or ticks is closed, whichever happens first: "stops cleanly on
// server shutdown" (GOAL.md M6.8) means the caller cancels ctx (or closes
// ticks), receives from the returned channel to know the tick loop has
// actually stopped taking new ticks, and may then call Wait() to drain any
// firings already in flight rather than abandoning them mid-run.
func (ls *loopScheduler) Start(ctx context.Context, ticks <-chan time.Time) <-chan struct{} {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
				_ = ls.tick(ctx)
			}
		}
	}()
	return stopped
}

// Wait blocks until every firing spawned by tick() has completed —
// production calls this after Start's returned channel closes, so a serve
// shutdown doesn't abandon an in-flight loop firing mid-run.
func (ls *loopScheduler) Wait() {
	ls.wg.Wait()
}
