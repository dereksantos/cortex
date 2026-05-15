package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

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
}

// NewReadFileTool constructs the tool. workdir must be an absolute path.
func NewReadFileTool(workdir string) ToolHandler { return &readFileTool{workdir: workdir} }

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        t.Name(),
			Description: "Read a UTF-8 text file under the workdir. Returns up to 64 KiB; use run_shell with head/tail for slices of larger files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relative to the workdir (no leading slash, no .. segments)."}
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

// errorJSON builds the standard error-shaped tool-result string. We
// hand-build the JSON to avoid marshaling overhead for a single field;
// the keys are stable so the model can pattern-match.
func errorJSON(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}
