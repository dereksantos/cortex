//go:build !windows

package commands

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cliout"
	"github.com/dereksantos/cortex/pkg/config"
)

// TestMaybeStartJournalIngest_ExitsCleanlyOnClose is the daemon-
// retirement Phase 2.5 smoke test in unit-test form. The plan's
// manual smoke ("start the REPL, idle 60s, exit") isn't drivable
// without an interactive terminal, so this asserts the same
// acceptance contract — "clean exit, no goroutine leak" — in-process:
//
//  1. maybeStartJournalIngest sets state.ingestCancel.
//  2. The goroutine survives at least one ticker fire without panicking.
//  3. state.close() cancels the goroutine and it actually exits.
//
// Drains correctness is covered by `cortex journal ingest` smoke
// runs in tasks 2.1–2.3; this test is scoped to lifecycle.
func TestMaybeStartJournalIngest_ExitsCleanlyOnClose(t *testing.T) {
	tmp := t.TempDir()
	cortexDir := filepath.Join(tmp, ".cortex")

	cfg := &config.Config{ContextDir: cortexDir, ProjectRoot: tmp}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	state := &replState{
		workdir: tmp,
		store:   store,
		ui:      cliout.New(false),
	}

	origInterval := journalIngestInterval
	journalIngestInterval = 25 * time.Millisecond
	defer func() { journalIngestInterval = origInterval }()

	baseline := runtime.NumGoroutine()
	maybeStartJournalIngest(state)
	if state.ingestCancel == nil {
		t.Fatal("maybeStartJournalIngest did not set ingestCancel")
	}

	// Let the goroutine tick a few times so any panics surface.
	time.Sleep(120 * time.Millisecond)

	// close() must cancel + the goroutine must exit. Poll because
	// goroutine teardown is asynchronous.
	state.close()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine did not exit after close() — got %d goroutines, baseline %d",
		runtime.NumGoroutine(), baseline)
}
