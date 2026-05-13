package journal

import "testing"

func TestAssertLocalOnly_RejectsJournalPaths(t *testing.T) {
	rejected := []string{
		"/Users/foo/.cortex/journal/capture/0001.jsonl",
		".cortex/journal/dream",
		"journal/observation",
		"./.cortex/journal/eval/0042.jsonl.gz",
	}
	for _, p := range rejected {
		if err := AssertLocalOnly(p); err == nil {
			t.Errorf("AssertLocalOnly(%q) = nil, want error", p)
		}
	}
}

func TestAssertLocalOnly_AllowsNonJournalPaths(t *testing.T) {
	allowed := []string{
		"/Users/foo/.cortex/data/events.jsonl",
		"/tmp/output.json",
		".cortex/config.json",
		"docs/journal.md", // doc file mentioning "journal" as a name component is fine
	}
	for _, p := range allowed {
		if err := AssertLocalOnly(p); err != nil {
			t.Errorf("AssertLocalOnly(%q) = %v, want nil", p, err)
		}
	}
}
