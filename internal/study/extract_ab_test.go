package study_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/llm"
)

// The A/B eval is the hard gate before the controller commits to one
// extract op as default (see docs/study-dag-plan.md §A/B).
// It runs both maintain.extract_insight and maintain.extract_overview
// on a 12-chunk panel (4 Go + 4 Python + 4 TS; mixed source/config/
// test/doc) and produces a scoring table.
//
// Env-gated so it doesn't run in CI: set CORTEX_RUN_AB=1 to opt in.
// Requires a real LLM provider configured via the standard Cortex
// env vars (CORTEX_LLM_PROVIDER, etc.).
//
// Flow:
//
//	CORTEX_RUN_AB=1 go test -v ./internal/study -run TestExtractAB \
//	  -panel testdata/extract_ab_panel.json
//
// `-update` regenerates the panel from a fresh scan (useful when the
// fixture projects change).

var (
	abPanelPath = flag.String("panel", "", "Path to A/B panel JSON (chunks to score)")
	abUpdate    = flag.Bool("update", false, "Regenerate the panel from fixture projects")
)

// PanelEntry is one chunk in the A/B panel.
type PanelEntry struct {
	Source  string `json:"source"` // "cortex" | "python" | "ts"
	RelPath string `json:"rel_path"`
	Lang    string `json:"lang"`
	Role    string `json:"role"` // "source" | "config" | "test" | "doc"
	Content string `json:"content"`
}

// ABResult is one (panel entry × op) outcome.
type ABResult struct {
	Entry      PanelEntry      `json:"entry"`
	Op         string          `json:"op"`
	Output     json.RawMessage `json:"output"`
	Fallback   bool            `json:"fallback"`
	TokensUsed int             `json:"tokens_used"`
	LatencyMS  int             `json:"latency_ms"`
}

func TestExtractAB(t *testing.T) {
	if os.Getenv("CORTEX_RUN_AB") != "1" {
		t.Skip("set CORTEX_RUN_AB=1 to run the A/B eval (requires an LLM provider)")
	}
	if *abPanelPath == "" {
		t.Fatal("-panel <path/to/panel.json> is required")
	}
	if *abUpdate {
		t.Skipf("update mode not implemented in v1; populate %s manually", *abPanelPath)
	}

	panel, err := loadPanel(*abPanelPath)
	if err != nil {
		t.Fatalf("load panel: %v", err)
	}
	if len(panel) == 0 {
		t.Fatal("empty panel")
	}

	provider := buildABProvider(t)
	results := []ABResult{}
	for _, p := range panel {
		for _, op := range []string{"extract_insight", "extract_overview"} {
			res := runOnePanelEntry(t, provider, p, op)
			results = append(results, res)
		}
	}

	// Sort + emit a scoring table to stdout. The HUMAN scoring step
	// happens after the table is reviewed; the panel's `Role` ground
	// truth lets a reviewer assign 0/1/2 per (entry × op).
	sort.Slice(results, func(i, j int) bool {
		if results[i].Entry.RelPath != results[j].Entry.RelPath {
			return results[i].Entry.RelPath < results[j].Entry.RelPath
		}
		return results[i].Op < results[j].Op
	})
	emitTable(t, results)
}

func loadPanel(path string) ([]PanelEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p []PanelEntry
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("decode panel: %w", err)
	}
	return p, nil
}

func runOnePanelEntry(t *testing.T, provider llm.Provider, p PanelEntry, op string) ABResult {
	t.Helper()
	source := fmt.Sprintf("ab:%s:%s", p.Source, p.RelPath)
	started := time.Now()
	var spec dag.NodeSpec
	switch op {
	case "extract_insight":
		spec = ops.ExtractInsightSpec(ops.ExtractInsightConfig{Provider: provider})
	case "extract_overview":
		spec = ops.ExtractOverviewSpec(ops.ExtractOverviewConfig{Provider: provider})
	default:
		t.Fatalf("unknown op: %s", op)
	}
	in := map[string]any{
		"content":        p.Content,
		"source":         source,
		"lang_hint":      p.Lang,
		"file_role_hint": p.Role,
	}
	res, err := spec.Handler(context.Background(), in, dag.Budget{LatencyMS: 60000, Tokens: 1000, Depth: 5})
	if err != nil {
		t.Logf("%s on %s errored: %v", op, p.RelPath, err)
	}
	rb, _ := json.Marshal(res.Out)
	fb, _ := res.Out["fallback"].(bool)
	return ABResult{
		Entry:      p,
		Op:         op,
		Output:     rb,
		Fallback:   fb,
		TokensUsed: res.CostConsumed.Tokens,
		LatencyMS:  int(time.Since(started).Milliseconds()),
	}
}

func emitTable(t *testing.T, results []ABResult) {
	t.Helper()
	t.Log("=== A/B RESULTS ===")
	for _, r := range results {
		t.Logf("[%-7s] %-30s op=%-18s tokens=%-4d latency=%-5dms fallback=%v",
			r.Entry.Lang, r.Entry.RelPath, r.Op, r.TokensUsed, r.LatencyMS, r.Fallback)
	}

	// Persist the full result set (with each op's actual Output JSON)
	// to disk so the scoring step can read content, not just metrics.
	// Path defaults to <panelPath>.results.json so each panel keeps
	// its companion result file in lockstep.
	resultsPath := *abPanelPath + ".results.json"
	if b, err := json.MarshalIndent(results, "", "  "); err == nil {
		_ = os.WriteFile(resultsPath, b, 0o644)
		t.Logf("results dumped → %s", resultsPath)
	}

	t.Log("(Score each (entry × op) manually: 0=irrelevant, 1=partial, 2=full. " +
		"Record decision in docs/eval-journal.md per docs/study-dag-plan.md §A/B.)")
}

// buildABProvider returns a real LLM provider when CORTEX_RUN_AB=1.
// It defers all provider construction to the standard llm package
// factories the rest of Cortex uses.
//
// This is intentionally minimal — the test honors whatever provider
// the developer's environment is configured for.
func buildABProvider(t *testing.T) llm.Provider {
	t.Helper()
	// Lightweight: callers point CORTEX_LLM_KEY / CORTEX_LLM_MODEL.
	// The factory is in the codebase already; here we route through
	// OpenAI-compat (Ollama by default at localhost) so a local model
	// can drive the panel without network.
	endpoint := os.Getenv("CORTEX_AB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:11434/v1" // Ollama default
	}
	model := os.Getenv("CORTEX_AB_MODEL")
	if model == "" {
		model = "qwen2.5-coder:1.5b"
	}
	c := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:    "cortex-ab",
		BaseURL: endpoint,
	})
	c.SetModel(model)
	if !c.IsAvailable() {
		t.Skipf("A/B provider %s @ %s not available", model, endpoint)
	}
	return c
}
