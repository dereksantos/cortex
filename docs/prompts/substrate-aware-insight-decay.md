Goal: When the substrate an insight was derived from changes, the insight's
retrieval score decays. Stale insights ("we use jQuery for X") stop
dominating retrieval after the codebase has moved on.

PREREQUISITE: ABR measurement (see docs/prompts/measure-abr.md). Decay is
hard to validate without a quality metric — you want to see ABR rise or
noise drop as decay kicks in.

WHY THIS MATTERS: The O2 slice (see docs/journal-design-log.md) wired
ProjectSource and GitSource to emit `observation.project_file` and
`observation.git_commit` entries. Each observation captures a substrate URI
+ content_hash. But nothing currently CONSUMES these — they're a primitive
without a customer. The natural consumer is decay: when an observation's
content_hash for a given URI changes, insights derived from the old hash
were derived from substrate that no longer exists in its observed form.
Their score should decay accordingly.

This directly attacks lift: lower noise in retrieval → context Cortex
injects is more likely to be relevant → user gets fewer false positives.

INVESTIGATE FIRST:
- internal/journal/observation.go and internal/cognition/sources/ for how
  observations are emitted today.
- internal/cognition/dream.go's `emitInsightToJournal` — does the insight
  entry carry its source URI / content_hash? If not, that's the first
  schema add.
- internal/storage/storage.go for the insights table schema. Add a
  `source_uri` and `source_content_hash` column if missing (migration
  pattern is in persist.go).
- internal/cognition/reflex.go for the ranking function. The decay factor
  goes here.

DEFINITION OF DONE:
1. Dream insights (entry type `dream.insight`) carry `source_uri` and
   `source_content_hash` in their payload (omitempty for old entries).
   Dream populates these when emitting.
2. The insights table projection in internal/storage gains the new columns
   and the projector populates them.
3. A new method `Storage.LatestContentHash(uri string) (string, bool)`
   reads the most recent `observation.*` projection for the URI.
4. Reflex's scoring applies a decay multiplier:
   - score *= 1.0 if insight.SourceContentHash == latest hash for the URI
     (or if the URI is unobserved)
   - score *= decay_factor if hashes differ (start with 0.5, document the
     choice; make it tunable via config)
5. Insights with NO source_uri (legacy entries) get full weight — never
   penalize unknown.
6. Tests:
   - Table-driven for the decay function in Reflex (synthetic insights,
     synthetic observations, assert score multipliers).
   - Integration test: write an observation with hash A, an insight with
     hash A, then a second observation with hash B for the same URI;
     assert Reflex returns the insight with reduced score.
7. Run the eval suite (after measuring baseline ABR) and report whether
   ABR shifts. If it doesn't, that's information — the eval scenarios
   may not exercise stale-substrate cases.

CONSTRAINTS:
- Decay factor is a single config knob (e.g., cfg.Modes.Reflex.StaleDecay).
  Default 0.5. Document the rationale.
- Don't delete stale insights — decay them. Deletion is irreversible;
  decay is a soft signal that lets newer evidence win.
- Don't expand to git commit decay yet — file-substrate-based decay is
  enough scope for one slice.
- Standard library testing only.

DELIVERABLE: branch off main, commit schema + projection + ranking + tests
+ ABR re-measurement, report whether the noise reduction was measurable.
Honest report: "ABR moved from 0.X to 0.Y on the scenario set; the win is
driven by [decayed_insight_count] insights being scored ~half" — or, if no
movement: "ABR unchanged; suspect the eval scenarios don't have
stale-substrate cases; here's what would test it properly."
