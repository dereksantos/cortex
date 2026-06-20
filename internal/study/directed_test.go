package study

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNextDirectedFocus(t *testing.T) {
	findings := []Finding{
		{Pass: 0, Leads: []Lead{{RelPath: "a.go", NearLine: 10, Why: "old lead"}}},
		{Pass: 1, Leads: []Lead{{RelPath: "b.go", NearLine: 200, Why: "recent lead"}}},
	}

	t.Run("picks the most recent unused lead", func(t *testing.T) {
		f, key := nextDirectedFocus(findings, map[string]bool{})
		if f == nil || f.Path != "b.go" {
			t.Fatalf("expected the recent b.go lead, got %+v", f)
		}
		if key != "b.go:200" {
			t.Errorf("key = %q, want b.go:200", key)
		}
		// Focus window brackets the near-line.
		if f.Lines[0] >= 200 || f.Lines[1] <= 200 {
			t.Errorf("focus window %v should bracket line 200", f.Lines)
		}
	})

	t.Run("skips used leads, falls back to older", func(t *testing.T) {
		f, _ := nextDirectedFocus(findings, map[string]bool{"b.go:200": true})
		if f == nil || f.Path != "a.go" {
			t.Fatalf("expected fallback to a.go, got %+v", f)
		}
	})

	t.Run("returns nil when all leads used", func(t *testing.T) {
		used := map[string]bool{"a.go:10": true, "b.go:200": true}
		if f, _ := nextDirectedFocus(findings, used); f != nil {
			t.Errorf("expected nil, got %+v", f)
		}
	})

	t.Run("ignores malformed leads", func(t *testing.T) {
		bad := []Finding{{Pass: 0, Leads: []Lead{{RelPath: "", NearLine: 5}, {RelPath: "c.go", NearLine: 0}}}}
		if f, _ := nextDirectedFocus(bad, map[string]bool{}); f != nil {
			t.Errorf("malformed leads should yield nil, got %+v", f)
		}
	})
}

// Integration: with DirectedSampling on, a pass that emits a lead steers a later
// pass's inference to focus on that lead's region.
func TestStudyLoop_DirectedSamplingFollowsLeads(t *testing.T) {
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

	var focusSeen []*Focus
	pass := 0
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		focusSeen = append(focusSeen, in.Focus)
		defer func() { pass++ }()
		if pass == 0 {
			// Pass 0 surfaces a lead pointing deep into the file.
			return InferOutput{Digest: "first", Leads: []Lead{{RelPath: "big.txt", NearLine: 1500, Why: "investigate the spike"}}}, nil
		}
		return InferOutput{Digest: "later"}, nil
	}

	req := StudyRequest{Path: path, Window: 8192, Infer: infer, DirectedSampling: true}
	if _, err := StudyLoop(context.Background(), req, alwaysDensify{}, 3); err != nil {
		t.Fatalf("StudyLoop: %v", err)
	}
	if len(focusSeen) < 2 {
		t.Fatalf("want ≥2 passes, got %d", len(focusSeen))
	}
	if focusSeen[0] != nil {
		t.Errorf("pass 0 should have no directed focus, got %+v", focusSeen[0])
	}
	// A later pass must be directed to the pass-0 lead region.
	directed := false
	for _, f := range focusSeen[1:] {
		if f != nil && f.Path == "big.txt" && f.Lines[0] <= 1500 && f.Lines[1] >= 1500 {
			directed = true
		}
	}
	if !directed {
		t.Errorf("a later pass should be directed to the lead at line 1500; foci=%+v", focusSeen)
	}
}
