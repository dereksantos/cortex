package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// readFileTool exposes a single file read to the model. The model is
// expected to discover paths via list_dir before reading; we don't
// surface raw stat info because seeing whether a file *exists* in
// .git or .cortex would leak Cortex internals into the conversation.
type readFileTool struct {
	workdir string
}

// Hard caps. Models that ask for huge files get an error and a hint
// rather than a 10 MB tool-result message that blows the context
// window. The model can issue head/tail via run_shell if it needs a
// snippet of a large file.
const (
	maxReadFileBytes = 64 * 1024 // 64 KiB per call
)

type readFileArgs struct {
	Path string `json:"path"`
	// StartLine / EndLine are 1-indexed inclusive line bounds. When
	// both are zero (the legacy shape), the tool reads the file from
	// the start up to the byte cap. When set, the tool reads the
	// whole file then slices to lines [StartLine, EndLine]. EndLine
	// past EOF clips silently. Either side may be 0 to leave that end
	// open (StartLine=0 → from line 1; EndLine=0 → through EOF).
	//
	// The chunker's truncation marker emits a concrete StartLine when
	// the calling model needs to paginate past the first chunk window
	// — without this surface, that advice was unreachable.
	StartLine int `json:"start_line,omitempty"`
	EndLine   int `json:"end_line,omitempty"`
}

// NewReadFileTool constructs the tool. workdir must be an absolute path.
func NewReadFileTool(workdir string) ToolHandler { return &readFileTool{workdir: workdir} }

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        t.Name(),
			Description: "Read a UTF-8 text file under the workdir. Returns up to 64 KiB. Use start_line / end_line (1-indexed, inclusive) to fetch a specific line range — required for paginating past the first chunk when a previous read showed a '[truncated]' marker.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relative to the workdir (no leading slash, no .. segments)."},
					"start_line": {"type": "integer", "description": "1-indexed inclusive starting line. Omit or 0 = read from line 1."},
					"end_line": {"type": "integer", "description": "1-indexed inclusive ending line. Omit or 0 = read through EOF. Values past EOF clip silently."}
				},
				"required": ["path"]
			}`),
		},
	}
}

func (t *readFileTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args readFileArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}
	if args.StartLine < 0 || args.EndLine < 0 {
		return errorJSON(fmt.Errorf("start_line and end_line must be >= 0 (1-indexed)")), nil
	}
	if args.StartLine > 0 && args.EndLine > 0 && args.EndLine < args.StartLine {
		return errorJSON(fmt.Errorf("end_line (%d) must be >= start_line (%d)", args.EndLine, args.StartLine)), nil
	}

	abs, err := containPath(t.workdir, args.Path)
	if err != nil {
		return errorJSON(err), nil
	}

	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errorJSON(fmt.Errorf("file not found: %s", args.Path)), nil
		}
		return errorJSON(fmt.Errorf("lstat: %w", err)), nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errorJSON(errPathIsSymlink), nil
	}
	if info.IsDir() {
		return errorJSON(fmt.Errorf("is a directory: %s (use list_dir)", args.Path)), nil
	}

	// Line-range path: read the whole file first, slice by line, then
	// apply the byte cap to the slice. Reading the whole file is
	// acceptable because the workdir-contained files we serve are
	// bounded by repository scale, and the slice is what actually
	// flows to the model — pre-slicing keeps the 64 KiB cap honest
	// regardless of where the requested lines live.
	if args.StartLine > 0 || args.EndLine > 0 {
		content, ferr := os.ReadFile(abs)
		if ferr != nil {
			return errorJSON(fmt.Errorf("read: %w", ferr)), nil
		}
		sliced, actualStart, actualEnd := sliceByLine(string(content), args.StartLine, args.EndLine)
		truncated := false
		if len(sliced) > maxReadFileBytes {
			sliced = sliced[:maxReadFileBytes]
			truncated = true
		}
		return fmt.Sprintf(
			`{"path":%q,"truncated":%t,"start_line":%d,"end_line":%d,"content":%q}`,
			args.Path, truncated, actualStart, actualEnd, sliced,
		), nil
	}

	// Read up to the cap. We deliberately read max+1 to detect truncation.
	f, err := os.Open(abs)
	if err != nil {
		return errorJSON(fmt.Errorf("open: %w", err)), nil
	}
	defer f.Close()

	buf := make([]byte, maxReadFileBytes+1)
	n, _ := f.Read(buf)
	if n > maxReadFileBytes {
		return fmt.Sprintf(`{"path":%q,"truncated":true,"content":%q}`, args.Path, string(buf[:maxReadFileBytes])), nil
	}
	return fmt.Sprintf(`{"path":%q,"truncated":false,"content":%q}`, args.Path, string(buf[:n])), nil
}

// sliceByLine extracts the (1-indexed inclusive) line range
// [startLine, endLine] from content. Zero on either side means
// "open": startLine=0 → from line 1, endLine=0 → through EOF.
// Out-of-range endLine clips at EOF without erroring — the caller
// (a model paginating a chunked file) shouldn't be punished for
// asking for one line past the last chunk.
//
// Returns the sliced content plus the actual line bounds applied
// (after clipping), so the tool response can echo back exactly what
// was returned. actualStart=actualEnd=0 means "no content in that
// range" (e.g. startLine past EOF).
func sliceByLine(content string, startLine, endLine int) (sliced string, actualStart, actualEnd int) {
	if content == "" {
		return "", 0, 0
	}
	lines := strings.SplitAfter(content, "\n")
	// SplitAfter on a final "\n" produces a trailing "" element; drop it
	// so line counts match the conventional "no extra empty line."
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", 0, 0
	}
	start := startLine
	if start <= 0 {
		start = 1
	}
	end := endLine
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", 0, 0
	}
	if end < start {
		return "", 0, 0
	}
	var b strings.Builder
	for i := start - 1; i < end; i++ {
		b.WriteString(lines[i])
	}
	return b.String(), start, end
}

// errorJSON builds the standard error-shaped tool-result string. We
// hand-build the JSON to avoid marshaling overhead for a single field;
// the keys are stable so the model can pattern-match.
func errorJSON(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}
