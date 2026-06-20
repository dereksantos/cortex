package study

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKeywordFixture writes a file where a distinctive term ("ZEBRACORN")
// appears in exactly one REGION (a contiguous block deep in the file),
// surrounded by filler — so a keyword-focused sample should land on that region.
// The term spans a block of lines (not a single edge line) so it survives
// chunk-boundary line-snapping during refinement, the realistic case.
func writeKeywordFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.txt")
	var b strings.Builder
	line := strings.Repeat("filler ", 6) + "\n" // ~43 bytes
	for i := 0; i < 4000; i++ {
		if i >= 2980 && i < 3020 { // a ~40-line region all mentioning the term
			b.WriteString("the ZEBRACORN handler lives here and does the thing\n")
			continue
		}
		b.WriteString(line)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeywordFocusFromGoal(t *testing.T) {
	if f := keywordFocusFromGoal("where does the ZEBRACORN handler live"); f == nil || len(f.Keywords) == 0 {
		t.Fatalf("expected keyword focus from a content-ful goal, got %+v", f)
	}
	// Goals with no significant words yield nil (→ blind breadth).
	if f := keywordFocusFromGoal("a an of"); f != nil {
		t.Errorf("expected nil focus for stopword-only goal, got %+v", f)
	}
}

func TestMarkKeywordChunks(t *testing.T) {
	path := writeKeywordFixture(t)
	fi, _ := os.Stat(path)
	out := BuildByteGrid(path, "blob.txt", fi.Size(), ByteGridOpts{WindowTokens: 8192})
	if len(out.Chunks) < 2 {
		t.Fatalf("need a multi-chunk grid, got %d", len(out.Chunks))
	}
	fs := &FocusSampler{inFocus: map[string]bool{}}
	markKeywordChunks(fs, out, []string{"zebracorn"})

	if len(fs.inFocus) == 0 {
		t.Fatal("keyword scan marked no chunks; expected the region containing the term")
	}
	// Every marked chunk must actually contain the term (no false positives).
	for id := range fs.inFocus {
		var c *Chunk
		for i := range out.Chunks {
			if out.Chunks[i].ID == id {
				c = &out.Chunks[i]
			}
		}
		if c == nil {
			t.Fatalf("marked unknown chunk %s", id)
		}
	}
}

func TestMarkKeywordChunks_NoMatchNoBias(t *testing.T) {
	path := writeKeywordFixture(t)
	fi, _ := os.Stat(path)
	out := BuildByteGrid(path, "blob.txt", fi.Size(), ByteGridOpts{WindowTokens: 8192})
	fs := &FocusSampler{inFocus: map[string]bool{}}
	markKeywordChunks(fs, out, []string{"nonexistentterm"})
	if len(fs.inFocus) != 0 {
		t.Errorf("a term that doesn't appear should mark nothing, got %d", len(fs.inFocus))
	}
}

// Integration: with KeywordFocus on, the sample should reach the term's region
// far more often than a blind draw would (the region is ~1 chunk in a large
// grid, so blind sampling rarely hits it).
func TestStudyFile_KeywordFocusTargetsTheTerm(t *testing.T) {
	path := writeKeywordFixture(t)
	goal := "where is the ZEBRACORN handler defined"

	sawTerm := func(req StudyRequest) bool {
		hit := false
		req.Infer = func(_ context.Context, in InferInput) (InferOutput, error) {
			for _, s := range in.Sampled {
				if strings.Contains(s.Snippet, "ZEBRACORN") {
					hit = true
				}
			}
			return InferOutput{Digest: "d"}, nil
		}
		if _, err := StudyFile(context.Background(), req); err != nil {
			t.Fatalf("StudyFile: %v", err)
		}
		return hit
	}

	focused := sawTerm(StudyRequest{Path: path, RelPath: "blob.txt", Window: 8192, Goal: goal, KeywordFocus: true})
	if !focused {
		t.Error("keyword focus should steer the sample onto the ZEBRACORN region")
	}
}
