package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// fakeInferProvider is a minimal llm.Provider returning a canned response.
type fakeInferProvider struct {
	resp string
}

func (p fakeInferProvider) Generate(context.Context, string) (string, error) { return p.resp, nil }
func (p fakeInferProvider) GenerateWithSystem(context.Context, string, string) (string, error) {
	return p.resp, nil
}
func (p fakeInferProvider) GenerateWithStats(context.Context, string) (string, llm.GenerationStats, error) {
	return p.resp, llm.GenerationStats{}, nil
}
func (p fakeInferProvider) IsAvailable() bool { return true }
func (p fakeInferProvider) Name() string      { return "fake" }

func writeWorkdirFile(t *testing.T, name string, nBytes int) (workdir, rel string) {
	t.Helper()
	workdir = t.TempDir()
	rel = name
	b := make([]byte, nBytes)
	for i := range b {
		if (i+1)%50 == 0 {
			b[i] = '\n'
		} else {
			b[i] = 'a'
		}
	}
	if err := os.WriteFile(filepath.Join(workdir, name), b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return workdir, rel
}

func TestStudyFileTool_SmallFile_ReadParity(t *testing.T) {
	workdir, rel := writeWorkdirFile(t, "small.txt", 1000) // under window/2
	study := NewStudyFileTool(workdir, StudyFileToolOpts{Window: 8192})
	read := NewReadFileTool(workdir)

	args := `{"path":"small.txt"}`
	gotStudy, err := study.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("study_file Call: %v", err)
	}
	gotRead, err := read.Call(context.Background(), `{"path":"`+rel+`"}`)
	if err != nil {
		t.Fatalf("read_file Call: %v", err)
	}
	if gotStudy != gotRead {
		t.Errorf("sub-threshold study_file must be byte-identical to read_file:\nstudy=%s\nread =%s", gotStudy, gotRead)
	}
}

func TestStudyFileTool_LargeFile_StudyShape(t *testing.T) {
	workdir, _ := writeWorkdirFile(t, "big.txt", 120000) // over window/2
	prov := fakeInferProvider{resp: `{"digest":"studied","citations":[{"relpath":"big.txt","line_start":999999,"line_end":999999,"claim":"hallucinated"}],"leads":[{"relpath":"big.txt","near_line":5,"why":"ref"}]}`}
	study := NewStudyFileTool(workdir, StudyFileToolOpts{Window: 8192, Provider: prov})

	out, err := study.Call(context.Background(), `{"path":"big.txt","density":"sparse"}`)
	if err != nil {
		t.Fatalf("study_file Call: %v", err)
	}
	if !strings.Contains(out, `"mode":"study"`) {
		t.Errorf("expected study mode JSON, got: %s", out)
	}
	if !strings.Contains(out, `"digest":"studied"`) {
		t.Errorf("expected digest in output: %s", out)
	}
	if strings.Contains(out, "999999") {
		t.Errorf("hallucinated out-of-range citation should have been stripped: %s", out)
	}
	if !strings.Contains(out, `"coverage"`) {
		t.Errorf("expected coverage in output: %s", out)
	}
}

func TestStudyFileTool_MalformedArgs_SoftError(t *testing.T) {
	workdir := t.TempDir()
	study := NewStudyFileTool(workdir, StudyFileToolOpts{})
	out, err := study.Call(context.Background(), `{not json`)
	if err != nil {
		t.Fatalf("malformed args should be a soft error, got err: %v", err)
	}
	if !strings.Contains(out, `"error"`) {
		t.Errorf("expected an error-shaped result, got: %s", out)
	}
}

func TestStudyFileTool_PathContainment(t *testing.T) {
	workdir := t.TempDir()
	study := NewStudyFileTool(workdir, StudyFileToolOpts{})
	out, err := study.Call(context.Background(), `{"path":"../escape.txt"}`)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !strings.Contains(out, `"error"`) {
		t.Errorf("path traversal should be rejected, got: %s", out)
	}
}

func TestStudyFileTool_FocusAccepted(t *testing.T) {
	workdir, _ := writeWorkdirFile(t, "big.txt", 120000)
	prov := fakeInferProvider{resp: `{"digest":"d","citations":[],"leads":[]}`}
	study := NewStudyFileTool(workdir, StudyFileToolOpts{Window: 8192, Provider: prov})
	out, err := study.Call(context.Background(), `{"path":"big.txt","density":"normal","focus":{"lines":[100,200]}}`)
	if err != nil {
		t.Fatalf("focus arg should be accepted: %v", err)
	}
	if !strings.Contains(out, `"mode":"study"`) {
		t.Errorf("expected study mode with focus, got: %s", out)
	}
}

// writeWorkdirDir materializes rel→content files under workdir/dir.
func writeWorkdirDir(t *testing.T, dir string, files map[string]string) (workdir string) {
	t.Helper()
	workdir = t.TempDir()
	for rel, content := range files {
		p := filepath.Join(workdir, dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return workdir
}

func TestStudyFileTool_SmallDir_ReadShape(t *testing.T) {
	workdir := writeWorkdirDir(t, "pkg", map[string]string{
		"a.txt": "alpha\n",
		"b.txt": "beta\n",
	})
	study := NewStudyFileTool(workdir, StudyFileToolOpts{Window: 8192})
	out, err := study.Call(context.Background(), `{"path":"pkg"}`)
	if err != nil {
		t.Fatalf("study_file Call(dir): %v", err)
	}
	for _, want := range []string{`"truncated":false`, "----- pkg/a.txt -----", "alpha", "----- pkg/b.txt -----", "beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("small-dir read output missing %q in: %s", want, out)
		}
	}
}

func TestStudyFileTool_LargeDir_StudyShape(t *testing.T) {
	blob := strings.Repeat(strings.Repeat("a", 49)+"\n", 1200) // 60KB per file
	workdir := writeWorkdirDir(t, "pkg", map[string]string{
		"one.txt": blob,
		"two.txt": blob,
	})
	prov := fakeInferProvider{resp: `{"digest":"corpus digest","citations":[],"leads":[{"relpath":"pkg/two.txt","near_line":7,"why":"ref"}]}`}
	study := NewStudyFileTool(workdir, StudyFileToolOpts{Window: 8192, Provider: prov})
	out, err := study.Call(context.Background(), `{"path":"pkg","density":"sparse","focus":{"path":"pkg/one.txt"}}`)
	if err != nil {
		t.Fatalf("study_file Call(dir): %v", err)
	}
	for _, want := range []string{`"mode":"study"`, `"digest":"corpus digest"`, `"coverage"`, `"pkg/two.txt"`} {
		if !strings.Contains(out, want) {
			t.Errorf("large-dir study output missing %q in: %s", want, out)
		}
	}
}

func TestStudyFileTool_Spec(t *testing.T) {
	study := NewStudyFileTool(t.TempDir(), StudyFileToolOpts{})
	if study.Name() != "study_file" {
		t.Errorf("Name = %q, want study_file", study.Name())
	}
	if study.Spec().Function.Name != "study_file" {
		t.Errorf("Spec function name = %q", study.Spec().Function.Name)
	}
}
