package journal

import (
	"io"
	"testing"

	"github.com/dereksantos/cortex/internal/userhome"
)

// TestAppendLoopRunWritesEntryToUserLevelJournal pins the loop.run journal
// leg AppendLoopRun's callers (internal/loops.Scheduler in M6.2, the real
// firing machinery in M6.3/M6.4) both depend on: the event resolves its
// class dir through internal/userhome (not any project's .cortex/journal)
// and round-trips via the ordinary Reader.
func TestAppendLoopRunWritesEntryToUserLevelJournal(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	payload := LoopRunPayload{
		Name:      "nightly",
		Project:   "blog",
		Outcome:   LoopOutcomeSkipped,
		Reason:    "overlap",
		ChangeRef: "",
	}
	if err := AppendLoopRun(payload); err != nil {
		t.Fatalf("AppendLoopRun: %v", err)
	}

	dir, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	r, err := NewReader(dir)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	e, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if e.Type != TypeLoopRun {
		t.Errorf("Type = %q, want %q", e.Type, TypeLoopRun)
	}
	got, err := ParseLoopRun(e)
	if err != nil {
		t.Fatalf("ParseLoopRun: %v", err)
	}
	if *got != payload {
		t.Errorf("payload = %+v, want %+v", *got, payload)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF (exactly one entry)", err)
	}
}

// TestAppendLoopRunIsolatedByCortexHome proves the "user-level, not
// project-level" half: two different CORTEX_HOME values produce two
// independent journals — the event never lands under a project's own
// .cortex/journal.
func TestAppendLoopRunIsolatedByCortexHome(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("CORTEX_HOME", homeA)
	if err := AppendLoopRun(LoopRunPayload{Name: "nightly", Outcome: LoopOutcomeSuccess}); err != nil {
		t.Fatalf("AppendLoopRun (homeA): %v", err)
	}

	t.Setenv("CORTEX_HOME", homeB)
	dirB, err := userhome.Path("journal", "loop")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	rB, err := NewReader(dirB)
	if err != nil {
		t.Fatalf("NewReader(dirB): %v", err)
	}
	defer rB.Close()
	if _, err := rB.Next(); err != io.EOF {
		t.Errorf("homeB journal Next() error = %v, want io.EOF (homeA's write must not leak into homeB)", err)
	}
}
