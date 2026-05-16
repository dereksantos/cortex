package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// listDirTool returns the immediate (depth-1) entries of a workdir
// subdirectory. Excludes the same noise that AiderHarness's
// discoverChatFiles excludes — .git, vendor, node_modules — so the
// model sees the actual project structure rather than build outputs.
type listDirTool struct {
	workdir string
}

// NewListDirTool constructs the tool. workdir must be absolute.
func NewListDirTool(workdir string) ToolHandler { return &listDirTool{workdir: workdir} }

func (t *listDirTool) Name() string { return "list_dir" }

// skippedNames is the set of directory basenames the tool hides.
// Same list as in tool_read_file's reserved-dir check plus build
// noise. The reserved dirs (.git, .cortex) are also enforced by
// containPath when the model tries to recurse — listing here just
// keeps them out of the obvious-listing path.
var skippedNames = map[string]bool{
	".git":         true,
	".cortex":      true,
	"vendor":       true,
	"node_modules": true,
	".gocache":     true,
	".gopath":      true,
}

type listDirArgs struct {
	Path string `json:"path"`
}

func (t *listDirTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        t.Name(),
			Description: "List the immediate entries of a directory under the workdir. Pass \".\" for the workdir root. Hides .git, .cortex, vendor, node_modules.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Directory path relative to the workdir; '.' for the root."}
				},
				"required": ["path"]
			}`),
		},
	}
}

func (t *listDirTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args listDirArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}

	abs, err := containPath(t.workdir, args.Path)
	if err != nil {
		return errorJSON(err), nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		return errorJSON(fmt.Errorf("stat: %w", err)), nil
	}
	if !info.IsDir() {
		return errorJSON(fmt.Errorf("not a directory: %s", args.Path)), nil
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return errorJSON(fmt.Errorf("readdir: %w", err)), nil
	}

	var lines []string
	for _, e := range entries {
		if skippedNames[e.Name()] {
			continue
		}
		marker := "file"
		if e.IsDir() {
			marker = "dir"
		}
		lines = append(lines, fmt.Sprintf("%s\t%s", marker, e.Name()))
	}
	// Stable order for cache-friendly transcripts.
	for i := 0; i < len(lines); i++ {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}

	rel, _ := filepath.Rel(t.workdir, abs)
	if rel == "." {
		rel = "(workdir root)"
	}
	out := fmt.Sprintf("# %s\n%s\n", rel, strings.Join(lines, "\n"))
	return fmt.Sprintf(`{"path":%q,"listing":%q}`, args.Path, out), nil
}
