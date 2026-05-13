package cognition

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestParseRerankResponse_FiltersFabricatedIDs asserts that ranking IDs
// the LLM returned but were NOT in the input candidate set are dropped.
// This defends against a poisoned candidate fabricating high-rank IDs
// for content the user never indexed — without this check, a single
// poisoned source could effectively inject arbitrary IDs into the
// retrieval stream.
func TestParseRerankResponse_FiltersFabricatedIDs(t *testing.T) {
	r := NewReflect(nil)
	candidates := []cognition.Result{
		{ID: "real-1", Content: "alpha"},
		{ID: "real-2", Content: "beta"},
	}
	// LLM response references real-1 (valid), then "FAKE-1" (fabricated).
	response := `{
		"ranking": ["FAKE-1", "real-1", "ANOTHER-FAKE"],
		"contradictions": [],
		"reasoning": "test"
	}`

	got, err := r.parseRerankResponse(response, candidates)
	if err != nil {
		t.Fatalf("parseRerankResponse error: %v", err)
	}

	for _, c := range got {
		if c.ID == "FAKE-1" || c.ID == "ANOTHER-FAKE" {
			t.Errorf("fabricated ID %q present in reranked output", c.ID)
		}
	}

	// real-1 should be first (it's the only valid ranked ID); real-2
	// gets appended as an omitted candidate.
	if len(got) < 1 || got[0].ID != "real-1" {
		t.Errorf("expected real-1 first, got %v", idsOf(got))
	}
}

// TestParseRerankResponse_FiltersFabricatedContradictionIDs asserts that
// a contradiction the LLM claims between two IDs where at least one was
// fabricated is dropped from the metadata attached to results. Otherwise
// a poisoned candidate could fabricate a contradiction against a known
// real ID to make a legitimate decision look unreliable.
func TestParseRerankResponse_FiltersFabricatedContradictionIDs(t *testing.T) {
	r := NewReflect(nil)
	candidates := []cognition.Result{
		{ID: "real-1", Content: "use pgx"},
		{ID: "real-2", Content: "use postgres"},
	}
	// Order matters: put the fabricated contradiction LAST so any naive
	// "last-write-wins" implementation gets caught. The legitimate one
	// is first so we can also assert it didn't get clobbered by the
	// filter.
	response := `{
		"ranking": ["real-1", "real-2"],
		"contradictions": [
			{"ids": ["real-1", "real-2"], "reason": "legitimate conflict"},
			{"ids": ["real-1", "FAKE-ID"], "reason": "fabricated"}
		],
		"reasoning": "test"
	}`

	got, err := r.parseRerankResponse(response, candidates)
	if err != nil {
		t.Fatalf("parseRerankResponse error: %v", err)
	}

	// Find real-1 in the output and inspect its metadata.
	var real1 *cognition.Result
	for i := range got {
		if got[i].ID == "real-1" {
			real1 = &got[i]
			break
		}
	}
	if real1 == nil {
		t.Fatal("real-1 missing from output")
	}

	conflicts, _ := real1.Metadata["conflicts_with"].([]string)
	// The legitimate contradiction (real-1 vs real-2) must survive; the
	// fabricated one (real-1 vs FAKE-ID) must be filtered out.
	if !containsID(conflicts, "real-2") {
		t.Errorf("expected legitimate conflict with real-2; conflicts=%v", conflicts)
	}
	if containsID(conflicts, "FAKE-ID") {
		t.Errorf("fabricated conflict ID leaked into metadata: %v", conflicts)
	}
	reason, _ := real1.Metadata["contradiction"].(string)
	if strings.Contains(reason, "fabricated") {
		t.Errorf("fabricated contradiction reason leaked: %q", reason)
	}
}

func idsOf(rs []cognition.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}
