// serve_drain.go — the graceful-shutdown drain helper (design item 2):
// stop accepting HTTP, stop the scheduler cleanly, let an in-flight loop
// firing finish journaling. Kept separate from runServeCLI's signal wiring
// (serve.go) so the shutdown sequence itself is directly unit-testable
// without ever raising a real OS signal — see serve_drain_test.go.
package main

import (
	"context"
	"net/http"
	"os"
	"time"
)

// drainServe blocks until a value arrives on trigger (SIGTERM/SIGINT in
// production — runServeCLI's signal.Notify channel; a directly-sent
// os.Signal in tests, so the shutdown logic runs without any real OS
// signal involved), then runs the graceful shutdown sequence:
//
//  1. stop accepting new HTTP connections (srv.Shutdown, bounded by
//     shutdownTimeout so a wedged connection can't hang shutdown forever —
//     requests already in flight get to finish; new ones are refused);
//  2. stop the scheduler: stopSched cancels its tick loop, schedStopped is
//     awaited so the loop has actually stopped taking new ticks (not just
//     been asked to), and sched.Wait() then blocks until any
//     already-in-flight firing completes — a loop mid-run gets to finish
//     journaling rather than being abandoned.
//
// Every step here is safe to run more than once (stopSched is a
// context.CancelFunc; schedStopped is a channel that stays closed once
// closed; sched.Wait() is a plain sync.WaitGroup.Wait) — a caller that also
// tears down on a non-signal exit path (srv.Serve returning on its own)
// can re-run the same steps afterward with no double-teardown hazard.
func drainServe(trigger <-chan os.Signal, srv *http.Server, stopSched context.CancelFunc, schedStopped <-chan struct{}, sched *loopScheduler, shutdownTimeout time.Duration) error {
	<-trigger
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(ctx)
	stopSched()
	<-schedStopped
	sched.Wait()
	return err
}
