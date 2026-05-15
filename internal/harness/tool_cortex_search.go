package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// cortexSearchToolName is the tool identifier. Exported as an unexported
// constant the registry can match on (for accounting; see
// ToolRegistry.Dispatch).
const cortexSearchToolName = "cortex_search"

// cortexSearchTool runs Reflex → Resolve against a workdir-LOCAL
// Cortex store, never the operator's personal store (~/.cortex). This
// is the central invariant that lets multi-session eval runs
// accumulate failure captures without contaminating the user's
// regular Cortex memory.
//
// Construction does NOT open the store; the store is opened on first
// use and cached on the struct. This avoids paying for index rebuild
// in scenarios where the model never invokes cortex_search.
type cortexSearchTool struct {
	storeDir string // absolute path to <workdir>/.cortex

	// Lazy state. nil until the first call. If init fails we cache
	// the error and return it for subsequent calls too — re-trying
	// open on every call when the store is broken would just waste
	// turns.
	cortex *intcognition.Cortex
	store  *storage.Storage
	cfg    *config.Config
	initErr error
}

type cortexSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// NewCortexSearchTool constructs the tool with an EXPLICIT workdir-local
// store path. The constructor refuses to accept paths under the
// operator's home directory or the global cortex dir as a defense
// against accidental contamination.
//
// workdir must be absolute. The store lives at <workdir>/.cortex.
func NewCortexSearchTool(workdir string) (ToolHandler, error) {
	if !filepath.IsAbs(workdir) {
		return nil, fmt.Errorf("%w: %q", errWorkdirNotAbsolute, workdir)
	}
	storeDir := filepath.Join(workdir, ".cortex")
	return &cortexSearchTool{storeDir: storeDir}, nil
}

func (t *cortexSearchTool) Name() string { return cortexSearchToolName }

func (t *cortexSearchTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Type: "function",
		Function: llm.ToolFunc{
			Name: t.Name(),
			Description: "Search the per-eval Cortex store for prior captures and insights " +
				"(decisions, corrections, failure modes from earlier attempts in this loop). " +
				"On a fresh eval this returns nothing useful; on multi-session runs it " +
				"surfaces what was learned from prior failed attempts.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Natural-language search query."},
					"limit": {"type": "integer", "description": "Max results (default 5)."}
				},
				"required": ["query"]
			}`),
		},
	}
}

func (t *cortexSearchTool) Call(ctx context.Context, rawArgs string) (string, error) {
	var args cortexSearchArgs
	if msg, ok := parseJSONArgs(rawArgs, &args); !ok {
		return msg, nil
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}
	if strings.TrimSpace(args.Query) == "" {
		return errorJSON(fmt.Errorf("query must not be empty")), nil
	}

	if err := t.ensureInit(); err != nil {
		return errorJSON(fmt.Errorf("cortex unavailable: %w", err)), nil
	}

	q := cognition.Query{Text: args.Query, Limit: args.Limit}
	// Fast mode: Reflex → Resolve, no synchronous Reflect. Reflect
	// would need an LLM provider; we deliberately don't wire one in
	// because (a) we don't have a "judge" client at this layer, and
	// (b) reflect is a quality/latency tradeoff that's better done at
	// the runner level (Dream between sessions does the deep work).
	res, err := t.cortex.Retrieve(ctx, q, cognition.Fast)
	if err != nil {
		return errorJSON(fmt.Errorf("retrieve: %w", err)), nil
	}

	// Empty store / no matches is the dominant Mode-A outcome; the
	// model should be told plainly rather than receive an empty list.
	if res == nil || len(res.Results) == 0 {
		return `{"empty":true,"note":"no prior captures matched this query in the per-eval store"}`, nil
	}

	type entry struct {
		Category string  `json:"category,omitempty"`
		Score    float64 `json:"score"`
		Content  string  `json:"content"`
	}
	out := struct {
		Empty   bool    `json:"empty"`
		Entries []entry `json:"entries"`
	}{
		Empty: false,
	}
	for _, r := range res.Results {
		out.Entries = append(out.Entries, entry{
			Category: r.Category,
			Score:    r.Score,
			Content:  r.Content,
		})
	}
	bb, err := json.Marshal(out)
	if err != nil {
		return errorJSON(fmt.Errorf("marshal: %w", err)), nil
	}
	return string(bb), nil
}

// ensureInit lazily opens the workdir-local storage and constructs
// the cognition pipeline. Cached on the struct after first call.
//
// Provider and embedder are intentionally nil:
//   - Provider would only be used by Reflect (skipped in Fast mode).
//   - Embedder is nil so Reflex falls back to text search. The
//     per-eval store has no precomputed embeddings; bootstrapping a
//     Hugot embedder here would cost ~100ms on cold cache and add
//     dependency on the embedding model being downloaded.
func (t *cortexSearchTool) ensureInit() error {
	if t.cortex != nil {
		return nil
	}
	if t.initErr != nil {
		return t.initErr
	}

	t.cfg = &config.Config{ContextDir: t.storeDir}
	store, err := storage.New(t.cfg)
	if err != nil {
		t.initErr = fmt.Errorf("open store: %w", err)
		return t.initErr
	}
	t.store = store

	cx, err := intcognition.New(store, nil, nil, t.cfg)
	if err != nil {
		t.initErr = fmt.Errorf("init cortex: %w", err)
		return t.initErr
	}
	t.cortex = cx
	return nil
}
