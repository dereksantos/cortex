package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile creates a temp file with the given content and returns
// its (workdir, relPath) pair. The caller registers t.Cleanup itself
// when the workdir should survive beyond the test.
func writeTestFile(t *testing.T, content string) (workdir, relPath string) {
	t.Helper()
	dir := t.TempDir()
	rel := "f.txt"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir, rel
}

// decodeReadFileResult parses the tool's JSON output into a map so tests
// can assert on individual fields without parsing strings by hand.
func decodeReadFileResult(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, raw)
	}
	return m
}

// TestReadFile_NoRange_ReturnsFullFile pins the legacy path: when
// start_line / end_line are absent, read_file returns the whole file
// (up to the 64 KiB cap) just like before.
func TestReadFile_NoRange_ReturnsFullFile(t *testing.T) {
	content := "line1\nline2\nline3\n"
	dir, rel := writeTestFile(t, content)
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	if got, _ := res["content"].(string); got != content {
		t.Errorf("content = %q, want %q", got, content)
	}
	if res["truncated"] != false {
		t.Errorf("truncated = %v, want false", res["truncated"])
	}
}

// TestReadFile_LineRange_ReturnsSlice pins the new path: when
// start_line and end_line are set, read_file returns exactly those
// lines (1-indexed, inclusive).
func TestReadFile_LineRange_ReturnsSlice(t *testing.T) {
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	dir, rel := writeTestFile(t, content)
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":2,"end_line":4}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	got, _ := res["content"].(string)
	want := "beta\ngamma\ndelta\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	// Tool echoes the actual range applied so the model can correlate.
	if sl, _ := res["start_line"].(float64); int(sl) != 2 {
		t.Errorf("start_line echo = %v, want 2", res["start_line"])
	}
	if el, _ := res["end_line"].(float64); int(el) != 4 {
		t.Errorf("end_line echo = %v, want 4", res["end_line"])
	}
}

// TestReadFile_EndPastEOF_ClipsSilently pins the chunker-friendly
// behavior: when the caller asks for an end_line past EOF, the tool
// returns what's there without erroring. The chunker's truncation
// marker emits the last-line bound as end_line; this lets a model
// follow the marker verbatim even when the file has been edited
// between reads.
func TestReadFile_EndPastEOF_ClipsSilently(t *testing.T) {
	content := "a\nb\nc\n" // 3 lines
	dir, rel := writeTestFile(t, content)
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":2,"end_line":100}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	got, _ := res["content"].(string)
	want := "b\nc\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if el, _ := res["end_line"].(float64); int(el) != 3 {
		t.Errorf("end_line echo = %v, want 3 (clipped to EOF)", res["end_line"])
	}
}

// TestReadFile_StartPastEOF_ReturnsEmpty pins the boundary case: when
// start_line is past EOF, the tool returns empty content (not an error).
// The model can interpret empty content + (start_line, end_line)=(0,0)
// as "nothing left in that range."
func TestReadFile_StartPastEOF_ReturnsEmpty(t *testing.T) {
	content := "x\ny\n"
	dir, rel := writeTestFile(t, content)
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":99}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	if got, _ := res["content"].(string); got != "" {
		t.Errorf("content = %q, want empty (start past EOF)", got)
	}
}

// TestReadFile_StartLineOnly_ReadsThroughEOF pins the "open-ended"
// shape: when only start_line is set, read continues through EOF.
// Symmetric to "only end_line set" reading from line 1.
func TestReadFile_StartLineOnly_ReadsThroughEOF(t *testing.T) {
	content := "a\nb\nc\nd\n"
	dir, rel := writeTestFile(t, content)
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":3}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	got, _ := res["content"].(string)
	want := "c\nd\n"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestReadFile_RejectsInvertedRange pins an error case: end_line <
// start_line is nonsense and should fail loudly before reading the
// file, not silently return empty (which would look like an EOF clip).
func TestReadFile_RejectsInvertedRange(t *testing.T) {
	dir, rel := writeTestFile(t, "anything\n")
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":10,"end_line":2}`)
	if err != nil {
		t.Fatalf("Call returned err (should surface via JSON instead): %v", err)
	}
	res := decodeReadFileResult(t, out)
	if _, ok := res["error"].(string); !ok {
		t.Errorf("expected error field for inverted range, got %v", res)
	}
}

// TestReadFile_NegativeRangeRejected pins that negative line numbers
// fail loudly — they'd otherwise read as "open end" via the <= 0
// branch and silently confuse the model.
func TestReadFile_NegativeRangeRejected(t *testing.T) {
	dir, rel := writeTestFile(t, "x\n")
	tool := NewReadFileTool(dir)

	out, err := tool.Call(context.Background(), `{"path":"`+rel+`","start_line":-1}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	res := decodeReadFileResult(t, out)
	if _, ok := res["error"].(string); !ok {
		t.Errorf("expected error field for negative start_line, got %v", res)
	}
}

// TestReadFile_SpecAdvertisesLineRange pins that the tool spec the LLM
// sees mentions start_line / end_line — without that, models won't know
// the params exist even when the chunker's truncation marker tells
// them to use it.
func TestReadFile_SpecAdvertisesLineRange(t *testing.T) {
	tool := NewReadFileTool("/tmp")
	spec := tool.Spec()
	params := string(spec.Function.Parameters)
	if !strings.Contains(params, "start_line") {
		t.Errorf("tool spec params should advertise start_line; got %s", params)
	}
	if !strings.Contains(params, "end_line") {
		t.Errorf("tool spec params should advertise end_line; got %s", params)
	}
}

// TestSliceByLine_OneIndexedInclusive pins the indexing convention.
// 1-indexed inclusive matches editor convention and what the chunker
// emits in "[chunk i/N, lines a-b]" headers — keeping the two surfaces
// aligned is the whole point of Piece 2.
func TestSliceByLine_OneIndexedInclusive(t *testing.T) {
	content := "L1\nL2\nL3\nL4\nL5\n"
	tests := []struct {
		startLine, endLine int
		want               string
	}{
		{1, 1, "L1\n"},           // single line
		{1, 5, content},          // full range
		{0, 0, content},          // both zero = whole file
		{0, 2, "L1\nL2\n"},       // open start
		{4, 0, "L4\nL5\n"},       // open end
		{2, 4, "L2\nL3\nL4\n"},   // middle slice
		{3, 100, "L3\nL4\nL5\n"}, // clipped tail
	}
	for _, tc := range tests {
		got, _, _ := sliceByLine(content, tc.startLine, tc.endLine)
		if got != tc.want {
			t.Errorf("sliceByLine(%d, %d) = %q, want %q", tc.startLine, tc.endLine, got, tc.want)
		}
	}
}
