package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

func studyCall(id, path string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: "study_file", Arguments: `{"path":"` + path + `"}`},
	}
}

func TestProgressTracker_StudyFileCountsAsRead(t *testing.T) {
	p := &progressTracker{}
	p.recordTurn([]llm.ToolCall{studyCall("c0", "a.go")})
	if len(p.turnShapes) != 1 || p.turnShapes[0].readTargets == "" {
		t.Fatalf("study_file should bucket as a read, got turnShape=%+v", p.turnShapes)
	}

	// Repeated identical study_file reads with no write must trip the
	// no-progress detector exactly like read_file does.
	q := &progressTracker{}
	for i := 0; i < noProgressWindow; i++ {
		q.recordTurn([]llm.ToolCall{studyCall(fmt.Sprintf("c%d", i), "a.go")})
	}
	if !q.noProgress("code") {
		t.Errorf("repeated identical study_file reads should signal no-progress")
	}
}

func TestLoop_StudyFileDispatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewToolRegistry()
	reg.Register(NewStudyFileTool(dir, StudyFileToolOpts{Window: 8192}))

	out, err := reg.Dispatch(context.Background(), studyCall("c0", "small.txt"))
	if err != nil {
		t.Fatalf("dispatch study_file: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected file content through study_file read mode, got: %s", out)
	}
}
