// telemetry.go — unified cell_results.jsonl row writer (axis 6
// Observability; see docs/tool-surface.md). Every CLI invocation
// appends one row to .cortex/db/cell_results.jsonl regardless of
// whether it ran inside an eval cell — so the same `cortex search`
// query produces structurally identical telemetry inside and outside
// an eval.
//
// Schema decision: the row is a UNION (superset) of the existing
// eval CellResult shape and the new CLI telemetry fields. Eval cells
// continue writing their full CellResult; CLI invocations write only
// the telemetry-specific fields plus a discriminator
// `source: "cli"`. JSON's heterogeneous-row nature makes this safe —
// downstream consumers filter by `source` when they care.
//
// SQLite is NOT mirrored for CLI rows. The eval cell_results table
// has NOT NULL constraints (scenario_id, harness, ...) that would
// require sentinel values for ad-hoc rows, polluting analytic queries.
// The append log is the canonical analysis source per Hard Constraint
// #8 from the eval prep epic anyway; SQLite is a query-side
// projection of eval rows specifically.

package cliout

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CellResultsPath is the canonical path inside a project's .cortex/db/
// directory. Callers join with the project root or a workdir.
const CellResultsPath = ".cortex/db/cell_results.jsonl"

// TelemetryRow is one CLI-invocation row. Field tags carry the schema
// the unified sink commits to. Optional fields use omitempty so eval
// readers parsing the same line don't see zero-valued noise.
//
// `source: "cli"` is the discriminator that distinguishes these rows
// from eval CellResult rows in the same file. Eval rows omit `source`
// (or set it to "eval" in a future migration).
type TelemetryRow struct {
	Source         string  `json:"source"` // always "cli" here
	Timestamp      string  `json:"timestamp"`
	TraceID        string  `json:"trace_id"`
	CortexFunction string  `json:"cortex_function,omitempty"`
	Tool           string  `json:"tool,omitempty"`
	Command        string  `json:"command"`
	LatencyMs      int64   `json:"latency_ms"`
	Ok             bool    `json:"ok"`
	ErrorCode      string  `json:"error_code,omitempty"`
	Tokens         int     `json:"tokens,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	BytesIn        int     `json:"bytes_in,omitempty"`
	BytesOut       int     `json:"bytes_out,omitempty"`
}

// telemetryEnvDisable is the env var that opts out of telemetry. Set
// to any non-empty value to suppress every row write. Mirrors the
// --no-telemetry flag main.go honors before dispatch.
const TelemetryDisableEnv = "CORTEX_NO_TELEMETRY"

// Invocation captures one CLI invocation's lifecycle: started at
// construction, finalized via Finish, written to disk by WriteRow.
// One per main()/Execute pass.
type Invocation struct {
	command        string
	cortexFunction string
	traceID        string
	start          time.Time
	workdir        string // explicit --workdir if any; otherwise empty
}

// NewInvocation stamps an invocation with the time it started. command
// is the CLI verb (e.g. "search"); cortexFunction is the architectural
// function this command participates in (see CortexFunctionFor).
// workdir is the explicit --workdir flag value (empty if absent).
func NewInvocation(command, cortexFunction, workdir string) *Invocation {
	return &Invocation{
		command:        command,
		cortexFunction: cortexFunction,
		traceID:        NewTraceID(),
		start:          time.Now(),
		workdir:        workdir,
	}
}

// TraceID exposes the id so callers — typically the command's own
// Emitter — can stamp it on emitted envelopes. Joining envelope meta
// to telemetry rows requires this id matching across both.
func (inv *Invocation) TraceID() string { return inv.traceID }

// Emitter returns an envelope Emitter that shares this invocation's
// trace id and start time. Commands SHOULD construct their `--json`
// emitter via this method instead of NewEmitter so the envelope's
// `meta.trace_id` matches the telemetry row's `trace_id` — that's the
// join key analysis pipelines use to correlate envelopes with rows.
//
// projectDir is forwarded to the Emitter for path redaction; pass the
// command's --workdir (or "") so paths outside the workdir get redacted.
func (inv *Invocation) Emitter(projectDir string) *Emitter {
	home, _ := os.UserHomeDir()
	cortexHome := ""
	if home != "" {
		cortexHome = filepath.Join(home, ".cortex")
	}
	abs := projectDir
	if abs != "" {
		if resolved, err := filepath.Abs(abs); err == nil {
			abs = resolved
		}
	}
	return &Emitter{
		traceID:    inv.traceID,
		start:      inv.start,
		projectDir: abs,
		homeDir:    cortexHome,
	}
}

// FinishOk builds a success row.
func (inv *Invocation) FinishOk() TelemetryRow {
	return inv.row(true, "")
}

// FinishErr builds a failure row, surfacing the error code if the
// caller has one (env error codes from cliout.ErrCodeXxx) or a
// generic "internal_error" otherwise.
func (inv *Invocation) FinishErr(code string) TelemetryRow {
	if code == "" {
		code = ErrCodeInternal
	}
	return inv.row(false, code)
}

func (inv *Invocation) row(ok bool, errCode string) TelemetryRow {
	return TelemetryRow{
		Source:         "cli",
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		TraceID:        inv.traceID,
		CortexFunction: inv.cortexFunction,
		Command:        inv.command,
		LatencyMs:      time.Since(inv.start).Milliseconds(),
		Ok:             ok,
		ErrorCode:      errCode,
	}
}

// jsonlWriteMu serializes appends within a single process so the
// kernel-level O_APPEND atomicity isn't the only guard. Cross-process
// safety relies on POSIX small-write atomicity (rows are ~200 bytes,
// well under PIPE_BUF); a future grow may need flock here.
var jsonlWriteMu sync.Mutex

// WriteRow appends one row to the resolved telemetry path. Resolution
// order:
//  1. If env TelemetryDisableEnv is set → no-op, no error.
//  2. If --workdir is set and points at an initialized cortex tree
//     (<workdir>/.cortex exists) → <workdir>/.cortex/db/cell_results.jsonl.
//  3. Else if cwd is initialized (./.cortex exists) → ./.cortex/db/cell_results.jsonl.
//  4. Else → no-op (don't pollute non-cortex directories with
//     stray .cortex/ trees).
//
// Failures from os.OpenFile/Write are returned but should be treated
// as advisory by callers (telemetry must not crash a successful
// command).
func (inv *Invocation) WriteRow(row TelemetryRow) error {
	if os.Getenv(TelemetryDisableEnv) != "" {
		return nil
	}
	dir, ok := resolveTelemetryDir(inv.workdir)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "cell_results.jsonl")
	line, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("marshal row: %w", err)
	}
	line = append(line, '\n')

	jsonlWriteMu.Lock()
	defer jsonlWriteMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// resolveTelemetryDir returns the directory whose .cortex/db/ should
// receive the row. Returns (path, true) when a candidate is found and
// (path, false) when no initialized cortex tree exists nearby — caller
// should skip the write in that case.
func resolveTelemetryDir(workdir string) (string, bool) {
	if workdir != "" {
		cortexDir := filepath.Join(workdir, ".cortex")
		if info, err := os.Stat(cortexDir); err == nil && info.IsDir() {
			return filepath.Join(cortexDir, "db"), true
		}
		// Workdir specified but not initialized: still write under it
		// so benchmarks that point at fresh tempdirs get telemetry.
		// MkdirAll in WriteRow will create both .cortex/ and .cortex/db/.
		return filepath.Join(workdir, ".cortex", "db"), true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	if info, err := os.Stat(filepath.Join(cwd, ".cortex")); err == nil && info.IsDir() {
		return filepath.Join(cwd, ".cortex", "db"), true
	}
	return "", false
}

// HasNoTelemetryFlag is a helper for main.go's dispatch: scans args
// for `--no-telemetry` (or `--no-telemetry=true`) and reports whether
// telemetry should be suppressed for this invocation. Side-effect-free
// — caller is responsible for stripping the flag from args if downstream
// argument parsers would reject it.
func HasNoTelemetryFlag(args []string) bool {
	for _, a := range args {
		if a == "--no-telemetry" || strings.HasPrefix(a, "--no-telemetry=") {
			return true
		}
	}
	return false
}

// StripNoTelemetry returns args with --no-telemetry / --no-telemetry=...
// removed. Use this before dispatching to a command whose own flag
// parser doesn't know about the global flag.
func StripNoTelemetry(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--no-telemetry" || strings.HasPrefix(a, "--no-telemetry=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// WorkdirFromArgs scans args for --workdir and returns its value (or
// empty when absent). Mirrors hasWorkdirFlag in cmd/cortex/main.go but
// returns the value, not just presence, so telemetry can route to the
// right .cortex/ tree.
func WorkdirFromArgs(args []string) string {
	for i, a := range args {
		if a == "--workdir" || a == "-w" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if strings.HasPrefix(a, "--workdir=") {
			return strings.TrimPrefix(a, "--workdir=")
		}
	}
	return ""
}

// CortexFunctionFor maps a command verb to its architectural cortex
// function. Used to stamp the telemetry row so post-hoc analysis can
// chart per-function cost/latency.
//
// The mapping is a deliberate, hand-maintained simplification: most
// commands cleanly belong to one function. Commands the architecture
// doesn't yet model get "" (omitted from the row's cortex_function
// field). Add entries here as commands land.
//
// See docs/integration-roadmap.md "framework triangulation" for the
// 9-function vocabulary.
func CortexFunctionFor(command string) string {
	switch command {
	case "search", "search-vector":
		return "Attend" // salience-over-substrate; surfaces candidates
	case "capture", "ingest", "feed", "embed", "reembed":
		return "Sense" // intake / encoding / indexing
	case "analyze", "dream-debug":
		return "Maintain" // offline consolidation
	case "code", "repl":
		return "Decide" // model selects + acts
	case "eval", "measure":
		return "" // meta-tool over the rest; no single function
	case "journal", "prune", "forget":
		return "Maintain"
	case "watch", "status", "tools":
		return "" // observability / config; no cortex function
	case "init", "install", "uninstall", "projects", "daemon",
		"setup", "test":
		return "" // lifecycle / harness wiring; no cortex function
	}
	return ""
}
