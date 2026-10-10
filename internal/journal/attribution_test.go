package journal

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/userhome"
)

// pendTail appends one raw JSONL line to the class's active segment by hand —
// used to plant an entry the constructors deliberately refuse.
func pendTail(t *testing.T, classDir, line string) {
	t.Helper()
	f, err := os.OpenFile(segmentPath(classDir, 1), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open segment for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write line: %v", err)
	}
}

// TestAttributionCommitOutlines pins the outcome vocabulary the bash
// backstop and `cortex change commit` report: the six cases must be distinct
// strings matching what issue #146 names, so a reader grepping the journal
// for "skipped_amend" finds exactly the amended commits.
func TestAttributionCommitOutcomes(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{AttributionOutcomeAdded, "added"},
		{AttributionOutcomeAlreadyPresent, "already_present"},
		{AttributionOutcomeSkippedUnparseable, "skipped_unparseable"},
		{AttributionOutcomeSkippedAmend, "skipped_amend"},
		{AttributionOutcomeSkippedStdin, "skipped_stdin"},
		{AttributionOutcomeDisabled, "disabled"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("outcome constant = %q, want %q", c.got, c.want)
		}
		if seen[c.got] {
			t.Errorf("outcome %q duplicated across constants", c.got)
		}
		seen[c.got] = true
	}
}

// TestNewAttributionCommitEntryRoundTrip checks the constructor encodes every
// payload field and ParseAttributionCommit gets them back byte-equal,
// including the post-commit verification fields.
func TestNewAttributionCommitEntryRoundTrip(t *testing.T) {
	p := AttributionCommitPayload{
		SessionID:      "20260730-101112",
		Turn:           3,
		Project:        "/home/u/blog",
		Outcome:        AttributionOutcomeAdded,
		Command:        `git commit --trailer='Co-Authored-By: Cortex (m1)' -m "fix"`,
		SHA:            "0f2e1d3",
		TrailerPresent: true,
		Verified:       true,
	}
	e, err := NewAttributionCommitEntry(p)
	if err != nil {
		t.Fatalf("NewAttributionCommitEntry: %v", err)
	}
	if e.Type != TypeAttributionCommit {
		t.Errorf("Type = %q, want %q", e.Type, TypeAttributionCommit)
	}
	if e.V != 1 {
		t.Errorf("V = %d, want 1", e.V)
	}
	got, err := ParseAttributionCommit(e)
	if err != nil {
		t.Fatalf("ParseAttributionCommit: %v", err)
	}
	if *got != p {
		t.Errorf("payload = %+v, want %+v", *got, p)
	}
}

// TestNewAttributionCommitEntryRejects cases the constructor must refuse: no
// outcome (nothing measurable), and an entry of the wrong type on parse.
func TestNewAttributionCommitEntryRejects(t *testing.T) {
	if _, err := NewAttributionCommitEntry(AttributionCommitPayload{}); err == nil {
		t.Error("NewAttributionCommitEntry with empty Outcome = nil error, want one")
	}
	other := &Entry{Type: TypeLoopRun}
	if _, err := ParseAttributionCommit(other); err == nil {
		t.Errorf("ParseAttributionCommit(%q) = nil error, want one", other.Type)
	}
}

// TestAppendAttributionCommitWritesUserLevelJournal pins the write leg: the
// event resolves its class dir through internal/userhome (not any project's
// .cortex/journal) and round-trips through the ordinary Reader.
func TestAppendAttributionCommitWritesUserLevelJournal(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	payload := AttributionCommitPayload{
		SessionID:      "20260730-101112",
		Turn:           7,
		Project:        "/home/u/blog",
		Outcome:        AttributionOutcomeSkippedAmend,
		Command:        "git commit --amend --no-edit",
		SHA:            "abc1234",
		TrailerPresent: false,
		Verified:       true,
	}
	if err := AppendAttributionCommit(payload); err != nil {
		t.Fatalf("AppendAttributionCommit: %v", err)
	}

	dir, err := userhome.Path("journal", "attribution")
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
	if e.Type != TypeAttributionCommit {
		t.Errorf("Type = %q, want %q", e.Type, TypeAttributionCommit)
	}
	if e.TS.IsZero() {
		t.Error("entry TS is zero, want the writer's timestamp")
	}
	got, err := ParseAttributionCommit(e)
	if err != nil {
		t.Fatalf("ParseAttributionCommit: %v", err)
	}
	if *got != payload {
		t.Errorf("payload = %+v, want %+v", *got, payload)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("second Next() error = %v, want io.EOF (exactly one entry)", err)
	}
}

// TestAppendAttributionCommitIsolatedByCortexHome proves the "user-level, not
// project-level" half: two CORTEX_HOME values give two independent journals,
// so a machine-level attribution receipt never leaks into another profile's
// (or a project's) journal.
func TestAppendAttributionCommitIsolatedByCortexHome(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("CORTEX_HOME", homeA)
	if err := AppendAttributionCommit(AttributionCommitPayload{Outcome: AttributionOutcomeDisabled}); err != nil {
		t.Fatalf("AppendAttributionCommit (homeA): %v", err)
	}

	t.Setenv("CORTEX_HOME", homeB)
	dirB, err := userhome.Path("journal", "attribution")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	rB, err := NewReader(dirB)
	if err != nil {
		t.Fatalf("NewReader(dirB): %v", err)
	}
	defer rB.Close()
	if _, err := rB.Next(); err != io.EOF {
		t.Errorf("homeB journal Next() error = %v, want io.EOF (homeA's write must not leak)", err)
	}
}

// TestLatestAttributionCommitsReadsBackInOrder checks the audit reader: every
// event comes back oldest first, the verification and intent writes stay
// distinct, and a malformed line mid-stream is skipped rather than fatal.
func TestLatestAttributionCommitsReadsBackInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	dir, err := userhome.Path("journal", "attribution")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	base := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	appendAt := func(p AttributionCommitPayload, offset time.Duration) {
		t.Helper()
		e, err := NewAttributionCommitEntry(p)
		if err != nil {
			t.Fatalf("NewAttributionCommitEntry: %v", err)
		}
		e.TS = base.Add(offset)
		if _, err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	appendAt(AttributionCommitPayload{Outcome: AttributionOutcomeAdded, Command: "git commit -m a"}, time.Minute)
	appendAt(AttributionCommitPayload{Outcome: AttributionOutcomeAdded, Command: "git commit -m a", SHA: "aaa1111", TrailerPresent: true, Verified: true}, 2*time.Minute)
	appendAt(AttributionCommitPayload{Outcome: AttributionOutcomeSkippedStdin, Command: "echo msg | git commit -F -"}, 3*time.Minute)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, stamps, err := LatestAttributionCommits()
	if err != nil {
		t.Fatalf("LatestAttributionCommits: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(got), got)
	}
	if got[0].Verified || got[0].SHA != "" {
		t.Errorf("first entry = %+v, want the intent write (no SHA, not verified)", got[0])
	}
	if !got[1].Verified || got[1].SHA != "aaa1111" || !got[1].TrailerPresent {
		t.Errorf("second entry = %+v, want the verified write (sha aaa1111, trailer present)", got[1])
	}
	if got[2].Outcome != AttributionOutcomeSkippedStdin {
		t.Errorf("third entry outcome = %q, want %q", got[2].Outcome, AttributionOutcomeSkippedStdin)
	}
	for i := 1; i < len(stamps); i++ {
		if stamps[i].Before(stamps[i-1]) {
			t.Errorf("stamps not oldest-first: %v before %v", stamps[i], stamps[i-1])
		}
	}
}

// TestLatestAttributionCommitsSkipsMalformed proves the audit read degrades
// quietly: an unparsable payload line mid-stream is skipped, the good entries
// around it still come back, and the error stays nil.
func TestLatestAttributionCommitsSkipsMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)

	dir, err := userhome.Path("journal", "attribution")
	if err != nil {
		t.Fatalf("userhome.Path: %v", err)
	}
	if err := AppendAttributionCommit(AttributionCommitPayload{Outcome: AttributionOutcomeAdded}); err != nil {
		t.Fatalf("AppendAttributionCommit: %v", err)
	}
	// Corrupt the segment by hand: an attribution.commit envelope whose
	// payload is not valid JSON for the payload struct.
	pendTail(t, dir, `{"type":"attribution.commit","v":1,"offset":99,"ts":"2026-07-30T10:00:00Z","payload":"not-an-object"}`)
	if err := AppendAttributionCommit(AttributionCommitPayload{Outcome: AttributionOutcomeDisabled}); err != nil {
		t.Fatalf("AppendAttributionCommit (second): %v", err)
	}

	got, _, err := LatestAttributionCommits()
	if err != nil {
		t.Fatalf("LatestAttributionCommits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2 (the malformed one skipped): %+v", len(got), got)
	}
	if got[0].Outcome != AttributionOutcomeAdded || got[1].Outcome != AttributionOutcomeDisabled {
		t.Errorf("outcomes = %q, %q, want added then disabled", got[0].Outcome, got[1].Outcome)
	}
}
