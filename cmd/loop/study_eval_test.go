package main

import "testing"

func TestParseNavCitations(t *testing.T) {
	// Both citation forms the model emits: @path:start-end and bare Lstart-end,
	// plus a single-line ref and a typographic-hyphen range (U+2011).
	digest := "The dispatcher runs in Resolve @cmd/loop/main.go:3086-3165. " +
		"The skeleton renders via renderSkeleton L97-104. A single ref L42. " +
		"A typographic range Span L19‑63 too."
	cits := parseNavCitations(digest)
	if len(cits) != 4 {
		t.Fatalf("want 4 citations, got %d: %+v", len(cits), cits)
	}
	if cits[0].LineStart != 3086 || cits[0].LineEnd != 3165 {
		t.Errorf("@path ref wrong: %+v", cits[0])
	}
	if cits[1].LineStart != 97 || cits[1].LineEnd != 104 {
		t.Errorf("bare L ref wrong: %+v", cits[1])
	}
	if cits[2].LineStart != 42 || cits[2].LineEnd != 42 {
		t.Errorf("single-line ref should have start==end, got %+v", cits[2])
	}
	if cits[3].LineStart != 19 || cits[3].LineEnd != 63 {
		t.Errorf("typographic-hyphen range should parse, got %+v", cits[3])
	}
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
