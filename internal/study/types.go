// Package study implements the project-study DAG: a one-shot
// scan of a project's source files that samples chunks via a
// hierarchical fractal sampler and emits dream.insight journal entries
// until coverage hits a target threshold.
//
// See docs/study-dag-plan.md for the architecture and step-by-step
// rationale. The package is designed so the controller, analyzer, and
// sampler are independently testable: the Sampler interface lets a
// future Lévy / RWR sampler drop in without touching the controller,
// and the BoundaryAnalyzer interface leaves room for Tier 2 (regex
// imports per language) and Tier 3 (go/ast, tree-sitter) plugins.
//
// Determinism contract (binding for every file in this package):
//   - no time.Now() in the seed-derived path; RNGSeed comes solely
//     from StateHash + cfg.Salt
//   - no map iteration in any sampler hot path; sort to []T first
//   - bufio.Scanner default ScanLines for line counting
//   - filepath.WalkDir results are re-sorted lexically before any
//     output (defensive)
package study

import (
	"context"
	"math/rand"
	"time"

	"github.com/dereksantos/cortex/internal/projectscan"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Chunk is one unit the sampler hands out and the LLM ingests. Chunk
// IDs are stable: same (relpath, line_start, line_end) always hashes
// the same way, so the coverage bitmap survives re-scans.
type Chunk struct {
	ID         string // sha256(relpath + ":" + line_start + ":" + line_end)[:16]
	Path       string // absolute
	RelPath    string // relative to project root
	LineStart  int    // 1-indexed
	LineEnd    int    // inclusive
	ByteOffset int64  // for fractal.ReadRegion
	ByteLength int    // for fractal.ReadRegion
	EffLines   int    // non-blank + non-comment lines in chunk
	EstTokens  int    // chars / 4 (rough)
	ModuleID   string // module assignment (rel path of marker dir, or top-level dir)
	Lang       string // extension-derived hint ("go", "py", "md", "unknown", ...)
}

// Edge connects two modules. Tier 1 only emits Kind="fs_dir".
type Edge struct {
	FromModuleID string
	ToModuleID   string
	Kind         string  // "fs_dir" | "import" | "ast_call"
	Weight       float64 // 1.0 for fs_dir
}

// Module is a group of chunks sharing a directory or marker root.
type Module struct {
	ID         string // rel path of marker dir, or top-level dir under root
	RootPath   string // absolute
	ChunkIDs   []string
	Lines      int    // raw line total across module's chunks
	EffLines   int    // effective lines
	Files      int    // unique files (chunks may span the same file)
	HasMarker  bool   // true if a language-root or build-helper marker fired here
	MarkerName string // "go.mod" / "package.json" / "" (no marker; top-level fallback)
}

// BoundaryOutput is the full project carve-up emitted by a
// BoundaryAnalyzer. Same input + same analyzer config → identical
// output (determinism contract).
type BoundaryOutput struct {
	ProjectRoot   string
	Modules       []Module
	Chunks        []Chunk
	Edges         []Edge // Tier 1: fs_dir only
	TotalLines    int    // raw line count across all chunks (diagnostic)
	EffTotalLines int    // primary-signal denominator
	TotalFiles    int    // secondary-signal denominator (files with ≥1 chunk)
	RNGSeed       int64
	StateHash     string            // sha256(sorted "relpath:size:mtime_unix") — diagnostic only
	FileHashes    map[string]string // relpath → sha256(content); drift-detection key
}

// BoundaryAnalyzer carves a project into modules + chunks + edges. The
// concrete implementation carries its own configuration (window knobs,
// salt, etc.) so the interface stays minimal.
type BoundaryAnalyzer interface {
	Analyze(ctx context.Context, projectRoot string, ignore *projectscan.IgnoreSet) (*BoundaryOutput, error)
	Tier() int // 1 = universal, 2 = regex imports, 3 = AST
}

// Sampler picks the next K chunk IDs given coverage state. The
// covered map is keyed by Chunk.ID with value true for "already
// extracted." The rng is seeded by the caller from BoundaryOutput.
type Sampler interface {
	Next(out *BoundaryOutput, covered map[string]bool, k int, rng *rand.Rand) []string
	Name() string
}

// Config bundles every knob the controller, analyzer, and extract
// router need. Defaults are sensible — the moment we compare quality
// across languages we'll want to tune per-(language, intent), so the
// knobs ship on day one rather than being baked into the analyzer.
type Config struct {
	ProjectRoot    string
	ContextDir     string // .cortex/
	Provider       llm.Provider
	Storage        *storage.Storage
	TargetCoverage float64 // default 0.80; applied to BOTH signals
	BudgetMax      int     // default 200 iterations
	BatchSize      int     // chunks per iteration, default 4
	WindowLines    int     // default 400
	WindowOverlap  int     // default 40
	ExtractOp      string  // "auto" (default) | "extract_insight" | "extract_overview"
	Salt           string  // optional, mixed into RNG seed
	DryRun         bool    // skip LLM + journal writes
	Banner         func(string)

	// RunID + RunShorthand are set by `cortex study` and (optionally)
	// other callers that want emitted dream.insight entries tagged for
	// later comparison. When non-empty, both appear in every emitted
	// insight's Tags slice and in the meta-insight that opens the run.
	RunID        string
	RunShorthand string
}

// FileCoverage is the per-file unit of study state. Drift detection
// keys on ContentHash: a file's stored ChunkIDs remain covered for as
// long as the file's content sha256 matches what was studied. The
// moment the file changes, its hash changes, the controller treats
// every chunk as uncovered again on the next run.
//
// EffLines is the sum of EffLines across ChunkIDs at the time of
// extraction. Stored so we can recompute coverage fractions without
// needing the BoundaryOutput in hand.
type FileCoverage struct {
	ContentHash string    `json:"content_hash"`
	CoveredAt   time.Time `json:"covered_at"`
	ChunkIDs    []string  `json:"chunk_ids"` // sorted; the chunks of this file that produced an insight
	EffLines    int       `json:"eff_lines"` // sum across ChunkIDs
}

// State is the persisted controller state. It's written to
// .cortex/study_state.json atomically (temp + rename) and read on
// process start.
//
// Per-file coverage (CoveredFiles) is the source of truth from v2 on:
// a file is "covered" iff its current content hash matches the stored
// ContentHash. CompletedAt is informational ("we hit target at least
// once") and is no longer the gate — the gate is current-snapshot
// coverage vs TargetCoverage, recomputed each run.
//
// CoveredChunkIDs is retained as the v1 migration source; on the first
// v2 run the controller folds it into CoveredFiles using BoundaryOutput
// to recover per-file groupings.
type State struct {
	Version         int                     `json:"v"`
	ProjectRoot     string                  `json:"project_root"`
	StateHash       string                  `json:"state_hash"` // diagnostic only from v2
	RNGSeed         int64                   `json:"rng_seed"`
	TargetCoverage  float64                 `json:"target_coverage"`
	BudgetMax       int                     `json:"budget_max"`
	BatchSize       int                     `json:"batch_size"`
	StartedAt       time.Time               `json:"started_at"`
	CompletedAt     *time.Time              `json:"completed_at,omitempty"`
	Iteration       int                     `json:"iteration"`
	CoveredChunkIDs []string                `json:"covered_chunk_ids,omitempty"` // legacy v1; kept for migration
	CoveredFiles    map[string]FileCoverage `json:"covered_files_v2,omitempty"`  // v2 source of truth (relpath → coverage)
	CoveredEffLines int                     `json:"covered_eff_lines"`           // recomputed each run from CoveredFiles
	EffTotalLines   int                     `json:"eff_total_lines"`
	CoveredFileN    int                     `json:"covered_file_count"` // count of files in CoveredFiles intersected with current snapshot
	TotalFiles      int                     `json:"total_files"`
	InsightsEmitted int                     `json:"insights_emitted"`
	ExtractOpUsed   map[string]int          `json:"extract_op_used,omitempty"`
	Halted          string                  `json:"halted,omitempty"` // "" | "target" | "no_drift" | "budget_loc" | "budget_files" | "canceled" | "error"
}
