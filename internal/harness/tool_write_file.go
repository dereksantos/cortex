package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dereksantos/cortex/pkg/llm"
)

// writeFileTool writes a UTF-8 text file under the workdir, creating
// parent directories as needed. Atomic via temp-file + rename so a
// partially-written file never appears to read_file on the next turn.
type writeFileTool struct {
	workdir  string
	registry *ToolRegistry // for noteFileWritten
}

// maxWriteFileBytes caps individual write_file calls. Going higher
// would let the model dump megabytes through a single tool turn and
// blow the context budget; for iteration 1, 128 KiB is plenty for a
// Conway's Game of Life implementation.
const maxWriteFileBytes = 128 * 1024

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// NewWriteFileTool constructs the tool. workdir must be absolute; reg
// is used to record FilesWritten for HarnessResult.FilesChanged.
func NewWriteFileTool(workdir string, reg *ToolRegistry) ToolHandler {
	return &writeFileTool{workdir: workdir, registry: reg}
}

func (t *writeFileTool) Name() string { return "write_file" }

func (t *writeFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        t.Name(),
			Description: "Create or overwrite a UTF-8 text file under the workdir. Parent directories are created automatically. Atomic.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relative to the workdir (no leading slash, no .. segments)."},
					"content": {"type": "string", "description": "Full file contents. Existing file is replaced."}
				},
				"required": ["path", "content"]
			}`),
		},
	}
}

func (t *writeFileTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args writeFileArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}
	if len(args.Content) > maxWriteFileBytes {
		return errorJSON(fmt.Errorf("content exceeds %d bytes (got %d); split the write", maxWriteFileBytes, len(args.Content))), nil
	}

	abs, err := containPath(t.workdir, args.Path)
	if err != nil {
		return errorJSON(err), nil
	}

	// Reject overwriting a symlink — would let the model redirect
	// writes outside the workdir on a subsequent call.
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errorJSON(errPathIsSymlink), nil
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return errorJSON(fmt.Errorf("mkdir: %w", err)), nil
	}

	// Atomic write: temp file in the same dir, then rename. Same-dir
	// is important so rename is a metadata op rather than a copy
	// (cross-device rename fails).
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".write_file-*.tmp")
	if err != nil {
		return errorJSON(fmt.Errorf("temp file: %w", err)), nil
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.WriteString(args.Content); err != nil {
		_ = tmp.Close()
		cleanup()
		return errorJSON(fmt.Errorf("write: %w", err)), nil
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return errorJSON(fmt.Errorf("close: %w", err)), nil
	}
	if err := os.Rename(tmpName, abs); err != nil {
		cleanup()
		return errorJSON(fmt.Errorf("rename: %w", err)), nil
	}

	if t.registry != nil {
		t.registry.noteFileWritten(args.Path)
	}
	return fmt.Sprintf(`{"path":%q,"bytes":%d}`, args.Path, len(args.Content)), nil
}
