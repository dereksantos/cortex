package study

import (
	"context"
	"errors"
	"testing"
)

func TestStudyFile_Agentic_ValidatesCitations(t *testing.T) {
	path := writeBytesFile(t, 60000)
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		s := in.Sampled[0]
		return InferOutput{
			Digest: "summary",
			Citations: []Citation{
				{RelPath: s.RelPath, LineStart: s.LineStart, LineEnd: s.LineStart, Claim: "valid"},
				{RelPath: s.RelPath, LineStart: 999999, LineEnd: 999999, Claim: "hallucinated"},
			},
			Leads: []Lead{{RelPath: s.RelPath, NearLine: 5, Why: "off-sample"}},
		}, nil
	}
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "sparse", Infer: infer})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if len(resp.Citations) != 1 {
		t.Fatalf("want 1 validated citation (hallucinated dropped), got %d", len(resp.Citations))
	}
	if resp.Citations[0].Claim != "valid" {
		t.Errorf("kept the wrong citation: %+v", resp.Citations[0])
	}
	if resp.Citations[0].ByteOffset <= 0 {
		t.Errorf("validated citation not anchored to a byte offset")
	}
}

func TestStudyFile_Agentic_LeadsAndDeepen(t *testing.T) {
	path := writeBytesFile(t, 80000)
	focus := &Focus{Lines: [2]int{100, 200}}
	infer := func(_ context.Context, in InferInput) (InferOutput, error) {
		return InferOutput{Digest: "d", Leads: []Lead{{RelPath: "blob.txt", NearLine: 42, Why: "ref"}}}, nil
	}
	resp, err := StudyFile(context.Background(), StudyRequest{
		Path: path, Window: 8192, Density: "sparse", Session: "sess-1", Focus: focus, Infer: infer,
	})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if len(resp.Leads) != 1 || resp.Leads[0].NearLine != 42 {
		t.Errorf("leads not passed through: %+v", resp.Leads)
	}
	if resp.Deepen.Densify.Session != "sess-1" {
		t.Errorf("Deepen.Densify.Session = %q, want sess-1", resp.Deepen.Densify.Session)
	}
	if ResolveDensity(resp.Deepen.Densify.Density) != densityNormalK {
		t.Errorf("Deepen.Densify.Density should step sparse→normal, got k=%d", ResolveDensity(resp.Deepen.Densify.Density))
	}
	if resp.Deepen.Target.Focus != focus {
		t.Errorf("Deepen.Target.Focus should mirror the request focus")
	}
}

func TestStudyFile_Agentic_ExhaustedAtKnee(t *testing.T) {
	// A file with fewer chunks than a dense k → sampler returns < k → knee.
	path := writeBytesFile(t, 18000)
	infer := func(_ context.Context, _ InferInput) (InferOutput, error) {
		return InferOutput{Digest: "d"}, nil
	}
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "dense", Infer: infer})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if !resp.Exhausted {
		t.Errorf("expected Exhausted=true at the coverage knee (sampled %d chunks)", len(resp.Sampled))
	}
}

func TestStudyFile_Agentic_DigestPresent(t *testing.T) {
	path := writeBytesFile(t, 60000)
	infer := func(_ context.Context, _ InferInput) (InferOutput, error) {
		return InferOutput{Digest: "the file does X"}, nil
	}
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "sparse", Infer: infer})
	if err != nil {
		t.Fatalf("StudyFile: %v", err)
	}
	if resp.Digest != "the file does X" {
		t.Errorf("Digest = %q", resp.Digest)
	}
}

func TestStudyFile_Agentic_InferErrorKeepsSample(t *testing.T) {
	path := writeBytesFile(t, 60000)
	infer := func(_ context.Context, _ InferInput) (InferOutput, error) {
		return InferOutput{}, errors.New("model down")
	}
	resp, err := StudyFile(context.Background(), StudyRequest{Path: path, Window: 8192, Density: "sparse", Infer: infer})
	if err == nil {
		t.Fatal("expected inference error to surface")
	}
	if len(resp.Sampled) == 0 {
		t.Errorf("mechanical sample should survive an inference failure for graceful degradation")
	}
}
