package study

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheablePrefix_ExcludesVolatileTail(t *testing.T) {
	base := InferInput{
		Goal:          "find the timeout source",
		PriorFindings: []Finding{{Pass: 0, Digest: "billing times out"}},
	}
	// Changing the sample must NOT change the cacheable prefix.
	a := base
	a.Sampled = []SampledChunk{{RelPath: "x", LineStart: 1, LineEnd: 2, Snippet: "alpha"}}
	b := base
	b.Sampled = []SampledChunk{{RelPath: "y", LineStart: 9, LineEnd: 9, Snippet: "beta"}}
	if CacheablePrefix(a) != CacheablePrefix(b) {
		t.Error("prefix must be independent of the sample")
	}
	// Changing the per-pass Focus must NOT change the prefix either.
	c := base
	c.Focus = &Focus{Query: "look here"}
	if CacheablePrefix(c) != CacheablePrefix(base) {
		t.Error("prefix must be independent of Focus (volatile tail)")
	}
}

func TestCacheablePrefix_ExtendsOnAppend(t *testing.T) {
	p1 := InferInput{Goal: "g", PriorFindings: []Finding{{Pass: 0, Digest: "first"}}}
	p2 := InferInput{Goal: "g", PriorFindings: []Finding{
		{Pass: 0, Digest: "first"},
		{Pass: 1, Digest: "second"},
	}}
	pre1, pre2 := CacheablePrefix(p1), CacheablePrefix(p2)
	if !strings.HasPrefix(pre2, pre1) {
		t.Error("append-only findings: pass-2 prefix must extend pass-1's (cache hit)")
	}
}

func TestCacheablePrefix_BreaksOnHeadChange(t *testing.T) {
	// Dropping the oldest finding (a recency-trim head change) breaks the prefix.
	p1 := InferInput{Goal: "g", PriorFindings: []Finding{
		{Pass: 0, Digest: "first"},
		{Pass: 1, Digest: "second"},
	}}
	p2 := InferInput{Goal: "g", PriorFindings: []Finding{
		{Pass: 1, Digest: "second"}, // oldest dropped → head changed
	}}
	if strings.HasPrefix(CacheablePrefix(p2), CacheablePrefix(p1)) {
		t.Error("a head change must break the cacheable prefix")
	}
}

// Integration: an append-only multi-pass run keeps the prefix warm (no breaks).
func TestStudyLoop_PrefixWarmWhenAppendOnly(t *testing.T) {
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
	infer := func(_ context.Context, _ InferInput) (InferOutput, error) {
		return InferOutput{Digest: "small digest"}, nil // tiny → never hits the cap
	}
	req := StudyRequest{Path: path, Window: 16384, Infer: infer}
	res, err := StudyLoop(context.Background(), req, alwaysDensify{}, 4)
	if err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if res.PrefixBreaks != 0 {
		t.Errorf("append-only run should have 0 prefix breaks, got %d", res.PrefixBreaks)
	}
	if res.PrefixWarmPasses < 2 {
		t.Errorf("expected several warm passes, got %d (passes=%d)", res.PrefixWarmPasses, len(res.Passes))
	}
}
