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

	// Preserve raw stdout for error diagnostics; json.NewDecoder consumes
	// the buffer it reads from, so the otherwise-empty `(stdout: )` in
	// a wrapped error hides what actually came back.
	raw := stdout.String()
	out := &SearchOutput{}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return nil, fmt.Errorf("decode search JSON: %w (stdout %d bytes: %q)", err, len(raw), truncate(raw, 400))
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

// CodeOpts configures a `cortex code` subprocess invocation. Workdir,
// Model, and Prompt are required; other fields are forwarded as flags
// only when non-zero so the CLI defaults stay in charge.
//
// SystemPrompt overrides the harness's default system prompt. Per
// eval-principles #2 (no coaching): use this for *framing* the task
// (declaring role, output format), NOT for *coaching* tool use. A
// benchmark that teaches the model "call cortex_search aggressively"
// is laundering its score.
type CodeOpts struct {
	Workdir      string
	Model        string
	Prompt       string
	SystemPrompt string  // --system-prompt (empty = harness default)
	NoSearch     bool    // --no-search (omit cortex_search from tool registry)
	MaxTurns     int     // --max-turns (0 = CLI default)
	MaxCost      float64 // --max-cost USD (0 = CLI default)
	APIURL       string  // --api-url (empty = OpenRouter default)
}

// CodeOutput mirrors the codeJSONOutput struct emitted by `cortex code
// --json` (defined in cmd/cortex/commands/code.go). Keep the two in
// sync — this is the public CLI contract benchmarks parse.
type CodeOutput struct {
	Workdir         string   `json:"workdir"`
	Model           string   `json:"model"`
	Turns           int      `json:"turns"`
	TokensIn        int      `json:"tokens_in"`
	TokensOut       int      `json:"tokens_out"`
	CostUSD         float64  `json:"cost_usd"`
	LatencyMs       int64    `json:"latency_ms"`
	Reason          string   `json:"reason"`
	FilesChanged    []string `json:"files_changed"`
	Final           string   `json:"final"`
	InjectedContext int      `json:"injected_context_tokens"`
}

// RunCode invokes `cortex code --json` and decodes the structured
// output. Used by SWE-bench (and future agent-driven benchmarks) to
// drive the Cortex coding harness through the CLI without importing
// internal/harness or evalv2.NewCortexHarness directly.
//
// The returned error carries CLI stderr (truncated) for triage. A nil
// CodeOutput with non-nil error means the subprocess failed before
// emitting JSON; a non-nil CodeOutput with TaskSuccess-like fields set
// to zero means the agent ran but didn't accomplish anything (use
// CodeOutput.FilesChanged and Final to triage further).
func RunCode(ctx context.Context, binary string, opts CodeOpts) (*CodeOutput, error) {
	if binary == "" {
		return nil, errors.New("cortex binary is empty")
	}
	if opts.Workdir == "" {
		return nil, errors.New("workdir is empty")
	}
	if opts.Model == "" {
		return nil, errors.New("model is empty")
	}
	if opts.Prompt == "" {
		return nil, errors.New("prompt is empty")
	}

	args := []string{"code", "--workdir", opts.Workdir, "--model", opts.Model, "--json"}
	if opts.NoSearch {
		args = append(args, "--no-search")
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.MaxCost > 0 {
		args = append(args, "--max-cost", strconv.FormatFloat(opts.MaxCost, 'f', -1, 64))
	}
	if opts.APIURL != "" {
		args = append(args, "--api-url", opts.APIURL)
	}
	args = append(args, opts.Prompt)

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cortex code: %w (stderr: %s)", err, truncate(stderr.String(), 500))
	}

	out := &CodeOutput{}
	if err := json.NewDecoder(&stdout).Decode(out); err != nil {
		return nil, fmt.Errorf("decode code JSON: %w (stdout: %s)", err, truncate(stdout.String(), 200))
	}
	return out, nil
}

// EmbedBulkRequest is one entry in the NDJSON stream piped to
// `cortex embed --bulk`. ContentType is optional — when empty, the
// CLI applies its --content-type default (typically "corpus" for
// MTEB-style indexing).
type EmbedBulkRequest struct {
	DocID       string `json:"doc_id"`
	ContentType string `json:"content_type,omitempty"`
	Text        string `json:"text"`
}

// EmbedBulkSummary mirrors the stdout JSON returned by --bulk:
// {stored, model, provider, dim}. Callers should assert Stored equals
// the number of requests they piped in — silent drops are a bug.
type EmbedBulkSummary struct {
	Stored   int    `json:"stored"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Dim      int    `json:"dim"`
}

// RunEmbedBulk pipes the requests to `cortex embed --bulk --workdir
// <workdir> --content-type <defaultContentType>` and decodes the
// summary. defaultContentType is forwarded as the CLI flag; per-request
// ContentType overrides win at decode time inside the CLI.
//
// Returns the summary so callers can verify Stored == len(requests).
// A subprocess failure surfaces with stderr (truncated) in the wrapped
// error.
func RunEmbedBulk(ctx context.Context, binary, workdir, defaultContentType string, requests []EmbedBulkRequest) (*EmbedBulkSummary, error) {
	if binary == "" {
		return nil, errors.New("cortex binary is empty")
	}
	if workdir == "" {
		return nil, errors.New("workdir is empty")
	}
	if len(requests) == 0 {
		return &EmbedBulkSummary{}, nil
	}

	var stdin bytes.Buffer
	enc := json.NewEncoder(&stdin)
	for i, req := range requests {
		if err := enc.Encode(req); err != nil {
			return nil, fmt.Errorf("encode request %d: %w", i, err)
		}
	}

	args := []string{"embed", "--bulk", "--workdir", workdir}
	if defaultContentType != "" {
		args = append(args, "--content-type", defaultContentType)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = &stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cortex embed --bulk: %w (stderr: %s)", err, truncate(stderr.String(), 500))
	}
	out := &EmbedBulkSummary{}
	if err := json.NewDecoder(&stdout).Decode(out); err != nil {
		return nil, fmt.Errorf("decode embed --bulk summary: %w (stdout: %s)", err, truncate(stdout.String(), 200))
	}
	return out, nil
}

// SearchVectorOpts configures `cortex search-vector`. Workdir is
// required. Exactly one of Text or Vector must be set (the CLI rejects
// otherwise). ContentType filters server-side to a single bucket.
type SearchVectorOpts struct {
	Workdir     string
	Text        string    // --text: CLI embeds + searches in one shot
	Vector      []float32 // --vector: caller-supplied pre-computed vector
	TopK        int       // --top-k (defaults to 10 in the CLI when ≤0)
	Threshold   float64   // --threshold (0.0 = no filter)
	ContentType string    // --content-type bucket filter (empty = no filter)
}

// SearchVectorResult is one entry in the search-vector JSON output.
type SearchVectorResult struct {
	ContentID   string  `json:"content_id"`
	ContentType string  `json:"content_type"`
	Score       float64 `json:"score"`
	Content     string  `json:"content,omitempty"`
}

// SearchVectorOutput is the top-level shape of `cortex search-vector`.
// Model + Provider are present only when the query came from --text
// (i.e. the CLI embedded it).
type SearchVectorOutput struct {
	Results   []SearchVectorResult `json:"results"`
	K         int                  `json:"k"`
	ElapsedMs int64                `json:"elapsed_ms"`
	Model     string               `json:"model,omitempty"`
	Provider  string               `json:"provider,omitempty"`
}

// RunSearchVector invokes `cortex search-vector --json` and decodes
// the structured output. Used by MTEB to issue per-query retrievals
// without importing internal/storage or internal/cognition.
func RunSearchVector(ctx context.Context, binary string, opts SearchVectorOpts) (*SearchVectorOutput, error) {
	if binary == "" {
		return nil, errors.New("cortex binary is empty")
	}
	if opts.Workdir == "" {
		return nil, errors.New("workdir is empty")
	}
	hasText := opts.Text != ""
	hasVec := len(opts.Vector) > 0
	if hasText == hasVec {
		return nil, errors.New("exactly one of Text or Vector must be set")
	}

	args := []string{"search-vector", "--workdir", opts.Workdir}
	if opts.TopK > 0 {
		args = append(args, "--top-k", strconv.Itoa(opts.TopK))
	}
	if opts.Threshold > 0 {
		args = append(args, "--threshold", strconv.FormatFloat(opts.Threshold, 'f', -1, 64))
	}
	if opts.ContentType != "" {
		args = append(args, "--content-type", opts.ContentType)
	}
	if hasText {
		args = append(args, "--text", opts.Text)
	} else {
		v, err := json.Marshal(opts.Vector)
		if err != nil {
			return nil, fmt.Errorf("marshal vector: %w", err)
		}
		args = append(args, "--vector", string(v))
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cortex search-vector: %w (stderr: %s)", err, truncate(stderr.String(), 500))
	}
	out := &SearchVectorOutput{}
	if err := json.NewDecoder(&stdout).Decode(out); err != nil {
		return nil, fmt.Errorf("decode search-vector JSON: %w (stdout: %s)", err, truncate(stdout.String(), 200))
	}
	return out, nil
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
