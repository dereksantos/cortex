package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/llm"
)

// CortexSearchDefaultModeEnv overrides the default retrieval mode when
// the agent calls cortex_search without specifying one. Set to "fast"
// or "full" by the ABR session adapter so the same prompt sequence can
// be replayed under each mode without prompt-engineering the agent
// (which is unreliable). Unset / unknown values fall through to "fast",
// matching the historical default.
const CortexSearchDefaultModeEnv = "CORTEX_SEARCH_DEFAULT_MODE"

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

	// provider, when non-nil, enables Full retrieval mode (Reflect needs
	// an LLM). Fast mode never touches it. Passed in at construction
	// time so the tool can share the agent loop's LLM client instead of
	// spinning up its own — keeps spend on one quota.
	provider llm.Provider

	// sharedCortex, when non-nil, replaces the tool's lazy
	// self-construction. This is the wire that lets auto-capture from
	// the REPL turn loop become visible to retrievals in-process —
	// without a shared instance, captures and retrievals each open
	// their own Storage and the in-memory indexes drift.
	sharedCortex *intcognition.Cortex

	// Lazy state. nil until the first call. If init fails we cache
	// the error and return it for subsequent calls too — re-trying
	// open on every call when the store is broken would just waste
	// turns. When sharedCortex is non-nil, these stay nil.
	cortex  *intcognition.Cortex
	store   *storage.Storage
	cfg     *config.Config
	initErr error
}

type cortexSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
	// Mode selects the retrieval pipeline: "fast" (Reflex → Resolve,
	// default) or "full" (Reflex → Reflect → Resolve). Full requires
	// the tool to have been constructed with a non-nil provider; if
	// missing, Full silently degrades to Fast and records a notes field
	// in the response so callers can audit. ABR measurement runs need
	// both: same prompt sequence under each mode, score ratio = ABR.
	Mode string `json:"mode,omitempty"`
}

// NewCortexSearchTool constructs the tool with an EXPLICIT workdir-local
// store path. The constructor refuses to accept paths under the
// operator's home directory or the global cortex dir as a defense
// against accidental contamination.
//
// workdir must be absolute. The store lives at <workdir>/.cortex.
//
// provider may be nil; when nil, the tool serves Fast mode only and
// Full requests degrade to Fast with a `degraded_to_fast: true` note
// in the response payload.
func NewCortexSearchTool(workdir string, provider llm.Provider) (ToolHandler, error) {
	if !filepath.IsAbs(workdir) {
		return nil, fmt.Errorf("%w: %q", errWorkdirNotAbsolute, workdir)
	}
	storeDir := filepath.Join(workdir, ".cortex")
	return &cortexSearchTool{storeDir: storeDir, provider: provider}, nil
}

// NewCortexSearchToolFromCortex wires the tool to a pre-built Cortex
// instance instead of having it construct its own on first call. This
// is the path the REPL uses to share one Cortex across the
// captureClient (write side) and this tool (read side) — captures
// land in storage and become visible to Retrieve in the same session,
// which is what makes intra-session Think learning measurable at all.
//
// The provider for Full mode is taken from the shared Cortex; passing
// it again here would just be ignored.
//
// workdir is still required so the tool can validate path containment
// for any per-tool diagnostics, but it does NOT have to match the
// Cortex's storage path (the caller is responsible for that).
func NewCortexSearchToolFromCortex(workdir string, cortex *intcognition.Cortex, provider llm.Provider) (ToolHandler, error) {
	if !filepath.IsAbs(workdir) {
		return nil, fmt.Errorf("%w: %q", errWorkdirNotAbsolute, workdir)
	}
	if cortex == nil {
		return nil, fmt.Errorf("nil cortex (use NewCortexSearchTool for self-construction)")
	}
	storeDir := filepath.Join(workdir, ".cortex")
	return &cortexSearchTool{
		storeDir:     storeDir,
		provider:     provider,
		sharedCortex: cortex,
	}, nil
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
					"limit": {"type": "integer", "description": "Max results (default 5)."},
					"mode":  {"type": "string", "enum": ["fast", "full"], "description": "Retrieval mode: fast (Reflex → Resolve, default) or full (Reflex → Reflect → Resolve, requires LLM judge — slower, higher quality)."}
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

	mode, degraded, err := resolveRetrieveMode(args.Mode, t.provider != nil)
	if err != nil {
		return errorJSON(err), nil
	}

	cx, err := t.resolveCortex()
	if err != nil {
		return errorJSON(fmt.Errorf("cortex unavailable: %w", err)), nil
	}

	q := cognition.Query{Text: args.Query, Limit: args.Limit}
	res, err := cx.Retrieve(ctx, q, mode)
	if err != nil {
		return errorJSON(fmt.Errorf("retrieve: %w", err)), nil
	}

	// Empty store / no matches is the dominant Mode-A outcome; the
	// model should be told plainly rather than receive an empty list.
	if res == nil || len(res.Results) == 0 {
		if degraded {
			return `{"empty":true,"note":"no prior captures matched this query in the per-eval store","degraded_to_fast":true}`, nil
		}
		return `{"empty":true,"note":"no prior captures matched this query in the per-eval store"}`, nil
	}

	type entry struct {
		Category string  `json:"category,omitempty"`
		Score    float64 `json:"score"`
		Content  string  `json:"content"`
	}
	out := struct {
		Empty          bool    `json:"empty"`
		Mode           string  `json:"mode"`
		DegradedToFast bool    `json:"degraded_to_fast,omitempty"`
		Entries        []entry `json:"entries"`
	}{
		Empty:          false,
		Mode:           modeString(mode),
		DegradedToFast: degraded,
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

// resolveRetrieveMode parses the requested mode and decides whether
// Full is actually serviceable. Unknown values are an error so callers
// see a typo immediately rather than silently getting Fast.
//
// When `requested` is empty, the CortexSearchDefaultModeEnv environment
// variable is consulted — this is the hook ABR session runs use to
// flip the default per pass without injecting mode= into every prompt.
// An unset / unknown env var falls through to "fast".
//
// Returns (mode, degradedToFast, error). degradedToFast = true means
// the caller asked for Full but we have no provider, so we ran Fast.
func resolveRetrieveMode(requested string, haveProvider bool) (cognition.RetrieveMode, bool, error) {
	effective := strings.ToLower(strings.TrimSpace(requested))
	if effective == "" {
		envDefault := strings.ToLower(strings.TrimSpace(os.Getenv(CortexSearchDefaultModeEnv)))
		if envDefault == "fast" || envDefault == "full" {
			effective = envDefault
		}
	}
	switch effective {
	case "", "fast":
		return cognition.Fast, false, nil
	case "full":
		if !haveProvider {
			return cognition.Fast, true, nil
		}
		return cognition.Full, false, nil
	default:
		return cognition.Fast, false, fmt.Errorf("unknown mode %q (want fast or full)", requested)
	}
}

func modeString(m cognition.RetrieveMode) string {
	if m == cognition.Full {
		return "full"
	}
	return "fast"
}

// resolveCortex returns the Cortex instance to retrieve against. When
// a shared one was supplied at construction time it wins (the REPL
// path); otherwise we lazy-build a workdir-local one (the legacy
// path, used by harness paths that don't run capture).
func (t *cortexSearchTool) resolveCortex() (*intcognition.Cortex, error) {
	if t.sharedCortex != nil {
		return t.sharedCortex, nil
	}
	if err := t.ensureInit(); err != nil {
		return nil, err
	}
	return t.cortex, nil
}

// ensureInit lazily opens the workdir-local storage and constructs
// the cognition pipeline. Cached on the struct after first call.
//
// Provider is threaded through from the constructor — nil disables
// Full mode (calls degrade to Fast). Embedder stays nil so Reflex
// falls back to text search; the per-eval store has no precomputed
// embeddings, and bootstrapping a Hugot embedder here would cost
// ~100ms on cold cache and add a dependency on the embedding model
// being downloaded.
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

	cx, err := intcognition.New(store, t.provider, nil, t.cfg)
	if err != nil {
		t.initErr = fmt.Errorf("init cortex: %w", err)
		return t.initErr
	}
	t.cortex = cx
	return nil
}
