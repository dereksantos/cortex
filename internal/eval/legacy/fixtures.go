// Package legacy — canonical fixture set for storage-dependent
// scenarios under test/evals/legacy/cognition/.
//
// The 19 storage-dependent scenarios (reflex_*, reflect_*, dream_*,
// abr_*, session_*, indent_conflict, testing_conflict) reference
// fixture IDs that aren't defined inline. This file enumerates the
// canonical set those IDs map to; a follow-up integration wires
// SeedFixtures to load them into a real storage.Storage instance
// + embedder so the runner can dispatch reflex/reflect/dream/etc.
//
// See:
//   - docs/eval-prep-epic.md Phase B (the deliverable)
//   - docs/prompts/loop-phase-b-legacy-cognition.md (the implementing
//     session prompt)
//   - Phase B + D audit entry in docs/eval-journal.md (path decision)
//
// Status: data authored; storage integration is the remaining work.
// The runner currently reports needs_fixture_seed for these scenarios;
// once SeedFixtures lands, the runner detects fixture availability
// and dispatches instead of skipping.
package legacy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
)

// Fixture is the canonical insight data + the EventID alias the
// scenarios reference. EventID is what scenario `expected.result_ids`
// blocks compare against after Reflex returns Results.
type Fixture struct {
	EventID      string    // matches scenario result_ids entries
	Category     string    // decision | pattern | constraint | insight | correction
	Summary      string    // canonical content
	Importance   int       // 1-10
	Tags         []string
	Reasoning    string    // for decision-type fixtures
	CreatedAt    time.Time // older = lower recency boost
}

// CanonicalFixtures is the full set referenced across the 19 storage-
// dependent scenarios. Grouping by topic for readability.
var CanonicalFixtures = []Fixture{
	// --- Auth domain ---
	{
		EventID:    "auth_decision",
		Category:   "decision",
		Summary:    "Authentication decision: use JWT with 24h expiry, Bearer scheme. Do NOT support session cookies.",
		Importance: 10,
		Tags:       []string{"auth", "authentication", "jwt", "security"},
		Reasoning:  "JWT integrates with our stateless API; sessions create scaling pain.",
	},
	{
		EventID:    "auth_module",
		Category:   "pattern",
		Summary:    "Auth module handles user authentication: login, JWT token issuance, and session validation.",
		Importance: 10,
		Tags:       []string{"auth", "authentication", "module"},
	},
	{
		EventID:    "jwt_handler",
		Category:   "pattern",
		Summary:    "JWT validation handler: Authorization: Bearer <token> header, HS256 signing, generation via internal/auth.",
		Importance: 10,
		Tags:       []string{"auth", "authentication", "jwt", "header"},
	},
	{
		EventID:    "token_validation",
		Category:   "constraint",
		Summary:    "Token validation constraint: every authentication-requiring request must carry a valid Bearer JWT token (except /health, /metrics).",
		Importance: 10,
		Tags:       []string{"auth", "authentication", "jwt", "token", "middleware", "constraint"},
	},
	{
		EventID:    "middleware_pattern",
		Category:   "constraint",
		Summary:    "All endpoints must use AuthMiddleware. Order: logging → auth → handler.",
		Importance: 7,
		Tags:       []string{"auth", "middleware"},
	},

	// --- Database domain ---
	{
		EventID:    "db_schema",
		Category:   "decision",
		Summary:    "Database schema: PostgreSQL for prod, SQLite for local dev. Migrations via golang-migrate.",
		Importance: 9,
		Tags:       []string{"db", "database", "schema", "decision"},
		Reasoning:  "Local SQLite avoids docker dependency; prod PG for write scale.",
	},
	{
		EventID:    "db_connection",
		Category:   "pattern",
		Summary:    "Backend infrastructure: database/sql with pgx driver. Pool sized to 4× CPU cores.",
		Importance: 7,
		Tags:       []string{"db", "connection", "backend", "infrastructure"},
	},
	{
		EventID:    "db_pool",
		Category:   "pattern",
		Summary:    "Backend infrastructure: connection pool max=32, idle=8, max_lifetime=5m. Configure at startup.",
		Importance: 6,
		Tags:       []string{"db", "pool", "backend", "infrastructure"},
	},
	{
		EventID:    "db_config_v2",
		Category:   "decision",
		Summary:    "Database config v2: SQLite for local dev, PostgreSQL for prod. Supersedes v1 (SQLite-only).",
		Importance: 8,
		Tags:       []string{"db", "config", "decision"},
		Reasoning:  "Prod load needs PG; v1's SQLite-only choice didn't scale.",
	},

	// --- Error handling ---
	{
		EventID:    "error_decision",
		Category:   "decision",
		Summary:    "Rejected pkg/errors. Use stdlib fmt.Errorf with %w wrapping.",
		Importance: 9,
		Tags:       []string{"error", "decision"},
		Reasoning:  "Stdlib %w is sufficient since Go 1.13; less surface area than pkg/errors.",
	},
	{
		EventID:    "error_pattern",
		Category:   "pattern",
		Summary:    "Wrap errors with context: fmt.Errorf(\"do X: %w\", err). Never log+return.",
		Importance: 8,
		Tags:       []string{"error", "pattern"},
	},

	// --- Logging ---
	{
		EventID:    "logging_config",
		Category:   "pattern",
		Summary:    "slog with JSON handler in prod, text in dev. Levels: DEBUG, INFO, WARN, ERROR.",
		Importance: 6,
		Tags:       []string{"logging"},
	},

	// --- Handlers / API ---
	{
		EventID:    "handler_pattern",
		Category:   "constraint",
		Summary:    "All handlers return (T, error) — never just error. Allows typed responses.",
		Importance: 7,
		Tags:       []string{"handler", "pattern"},
	},
	{
		EventID:    "api_decision",
		Category:   "decision",
		Summary:    "REST + JSON. Path versioning: /v1, /v2. Avoid GraphQL for now.",
		Importance: 8,
		Tags:       []string{"api"},
	},
	{
		EventID:    "api_v1",
		Category:   "decision",
		Summary:    "API v1: REST endpoints under /v1. Initial public surface.",
		Importance: 6,
		Tags:       []string{"api", "version"},
	},
	{
		EventID:    "api_v2",
		Category:   "decision",
		Summary:    "API v2: REST endpoints under /v2. Adds typed pagination and structured errors.",
		Importance: 7,
		Tags:       []string{"api", "version"},
	},

	// --- Testing ---
	{
		EventID:    "stdlib_testing",
		Category:   "constraint",
		Summary:    "Use stdlib testing only. No testify, no ginkgo. Table-driven tests preferred.",
		Importance: 8,
		Tags:       []string{"testing", "constraint"},
	},
	{
		EventID:    "testify_mention",
		Category:   "correction",
		Summary:    "Saw testify imported; should be removed. Per stdlib_testing constraint.",
		Importance: 5,
		Tags:       []string{"testing", "correction"},
	},

	// --- Noise / negative-match fixtures ---
	{
		EventID:    "unrelated_quantum",
		Category:   "insight",
		Summary:    "Quantum computing is interesting but not relevant to this project.",
		Importance: 2,
		Tags:       []string{"noise"},
	},
}

// SeedFixtures writes CanonicalFixtures to the insights.jsonl backing
// file under contextDir, so a subsequent storage.New(cfg) call with
// cfg.ContextDir == contextDir loads them via the standard JSONL path.
//
// This honors the public storage API: storage.New reads
// <contextDir>/data/insights.jsonl as a sequence of insightRecord
// JSON lines (one per fixture). No internal-only seed helper is
// needed; no parallel write path. Determinism is by construction
// (the JSONL is byte-identical given the same CanonicalFixtures).
//
// Note: the file is OVERWRITTEN — SeedFixtures replaces any prior
// content under the same contextDir. Callers are expected to use a
// fresh temp dir per scenario invocation.
//
// Embeddings are not seeded here. Reflex with embedder=nil falls back
// to text-based scoring per its constructor doc — which is sufficient
// for the legacy/cognition scenarios that score against EventID +
// text-match (not semantic similarity). Reflex's text-based path is
// what runs against these fixtures.
func SeedFixtures(contextDir string) error {
	dataDir := filepath.Join(contextDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("seed fixtures: mkdir %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, "insights.jsonl")
	f, err := os.Create(path) // truncates if exists
	if err != nil {
		return fmt.Errorf("seed fixtures: open %s: %w", path, err)
	}
	defer f.Close()

	// Use a stable base time so different runs produce identical files
	// (determinism per eval-principles 4). Fixtures are spaced 1s apart
	// in importance-descending order, so recency boost matches importance
	// loosely — useful for scenarios that test recency-vs-importance.
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	enc := json.NewEncoder(f)
	for i, fx := range CanonicalFixtures {
		rec := map[string]any{
			"id":          int64(i + 1),
			"event_id":    fx.EventID,
			"category":    fx.Category,
			"summary":     fx.Summary,
			"importance":  fx.Importance,
			"tags":        fx.Tags,
			"reasoning":   fx.Reasoning,
			"source_type": "fixture",
			"created_at":  base.Add(time.Duration(i) * time.Second),
		}
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("seed fixtures: encode %s: %w", fx.EventID, err)
		}
	}
	return nil
}

// SeedFixturesInto is a convenience wrapper that seeds fixtures into
// the contextDir and then opens a *storage.Storage rooted there.
// Callers that need both steps can use this; callers that want the
// JSONL on disk for inspection should use SeedFixtures + their own
// storage.New() call.
func SeedFixturesInto(contextDir string, openStorage func(string) (*storage.Storage, error)) (*storage.Storage, error) {
	if err := SeedFixtures(contextDir); err != nil {
		return nil, err
	}
	return openStorage(contextDir)
}

// FixtureByID returns the canonical fixture with the given EventID,
// or nil if not present. Useful for runners that want to validate a
// scenario's expected_result_ids reference real fixtures before
// dispatching.
func FixtureByID(eventID string) *Fixture {
	for i := range CanonicalFixtures {
		if CanonicalFixtures[i].EventID == eventID {
			return &CanonicalFixtures[i]
		}
	}
	return nil
}

// AllFixtureIDs returns every canonical EventID. Useful for the
// legacy runner's pre-flight check (verify scenario references
// known fixtures before attempting to seed).
func AllFixtureIDs() []string {
	out := make([]string, len(CanonicalFixtures))
	for i, f := range CanonicalFixtures {
		out[i] = f.EventID
	}
	return out
}
