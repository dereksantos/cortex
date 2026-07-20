// serve_drain_test.go — the graceful-shutdown drain helper (serve_drain.go,
// design item 2). Exercises drainServe directly against a manually-fired
// trigger channel; never raises a real OS signal (per design: "don't test
// actual OS signals — test the drain helper").
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/loops"
)

func TestDrainServeShutsDownServerAndScheduler(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}

	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- srv.Serve(ln) }()

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	running := newRunningSet()
	schedCtx, stopSched := context.WithCancel(context.Background())
	sched := newLoopScheduler(store, reg, hermeticSessionFactory(), time.Now, running)
	ticks := make(chan time.Time) // never fires — this test only cares about teardown
	schedStopped := sched.Start(schedCtx, ticks)

	trigger := make(chan os.Signal, 1)
	trigger <- syscall.SIGTERM

	done := make(chan error, 1)
	go func() {
		done <- drainServe(trigger, srv, stopSched, schedStopped, sched, 2*time.Second)
	}()

	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("drainServe returned %v, want nil or http.ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drainServe did not return within 3s")
	}

	select {
	case err := <-serveErrCh:
		if err != http.ErrServerClosed {
			t.Errorf("srv.Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("srv.Serve did not return after drainServe — server was not shut down")
	}

	select {
	case <-schedStopped:
	default:
		t.Error("schedStopped channel not closed after drainServe — scheduler tick loop not stopped")
	}
}

// TestDrainServeIsIdempotentToRepeat proves the teardown sequence
// (stopSched/schedStopped/sched.Wait) can be safely re-run by a caller that
// also tears down on a non-signal exit path (serve.go's runServeCLI does
// this for the srv.Serve-returned-on-its-own branch) — no panic, no hang.
func TestDrainServeIsIdempotentToRepeat(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}
	go func() { _ = srv.Serve(ln) }()

	store := loops.NewAt(filepath.Join(t.TempDir(), "loops.json"))
	reg := &fakeRegistry{}
	running := newRunningSet()
	schedCtx, stopSched := context.WithCancel(context.Background())
	sched := newLoopScheduler(store, reg, hermeticSessionFactory(), time.Now, running)
	ticks := make(chan time.Time)
	schedStopped := sched.Start(schedCtx, ticks)

	trigger := make(chan os.Signal, 1)
	trigger <- syscall.SIGTERM
	_ = drainServe(trigger, srv, stopSched, schedStopped, sched, 2*time.Second)

	// Re-running the same steps directly (mirroring what runServeCLI does
	// on its "server returned on its own" branch) must not hang or panic.
	stopSched()
	<-schedStopped
	sched.Wait()
}
