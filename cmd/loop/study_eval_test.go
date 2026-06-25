package main

import "testing"

func TestParseNavCitations(t *testing.T) {
	digest := "The dispatcher runs tools in Resolve @cmd/loop/main.go:3086-3165. " +
		"A single line is cited @tools.go:42. Non-citation: see line 99 here."
	cits := parseNavCitations(digest)
	if len(cits) != 2 {
		t.Fatalf("want 2 citations, got %d: %+v", len(cits), cits)
	}
	if cits[0].RelPath != "cmd/loop/main.go" || cits[0].LineStart != 3086 || cits[0].LineEnd != 3165 {
		t.Errorf("first citation wrong: %+v", cits[0])
	}
	// Single-line @ref → start == end.
	if cits[1].LineStart != 42 || cits[1].LineEnd != 42 {
		t.Errorf("single-line citation should have start==end, got %+v", cits[1])
	}
	// The claim is the sentence text preceding the ref (so the scorer can match
	// 'Resolve' against the cited lines).
	if !contains(cits[0].Claim, "Resolve") {
		t.Errorf("claim should carry preceding prose; got %q", cits[0].Claim)
	}
}

func TestSentenceStart(t *testing.T) {
	s := "First sentence. Second one here"
	// pos inside the second sentence resolves to just after the period+space.
	if got := sentenceStart(s, len(s)); got != len("First sentence.") {
		t.Errorf("sentenceStart = %d, want %d", got, len("First sentence."))
	}
	if got := sentenceStart("no terminator", 5); got != 0 {
		t.Errorf("no terminator should yield 0, got %d", got)
	}
}

func TestCountGoalHits(t *testing.T) {
	text := "The Resolve loop dispatches tool calls to Execute."
	if got := countGoalHits(text, []string{"Resolve", "Execute", "missing"}); got != 2 {
		t.Errorf("countGoalHits = %d, want 2", got)
	}
	if got := countGoalHits(text, nil); got != 0 {
		t.Errorf("no wants → 0, got %d", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
