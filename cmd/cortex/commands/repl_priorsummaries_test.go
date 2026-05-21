//go:build !windows

package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
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

	s := &replState{workdir: workdir, sessionID: "current"}

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
