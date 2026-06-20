package study

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var errTransport = errors.New("transport boom")

func TestBuildInferPrompt_LabelsRealRanges(t *testing.T) {
	in := InferInput{
		RelPath: "foo/bar.go",
		Goal:    "where is PgStorage evicted",
		Sampled: []SampledChunk{
			{RelPath: "foo/bar.go", LineStart: 10, LineEnd: 20, Snippet: "func Alpha() {}"},
			{RelPath: "foo/bar.go", LineStart: 90, LineEnd: 99, Snippet: "func Bravo() {}"},
		},
	}
	sys, user := BuildInferPrompt(in)

	for _, want := range []string{"foo/bar.go:10-20", "foo/bar.go:90-99", "func Alpha() {}", "func Bravo() {}", "where is PgStorage evicted"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
	// The four hard provenance constraints must be present.
	for _, want := range []string{"MUST", "NEVER cite", "lead", "validated"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing constraint marker %q", want)
		}
	}
	// Reporter-not-critic framing: study surfaces grounded material, never verdicts.
	for _, want := range []string{"REPORTER", "not a critic"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing reporter-framing marker %q", want)
		}
	}
}

func sampledFixture() []SampledChunk {
	return []SampledChunk{
		{RelPath: "a.go", LineStart: 10, LineEnd: 20},
		{RelPath: "a.go", LineStart: 50, LineEnd: 60},
	}
}

func TestValidateCitations_KeepsInRange(t *testing.T) {
	valid := ValidateCitations([]Citation{{RelPath: "a.go", LineStart: 12, LineEnd: 15, Claim: "x"}}, sampledFixture(), nil)
	if len(valid) != 1 {
		t.Fatalf("want 1 valid citation, got %d", len(valid))
	}
}

func TestValidateCitations_DropsOutOfRange(t *testing.T) {
	var dropped []Citation
	valid := ValidateCitations(
		[]Citation{{RelPath: "a.go", LineStart: 30, LineEnd: 35, Claim: "between chunks"}},
		sampledFixture(),
		func(c Citation) { dropped = append(dropped, c) },
	)
	if len(valid) != 0 {
		t.Errorf("out-of-range citation should be dropped, got %d valid", len(valid))
	}
	if len(dropped) != 1 {
		t.Errorf("drop sink should have fired once, got %d", len(dropped))
	}
}

func TestValidateCitations_DropsUnknownRelPath(t *testing.T) {
	valid := ValidateCitations([]Citation{{RelPath: "other.go", LineStart: 12, LineEnd: 15}}, sampledFixture(), nil)
	if len(valid) != 0 {
		t.Errorf("citation for an unsampled file should be dropped, got %d valid", len(valid))
	}
}

func TestValidateCitations_DropsStraddlingChunkEdge(t *testing.T) {
	// 18-25 straddles the 10-20 chunk edge — not fully contained in any
	// single sampled range, so it must be dropped (can't be attributed).
	valid := ValidateCitations([]Citation{{RelPath: "a.go", LineStart: 18, LineEnd: 25}}, sampledFixture(), nil)
	if len(valid) != 0 {
		t.Errorf("straddling citation should be dropped, got %d valid", len(valid))
	}
}

func TestParseInferResponse_PlainJSON(t *testing.T) {
	raw := `{"digest":"d","citations":[{"relpath":"a.go","line_start":10,"line_end":12,"claim":"c"}],"leads":[{"relpath":"a.go","near_line":40,"why":"w"}]}`
	out, err := ParseInferResponse(raw)
	if err != nil {
		t.Fatalf("ParseInferResponse: %v", err)
	}
	if out.Digest != "d" {
		t.Errorf("Digest = %q, want d", out.Digest)
	}
	if len(out.Citations) != 1 || out.Citations[0].LineStart != 10 || out.Citations[0].Claim != "c" {
		t.Errorf("citations parsed wrong: %+v", out.Citations)
	}
	if len(out.Leads) != 1 || out.Leads[0].NearLine != 40 {
		t.Errorf("leads parsed wrong: %+v", out.Leads)
	}
}

func TestParseInferResponse_FencedJSON(t *testing.T) {
	raw := "Sure, here is the result:\n```json\n{\"digest\":\"hello\",\"citations\":[],\"leads\":[]}\n```\nlet me know!"
	out, err := ParseInferResponse(raw)
	if err != nil {
		t.Fatalf("ParseInferResponse: %v", err)
	}
	if out.Digest != "hello" {
		t.Errorf("Digest = %q, want hello", out.Digest)
	}
}

func TestParseInferResponse_Garbage(t *testing.T) {
	if _, err := ParseInferResponse("there is no json here"); err == nil {
		t.Error("expected an error parsing non-JSON")
	}
}

func TestParseInferResponse_TrailingCommas(t *testing.T) {
	raw := `{"digest":"d","citations":[{"relpath":"a.go","line_start":1,"line_end":2,"claim":"c"},],"leads":[],}`
	out, err := ParseInferResponse(raw)
	if err != nil {
		t.Fatalf("trailing commas should be repaired: %v", err)
	}
	if out.Digest != "d" || len(out.Citations) != 1 {
		t.Errorf("got %+v", out)
	}
}

func TestProviderInfer_SalvagesMalformedJSON(t *testing.T) {
	// Unquoted key — beyond trailing-comma repair. Must NOT error; the
	// digest is salvaged and no citations are emitted.
	prov := scriptedCuratorProvider{resp: `{"digest":"the router picks the model", citations: nope}`, avail: true}
	out, err := ProviderInfer(prov)(context.Background(), InferInput{})
	if err != nil {
		t.Fatalf("malformed JSON should degrade, not error: %v", err)
	}
	if out.Digest != "the router picks the model" {
		t.Errorf("Digest = %q, want salvaged digest", out.Digest)
	}
	if len(out.Citations) != 0 {
		t.Errorf("unparseable response must yield no citations, got %d", len(out.Citations))
	}
}

func TestProviderInfer_TransportErrorSurfaces(t *testing.T) {
	prov := scriptedCuratorProvider{avail: true, err: errTransport}
	if _, err := ProviderInfer(prov)(context.Background(), InferInput{}); err == nil {
		t.Error("a transport error must surface (not be salvaged)")
	}
}

func TestSalvageDigest(t *testing.T) {
	if got := salvageDigest(`prose {"digest":"hello world","x":bad}`); got != "hello world" {
		t.Errorf("salvageDigest extracted %q, want hello world", got)
	}
	if got := salvageDigest("```\njust prose\n```"); got != "just prose" {
		t.Errorf("salvageDigest fence-strip = %q, want just prose", got)
	}
}

// Adjacent sampled fragments merge for validation: at unit granularity a
// legitimate claim spans several contiguous fragments the model saw — a
// citation across them must validate. Pinhole gaps from edge refinement
// (≤ citationMergeGapLines) merge too; real gaps do not.
func TestValidateCitations_UnionOfAdjacentFragments(t *testing.T) {
	sampled := []SampledChunk{
		{RelPath: "doc.md", LineStart: 10, LineEnd: 30},
		{RelPath: "doc.md", LineStart: 31, LineEnd: 55},   // exactly adjacent
		{RelPath: "doc.md", LineStart: 58, LineEnd: 80},   // pinhole gap (2 lines)
		{RelPath: "doc.md", LineStart: 200, LineEnd: 220}, // real gap
	}
	t.Run("section claim across contiguous fragments validates", func(t *testing.T) {
		valid := ValidateCitations([]Citation{{RelPath: "doc.md", LineStart: 12, LineEnd: 75, Claim: "whole section"}}, sampled, nil)
		if len(valid) != 1 {
			t.Errorf("union-contained citation should validate, got %d", len(valid))
		}
	})
	t.Run("claim spanning a real gap is dropped", func(t *testing.T) {
		valid := ValidateCitations([]Citation{{RelPath: "doc.md", LineStart: 60, LineEnd: 210, Claim: "spans unseen"}}, sampled, nil)
		if len(valid) != 0 {
			t.Errorf("citation across an unseen gap should drop, got %d valid", len(valid))
		}
	})
}

// Data and code get per-line numbers in the prompt (data: the model
// otherwise cites record ids as line numbers; code: 52% -> 100% grounded
// in the n=10 2x2 grid); prose stays unnumbered (sections anchor well
// and the prefix costs budget). The Numbered override forces either way.
func TestBuildInferPrompt_NumbersDataLines(t *testing.T) {
	mk := func(rel string, numbered *bool) InferInput {
		return InferInput{
			RelPath:  rel,
			Numbered: numbered,
			Sampled:  []SampledChunk{{RelPath: rel, LineStart: 51, LineEnd: 52, Snippet: "{\"id\":1}\n{\"id\":2}\n"}},
		}
	}
	for _, rel := range []string{"events.jsonl", "main.go"} {
		_, user := BuildInferPrompt(mk(rel, nil))
		for _, want := range []string{"51| {\"id\":1}", "52| {\"id\":2}"} {
			if !strings.Contains(user, want) {
				t.Errorf("%s prompt missing numbered line %q in:\n%s", rel, want, user)
			}
		}
	}
	_, user := BuildInferPrompt(mk("doc.md", nil))
	if strings.Contains(user, "51| ") {
		t.Errorf("prose prompt should not number lines:\n%s", user)
	}
	off := false
	_, user = BuildInferPrompt(mk("main.go", &off))
	if strings.Contains(user, "51| ") {
		t.Errorf("Numbered=false override should suppress numbering:\n%s", user)
	}
}

// A corpus mixes formats; the numbering rule is per region, not per
// study target. Code regions get coordinates, prose regions stay bare.
func TestBuildInferPrompt_NumbersPerRegionInCorpus(t *testing.T) {
	in := InferInput{
		RelPath: "pkg/stuff", // a directory display path
		Sampled: []SampledChunk{
			{RelPath: "pkg/stuff/main.go", LineStart: 10, LineEnd: 11, Snippet: "func a() {}\nfunc b() {}\n"},
			{RelPath: "pkg/stuff/README.md", LineStart: 5, LineEnd: 6, Snippet: "# Title\nProse line.\n"},
		},
	}
	_, user := BuildInferPrompt(in)
	if !strings.Contains(user, "10| func a() {}") {
		t.Errorf("code region in corpus should be numbered:\n%s", user)
	}
	if strings.Contains(user, "5| # Title") {
		t.Errorf("prose region in corpus should stay bare:\n%s", user)
	}
}

// Verbatim relays validate; invented ranges on the same file do not.
// This is the digest-of-digests hierarchy contract: a lower level's
// citation visible in a sampled snippet may propagate upward unchanged.
func TestValidateCitations_VerbatimRelay(t *testing.T) {
	sampled := []SampledChunk{{
		RelPath: "corpus.txt", LineStart: 1, LineEnd: 4,
		Snippet: "===== LEVEL-0 DIGEST OF: pkg/a/x.go =====\nThe widget frobs.\ncitations:\n  pkg/a/x.go:35-48  Frob entrypoint.\n",
	}}
	valid := ValidateCitations([]Citation{
		{RelPath: "pkg/a/x.go", LineStart: 35, LineEnd: 48, Claim: "faithful relay"},
		{RelPath: "pkg/a/x.go", LineStart: 30, LineEnd: 50, Claim: "invented range"},
		{RelPath: "corpus.txt", LineStart: 2, LineEnd: 2, Claim: "normal in-sample"},
	}, sampled, nil)
	if len(valid) != 2 {
		t.Fatalf("want 2 valid (relay + in-sample), got %d: %+v", len(valid), valid)
	}
	for _, v := range valid {
		if v.Claim == "invented range" {
			t.Errorf("invented relay range must drop")
		}
	}
}
