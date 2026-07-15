// webui_loops_test.go — M6.7a: the loops screen view-model (GOAL.md §6
// M6.7, split into M6.7a-e per STATE.md's 2026-07-13 split; GOAL.md §2
// names cmd/cortex/webui*.go as home for the web UI's Go view-model
// builders). Golden-pins the exact JSON shape buildLoopsViewModel produces
// over a fixed loops.json + loop.run journal fixture, following the
// "golden-pinned = literal string in a test" convention established at
// M1.5/M2.5/M5.2a (no on-disk golden-file mechanism exists in this repo).
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loops"
	"github.com/dereksantos/cortex/internal/userhome"
)

// writeLoopRunAt appends one loop.run journal entry with a caller-fixed
// timestamp — bypassing journal.AppendLoopRun's time.Now() stamping
// (journal/writer.go's Append only stamps when Entry.TS is zero) so the
// golden test below can pin an exact JSON shape.
func writeLoopRunAt(t *testing.T, ts time.Time, p journal.LoopRunPayload) {
	t.Helper()
	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("resolve loop journal dir: %v", err)
	}
	entry, err := journal.NewLoopRunEntry(p)
	if err != nil {
		t.Fatalf("NewLoopRunEntry: %v", err)
	}
	entry.TS = ts
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestBuildLoopsViewModelGolden(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	store, err := loops.New()
	if err != nil {
		t.Fatalf("loops.New: %v", err)
	}
	if err := store.Save(loops.Spec{Name: "nightly", Project: "blog", Prompt: "sweep TODOs", IntervalMinutes: 60, MaxTurns: 25, Enabled: true}); err != nil {
		t.Fatalf("Save(nightly): %v", err)
	}
	if err := store.Save(loops.Spec{Name: "manual-only", Project: "cortex", Prompt: "run the eval"}); err != nil {
		t.Fatalf("Save(manual-only): %v", err)
	}

	writeLoopRunAt(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		journal.LoopRunPayload{Name: "nightly", Project: "blog", Outcome: journal.LoopOutcomeFailed, Reason: "budget"})
	writeLoopRunAt(t, time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
		journal.LoopRunPayload{Name: "nightly", Project: "blog", Outcome: journal.LoopOutcomeSuccess, ChangeRef: "cortex/fix-todos"})

	vm, err := buildLoopsViewModel(store)
	if err != nil {
		t.Fatalf("buildLoopsViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"loops":[` +
		`{"name":"manual-only","project":"cortex","prompt":"run the eval","enabled":false,"runs":[],"next_run":""},` +
		`{"name":"nightly","project":"blog","prompt":"sweep TODOs","interval_minutes":60,"max_turns":25,"enabled":true,"runs":[` +
		`{"timestamp":"2026-01-01T00:00:00Z","outcome":"failed","reason":"budget"},` +
		`{"timestamp":"2026-01-01T01:00:00Z","outcome":"success","change_ref":"cortex/fix-todos"}` +
		`],"next_run":"2026-01-01T02:00:00Z"}` +
		`]}`
	if string(got) != want {
		t.Errorf("loops view-model JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildLoopsViewModelEmptyStoreReturnsEmptyLoopsNotNull(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	store, err := loops.New()
	if err != nil {
		t.Fatalf("loops.New: %v", err)
	}

	vm, err := buildLoopsViewModel(store)
	if err != nil {
		t.Fatalf("buildLoopsViewModel: %v", err)
	}
	got, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"loops":[]}` {
		t.Errorf("got %s, want empty loops array", got)
	}
}
