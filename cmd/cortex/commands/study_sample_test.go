package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBigFile(t *testing.T, nBytes int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	b := make([]byte, nBytes)
	for i := range b {
		if (i+1)%50 == 0 {
			b[i] = '\n'
		} else {
			b[i] = 'a'
		}
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func TestRunSampleOnly_PrintsChunkTable(t *testing.T) {
	path := writeBigFile(t, 120000) // well over window/2 → study path
	var buf bytes.Buffer
	if err := runSampleOnly(path, "sparse", 8192, nil, &buf); err != nil {
		t.Fatalf("runSampleOnly: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mode: study") {
		t.Errorf("expected 'mode: study' in output:\n%s", out)
	}
	if !strings.Contains(out, "sampled=4 chunks") {
		t.Errorf("expected 4 sparse chunks reported:\n%s", out)
	}
	if !strings.Contains(out, "coverage:") {
		t.Errorf("expected a coverage line:\n%s", out)
	}
	// Each sampled row carries a "relpath:line-line" region (the header
	// shows "path=...big.txt" with no trailing colon, so it isn't counted).
	rows := strings.Count(out, "big.txt:")
	if rows != 4 {
		t.Errorf("expected 4 region rows, counted %d in:\n%s", rows, out)
	}
}

func TestRunSampleOnly_SmallFileReadMode(t *testing.T) {
	path := writeBigFile(t, 1000) // under window/2
	var buf bytes.Buffer
	if err := runSampleOnly(path, "", 8192, nil, &buf); err != nil {
		t.Fatalf("runSampleOnly: %v", err)
	}
	if !strings.Contains(buf.String(), "mode: read") {
		t.Errorf("small file should report read mode, got:\n%s", buf.String())
	}
}

func TestStudyCommand_FilePositional_RoutesToSampleOnly(t *testing.T) {
	path := writeBigFile(t, 120000)
	// A FILE positional + --sample-only must NOT be parsed as a duration;
	// if routing failed it would hit time.ParseDuration(path) and error.
	err := (&StudyCommand{}).Execute(&Context{Args: []string{path, "--sample-only", "--density", "sparse"}})
	if err != nil {
		t.Fatalf("sample-only routing returned error: %v", err)
	}
}

func TestStudyCommand_SampleOnlyRequiresFile(t *testing.T) {
	err := (&StudyCommand{}).Execute(&Context{Args: []string{"--sample-only"}})
	if err == nil {
		t.Error("expected error when --sample-only has no FILE argument")
	}
}

func TestParseFocusLines(t *testing.T) {
	f, err := parseFocusLines("1380,1460")
	if err != nil {
		t.Fatalf("parseFocusLines: %v", err)
	}
	if f == nil || f.Lines != [2]int{1380, 1460} {
		t.Errorf("got %+v, want lines [1380 1460]", f)
	}
	if nilF, _ := parseFocusLines(""); nilF != nil {
		t.Errorf("empty input should yield nil focus, got %+v", nilF)
	}
	if _, err := parseFocusLines("bogus"); err == nil {
		t.Error("expected error for malformed focus-lines")
	}
}
