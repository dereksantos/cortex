package bootstrap

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPIDLock_ExclusiveAcquisition(t *testing.T) {
	dir := t.TempDir()

	pid, ok, err := AcquirePIDLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !ok {
		t.Fatal("first acquire returned ok=false")
	}
	defer pid.Release()

	// Second attempt must NOT acquire while the first holds the lock.
	pid2, ok2, err2 := AcquirePIDLock(dir)
	if err2 != nil {
		t.Fatalf("second acquire returned error: %v", err2)
	}
	if ok2 {
		t.Fatal("second acquire returned ok=true; expected false")
	}
	if pid2 != nil {
		t.Fatal("second acquire returned non-nil lock; expected nil")
	}
}

func TestPIDLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	pid, ok, err := AcquirePIDLock(dir)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	pid.Release()

	pid2, ok2, err2 := AcquirePIDLock(dir)
	if err2 != nil || !ok2 {
		t.Fatalf("second acquire after release: ok=%v err=%v", ok2, err2)
	}
	pid2.Release()
}

func TestPIDLock_HolderPIDRecorded(t *testing.T) {
	dir := t.TempDir()

	pid, ok, err := AcquirePIDLock(dir)
	if err != nil || !ok {
		t.Fatalf("acquire: %v", err)
	}
	defer pid.Release()

	got := PIDLockHolderPID(dir)
	if got != os.Getpid() {
		t.Errorf("PIDLockHolderPID = %d, want %d", got, os.Getpid())
	}
}

// TestPIDLock_RaceProtection spawns two goroutines that both try to
// acquire the same lock concurrently. Exactly one must succeed, and
// the other must get ok=false (no error). Verifies the race-protection
// contract described in docs/bootstrap-dag-plan.md §Race protection.
func TestPIDLock_RaceProtection(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the dir so AcquirePIDLock's MkdirAll doesn't race.
	if err := os.MkdirAll(filepath.Join(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	type result struct {
		ok  bool
		err error
		pid *PIDLock
	}
	var wg sync.WaitGroup
	results := make(chan result, 2)

	// Synchronize start.
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, ok, err := AcquirePIDLock(dir)
			results <- result{ok: ok, err: err, pid: p}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for r := range results {
		if r.err != nil {
			t.Errorf("acquire returned error: %v", r.err)
		}
		if r.ok {
			successes++
			r.pid.Release()
		} else {
			failures++
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want 1", successes)
	}
	if failures != 1 {
		t.Errorf("failures = %d, want 1", failures)
	}
}
