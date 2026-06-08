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
