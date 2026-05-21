package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDLockFileName is the basename of the bootstrap lock file. Lives
// under Config.ContextDir.
const PIDLockFileName = "bootstrap.pid"

// PIDLock holds an exclusive non-blocking flock on .cortex/bootstrap.pid
// while a controller is running. Second invocations in the same project
// see the lock and skip (controller.Run reports "already running" and
// returns nil — concurrent invocation is an expected case when REPL
// auto-spawn meets a manual cortex bootstrap).
type PIDLock struct {
	path string
	f    *os.File
}

// AcquirePIDLock takes an exclusive non-blocking flock on the bootstrap
// pid file. On success the caller owns the lock and must call Release
// before exit (or the lock will leak until the OS closes the fd).
//
// Returns (lock, true, nil) on success. Returns (nil, false, nil) when
// another process holds the lock (no error — caller logs + skips).
// Returns (nil, false, err) on other failures (mkdir, open).
//
// Unix-only: relies on syscall.Flock. Windows support is a future
// enhancement (filesystem locks via LockFileEx).
func AcquirePIDLock(contextDir string) (*PIDLock, bool, error) {
	path := filepath.Join(contextDir, PIDLockFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("pidlock mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("pidlock open: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("pidlock flock: %w", err)
	}
	// Write our PID for human-readability. Not used by the lock itself
	// (flock identity is the fd, not the file contents) but useful for
	// "already running (pid N)" messages.
	if err := f.Truncate(0); err != nil {
		_ = unlock(f, path)
		return nil, false, fmt.Errorf("pidlock truncate: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = unlock(f, path)
		return nil, false, fmt.Errorf("pidlock seek: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = unlock(f, path)
		return nil, false, fmt.Errorf("pidlock write pid: %w", err)
	}
	return &PIDLock{path: path, f: f}, true, nil
}

// Release drops the flock and removes the pid file. Idempotent.
func (l *PIDLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f, l.path)
	l.f = nil
}

// PIDLockHolderPID inspects the on-disk lock file's content and returns
// the PID written there, or -1 if the file is missing/unreadable.
// Best-effort — the lock identity is the fd, not the file content.
func PIDLockHolderPID(contextDir string) int {
	path := filepath.Join(contextDir, PIDLockFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	s := strings.TrimSpace(string(b))
	pid, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return pid
}

func unlock(f *os.File, path string) error {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	err := f.Close()
	_ = os.Remove(path)
	return err
}
