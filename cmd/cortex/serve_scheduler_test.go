// serve_scheduler_test.go — M6.8 (GOAL.md §6, owner amendment A4): the
// serve-resident scheduler tick goroutine. Reuses loop_run_test.go's
// scripted-Sender session factories, fakeRegistry (serve_routes_test.go),
// initGitFixture (change_status_test.go), and readLoopRunEntries
// (loop_run_test.go) rather than inventing parallel fixtures.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/registry"
)

// blockingTurnTestSessionFactory scripts a backend that blocks in the HTTP
// handler until release is closed, then answers with a plain final reply —
// used to hold a firing "in flight" for exactly as long as the test needs,
// with no sleep on either side of the synchronization.
func blockingTurnTestSessionFactory(t *testing.T, release <-chan struct{}) sessionFactory {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// blockingTurnTestSessionFactoryStarted is blockingTurnTestSessionFactory
// plus a started signal: the backend closes started the instant its HTTP
// handler is entered, before blocking on release. Since RunLoopFiring calls
// running.tryStart/start (loop_run.go) synchronously before it ever issues
// the turn's backend call, a test observing <-started already knows the
// overlap guard has been claimed — no sleep needed to make "a firing is in
// flight" deterministic from the outside.
func blockingTurnTestSessionFactoryStarted(t *testing.T, started chan<- struct{}, release <-chan struct{}) sessionFactory {
	t.Helper()
	var once bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !once {
			once = true
			close(started)
		}
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = srv.URL
		return cs
	}
}

// TestLoopSchedulerTickFiresDueEnabledSpec proves tick() composes
// Scheduler.Due with RunLoopFiring: an enabled spec with no prior run is
// due immediately (first-ever run), and firing it journals success.
func TestLoopSchedulerTickFiresDueEnabledSpec(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 15, Enabled: true}
	if err := store.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sched := newLoopScheduler(store, reg, noopTurnTestSessionFactory(t), func() time.Time { return now }, newRunningSet())

	if err := sched.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	sched.Wait()

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Name != "nightly" || entries[0].Outcome != journal.LoopOutcomeSuccess {
		t.Errorf("entry = %+v, want Name=nightly Outcome=success", entries[0])
	}
}

// TestLoopSchedulerTickSkipsDisabledSpec proves a disabled spec is never
// fired by tick(), regardless of cadence — composed straight through from
// Scheduler.Due's own disabled-never-fires rule (M6.2).
func TestLoopSchedulerTickSkipsDisabledSpec(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 15, Enabled: false}
	if err := store.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sched := newLoopScheduler(store, reg, noopTurnTestSessionFactory(t), func() time.Time { return now }, newRunningSet())

	if err := sched.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	sched.Wait()

	entries := readLoopRunEntries(t)
	if len(entries) != 0 {
		t.Fatalf("loop.run entries = %d, want 0 (disabled spec must never fire): %+v", len(entries), entries)
	}
}

// TestLoopSchedulerTickOverlapSkipsAndJournalsSkip proves the overlap guard
// is real, not just composed-but-unreachable: a firing spawned by one
// tick() call is still "running" (running.start happens synchronously,
// before the firing's goroutine is even spawned) when a second tick() call
// is made immediately after — Scheduler.Due excludes it and journals a
// skip, exactly as M6.2's own overlap test proves against a directly
// injected Running function.
func TestLoopSchedulerTickOverlapSkipsAndJournalsSkip(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 15, Enabled: true}
	if err := store.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	release := make(chan struct{})
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sched := newLoopScheduler(store, reg, blockingTurnTestSessionFactory(t, release), func() time.Time { return now }, newRunningSet())

	if err := sched.tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	// The first tick's firing goroutine is blocked inside the scripted
	// backend, but running.start already ran synchronously before tick()
	// returned — no synchronization needed to make the second tick's
	// overlap check deterministic.
	if err := sched.tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	close(release)
	sched.Wait()

	entries := readLoopRunEntries(t)
	if len(entries) != 2 {
		t.Fatalf("loop.run entries = %d, want 2 (one success, one overlap skip): %+v", len(entries), entries)
	}
	var success, skipped int
	for _, e := range entries {
		switch e.Outcome {
		case journal.LoopOutcomeSuccess:
			success++
		case journal.LoopOutcomeSkipped:
			skipped++
			if e.Reason != "overlap" {
				t.Errorf("skipped entry Reason = %q, want %q", e.Reason, "overlap")
			}
		default:
			t.Errorf("unexpected outcome %q", e.Outcome)
		}
	}
	if success != 1 || skipped != 1 {
		t.Errorf("success=%d skipped=%d, want 1 and 1", success, skipped)
	}
}

// TestLoopSchedulerStartFiresOnTickAndStopsWhenTicksClosed drives Start via
// a buffered ticks channel: send one tick, close the channel, and wait on
// the returned stopped channel — deterministic (the buffered value is
// drained before the closed-channel case can be observed), no sleeps.
func TestLoopSchedulerStartFiresOnTickAndStopsWhenTicksClosed(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 15, Enabled: true}
	if err := store.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sched := newLoopScheduler(store, reg, noopTurnTestSessionFactory(t), func() time.Time { return now }, newRunningSet())

	ticks := make(chan time.Time, 1)
	stopped := sched.Start(context.Background(), ticks)
	ticks <- now
	close(ticks)
	<-stopped
	sched.Wait()

	entries := readLoopRunEntries(t)
	if len(entries) != 1 {
		t.Fatalf("loop.run entries = %d, want 1 (the single tick's firing): %+v", len(entries), entries)
	}
}

// TestLoopSchedulerStartStopsOnContextCancel proves "stops cleanly on
// server shutdown": canceling ctx closes the returned stopped channel even
// with no tick ever having been sent, and fires nothing.
func TestLoopSchedulerStartStopsOnContextCancel(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	sched := newLoopScheduler(store, reg, noopTurnTestSessionFactory(t), time.Now, newRunningSet())

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	stopped := sched.Start(ctx, ticks)
	cancel()
	<-stopped
	sched.Wait()

	entries := readLoopRunEntries(t)
	if len(entries) != 0 {
		t.Fatalf("loop.run entries = %d, want 0 (no tick ever fired): %+v", len(entries), entries)
	}
}

// TestLoopSchedulerTickSkipsWhileRunNowInFlight proves the overlap guard is
// shared, not scheduler-local: a run-now firing (POST /api/loops/{name}/
// run-now, serve_loops.go's handleRunLoop) claims the SAME runningSet a
// scheduler tick consults via Scheduler.Due's Running check — production
// (serve.go's runServeCLI) constructs exactly one runningSet and threads it
// into both newServeMux and newLoopScheduler, and this test wires the two
// the same way rather than each getting its own independent guard (which
// would let the live double-firing bug back in). Mirrors
// TestLoopSchedulerTickOverlapSkipsAndJournalsSkip's assertions, just with
// the "occupier" being a run-now HTTP request instead of a prior tick.
func TestLoopSchedulerTickSkipsWhileRunNowInFlight(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	root := initGitFixture(t, "main", false)
	t.Chdir(root)

	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	spec := loops.Spec{Name: "nightly", Project: "blog", Prompt: "leave a note", IntervalMinutes: 15, Enabled: true}
	if err := store.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	release := make(chan struct{})
	started := make(chan struct{})
	running := newRunningSet()
	mgr := NewSessionManager(reg, blockingTurnTestSessionFactoryStarted(t, started, release))
	ts := httptest.NewServer(authMiddleware("tok", newServeMux(reg, mgr, "", "", store, running)))
	defer ts.Close()

	runNowDone := make(chan *http.Response, 1)
	go func() {
		runNowDone <- doAuthedPost(t, ts.URL+"/api/loops/nightly/run-now", "tok", "")
	}()
	// <-started only closes once the run-now firing's turn call has actually
	// reached the scripted backend — which happens strictly after
	// handleRunLoop's running.tryStart, so the tick below deterministically
	// observes the loop as running with no sleep needed.
	<-started

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sched := newLoopScheduler(store, reg, noopTurnTestSessionFactory(t), func() time.Time { return now }, running)
	if err := sched.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	sched.Wait()

	close(release)
	resp := <-runNowDone
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run-now status = %d, want 200", resp.StatusCode)
	}

	entries := readLoopRunEntries(t)
	if len(entries) != 2 {
		t.Fatalf("loop.run entries = %d, want 2 (one success from run-now, one overlap skip from the tick): %+v", len(entries), entries)
	}
	var success, skipped int
	for _, e := range entries {
		switch e.Outcome {
		case journal.LoopOutcomeSuccess:
			success++
		case journal.LoopOutcomeSkipped:
			skipped++
			if e.Reason != "overlap" {
				t.Errorf("skipped entry Reason = %q, want %q", e.Reason, "overlap")
			}
		default:
			t.Errorf("unexpected outcome %q", e.Outcome)
		}
	}
	if success != 1 || skipped != 1 {
		t.Errorf("success=%d skipped=%d, want 1 and 1", success, skipped)
	}

	// The guard clears once the run-now firing completes: a fresh run-now
	// against the same loop must succeed rather than staying wedged at 409.
	if running.Check(spec.Name) {
		t.Errorf("running.Check(%q) = true after completion, want false (guard must clear)", spec.Name)
	}
}
