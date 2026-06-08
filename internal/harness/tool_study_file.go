package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/llm"
)

// studyFileTool is the size-adaptive reading primitive that subsumes
// read_file. It is the agent's sole reading tool: for a file that fits
// the consuming model's window it behaves byte-for-byte like read_file
// (mode "read"); for a file over the threshold it samples bounded byte
// regions and infers a provenance-constrained digest (mode "study"),
// so a huge file costs the same to read as a merely-large one and never
// blows the context window. See docs/study-file.md.
type studyFileTool struct {
	workdir string
	opts    StudyFileToolOpts
}

// StudyFileToolOpts configures the tool. Window is the consuming model's
// context window in tokens; when 0 it is resolved from a cached probe
// (ModelID/Endpoint under ContextDir), else the library default. Provider
// backs phase-2 inference; nil → mechanical sample only.
type StudyFileToolOpts struct {
	Window     int
	ContextDir string
	ModelID    string
	Endpoint   string
	Provider   llm.Provider
}

// NewStudyFileTool constructs the tool. workdir must be an absolute path.
func NewStudyFileTool(workdir string, opts StudyFileToolOpts) ToolHandler {
	return &studyFileTool{workdir: workdir, opts: opts}
}

func (t *studyFileTool) Name() string { return "study_file" }

func (t *studyFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name:        t.Name(),
			Description: "Read a file under the workdir. Small files are returned whole (like read_file). Files too large to fit the model's context are SAMPLED instead: you get a bounded digest with file:line citations, a coverage map, and leads. To go deeper, call again with focus.lines/focus.symbol (TARGET) or a higher density (DENSIFY) — citations are validated against what was actually sampled, so unsampled lines are never cited.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relative to the workdir (no leading slash, no .. segments)."},
					"density": {"description": "Sampling density when the file is studied: \"sparse\" | \"normal\" | \"dense\", or an integer chunk count."},
					"focus": {
						"type": "object",
						"description": "Deepen toward a region.",
						"properties": {
							"lines": {"type": "array", "items": {"type": "integer"}, "description": "[start, end] line range to bias the sample toward."},
							"symbol": {"type": "string", "description": "Symbol/identifier to target."},
							"query": {"type": "string", "description": "Semantic lead to target."}
						}
					},
					"session": {"type": "string", "description": "Resumable coverage key; reuse across deepening calls."}
				},
				"required": ["path"]
			}`),
		},
	}
}

type studyFileArgs struct {
	Path    string          `json:"path"`
	Density json.RawMessage `json:"density,omitempty"`
	Focus   *struct {
		Lines  []int  `json:"lines,omitempty"`
		Symbol string `json:"symbol,omitempty"`
		Query  string `json:"query,omitempty"`
	} `json:"focus,omitempty"`
	Session string `json:"session,omitempty"`
	Window  int    `json:"window,omitempty"`
	Goal    string `json:"goal,omitempty"`
}

func (t *studyFileTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args studyFileArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}
	abs, err := containPath(t.workdir, args.Path)
	if err != nil {
		return errorJSON(err), nil
	}

	window := args.Window
	if window <= 0 {
		window = t.resolveWindow()
	}

	resp, err := study.StudyFile(ctx, study.StudyRequest{
		Path:       abs,
		RelPath:    args.Path,
		Density:    parseDensity(args.Density),
		Focus:      t.focus(args),
		Session:    args.Session,
		Window:     window,
		ContextDir: t.opts.ContextDir,
		Goal:       args.Goal,
		Infer:      t.infer(),
	})
	if err != nil {
		return errorJSON(err), nil
	}

	if resp.Mode == "read" {
		// Byte-identical to read_file's whole-file shape so the
		// sub-threshold fixtures are unchanged.
		return fmt.Sprintf(`{"path":%q,"truncated":false,"content":%q}`, args.Path, resp.ReadContent), nil
	}

	// Study shape. resp's json tags exclude ReadContent + Sampled.
	b, merr := json.Marshal(resp)
	if merr != nil {
		return errorJSON(fmt.Errorf("marshal study result: %w", merr)), nil
	}
	return string(b), nil
}

// resolveWindow fills the consuming-model window when the caller didn't
// pass one: a cached probe if available, else the library default.
func (t *studyFileTool) resolveWindow() int {
	if t.opts.Window > 0 {
		return t.opts.Window
	}
	if t.opts.ContextDir != "" && t.opts.ModelID != "" {
		if p, ok := study.LookupCached(t.opts.ContextDir, t.opts.ModelID, t.opts.Endpoint); ok && p.CtxWindowTokens > 0 {
			return p.CtxWindowTokens
		}
	}
	return 0 // library applies its conservative default
}

func (t *studyFileTool) focus(args studyFileArgs) *study.Focus {
	if args.Focus == nil {
		return nil
	}
	f := &study.Focus{Symbol: args.Focus.Symbol, Query: args.Focus.Query}
	if len(args.Focus.Lines) == 2 {
		f.Lines = [2]int{args.Focus.Lines[0], args.Focus.Lines[1]}
	}
	return f
}

// infer builds the provenance-constrained InferFunc from the configured
// provider, or nil for mechanical-only operation.
func (t *studyFileTool) infer() study.InferFunc {
	if t.opts.Provider == nil {
		return nil
	}
	return study.ProviderInfer(t.opts.Provider)
}

// parseDensity decodes the density arg, which may be an int or a string.
func parseDensity(raw json.RawMessage) study.Density {
	if len(raw) == 0 {
		return nil
	}
	var i int
	if err := json.Unmarshal(raw, &i); err == nil {
		return i
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return nil
}
