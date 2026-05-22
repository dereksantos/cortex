//go:build !windows

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cliout"
)

// writeSummary appends one think.session_summary entry under the
// given workdir/.cortex/journal/think/ directory. Mirrors what the
// finalize path does in repl.go; minimal subset that exercises the
// readRecentSessionSummaries path.
func writeSummary(t *testing.T, workdir string, p journal.ThinkSessionSummaryPayload) {
	t.Helper()
	classDir := filepath.Join(workdir, ".cortex", "journal", "think")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: classDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	e, err := journal.NewThinkSessionSummaryEntry(p)
	if err != nil {
		t.Fatalf("NewThinkSessionSummaryEntry: %v", err)
	}
	if _, err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestReadRecentSessionSummaries_LiftsCrossSessionFilter(t *testing.T) {
	workdir := t.TempDir()

	// Three prior-session summaries + two current-session summaries.
	for i, turn := range []int{1, 2, 3} {
		_ = i
		writeSummary(t, workdir, journal.ThinkSessionSummaryPayload{
			SessionID: "prior-A",
			Turn:      turn,
			Summary:   "old session A summary",
		})
	}
	for _, turn := range []int{1, 2} {
		writeSummary(t, workdir, journal.ThinkSessionSummaryPayload{
			SessionID: "prior-B",
			Turn:      turn,
			Summary:   "old session B summary",
		})
	}
	for _, turn := range []int{1, 2, 3, 4} {
		writeSummary(t, workdir, journal.ThinkSessionSummaryPayload{
			SessionID: "current",
			Turn:      turn,
			Summary:   "current session summary",
		})
	}

	s := &replState{workdir: workdir, sessionID: "current", ui: cliout.Discard()}

	t.Run("default-both-caps_includes_prior_and_current", func(t *testing.T) {
		got := s.readRecentSessionSummaries(3, 2) // 3 current, 2 prior
		if len(got) != 5 {
			t.Fatalf("len(got)=%d; want 5 (3 current + 2 prior)", len(got))
		}
		// Prior entries should appear first.
		priorCount := 0
		for _, p := range got {
			if p.SessionID != "current" {
				priorCount++
			}
		}
		if priorCount != 2 {
			t.Errorf("prior count = %d, want 2", priorCount)
		}
	})

	t.Run("zero-prior-cap_falls_back_to_current_only", func(t *testing.T) {
		got := s.readRecentSessionSummaries(3, 0)
		if len(got) != 3 {
			t.Fatalf("len(got)=%d; want 3 (current only, prior disabled)", len(got))
		}
		for _, p := range got {
			if p.SessionID != "current" {
				t.Errorf("unexpected prior session leaked: %+v", p)
			}
		}
	})

	t.Run("zero-current-cap_returns_prior_only", func(t *testing.T) {
		got := s.readRecentSessionSummaries(0, 2)
		if len(got) != 2 {
			t.Fatalf("len(got)=%d; want 2 (prior only)", len(got))
		}
		for _, p := range got {
			if p.SessionID == "current" {
				t.Errorf("current leaked when currentCap=0: %+v", p)
			}
		}
	})

	t.Run("both-zero_returns_nil", func(t *testing.T) {
		if got := s.readRecentSessionSummaries(0, 0); got != nil {
			t.Errorf("both caps=0 should return nil, got %d entries", len(got))
		}
	})
}

func TestSummariesAsChatMessages_TagsPriorSessions(t *testing.T) {
	summaries := []journal.ThinkSessionSummaryPayload{
		{SessionID: "old1", Turn: 1, Summary: "did X"},
		{SessionID: "old2", Turn: 1, Summary: "did Y"},
		{SessionID: "current", Turn: 1, Summary: "working on Z"},
		{SessionID: "current", Turn: 2, Summary: "still on Z"},
	}
	msgs := summariesAsChatMessages(summaries)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (user+assistant), got %d", len(msgs))
	}
	body := msgs[0].Content
	if !strings.Contains(body, "[prior session] [turn 1] did X") {
		t.Errorf("prior-session tag missing for old1; body:\n%s", body)
	}
	if !strings.Contains(body, "[prior session] [turn 1] did Y") {
		t.Errorf("prior-session tag missing for old2; body:\n%s", body)
	}
	// Current-session entries get no prior-session tag.
	if strings.Contains(body, "[prior session] [turn 1] working on Z") {
		t.Errorf("current entry was incorrectly tagged as prior: %s", body)
	}
	if !strings.Contains(body, "[turn 1] working on Z") {
		t.Errorf("current entry missing from body: %s", body)
	}
}

func TestSummariesAsChatMessages_TagsIntent(t *testing.T) {
	// Slice 3: each prior summary surfaces its classified intent in
	// the prior-message block so the next turn's model can passively
	// weight relevance. Legacy entries with no Intent set must render
	// without the tag (back-compat).
	summaries := []journal.ThinkSessionSummaryPayload{
		{SessionID: "s1", Turn: 1, Summary: "asked about postgres", Intent: "recall"},
		{SessionID: "s1", Turn: 2, Summary: "added a field", Intent: "code"},
		{SessionID: "s1", Turn: 3, Summary: "legacy entry, no intent"},
	}
	msgs := summariesAsChatMessages(summaries)
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	body := msgs[0].Content
	if !strings.Contains(body, "[intent=recall] [turn 1] asked about postgres") {
		t.Errorf("recall intent tag missing; body:\n%s", body)
	}
	if !strings.Contains(body, "[intent=code] [turn 2] added a field") {
		t.Errorf("code intent tag missing; body:\n%s", body)
	}
	if strings.Contains(body, "[intent=] ") || strings.Contains(body, "[intent= ]") {
		t.Errorf("legacy entry must NOT render an empty intent tag; body:\n%s", body)
	}
	if !strings.Contains(body, "[turn 3] legacy entry, no intent") {
		t.Errorf("legacy entry must still render with no intent tag; body:\n%s", body)
	}
}

func TestDigestAndSummariesAsChatMessages_noDigestPassesThrough(t *testing.T) {
	// When digest is nil, the function must behave exactly like
	// summariesAsChatMessages so the existing hydration path is
	// preserved.
	summaries := []journal.ThinkSessionSummaryPayload{
		{SessionID: "s1", Turn: 1, Summary: "did X", Intent: "code"},
	}
	got := digestAndSummariesAsChatMessages(nil, summaries)
	want := summariesAsChatMessages(summaries)
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d", len(got), len(want))
	}
	if got[0].Content != want[0].Content {
		t.Errorf("content mismatch:\n got: %q\nwant: %q", got[0].Content, want[0].Content)
	}
}

func TestDigestAndSummariesAsChatMessages_digestPresent(t *testing.T) {
	digest := &journal.DreamSessionDigestPayload{
		Narrative:        "Across the last 15 turns the user built intent ingestion + per-intent budgets.",
		SummaryCountIn:   15,
		CoversSessionIDs: []string{"s1", "s2"},
	}
	summaries := []journal.ThinkSessionSummaryPayload{
		{SessionID: "current", Turn: 16, Summary: "added feedback writer-class auto-emit", Intent: "code"},
		{SessionID: "current", Turn: 17, Summary: "wired clarify follow-up stitching", Intent: "code"},
	}
	msgs := digestAndSummariesAsChatMessages(digest, summaries)
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	body := msgs[0].Content
	if !strings.Contains(body, "digest covering 15 earlier turn(s)") {
		t.Errorf("digest header missing; body:\n%s", body)
	}
	if !strings.Contains(body, "intent ingestion") {
		t.Errorf("digest narrative missing; body:\n%s", body)
	}
	if !strings.Contains(body, "[turn 16] added feedback") {
		t.Errorf("post-digest summary 16 missing; body:\n%s", body)
	}
	if !strings.Contains(body, "[intent=code] [turn 17] wired clarify") {
		t.Errorf("post-digest summary 17 missing intent tag; body:\n%s", body)
	}
}

func TestDigestCoveredCount_noDigestReturnsZero(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-digest-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s"}
	if got := s.digestCoveredCount(); got != 0 {
		t.Errorf("empty workdir must yield 0, got %d", got)
	}
}

func TestDigestCoveredCount_returnsLatestSummaryCount(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-digest-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s"}

	// Write two digests; the LATER one's SummaryCountIn is what
	// digestCoveredCount must return.
	writeDigest(t, tempDir, 15)
	writeDigest(t, tempDir, 30)

	if got := s.digestCoveredCount(); got != 30 {
		t.Errorf("expected latest digest's SummaryCountIn (30), got %d", got)
	}
}

func TestReadLatestSessionDigest_returnsNilWhenAbsent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-digest-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s"}
	if got := s.readLatestSessionDigest(); got != nil {
		t.Errorf("expected nil with empty workdir, got %+v", got)
	}
}

func TestReadLatestSessionDigest_returnsLatest(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-digest-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s"}

	writeDigest(t, tempDir, 15)
	writeDigest(t, tempDir, 30)

	got := s.readLatestSessionDigest()
	if got == nil {
		t.Fatal("expected non-nil digest")
	}
	if got.SummaryCountIn != 30 {
		t.Errorf("expected latest digest (SummaryCountIn=30), got %d", got.SummaryCountIn)
	}
}

// writeDigest is a tiny test helper that appends a dream.session_digest
// entry with the given SummaryCountIn — used to seed the journal for
// hydration / count tests without going through maybeWriteSessionDigest.
func writeDigest(t *testing.T, workdir string, summaryCount int) {
	t.Helper()
	entry, err := journal.NewDreamSessionDigestEntry(journal.DreamSessionDigestPayload{
		Narrative:      "test digest narrative",
		SummaryCountIn: summaryCount,
		CompressOp:     "test",
	})
	if err != nil {
		t.Fatalf("NewDreamSessionDigestEntry: %v", err)
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(workdir, ".cortex", "journal", "dream"),
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestMaybeWriteSessionDigest_belowThresholdIsNoOp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-digest-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)
	s := &replState{workdir: tempDir, sessionID: "s", model: "no-such-model"}

	// Seed 5 summaries — below threshold of 15. Must not trigger digest
	// (and importantly, must not error trying to call a missing provider).
	for i := 1; i <= 5; i++ {
		writeSummary(t, tempDir, journal.ThinkSessionSummaryPayload{
			SessionID: "s",
			Turn:      i,
			Intent:    "code",
			Summary:   "test summary",
		})
	}
	if err := s.maybeWriteSessionDigest(); err != nil {
		t.Fatalf("below-threshold must not error, got: %v", err)
	}
	dreamDir := filepath.Join(tempDir, ".cortex", "journal", "dream")
	if _, err := os.Stat(dreamDir); !os.IsNotExist(err) {
		t.Errorf("below-threshold must not create dream dir: %v", err)
	}
}

func TestTailN_BoundedSlice(t *testing.T) {
	in := []journal.ThinkSessionSummaryPayload{
		{Turn: 1}, {Turn: 2}, {Turn: 3}, {Turn: 4},
	}
	cases := []struct {
		n        int
		wantLen  int
		wantTail int // wantTail is expected first-element Turn after slicing
	}{
		{0, 0, 0},
		{-3, 0, 0},
		{2, 2, 3},
		{5, 4, 1},
	}
	for _, tc := range cases {
		got := tailN(in, tc.n)
		if len(got) != tc.wantLen {
			t.Errorf("tailN(_, %d) len = %d, want %d", tc.n, len(got), tc.wantLen)
			continue
		}
		if tc.wantLen > 0 && got[0].Turn != tc.wantTail {
			t.Errorf("tailN(_, %d) first.Turn = %d, want %d", tc.n, got[0].Turn, tc.wantTail)
		}
	}
}
