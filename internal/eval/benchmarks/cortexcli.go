// cortexcli.go — subprocess helpers for benchmark runners that treat
// the cortex CLI as a black box (per docs/prompts/eval-principles.md).
//
// Every benchmark that needs to capture, ingest, or search MUST go
// through these helpers rather than importing internal/capture,
// internal/storage, internal/processor, or internal/cognition directly.
// That is the whole point: an eval that wraps the production pipeline
// in-process measures a configuration nobody runs.
//
// Binary resolution is shared (env override + PATH fallback). Each
// helper takes an explicit workdir so the caller controls isolation
// — no cwd-walking, no global state.

package benchmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/dereksantos/cortex/pkg/events"
)

// ResolveCortexBinary returns the cortex binary path. Resolution order:
//  1. $CORTEX_BINARY (absolute path, must exist)
//  2. PATH lookup for `cortex`
//
// Benchmarks should set $CORTEX_BINARY to a freshly-built artifact so
// they exercise the same code shipped to users. Falling back to PATH
// avoids hard-failing in environments where the installed `cortex` is
// the current binary (e.g. release smoke tests).
func ResolveCortexBinary() (string, error) {
	if env := os.Getenv("CORTEX_BINARY"); env != "" {
		if !filepath.IsAbs(env) {
			return "", fmt.Errorf("CORTEX_BINARY must be absolute, got %q", env)
		}
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("CORTEX_BINARY=%s: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("cortex")
	if err != nil {
		return "", fmt.Errorf("cortex binary not found in PATH (set $CORTEX_BINARY to override)")
	}
	return path, nil
}

// RunBulkCapture writes the given events to <workdir>/.cortex via
// `cortex capture --bulk --workdir <workdir>`. Events are encoded as
// NDJSON on stdin; a single subprocess handles all of them, so
// hydration cost scales with serialization, not fork+exec.
//
// Returns nil on full success; the first malformed event or write
// failure surfaces as a wrapped error including the CLI's stderr.
func RunBulkCapture(ctx context.Context, binary, workdir string, evs []*events.Event) error {
	if binary == "" {
		return errors.New("cortex binary is empty")
	}
	if workdir == "" {
		return errors.New("workdir is empty")
	}
	if len(evs) == 0 {
		return nil
	}

	var stdin bytes.Buffer
	enc := json.NewEncoder(&stdin)
	for i, ev := range evs {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("encode event %d: %w", i, err)
		}
	}

	cmd := exec.CommandContext(ctx, binary, "capture", "--bulk", "--workdir", workdir)
	cmd.Stdin = &stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cortex capture --bulk: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// RunIngest drains the capture journal at <workdir>/.cortex into the
// SQLite store via `cortex ingest --workdir <workdir>`. Must be called
// between RunBulkCapture and RunSearch; the search reads from SQLite,
// not the journal.
func RunIngest(ctx context.Context, binary, workdir string) error {
	if binary == "" {
		return errors.New("cortex binary is empty")
	}
	if workdir == "" {
		return errors.New("workdir is empty")
	}
	cmd := exec.CommandContext(ctx, binary, "ingest", "--workdir", workdir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cortex ingest: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// SearchMode selects the retrieval pipeline. Mirrors --mode on the CLI:
// Fast = Reflex only; Full = Reflex + Reflect.
type SearchMode string

const (
	SearchFast SearchMode = "fast"
	SearchFull SearchMode = "full"
)

// SearchResult is one entry from `cortex search --json` output. The
// schema is the public contract on the CLI side (searchJSONResult in
// cmd/cortex/commands/query.go); keep these structs in sync.
type SearchResult struct {
	Score   float64 `json:"score"`
	Content string  `json:"content"`
}

// SearchOutput is the top-level shape of `cortex search --json`.
type SearchOutput struct {
	Mode      string         `json:"mode"`
	ElapsedMs int64          `json:"elapsed_ms"`
	Results   []SearchResult `json:"results"`
}

// RunSearch invokes `cortex search --workdir <workdir> --json` and
// decodes the structured output. limit ≤ 0 leaves the CLI default
// (5 results). mode "" defaults to Fast.
func RunSearch(ctx context.Context, binary, workdir string, mode SearchMode, limit int, query string) (*SearchOutput, error) {
	if binary == "" {
		return nil, errors.New("cortex binary is empty")
	}
	if workdir == "" {
		return nil, errors.New("workdir is empty")
	}
	if query == "" {
		return nil, errors.New("query is empty")
	}
	if mode == "" {
		mode = SearchFast
	}

	args := []string{"search", "--workdir", workdir, "--json", "--mode", string(mode)}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	args = append(args, query)

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cortex search: %w (stderr: %s)", err, stderr.String())
	}

	out := &SearchOutput{}
	if err := json.NewDecoder(&stdout).Decode(out); err != nil {
		return nil, fmt.Errorf("decode search JSON: %w (stdout: %s)", err, truncate(stdout.String(), 200))
	}
	return out, nil
}

// truncate is a debug helper for error messages: keeps the first n
// bytes and appends "..." if the input is longer.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// CompileBinary builds the cortex CLI to a tempfile and returns its
// absolute path. Used by benchmark test suites that need a real binary
// before exercising the CLI helpers above. The caller is responsible
// for cleanup (typically via t.Cleanup).
//
// Build tags and ldflags match a plain `go build ./cmd/cortex`; if a
// benchmark needs a specialized build, it should construct exec.Command
// itself rather than extending this helper.
func CompileBinary(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", errors.New("repoRoot is empty")
	}
	dir, err := os.MkdirTemp("", "cortex-bench-bin-*")
	if err != nil {
		return "", fmt.Errorf("mkdir bin: %w", err)
	}
	out := filepath.Join(dir, "cortex")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/cortex")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("go build ./cmd/cortex: %w (stderr: %s)", err, stderr.String())
	}
	return out, nil
}
