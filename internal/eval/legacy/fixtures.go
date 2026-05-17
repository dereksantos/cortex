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
		Summary:    "Use JWT with 24h expiry. Bearer scheme. Do NOT support session cookies.",
		Importance: 9,
		Tags:       []string{"auth", "jwt", "security"},
		Reasoning:  "JWT integrates with our stateless API; sessions create scaling pain.",
	},
	{
		EventID:    "auth_module",
		Category:   "pattern",
		Summary:    "Auth module handles login, token issuance, and session validation.",
		Importance: 8,
		Tags:       []string{"auth", "module"},
	},
	{
		EventID:    "jwt_handler",
		Category:   "pattern",
		Summary:    "JWT validation via Authorization: Bearer <token> header. Use HS256 signing.",
		Importance: 8,
		Tags:       []string{"auth", "jwt", "header"},
	},
	{
		EventID:    "token_validation",
		Category:   "constraint",
		Summary:    "All requests except /health and /metrics require valid Bearer token.",
		Importance: 9,
		Tags:       []string{"auth", "middleware", "constraint"},
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
		Summary:    "PostgreSQL for prod, SQLite for local dev. Migrations via golang-migrate.",
		Importance: 9,
		Tags:       []string{"db", "schema", "decision"},
		Reasoning:  "Local SQLite avoids docker dependency; prod PG for write scale.",
	},
	{
		EventID:    "db_connection",
		Category:   "pattern",
		Summary:    "Use database/sql with pgx driver. Pool sized to 4× CPU cores.",
		Importance: 7,
		Tags:       []string{"db", "connection"},
	},
	{
		EventID:    "db_pool",
		Category:   "pattern",
		Summary:    "Connection pool: max=32, idle=8, max_lifetime=5m. Configure at startup.",
		Importance: 6,
		Tags:       []string{"db", "pool"},
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

// SeedFixtures loads CanonicalFixtures into the given storage.
//
// Status: stub (returns nil). Implementing this is genuine multi-hour
// work — see the design constraint below.
//
// Design constraint surfaced during Phase B work:
//   storage.Storage has NO public Insight-insertion API. The internal
//   `insights` map + `addInsightToSecondaryIndexes` are populated by
//   `recordToInsight(r)` calls inside the JSONL-loading code path.
//   The intended-by-design flow is:
//      event captured → ingested → LLM-analyzed → Insight derived
//   not direct fixture insertion.
//
// Two paths the implementing session must choose between:
//
//   (A) **Go through the ingest pipeline.** For each fixture:
//       construct an events.Event with the fixture content, call the
//       capture API, run analyze. Honors the storage API; produces
//       real Insights via the real path. Cost: requires LLM available
//       (for analyze), slow (per-fixture LLM call), and the analyzed
//       Insight may not match the canonical fixture EventID/text
//       exactly (the analyzer transforms content).
//
//   (B) **Bypass with an internal-only seed helper.** Add an internal
//       `seedInsight` method to storage.Storage (or expose
//       recordToInsight + index-add as a test-helper API) that
//       inserts a constructed Insight directly. Faster + deterministic
//       but creates a parallel write path the storage maintainers
//       must keep in sync.
//
// Recommended: (B) with a clear "test-only" marker. The fixtures are
// for eval verification, not production traffic; bypassing the
// ingest pipeline is justified by the determinism requirement
// (eval-principles 4: Reproducible — running the suite twice must
// produce byte-identical output).
//
// Implementing session: see docs/prompts/loop-phase-b-legacy-cognition.md.
// Carry-forward: this design decision is non-trivial; surface to
// the user before committing to (A) or (B) since it adds API surface.
func SeedFixtures(store *storage.Storage) error {
	// STUB. See doc comment above for the design constraint.
	return nil
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
