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

func TestStudyLoop_NoWorkingMemory_Independent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
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
	sawFindings := false
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		if len(in.PriorFindings) > 0 {
			sawFindings = true
		}
		return InferOutput{Digest: "d"}, nil
	}
	req := StudyRequest{Path: path, Window: 8192, Infer: infer, NoWorkingMemory: true}
	if _, err := StudyLoop(context.Background(), req, alwaysDensify{}, 3); err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if sawFindings {
		t.Error("NoWorkingMemory: no pass should receive prior findings")
	}
}

// End-to-end: a later pass that cites a prior finding's anchor (which it did
// NOT sample) has that citation admitted as a relay and counted.
func TestStudyLoop_CountsFindingRelays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	blob := make([]byte, 256*1024)
	for i := range blob {
		blob[i] = 'a'
		if (i+1)%50 == 0 {
			blob[i] = '\n'
		}
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	pass := 0
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		defer func() { pass++ }()
		if pass == 0 {
			// Ground a citation in this pass's own sample.
			s := in.Sampled[0]
			return InferOutput{Digest: "first", Citations: []Citation{{RelPath: s.RelPath, LineStart: s.LineStart, LineEnd: s.LineEnd}}}, nil
		}
		// Later pass cites pass 0's finding anchor verbatim — not in its sample,
		// so only the relay path can admit it.
		if len(in.PriorFindings) > 0 && len(in.PriorFindings[0].Citations) > 0 {
			c := in.PriorFindings[0].Citations[0]
			return InferOutput{Digest: "builds on first", Citations: []Citation{c}}, nil
		}
		return InferOutput{Digest: "no findings"}, nil
	}
	req := StudyRequest{Path: path, Window: 8192, Infer: infer}
	res, err := StudyLoop(context.Background(), req, alwaysDensify{}, 3)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if res.FindingRelays < 1 {
		t.Errorf("expected ≥1 finding relay across passes, got %d (stopped=%s)", res.FindingRelays, res.Stopped)
	}
}

// P2 end-to-end: with curation on, the findings block each pass receives never
// exceeds its budget, and eviction fires (demoting via OnEvict) once enough
// findings accumulate.
func TestStudyLoop_CurationBoundsFindings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	blob := make([]byte, 256*1024)
	for i := range blob {
		blob[i] = 'a'
		if (i+1)%50 == 0 {
			blob[i] = '\n'
		}
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}

	const window = 4096
	bigDigest := strings.Repeat("finding detail ", 200) // ~3KB, forces pressure fast

	// The bound that must never be exceeded is the cap (findings ≤ 30% of the
	// window). Per-pass budget grows with the pre-curation count up to that cap;
	// curation keeps each pass's prompt under the active budget, so the cap is
	// the externally-observable ceiling.
	capChars := FindingsBudgetChars(window, 1_000_000) // saturates to the cap
	var overBudget []int
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		if total := findingsCharsTotal(in.PriorFindings); total > capChars {
			overBudget = append(overBudget, total)
		}
		s := SampledChunk{RelPath: "big.txt", LineStart: 1, LineEnd: 2}
		if len(in.Sampled) > 0 {
			s = in.Sampled[0]
		}
		return InferOutput{Digest: bigDigest, Citations: []Citation{{RelPath: s.RelPath, LineStart: s.LineStart, LineEnd: s.LineEnd}}}, nil
	}

	evicted := 0
	req := StudyRequest{
		Path:           path,
		Window:         window,
		Infer:          infer,
		CurateFindings: true,
		OnEvict:        func(Finding) { evicted++ },
	}
	res, err := StudyLoop(context.Background(), req, alwaysDensify{}, 6)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(overBudget) > 0 {
		t.Errorf("findings block exceeded budget on passes with nPrior=%v", overBudget)
	}
	if evicted == 0 {
		t.Errorf("expected evictions under sustained pressure (passes=%d)", len(res.Passes))
	}
}
