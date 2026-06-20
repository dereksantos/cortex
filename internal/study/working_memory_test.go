package study

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildInferPrompt_FindingsLeadTheSample(t *testing.T) {
	in := InferInput{
		RelPath: "f.go",
		Sampled: []SampledChunk{{RelPath: "f.go", LineStart: 10, LineEnd: 20, Snippet: "func A() {}"}},
		PriorFindings: []Finding{
			{Pass: 0, Digest: "A initializes the store", Citations: []Citation{{RelPath: "f.go", LineStart: 3, LineEnd: 8}}},
		},
	}
	_, user := BuildInferPrompt(in)

	for _, want := range []string{"Findings so far", "[pass 1] A initializes the store", "f.go:3-8"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt missing %q\n%s", want, user)
		}
	}
	// The findings block must precede the sample so the stable prefix can cache.
	if strings.Index(user, "initializes the store") > strings.Index(user, "Sampled regions") {
		t.Error("findings should be rendered BEFORE the sample")
	}
	// The verbatim path:line anchor is what lets a later pass relay the citation.
	if !strings.Contains(user, "cite: f.go:3-8") {
		t.Errorf("citation anchor not rendered verbatim:\n%s", user)
	}
}

func TestBuildInferPrompt_NoFindingsUnchanged(t *testing.T) {
	// Pass 1 (no findings) must not gain a findings header.
	in := InferInput{RelPath: "f.go", Sampled: []SampledChunk{{RelPath: "f.go", LineStart: 1, LineEnd: 2, Snippet: "x"}}}
	_, user := BuildInferPrompt(in)
	if strings.Contains(user, "Findings so far") {
		t.Errorf("pass-1 prompt should have no findings block:\n%s", user)
	}
}

func TestFindingsBudgetChars_GrowsAndCaps(t *testing.T) {
	if got := FindingsBudgetChars(32768, 0); got != 0 {
		t.Errorf("no prior passes → 0, got %d", got)
	}
	a, b := FindingsBudgetChars(32768, 1), FindingsBudgetChars(32768, 3)
	if b <= a {
		t.Errorf("budget should grow with prior passes: f(3)=%d !> f(1)=%d", b, a)
	}
	w := 32768.0
	capChars := int(w*findingsBudgetCapFrac) * studyCharsPerToken
	if got := FindingsBudgetChars(32768, 100); got != capChars {
		t.Errorf("budget should cap at %d, got %d", capChars, got)
	}
}

func TestTrimFindingsToBudget(t *testing.T) {
	mk := func(p int, d string) Finding { return Finding{Pass: p, Digest: d} }
	findings := []Finding{mk(0, "first finding"), mk(1, "second finding"), mk(2, "third finding")}

	t.Run("keeps the most recent that fit", func(t *testing.T) {
		budget := findingChars(findings[1]) + findingChars(findings[2])
		got := trimFindingsToBudget(findings, budget)
		if len(got) != 2 || got[0].Pass != 1 || got[1].Pass != 2 {
			t.Errorf("want passes [1,2], got %+v", got)
		}
	})

	t.Run("always keeps the newest even if it alone exceeds budget", func(t *testing.T) {
		got := trimFindingsToBudget(findings, 1)
		if len(got) != 1 || got[0].Pass != 2 {
			t.Errorf("want newest only, got %+v", got)
		}
	})

	t.Run("zero budget drops everything", func(t *testing.T) {
		if got := trimFindingsToBudget(findings, 0); got != nil {
			t.Errorf("zero budget → nil, got %+v", got)
		}
	})
}

func TestAdmitFindingRelays(t *testing.T) {
	findings := []Finding{{Pass: 0, Citations: []Citation{{RelPath: "f.go", LineStart: 3, LineEnd: 8}}}}
	raw := []Citation{
		{RelPath: "f.go", LineStart: 3, LineEnd: 8, Claim: "relays a prior finding"},
		{RelPath: "f.go", LineStart: 99, LineEnd: 100, Claim: "invented, not in any sample or finding"},
	}

	t.Run("re-admits a faithful relay the sample dropped", func(t *testing.T) {
		got := admitFindingRelays(raw, nil, findings)
		if len(got) != 1 || got[0].LineStart != 3 {
			t.Errorf("want the relayed citation admitted, got %+v", got)
		}
	})

	t.Run("does not duplicate an already-validated citation", func(t *testing.T) {
		validated := []Citation{{RelPath: "f.go", LineStart: 3, LineEnd: 8}}
		got := admitFindingRelays(raw, validated, findings)
		if len(got) != 1 {
			t.Errorf("want no duplicate, got %d: %+v", len(got), got)
		}
	})

	t.Run("no findings → passthrough", func(t *testing.T) {
		validated := []Citation{{RelPath: "f.go", LineStart: 1, LineEnd: 2}}
		got := admitFindingRelays(raw, validated, nil)
		if len(got) != 1 {
			t.Errorf("want passthrough, got %+v", got)
		}
	})
}

// alwaysDensify keeps the loop deepening so we observe findings accumulate
// across passes rather than stopping after pass 1.
type alwaysDensify struct{}

func (alwaysDensify) Decide(StudyResponse, string) Decision { return Decision{Kind: DecisionDensify} }

func TestStudyLoop_ThreadsFindingsAcrossPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	// Comfortably over the read-vs-study threshold so it samples across passes.
	blob := make([]byte, 256*1024)
	for i := range blob {
		if (i+1)%50 == 0 {
			blob[i] = '\n'
		} else {
			blob[i] = 'a'
		}
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	var seen [][]Finding // PriorFindings observed per inference call
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		cp := make([]Finding, len(in.PriorFindings))
		copy(cp, in.PriorFindings)
		seen = append(seen, cp)
		if len(in.Sampled) == 0 {
			return InferOutput{Digest: "empty"}, nil
		}
		s := in.Sampled[0]
		return InferOutput{
			Digest:    fmt.Sprintf("pass digest over %d regions", len(in.Sampled)),
			Citations: []Citation{{RelPath: s.RelPath, LineStart: s.LineStart, LineEnd: s.LineEnd}},
		}, nil
	}

	req := StudyRequest{Path: path, Window: 8192, Infer: infer}
	res, err := StudyLoop(context.Background(), req, alwaysDensify{}, 3)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("want ≥2 passes to observe threading, got %d (stopped=%s)", len(seen), res.Stopped)
	}
	if len(seen[0]) != 0 {
		t.Errorf("pass 1 should see no prior findings, saw %d", len(seen[0]))
	}
	if len(seen[1]) == 0 {
		t.Errorf("pass 2 should see pass 1's finding, saw none")
	}
	// The threaded finding carries pass 1's digest forward.
	if len(seen[1]) > 0 && !strings.Contains(seen[1][0].Digest, "pass digest") {
		t.Errorf("threaded finding lost its digest: %+v", seen[1][0])
	}
}
