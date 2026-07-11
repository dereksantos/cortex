package fslock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// holdLockPath is the env-var contract used by TestOpenExclusive_CrossProcess
// to spawn a child process that holds the lock while the parent asserts the
// busy result.
const holdLockPath = "FSLOCK_TEST_HOLD_PATH"

func TestOpenExclusive_BlocksSecondOpenUntilReleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")

	first, err := OpenExclusive(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}

	// A second open of the same path must be refused: the kernel serializes
	// flock across open-file descriptions, which is exactly what two separate
	// processes would see for one inode.
	if _, err := OpenExclusive(path, os.O_WRONLY, 0o644); !errors.Is(err, ErrBusy) {
		t.Fatalf("second open err = %v, want fslock.ErrBusy", err)
	}

	// Once the first holder closes, the lock is released and a new open wins.
	first.Close()
	second, err := OpenExclusive(path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open after release: %v", err)
	}
	second.Close()
}

// TestLock_BlocksThenAcquiresAfterRelease checks the lower-level Lock guard, which
// is intentionally blocking: it waits for the lock rather than returning busy.
// The journal uses this form. We verify it does not acquire while another
// descriptor holds the lock, and does acquire once that holder releases.
func TestLock_BlocksThenAcquiresAfterRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")

	f1, err := OpenExclusive(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	f2, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open descriptor 2: %v", err)
	}
	defer f2.Close()

	got := make(chan error, 1)
	go func() { got <- Lock(int(f2.Fd())) }()

	// While f1 holds the lock, Lock must not return.
	select {
	case err := <-got:
		t.Fatalf("Lock acquired while holder still open: %v", err)
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	f1.Close() // release the held lock
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Lock after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lock did not acquire after the holder released")
	}
}

// TestOpenExclusive_CrossProcess is the strongest proof: a child process holds
// the lock while the parent tries (and must fail) to open the same file. The
// child is the test binary re-invoked with the hold contract, so no extra
// fixture is needed.
func TestOpenExclusive_CrossProcess(t *testing.T) {
	if p := os.Getenv(holdLockPath); p != "" {
		// Child: hold the exclusive lock, then wait to be killed.
		f, err := OpenExclusive(p, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(10 * time.Second)
		_ = f.Close()
		os.Exit(0)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")

	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenExclusive_CrossProcess$")
	cmd.Env = append(os.Environ(), holdLockPath+"="+path)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn test helper (skipping cross-process check): %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Give the child a moment to acquire the lock.
	time.Sleep(200 * time.Millisecond)

	if _, err := OpenExclusive(path, os.O_WRONLY, 0o644); !errors.Is(err, ErrBusy) {
		t.Fatalf("cross-process open err = %v, want fslock.ErrBusy", err)
	}
}
