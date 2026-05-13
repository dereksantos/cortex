package sources

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
)

// zero is a zero-value time used in tests where modified time is unknown.
var zero time.Time

func TestObserver_NilIsSafe(t *testing.T) {
	var o *Observer
	o.Observe(journal.TypeObservationMemoryFile, "src", "uri", []byte("data"), zero)
}

func TestObserver_AppendsObservationEntry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "observer-test-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	o := NewObserver(tempDir)
	if o == nil {
		t.Fatal("NewObserver returned nil for non-empty dir")
	}
	o.Observe(journal.TypeObservationMemoryFile, "memory-md",
		"file:///test/MEMORY.md", []byte("hello world"), zero)

	r, err := journal.NewReader(filepath.Join(tempDir, "journal", "observation"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	e, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if e.Type != journal.TypeObservationMemoryFile {
		t.Errorf("Type = %s, want %s", e.Type, journal.TypeObservationMemoryFile)
	}
	p, err := journal.ParseObservation(e)
	if err != nil {
		t.Fatalf("ParseObservation: %v", err)
	}
	if p.URI != "file:///test/MEMORY.md" {
		t.Errorf("URI = %s, want file:///test/MEMORY.md", p.URI)
	}
	if p.ContentHash != journal.HashContent([]byte("hello world")) {
		t.Errorf("ContentHash mismatch")
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("expected single entry, got more")
	}
}

// TestProjectSource_EmitsObservation verifies the O2 contract: every
// file that contributes regions to a Sample produces exactly one
// observation.project_file entry (deduped within the Sample).
func TestProjectSource_EmitsObservation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "projsrc-observe-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	src := filepath.Join(tempDir, "src.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cortexDir := filepath.Join(tempDir, ".cortex")
	o := NewObserver(cortexDir)
	ps := NewProjectSource(tempDir)
	ps.SetObserver(o)
	if _, err := ps.Sample(context.Background(), 4); err != nil {
		t.Fatalf("Sample: %v", err)
	}

	r, err := journal.NewReader(filepath.Join(cortexDir, "journal", "observation"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	var foundProjectFiles int
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if e.Type != journal.TypeObservationProjectFile {
			continue
		}
		p, err := journal.ParseObservation(e)
		if err != nil {
			t.Fatalf("ParseObservation: %v", err)
		}
		if p.SourceName != "project" {
			t.Errorf("SourceName=%q want %q", p.SourceName, "project")
		}
		foundProjectFiles++
	}
	if foundProjectFiles == 0 {
		t.Error("no observation.project_file entries emitted")
	}
}

func TestMemoryMDSource_EmitsObservation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "memorymd-observe-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Place a memory file the source will find.
	memoryDir := filepath.Join(tempDir, ".claude", "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	if err := os.WriteFile(memoryPath, []byte("# Memory\n\n## Section\nbody"), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	cortexDir := filepath.Join(tempDir, ".cortex")
	o := NewObserver(cortexDir)
	src := NewMemoryMDSource("", tempDir)
	src.SetObserver(o)

	if _, err := src.Sample(context.Background(), 5); err != nil {
		t.Fatalf("Sample: %v", err)
	}

	// Verify an observation entry was emitted for the memory file.
	r, err := journal.NewReader(filepath.Join(cortexDir, "journal", "observation"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()
	var found bool
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		p, err := journal.ParseObservation(e)
		if err != nil {
			t.Fatalf("ParseObservation: %v", err)
		}
		if p.URI == "file://"+memoryPath {
			found = true
			if p.SourceName != "memory-md" {
				t.Errorf("SourceName = %s, want memory-md", p.SourceName)
			}
			if p.Size == 0 {
				t.Error("Size should be > 0")
			}
		}
	}
	if !found {
		t.Errorf("no observation entry for %s", memoryPath)
	}
}
