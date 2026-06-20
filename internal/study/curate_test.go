package study

import (
	"strings"
	"testing"
)

func TestMechanicalCompress(t *testing.T) {
	long := strings.Repeat("alpha beta gamma ", 40) // > cap
	f := Finding{
		Pass:      2,
		Digest:    long,
		Citations: []Citation{{RelPath: "f.go", LineStart: 3, LineEnd: 8}},
		Leads:     []Lead{{RelPath: "f.go", NearLine: 99, Why: "look here"}},
	}
	got := MechanicalCompress(f)

	if len(got.Digest) > compressedDigestCap+4 { // +ellipsis
		t.Errorf("compressed digest too long: %d", len(got.Digest))
	}
	if len(got.Citations) != 1 || got.Citations[0].LineStart != 3 {
		t.Errorf("compression must preserve citation anchors, got %+v", got.Citations)
	}
	if len(got.Leads) != 0 {
		t.Errorf("compression should drop leads, got %+v", got.Leads)
	}
	if got.Pass != 2 {
		t.Errorf("compression must preserve pass number, got %d", got.Pass)
	}
}

func TestMechanicalCompress_ShortDigestUnchanged(t *testing.T) {
	f := Finding{Pass: 1, Digest: "short", Citations: []Citation{{RelPath: "a", LineStart: 1, LineEnd: 2}}}
	if got := MechanicalCompress(f); got.Digest != "short" {
		t.Errorf("short digest should be unchanged, got %q", got.Digest)
	}
}

func mkFinding(pass int, digest string, cites int) Finding {
	f := Finding{Pass: pass, Digest: digest}
	for i := 0; i < cites; i++ {
		f.Citations = append(f.Citations, Citation{RelPath: "f.go", LineStart: pass*10 + i, LineEnd: pass*10 + i + 1})
	}
	return f
}

func TestCurateFindings_NoOpWhenFits(t *testing.T) {
	findings := []Finding{mkFinding(0, "one", 0), mkFinding(1, "two", 0)}
	kept, evicted := curateFindings(findings, 10_000, "goal", nil)
	if len(kept) != 2 || len(evicted) != 0 {
		t.Errorf("under budget → no-op; got kept=%d evicted=%d", len(kept), len(evicted))
	}
}

func TestCurateFindings_BoundsTheBlock(t *testing.T) {
	// Five findings whose verbatim total far exceeds the budget.
	body := strings.Repeat("xyz ", 60)
	var findings []Finding
	for p := 0; p < 5; p++ {
		findings = append(findings, mkFinding(p, body, 1))
	}
	budget := findingChars(findings[0]) * 2 // room for ~2 verbatim
	kept, evicted := curateFindings(findings, budget, "goal", nil)

	if total := findingsCharsTotal(kept); total > budget {
		// Allowed only in the degenerate newest-bigger-than-budget case; here
		// each finding compresses well under budget, so it must hold.
		t.Errorf("curated block %d chars exceeds budget %d", total, budget)
	}
	if len(kept)+len(evicted) != 5 {
		t.Errorf("every finding must be kept or evicted; got %d+%d", len(kept), len(evicted))
	}
	if len(evicted) == 0 {
		t.Error("expected some evictions under tight budget")
	}
}

func TestCurateFindings_AlwaysKeepsNewest(t *testing.T) {
	body := strings.Repeat("data ", 60)
	var findings []Finding
	for p := 0; p < 5; p++ {
		findings = append(findings, mkFinding(p, body, 0))
	}
	// Budget so tight only a compressed finding or two fit.
	kept, _ := curateFindings(findings, 300, "goal", nil)
	foundNewest := false
	for _, f := range kept {
		if f.Pass == 4 {
			foundNewest = true
		}
	}
	if !foundNewest {
		t.Errorf("newest finding (pass 4) must always be retained; kept=%+v", passes(kept))
	}
}

func TestCurateFindings_PreservesCitationsOnCompressed(t *testing.T) {
	body := strings.Repeat("longcontent ", 60)
	var findings []Finding
	for p := 0; p < 4; p++ {
		findings = append(findings, mkFinding(p, body, 2)) // each has 2 citations
	}
	kept, _ := curateFindings(findings, findingChars(findings[0]), "goal", nil)
	// Whatever survived (likely compressed) must still carry its citations — the
	// relay contract.
	for _, f := range kept {
		if len(f.Citations) == 0 {
			t.Errorf("retained finding pass %d lost its citations under curation", f.Pass)
		}
	}
}

func TestCurateFindings_EvictsLowestValue(t *testing.T) {
	// Oldest, citation-less, goal-irrelevant finding is the lowest value and
	// should be the one evicted under pressure.
	body := strings.Repeat("filler ", 50)
	findings := []Finding{
		mkFinding(0, "irrelevant "+body, 0), // oldest, no cites, off-goal
		mkFinding(1, "timeout errors "+body, 2),
		mkFinding(2, "timeout errors "+body, 2),
	}
	budget := findingChars(findings[1]) * 2
	_, evicted := curateFindings(findings, budget, "timeout errors", nil)
	if len(evicted) == 0 {
		t.Fatal("expected an eviction")
	}
	for _, f := range evicted {
		if f.Pass != 0 {
			t.Errorf("expected the oldest off-goal finding (pass 0) evicted, got pass %d", f.Pass)
		}
	}
}

func TestFindingValue_RecencyDominates(t *testing.T) {
	old := mkFinding(0, "x", 3)
	recent := mkFinding(4, "x", 0)
	if findingValue(recent, "", 4) <= findingValue(old, "", 4) {
		t.Error("a much more recent finding should outrank an old one even with fewer citations")
	}
}

func TestSynthesisCarryForward(t *testing.T) {
	prior := []string{"the billing service reports timeout errors frequently"}
	sampled := []SampledChunk{{RelPath: "x", Snippet: "checkout latency increased during deploy"}}

	t.Run("counts terms from prior digests not in the sample", func(t *testing.T) {
		// "timeout" and "billing" come from the prior digest, not the sample →
		// carried forward. "checkout"/"latency" are in the sample → not counted.
		digest := "billing timeout also affects checkout latency"
		got := synthesisCarryForward(digest, prior, sampled)
		if got < 2 {
			t.Errorf("expected ≥2 carried terms (billing, timeout), got %d", got)
		}
	})

	t.Run("term present in the sample is not carry-forward", func(t *testing.T) {
		// A digest term that IS in the sample can't be attributed to prior passes.
		digest := "checkout latency"
		if got := synthesisCarryForward(digest, prior, sampled); got != 0 {
			t.Errorf("sample-derived terms must not count, got %d", got)
		}
	})

	t.Run("no prior digests → zero", func(t *testing.T) {
		if got := synthesisCarryForward("billing timeout", nil, sampled); got != 0 {
			t.Errorf("no prior → 0, got %d", got)
		}
	})

	t.Run("each carried term counted once", func(t *testing.T) {
		digest := "timeout timeout timeout"
		if got := synthesisCarryForward(digest, prior, sampled); got != 1 {
			t.Errorf("repeated term counted once, got %d", got)
		}
	})
}

func passes(fs []Finding) []int {
	out := make([]int, len(fs))
	for i, f := range fs {
		out[i] = f.Pass
	}
	return out
}
