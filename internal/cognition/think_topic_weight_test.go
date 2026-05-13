package cognition

import (
	"io"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestTopicWeightDeltas covers the delta computation: additions,
// removals, and below-threshold changes are filtered correctly.
func TestTopicWeightDeltas(t *testing.T) {
	tests := []struct {
		name      string
		old, new  map[string]float64
		threshold float64
		wantTerms []string
	}{
		{
			name:      "addition past threshold",
			old:       map[string]float64{},
			new:       map[string]float64{"auth": 0.6},
			threshold: 0.05,
			wantTerms: []string{"auth"},
		},
		{
			name:      "below threshold filtered",
			old:       map[string]float64{"auth": 0.50},
			new:       map[string]float64{"auth": 0.52},
			threshold: 0.05,
			wantTerms: nil,
		},
		{
			name:      "removal counts as delta",
			old:       map[string]float64{"old": 0.4},
			new:       map[string]float64{},
			threshold: 0.05,
			wantTerms: []string{"old"},
		},
		{
			name:      "addition + removal + unchanged",
			old:       map[string]float64{"old": 0.4, "stable": 0.5},
			new:       map[string]float64{"new": 0.7, "stable": 0.5},
			threshold: 0.05,
			wantTerms: []string{"new", "old"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := topicWeightDeltas(tc.old, tc.new, tc.threshold)
			gotTerms := make([]string, len(got))
			for i, d := range got {
				gotTerms[i] = d.Topic
			}
			sort.Strings(gotTerms)
			sort.Strings(tc.wantTerms)
			if !equalStrings(gotTerms, tc.wantTerms) {
				t.Errorf("terms=%v want %v", gotTerms, tc.wantTerms)
			}
		})
	}
}

// TestThink_UpdateTopicWeights_EmitsDeltaEntries is the T1 follow-up
// acceptance: updateTopicWeights writes one think.topic_weight entry
// per material change. Snapshot emission via MaybeThink is unaffected.
func TestThink_UpdateTopicWeights_EmitsDeltaEntries(t *testing.T) {
	tempDir := t.TempDir()
	journalRoot := filepath.Join(tempDir, "journal")

	think := NewThink(nil, nil, NewActivityTracker())
	think.SetJournalDir(journalRoot)
	think.SetSessionID("sess-T1")

	// Two queries sharing terms force counts ≥ 2 → weights ≥ 0.2 →
	// retained by updateTopicWeights.
	think.sessionCtx.RecentQueries = []cognition.Query{
		{Text: "authentication flow"},
		{Text: "authentication token"},
	}

	think.updateTopicWeights()

	r, err := journal.NewReader(filepath.Join(journalRoot, "think"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	var seenTopics []string
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if e.Type != journal.TypeThinkTopicWeight {
			continue
		}
		p, err := journal.ParseThinkTopicWeight(e)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if p.SessionID != "sess-T1" {
			t.Errorf("SessionID=%q want sess-T1", p.SessionID)
		}
		seenTopics = append(seenTopics, p.Topic)
	}
	if len(seenTopics) == 0 {
		t.Fatal("no think.topic_weight entries emitted; expected at least one delta")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
