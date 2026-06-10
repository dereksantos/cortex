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

// Record-shaped data gets per-line numbers in the prompt (the model
// otherwise locates records by their id fields and cites unverifiable
// line numbers); code and prose stay unnumbered (headers suffice and the
// prefix costs budget).
func TestBuildInferPrompt_NumbersDataLines(t *testing.T) {
	mk := func(rel string) InferInput {
		return InferInput{
			RelPath: rel,
			Sampled: []SampledChunk{{RelPath: rel, LineStart: 51, LineEnd: 52, Snippet: "{\"id\":1}\n{\"id\":2}\n"}},
		}
	}
	_, user := BuildInferPrompt(mk("events.jsonl"))
	for _, want := range []string{"51| {\"id\":1}", "52| {\"id\":2}"} {
		if !strings.Contains(user, want) {
			t.Errorf("data prompt missing numbered line %q in:\n%s", want, user)
		}
	}
	_, user = BuildInferPrompt(mk("main.go"))
	if strings.Contains(user, "51| ") {
		t.Errorf("code prompt should not number lines:\n%s", user)
	}
}
