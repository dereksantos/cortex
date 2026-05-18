// Package ops — remember.fetch_external.
//
// Pulls targeted API surface from an external source. V0 dispatch:
// Go packages → `go doc <pkg>` (one process invocation; ~50-200ms
// on cached deps). Per-project cache at
// .cortex/db/external_snippets/<sanitized-package>.txt so repeat
// fetches in the same session are free.
//
// Pairs with value.detect_unfamiliarity — when that op emits
// findings, each one becomes a fetch input. The fetched snippet is
// what a coding turn's re-attempt would prepend to its context.
//
// Privacy: `go doc` reads from the local Go module cache. It does
// NOT make outbound network requests for already-resolved packages
// (the dep needs to be in go.mod / go.sum / module cache first).
// For pkg.go.dev fetching (true network), a future iteration can
// add an HTTP client; the journal's local-only invariant gets a
// sibling "external lookups are opt-in and logged" rule per the
// third-arm prototype's caveats.
package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

// FetchExternalConfig is the registration shape. CacheDir defaults
// to .cortex/db/external_snippets relative to cwd when empty.
type FetchExternalConfig struct {
	CacheDir string
}

// FetchExternalSpec returns the NodeSpec for remember.fetch_external.
func FetchExternalSpec(cfg FetchExternalConfig) dag.NodeSpec {
	return dag.NodeSpec{
		Function:    dag.FuncRemember,
		Op:          "fetch_external",
		Description: "fetch targeted API surface from go doc (Go) or curated source",
		Inputs: []dag.ParamSpec{
			{Name: "package", Type: "string", Required: true},
			{Name: "symbol", Type: "string"},
			{Name: "language", Type: "string"},
		},
		Outputs: []dag.ParamSpec{
			{Name: "snippet", Type: "string"},
			{Name: "source", Type: "string"},
			{Name: "cached", Type: "bool"},
		},
		Cost:    dag.Cost{LatencyMS: 300, Tokens: 0},
		Handler: NewFetchExternalHandler(cfg),
	}
}

// NewFetchExternalHandler returns the dag.Handler. Mechanical: no
// LLM call. Latency is dominated by the `go doc` subprocess (or 0
// on cache hit).
func NewFetchExternalHandler(cfg FetchExternalConfig) dag.Handler {
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(".cortex", "db", "external_snippets")
	}
	return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
		pkg := readString(in, "package")
		if pkg == "" {
			return dag.NodeResult{
				Out:          map[string]any{"snippet": "", "source": "", "cached": false, "error": "missing package input"},
				CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
			}, nil
		}
		symbol := readString(in, "symbol")
		language := readString(in, "language")
		if language == "" {
			language = "go"
		}

		key := cacheKey(pkg, symbol)
		cachePath := filepath.Join(cacheDir, key+".txt")
		if cached, err := os.ReadFile(cachePath); err == nil {
			return dag.NodeResult{
				Out: map[string]any{
					"snippet": string(cached),
					"source":  "cache:" + cachePath,
					"cached":  true,
				},
				CostConsumed: dag.Cost{LatencyMS: 2, Tokens: 0},
			}, nil
		}

		started := time.Now()
		var (
			snippet string
			source  string
			err     error
		)
		switch language {
		case "go":
			snippet, source, err = fetchGoDoc(ctx, pkg, symbol)
		default:
			err = fmt.Errorf("remember.fetch_external: language %q not implemented (Go only in V0)", language)
		}
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			return dag.NodeResult{
				Out: map[string]any{
					"snippet": "",
					"source":  source,
					"cached":  false,
					"error":   err.Error(),
				},
				CostConsumed: dag.Cost{LatencyMS: latency, Tokens: 0},
			}, nil
		}

		// Cache write is best-effort; failures don't fail the op.
		_ = os.MkdirAll(cacheDir, 0o755)
		_ = os.WriteFile(cachePath, []byte(snippet), 0o644)

		return dag.NodeResult{
			Out: map[string]any{
				"snippet": snippet,
				"source":  source,
				"cached":  false,
			},
			CostConsumed: dag.Cost{LatencyMS: latency, Tokens: 0},
		}, nil
	}
}

// fetchGoDoc runs `go doc <pkg>[.<symbol>]` and returns its output.
// Bounded by a 3-second context deadline so a hung doc command can't
// block the executor.
func fetchGoDoc(ctx context.Context, pkg, symbol string) (string, string, error) {
	target := pkg
	if symbol != "" {
		target = pkg + "." + symbol
	}
	deadline, cancel := contextWithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, "go", "doc", target)
	out, err := cmd.CombinedOutput()
	source := "go-doc:" + target
	if err != nil {
		return "", source, fmt.Errorf("go doc %s: %w (%s)", target, err, strings.TrimSpace(string(out)))
	}
	return string(out), source, nil
}

// contextWithTimeout is a thin wrapper around context.WithTimeout
// kept here so unit tests can stub timing if they ever need to.
// (Not currently swapped out — direct passthrough.)
func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	type contextKey struct{}
	_ = contextKey{}
	return contextWithTimeoutImpl(parent, d)
}

// cacheKey sanitizes (package, symbol) into a filename-safe slug.
// Slashes become underscores so the import path doesn't escape the
// cache dir.
func cacheKey(pkg, symbol string) string {
	k := strings.ReplaceAll(pkg, "/", "_")
	k = strings.ReplaceAll(k, ":", "_")
	if symbol != "" {
		k = k + "." + symbol
	}
	return k
}
