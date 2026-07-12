// serve_lock_test.go — M4.4: cross-process session lock (GOAL.md §6 M4.4,
// amendment A1). internal/fslock is the pre-existing D8 lock, already
// proven cross-process by internal/fslock/fslock_test.go's
// TestOpenExclusive_CrossProcess; per A1, M4.4 is the SERVE integration
// proof only. SessionManager.Resume (serve_session.go) already routes
// through CortexSession.ResumeTranscript -> openTranscript (session.go) ->
// fslock.OpenExclusive, so this increment needs no production code change
// (mirrors M4.3's "test-only increment" precedent) — only the two-process
// test GOAL.md M4.4 asks for.
//
// A REAL second OS process (not a goroutine: flock is scoped to the OS
// process, so two goroutines in one process would share the same lock and
// never contend) tries to resume the exact session id/path the first
// process already holds open, using the same os/exec
// re-exec-the-test-binary idiom internal/fslock/fslock_test.go's
// TestOpenExclusive_CrossProcess established.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/fslock"
	"github.com/dereksantos/cortex/internal/registry"
)

// crossProcessLockChildRootEnv/IDEnv are the env-var contract the child
// process (a re-exec of this same test binary) reads to know which project
// root and session id to attempt resuming — mirroring fslock_test.go's
// holdLockPath contract.
const (
	crossProcessLockChildRootEnv = "CORTEX_SERVE_LOCK_TEST_ROOT"
	crossProcessLockChildIDEnv   = "CORTEX_SERVE_LOCK_TEST_ID"
)

func TestCrossProcessSessionResumeGetsBusyError(t *testing.T) {
	if root := os.Getenv(crossProcessLockChildRootEnv); root != "" {
		// Child mode: a fresh process with its own empty in-memory
		// SessionManager, attempting to resume the session id the parent
		// process still holds open. A recognizable marker on stdout lets
		// the parent (which can't type-assert an error across a process
		// boundary) tell the busy case apart from any other failure.
		id := os.Getenv(crossProcessLockChildIDEnv)
		reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
		mgr := NewSessionManager(reg, hermeticSessionFactory())
		_, err := mgr.Resume("blog", id)
		if err != nil && errors.Is(err, fslock.ErrBusy) {
			fmt.Println("RESUME-BLOCKED")
			os.Exit(0)
		}
		fmt.Printf("RESUME-ALLOWED (want blocked): %v\n", err)
		os.Exit(1)
	}

	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	mgr := NewSessionManager(reg, hermeticSessionFactory())

	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer created.cs.transcript.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrossProcessSessionResumeGetsBusyError$")
	cmd.Env = append(os.Environ(),
		crossProcessLockChildRootEnv+"="+root,
		crossProcessLockChildIDEnv+"="+created.ID(),
	)
	out, runErr := cmd.CombinedOutput()
	if !strings.Contains(string(out), "RESUME-BLOCKED") {
		t.Fatalf("child process did not observe fslock.ErrBusy resuming the live session; output=%q runErr=%v", out, runErr)
	}
}
