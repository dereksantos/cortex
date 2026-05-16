package niah

import (
	"strings"
	"testing"
)

// TestFillerModeAdversarialSharesProbeTerms — the adversarial corpus
// MUST contain phrases that share words with the default probe terms
// ("secret", "recipe", "code") for the benchmark to produce real
// signal. Without this, scoring discriminates trivially (only one
// chunk matches at all) and NIAH degenerates into a substring smoke.
func TestFillerModeAdversarialSharesProbeTerms(t *testing.T) {
	probeTerms := []string{"secret", "recipe", "code"}
	hits := map[string]int{}
	for _, phrase := range fillerCorpus(FillerAdversarial) {
		low := strings.ToLower(phrase)
		for _, term := range probeTerms {
			if strings.Contains(low, term) {
				hits[term]++
			}
		}
	}
	for _, term := range probeTerms {
		if hits[term] == 0 {
			t.Errorf("adversarial corpus has no phrase containing %q — won't compete with the default needle", term)
		}
	}
}

// TestFillerModeLoremIsClean — the lorem corpus, by contrast, MUST NOT
// contain any of the default probe terms. Lorem is the "easy" mode
// reserved for shape-of-pipeline smoke; if it accidentally picks up
// probe terms, callers using lorem to debug a regression will see
// noise that doesn't reflect their question.
func TestFillerModeLoremIsClean(t *testing.T) {
	bannedTerms := []string{"secret", "recipe", "code"}
	for _, phrase := range fillerCorpus(FillerLorem) {
		low := strings.ToLower(phrase)
		for _, term := range bannedTerms {
			if strings.Contains(low, term) {
				t.Errorf("lorem corpus phrase %q contains banned term %q (would contaminate the easy-mode smoke)", phrase, term)
			}
		}
	}
}

// TestFillerModeRoundTrip — every documented mode resolves to a
// non-empty corpus. Guards against a future enum addition that
// forgets to wire its corpus.
func TestFillerModeRoundTrip(t *testing.T) {
	for _, mode := range []FillerMode{FillerLorem, FillerAdversarial} {
		if got := fillerCorpus(mode); len(got) == 0 {
			t.Errorf("fillerCorpus(%q) returned empty slice", mode)
		}
	}
}

// TestGenerateAdversarialFillerProducesCompetition — when filler mode
// is adversarial, multiple chunks of the haystack contain probe terms,
// so retrieval must actually discriminate the needle from look-alike
// chunks. This is the property that turns NIAH from a substring smoke
// into a retrieval-quality benchmark.
func TestGenerateAdversarialFillerProducesCompetition(t *testing.T) {
	h := Generate(GenerateOpts{
		Length:     4096,
		Depth:      0.5,
		Needle:     "The secret recipe code is 4F-9X-2B.",
		Seed:       1,
		FillerMode: FillerAdversarial,
	})
	// Strip the needle so we're only counting filler matches; the
	// needle itself is excluded from the competition count.
	without := strings.Replace(h.Text, "The secret recipe code is 4F-9X-2B.", "", 1)
	low := strings.ToLower(without)
	hits := strings.Count(low, "secret") +
		strings.Count(low, "recipe") +
		strings.Count(low, "code")
	if hits < 3 {
		t.Fatalf("adversarial filler produced only %d competing-term occurrences; need ≥3 for the scorer to be exercised", hits)
	}
}

// TestGenerateDefaultModeIsAdversarial — the zero-value FillerMode
// resolves to adversarial. This is the load-bearing default: callers
// who don't specify a mode should get the meaningful benchmark, not
// the trivially-passing one.
func TestGenerateDefaultModeIsAdversarial(t *testing.T) {
	a := Generate(GenerateOpts{Length: 1024, Depth: 0.5, Needle: "x", Seed: 1})
	b := Generate(GenerateOpts{Length: 1024, Depth: 0.5, Needle: "x", Seed: 1, FillerMode: FillerAdversarial})
	if a.Text != b.Text {
		t.Fatalf("zero-value FillerMode should equal FillerAdversarial; got divergent text\n  a-len=%d\n  b-len=%d", len(a.Text), len(b.Text))
	}
}
