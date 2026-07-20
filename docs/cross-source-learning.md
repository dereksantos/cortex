# Cross-source learning — slice 2: the TOGETHER

> **Status: DESIGN (2026-07-19), partially BUILT (2026-07-20).** Slice 1
> ([`docs/learning-loop.md`](learning-loop.md), gate GREEN 2026-07-19) proved
> background learning within one project — a bounded `Learn` subagent reads
> a project's own journal and writes notes into that project's own
> `.cortex/memory/`. Its own "Out of scope" section named this doc
> explicitly: *"Cross-source / cross-project connection... slice 2, its own
> doc, only if this slice's gate goes green and a consumer is named for it
> too."* Gate is green; this doc names the consumer and specs the mechanism.
>
> Landed so far (Δ gate green, `cmd/cortex/learn_user_eval_test.go` +
> `internal/tools/learn_user_test.go`): slice **2a** (the user-tier store +
> injection + shadowing + manual `scope` hatch) and **2b** (the serve/capture
> gap fixes) shipped 2026-07-19 — see `docs/memory-tools.md`'s "User-tier
> memory — piece 1 shipped" note. **2c's promotion half** (deterministic
> candidate detection + the `LearnUser` subagent + `Spec.Scope`/
> `roleLearnUser` plumbing + `cortex learn --user`) and **2d's promotion
> un-link rule** (Δ4) shipped 2026-07-20 — `cmd/cortex/learn_user.go`. Still
> open: 2d's OTHER half (`Learn`/`LearnUser` writing `[[links]]` in the first
> place — no prompt extension landed, so there is nothing live to un-link
> yet beyond what a human writes by hand via `memory_write`), the `loop.run`-
> as-a-second-input-class idea floated in piece 3 item 3 (LearnUser today
> reads ONLY other projects' memory indices, no cursor, as decision 4
> describes — it does not yet notice cross-project *loop-failure* patterns),
> piece 4 (the web dashboard), and the ø gate (G1–G4). See each section
> below for the inline built/not-built marker.

Derek's framing (2026-07-19): the web track "can bring together context over
time from many sources and learn from it and connect it all together."
Slice 1 built the *learning*. This slice builds the **together**: one fact
learned in project A should be available in project B without re-teaching
it; the sources feeding that learning should widen past one project's own
journal; and a human should be able to see what the harness now knows, and
correct it.

## The consumer (named up front)

Per `think-dream-eval.md`'s "Blocked" row (never repeat `b63a83d` — 18k LOC
wired to the fleet with one narrow consumer, deleted nine days later): two
consumers, both already-existing surfaces that need no new wiring to
*receive* this work, only to be fed by it:

1. **The turn-start memory index injection** (`memoryIndexNote`,
   `docs/memory-tools.md`) — every foreground session in every project
   already reads an index at turn start. A user-tier index prepended to that
   injection reaches every project's sessions for free.
2. **The web dashboard's memory screen** (new, piece 4) — the human side of
   the same surface: what the harness knows, where it came from, and a
   delete button wired to the real `memory_forget` path.

## Code-reality check before designing

Three claims from the brief needed verification; two were wrong:

- **Discord captures already flow through capture.** `discord.go`'s
  `newDiscordSessionFactory` calls `cs.EnableMemory()` explicitly, and
  discord's bot handler runs through the same `SessionManager` → `cs.Turn()`
  path serve uses. `Turn` calls `cs.captureTurn(...)` at
  `cmd/cortex/turn.go:179` unconditionally. Confirmed working.
- **Serve-driven sessions are NOT already captured.**
  `cmd/cortex/serve_session.go`'s `newProductionSession()` — the factory the
  entire `cortex serve` / web UI surface uses — never calls
  `EnableMemory()`. `captureTurn` no-ops when `cs.capturer == nil`
  (`session_runtime.go:107`), and every memory tool needs `cs.memory !=
  nil`. **Every turn run through the web UI today is invisible to the
  journal and to memory.** Discord's factory patches this with one line;
  serve's factory never got it. Piece 3 specs the fix as a prerequisite,
  not a new feature.
- **`web_search`/`fetch_url` results are captured only weakly.**
  `session_runtime.go`'s `turnArtifacts` special-cases
  `FunctionWriteFile`/`FunctionEditFile`/`FunctionBash` for the capture
  summary's outcome line; a web tool call leaves no trace there. It only
  reaches the capture event if the final answer happens to restate it, cut
  to 280 chars. A fact learned from a search is materially harder for
  `Learn` to see than one learned from editing a file. Piece 3 specs the
  minimal fix.

Both gaps land in the same place: slice 1's consumer was correctly named
and works; slice 2's premise — "more sources feed the same learning" —
currently has two intended sources under-wired. Fixing them is in scope
here, because piece 4's dashboard would otherwise render an empty or
misleading picture for any project only ever touched through the web UI.

## 1. User-tier memory

**Store.** `~/.cortex/memory/` — the exact same `internal/memory.Store`
already used per-project, pointed at a different root. `memory.New(dir)`
takes any context directory, no project-specific assumption baked in.
Decision: **no new package** — a second handle (`cs.userMemory`) via
`memory.New(userhome.Path("memory"))`, the same resolution
`registry.New()`/`journal/landscape.go`/`journal/loop.go` already use for
machine-level state. Same file format, same `INDEX.md` regeneration.

**Injection order and caps.** User tier **before** project tier in
`memoryIndexNote()` — a cross-project fact is the more load-bearing one to
keep visible. Capped **independently**:
`limits.user_memory_index_cap_chars` (new, default 1500) vs. the existing
`limits.memory_index_cap_chars` (4000, unchanged). A small user cap is
also a soft precision signal — user-tier notes should stay few (promotion
only fires on ≥2-project recurrence), and if the user index ever needs
4000 chars, promotion has gotten too permissive, visible on the dashboard
before it's visible in a growing prompt.

**Who writes it — the honest mechanism. BUILT 2026-07-20** (`internal/loops.Spec.Scope`,
`cmd/cortex/learn_user.go`'s `RunLearnUserPass`/`detectCandidateGroups`/
`LearnUser`, `cortex learn --user`) — every decision below shipped as
specified, with one implementation choice not pinned by the design: the
harness invokes `LearnUser` **once per candidate group** (not one batched
call across every group) so the mechanical `preparePromotionWrite` step
always knows exactly which source-project list a given `memory_write` call
belongs to (needed for decision 3's provenance line — see its note below).
The per-project `Learn` pass
cannot detect cross-project recurrence; it only ever sees one project's
journal, by design. Decision: a **new loop `Scope`**, not a new `Kind`.
`internal/loops.Spec` gains `Scope` (`""`/`"project"` = today; `"user"` =
this pass) alongside the existing `Kind` — `Kind` picks the engine
(`turn`/`learn`), `Scope` picks what it reads, an orthogonal axis. A
`Kind:"learn", Scope:"user"` firing runs `runLearnUserFiring`
(`cmd/cortex/learn_user.go`, new). CLI mirror: `cortex learn --user`
(iterates the registry instead of resolving one project).

The mechanism follows slice 1's own split — harness decides what it gets
to look at, model decides what's worth saving:

1. **Mechanical, no model.** For every project in `registry.List()`, read
   that project's `.cortex/memory/INDEX.md` and note bodies — the already
   curated per-project notes, not the raw per-project journal (re-scanning
   every project's whole capture history from a user-level pass would blow
   any sane cost budget; the project-tier notes are the distillate that
   scanning already paid for once). Candidate recurrence = keyword/hook
   overlap across ≥2 projects' notes crossing a fixed threshold — plain
   string comparison, no model, Δ-testable. Coarse by design: candidates,
   not verdicts.
2. **Model-driven, one bounded call.** Candidate groups (bounded count and
   chars) go to a `LearnUser` subagent profile — same shape as `Learn`
   minus filesystem tools (it doesn't need `outline`/`grep`/`read_file`,
   only `memory_read`/`memory_search`/`memory_write`) — which decides per
   group whether the notes describe the same fact and, if so, writes one
   synthesized user-tier note. The model, not the overlap heuristic,
   decides worth-saving. (One call PER remaining candidate group, not one
   batched call across every group — see the mechanism's built-status note
   above.)
3. **Provenance is a body convention, not new frontmatter. BUILT, mechanically
   enforced rather than left to the model.** `memory-tools.md` already fixed
   frontmatter to timestamps only. `LearnUser`'s prompt explicitly tells the
   model NOT to write its own `Promoted from:` line; instead
   `cmd/cortex/learn_user.go`'s `preparePromotionWrite` (wired through
   `study.go`'s `dispatcherFor`) appends `Promoted from: <project-a>,
   <project-b>` mechanically from the candidate group the harness already
   knows, to every `memory_write` the profile issues — plain text, fixed
   format, grep-able by the Δ gate (`TestRunLearnUserPassPromotesWithProvenanceLine`),
   and correct regardless of what the live model actually produced (Δ2: "no
   more, no fewer" holds by construction, not by trusting model instruction-
   following).
4. **No cursor for this pass, by design — dedup made deterministic, not just
   model-driven. BUILT.** Unlike per-project `Learn` (cursors a journal
   offset), the user-level pass reads current note state, not an event
   stream — no natural offset. It re-scans every registered project's index
   each firing: cheap by construction (indices are capped via
   `recurrenceMaxNotesPerProject`/`recurrenceMaxNoteChars`, registries are
   expected small). Idempotence is enforced ONE LEVEL MORE MECHANICALLY than
   "check the target before writing" (which is still true — the model also
   sees the user index in its seed): `groupAlreadyPromoted` scans the user
   tier for an existing note whose `Promoted from:` line names EXACTLY this
   candidate group's project set, and skips calling the model at all when
   one exists — the re-run test (`TestRunLearnUserPassDedupSkipsAlreadyPromotedGroup`)
   asserts zero additional model round-trips, not just zero additional
   notes. A registry large enough to make full-rescan expensive is a sizing
   problem for later (flagged in Sizing, not solved here).

**Manual escape hatch: yes.** `memory_write`/`memory_read`/`memory_search`/
`memory_forget` gain an optional `scope` argument (`"project"` default,
`"user"`). A session told "remember this for every project" doesn't wait
for promotion — the model just calls the tool with `scope: "user"`. Same
natural-language-to-tool pattern `memory-tools.md` already established.

**Read resolution — project shadows user.** `memory_read(name)` with no
scope checks project first, falls back to user on a miss; a same-name
collision resolves to the project note silently (a project can locally
override a cross-project fact without deleting the promoted one).
`memory_search(query)` returns hits from **both** tiers, tagged
`[project]`/`[user]`, so a collision is visible in search even though
direct read hides it.

## 2. Connections

**`[[note-name]]` wiki-links, lightweight only.** Plain text inside a
note's body — no new file, no edge index, no store schema change.
Maintained by extending `Learn`/`LearnUser`'s existing prompt (it already
checks the index before writing — add "if this fact relates to an
existing note, link it with `[[note-name]]`"). The foreground coder can do
the same by hand via `memory_write`.

**Resolved at read time — names only, no auto-hop.** `memory_read` renders
the note verbatim, links as plain `[[name]]` text; it does not fetch the
linked note. Auto-following a link is a retrieval-policy decision, and
`memory-tools.md`'s whole bet keeps such decisions with the model. The
model reads the pointer and decides whether to `memory_read` it next —
the same shape as the index injection itself.

**Search already covers links for free.** `Store.Search` substring-matches
full note bodies, so a query matching a linked note's name already
surfaces the linking note too — no separate link index needed.

**Explicitly rejected for now:** embeddings-backed relation search, a
graph/edge database, multi-hop auto-resolution. The dormant embedder
substrate (`resolveEmbedder`, kept per `memory-tools.md`) remains the
evidence-gated door — revisit only if keyword search plus wiki-links
measurably misses real connections.

**Links across promotion. BUILT 2026-07-20** (`cmd/cortex/learn_user.go`'s
`unlinkAbsentTargets`, wired into `preparePromotionWrite` — the same
promotion write path decision 3 above appends provenance in). A promoted
project note may link (`[[local-only]]`) to a note that never got promoted.
Decision: **promotion drops (un-links) any wikilink whose target isn't
present in the destination tier** — the text survives as plain words, not
brackets. A bracketed reference the reader's tier can't resolve is worse
than none. Mechanical and deterministic (check target existence — against
the user tier's CURRENT `Store.List()`, including any note the same
promotion pass already wrote — before writing); the Δ gate's link-integrity
invariant is exactly this check
(`TestRunLearnUserPassPromotionUnlinksAbsentTarget`,
`TestUnlinkAbsentTargetsStripsOnlyUnresolvableLinks`). NOT built: the OTHER
half of piece 2 (extending `Learn`/`LearnUser`'s prompts to actually WRITE
`[[name]]` links in the first place, "if this fact relates to an existing
note, link it") — this un-link rule fires on any wikilink a note happens to
contain (including one a human wrote by hand via `memory_write`), but
nothing yet prompts a background pass to add one.

## 3. Multi-source intake

**Two real gaps, one structural generalization.**

1. **Fix: `newProductionSession()` calls `EnableMemory()`.** One line,
   `cmd/cortex/serve_session.go`, mirroring `discord.go`. Without it,
   pieces 1 and 4 read/display a stream the web surface never populates —
   a prerequisite fix, not new capability.
2. **Fix: `turnArtifacts` records web tool calls.** Extend the existing
   switch (`session_runtime.go`) to also record `FunctionWebSearch`
   (`searched: <query>`) and `FunctionFetchURL` (`fetched: <url>`) into the
   same outcome line already appended to the capture summary. No new
   journal class or event type — the capture event just stops being blind
   to two of the coder's read-only tools. This is the minimal capture
   addition the brief asked to spec, confirmed necessary above.
3. **Structural: cursor generalized to (source-class, scope). PARTIALLY
   BUILT 2026-07-20 — the SCOPE half only, via `internal/loops.Spec.Scope`
   (see the "Who writes it" section above) + `loop_run.go`'s
   `RunLoopFiring` branching a `Kind:"learn"` spec to `runLearnFiring`
   (`Scope` unset/`"project"`) or `runLearnUserFiring` (`Scope:"user"`) —
   both share the exact same `finalizeLoopFiring`/`journal.AppendLoopRun`
   choke point every other loop firing already uses, confirmed still
   user-level per its own doc comment
   (`TestRunLoopFiringLearnUserScopeRunsPromotionPassAndJournals`). NOT
   built: `learnCursorDir`/`captureClassDir`'s own generalization to an
   explicit `(class, scope)` parameter, and — the actual payoff this item
   describes — `LearnUser` reading the `loop.run` journal as a SECOND input
   class at all.** `LearnUser` as shipped has no cursor and no journal
   input whatsoever (per decision 4 above, "no cursor for this pass, by
   design" — the ENTIRE mechanism, not just the note-index half): its only
   input is other projects' current memory-note state, mechanically
   re-scanned every firing. Noticing cross-project *loop-failure* patterns
   ("the daily blog-sync loop has failed three firings running") is real,
   useful, and NOT built — it needs its own cursor (this item's original
   motivation) since `loop.run` is a genuine event stream, unlike the
   note-index scan. Left for a follow-up slice once this one has live
   receipts to justify the added surface (mirrors open question 5's
   "ship notes-only first" posture, extended to this second class).

**Bounds. BUILT.** `LearnUser` gets its own `subagents.learn_user` config
section — same three fields (`max_tokens`/`max_iter`/`read_budget_bytes`)
as `subagents.study`/`agent`/`learn`, same `Config.subagentBounds`
mechanism keyed off a new `roleLearnUser` constant (`cmd/cortex/config.go`).
Model: the `study` role binding, same rationale as `Learn` — a background
pass has no live coder model to inherit
(`TestSubagentRequestLearnUserProfileStaysOnStudyBinding`). Defaults tuned
lower than `Learn`'s (4096/8/32000 vs. 8192/12/96000) since one candidate
group's notes is a smaller seed than a whole capture-window digest — see
`docs/configuration.md`'s `subagents.*` table.

## 4. The web surface as reader

Within the existing `webui_*.go` (Go view-model, golden-tested) +
`serve_*.go` (HTTP handler) + `go:embed` no-build-JS convention already
used by the dashboard/landscape/loops screens:

- **`GET /api/memory`** — per-project note lists (name, hook, updated) for
  every registered project plus the user tier, composed the way
  `buildDashboardViewModel` already composes registry + sessions. Names/
  hooks/timestamps only — no full bodies in the list view, matching the
  landscape screen's names-only privacy stance.
- **`GET /api/memory/{scope}/{name}`** — one note's full body, with
  `[[links]]` rendered as clickable in-app links (client-side URL template
  over the name, resolved same-tier-by-default — no server-side link
  resolution needed).
- **Recent learning activity** — reuse `loops.JournalRunHistory` filtered
  to `Kind:"learn"` firings, the same pattern `webui_loops.go`'s
  `loopView` already applies; "notes written per firing" comes straight
  from `LearnResult.Report()`'s existing string.
- **`DELETE /api/memory/{scope}/{name}`** — the human correction loop,
  wired to the real `MemoryForget`/`Store.Forget` path, gated behind the
  same `hostOriginMiddleware` every `/api/...` route already requires. The
  one place a human, not a model, retracts memory directly — matching
  `memory-tools.md`'s stance that retraction judgment stays out of
  `Learn`'s own toolset.

No new screen-framework concept — the fourth instance of the dashboard/
landscape/loops pattern, not a new one.

## The gate

Same discipline as `learning-loop.md`: Δ (deterministic, no model) first,
ø (live, gated) only after Δ is green.

### Δ — deterministic

| # | Invariant | Status |
|---|---|---|
| Δ1 | **Promotion is deterministic up to the model call.** Fixed project-note fixtures → the candidate-detection step (overlap across projects) produces the same candidate groups every run; only the `LearnUser` call itself is scripted-`Sender`. | **GREEN** — `TestDetectCandidateGroupsThresholdBoundary`, `TestDetectCandidateGroupsDeterministic`, `TestRunLearnUserPassNoCandidatesMakesNoModelCall` (zero-candidate short-circuit, scripted sender proves zero requests). |
| Δ2 | **Provenance line present and correctly attributed.** Every user-tier note from a scripted `LearnUser` pass carries `Promoted from: <projects>` naming exactly the source projects — no more, no fewer. | **GREEN** — `TestRunLearnUserPassPromotesWithProvenanceLine` (mechanically enforced, not model-trusted — see decision 3's built-status note above). |
| Δ3 | **Shadowing resolves project-first.** A project note and a user note sharing a name: `memory_read(name)` (no scope) returns the project body; `memory_search` surfaces both, tagged by tier. | **GREEN**, landed with piece 2a (2026-07-19), unchanged by this slice. |
| Δ4 | **Link integrity across promotion.** A project note linking `[[local-only]]`, where `local-only` isn't promoted: the promoted note no longer contains `[[local-only]]` in bracket form. A link whose target *is* present in the destination tier survives unchanged. | **GREEN** — `TestRunLearnUserPassPromotionUnlinksAbsentTarget`, `TestUnlinkAbsentTargetsStripsOnlyUnresolvableLinks`. |
| Δ5 | **Per-source cursors are independent.** Advancing the project-scope `capture` cursor doesn't move the user-scope `loop.run` cursor or vice versa; zero new `loop.run` entries since cursor ⇒ no model call. | **NOT APPLICABLE YET** — `LearnUser` doesn't read `loop.run` at all in this slice (piece 3 item 3's cursor-generalization half is unbuilt; see that section). `TestRunLoopFiringKindLearnProjectScopeStillUsesLearnEngine` covers the adjacent regression (Scope unset must still route to the existing project-scope `Learn` cursor, unaffected). |
| Δ6 | **Local-only.** `journal.AssertLocalOnly` holds for the `loop.run` reader and the user-tier store's write path — no outbound path, carried over from slice 1 and `CLAUDE.md`. | **HOLDS BY CONSTRUCTION** — `learn_user.go` introduces no network/export code path at all (registry + `internal/memory` are both local-filesystem-only); nothing new to assert against. |

### ø — agentic, live-gated (`CORTEX_LIVE_FLEET=1`)

**Scenario — the cross-project needle.** A fact is planted once in
project A's session transcripts, off-task the same way slice 1's
NEEDLE-A was (foreground never `memory_write`s it). No session in project
B ever sees it. A user-level `LearnUser` pass runs. A **fresh session in
project B** is then asked the question only that fact answers.

- **ARM-CONTROL** — no user tier: project B has only its own project-tier
  memory, which never saw the fact.
- **ARM-USER-TIER** — identical, but `LearnUser` has run first and project
  B's turn-start injection includes the user index.

| # | Gate | Decided by |
|---|---|---|
| G1 | **Cross-project lift.** ARM-USER-TIER answers the project-B probe correctly (or via `memory_search`) strictly more often than ARM-CONTROL, n≥3. | machine, n≥3 |
| G2 | **Promotion precision floor.** Over a fixed multi-project NOISE fixture, the user tier gains ≤2× the gold cross-project facts actually planted — over-promoting project-local noise defeats piece 1's shadowing/cap design. | machine (note count vs. gold) |
| G3 | **Bounded cost.** Total `LearnUser` wall-clock + token spend for one pass over the fixture registry stays under a pinned budget. | machine (budget accounting) |
| G4 | **Named consumers actually change.** The dashboard's `GET /api/memory` includes the promoted note, and project B's turn-start seed (scripted-`Sender`) includes the user index line — both named consumers actually receive the artifact. | machine (response/seed inspection) |

## Sizing

| Slice | Scope | Size | Status |
|---|---|---|---|
| 2a | User-tier store + injection + shadowing + manual `scope` hatch (piece 1 minus promotion) | S | **DONE** (2026-07-19) |
| 2b | Serve/capture gap fixes: `EnableMemory()` on serve, `turnArtifacts` web-tool recording | S — do first; 2c/2d are pointless without it | **DONE** (2026-07-19) |
| 2c | `LearnUser` promotion: candidate detection + subagent + `Scope`/`roleLearnUser` plumbing + `loop.run` cursor | M | **PARTIAL** (2026-07-20) — candidate detection + subagent + `Scope`/`roleLearnUser` plumbing DONE (`cmd/cortex/learn_user.go`); the `loop.run` cursor (LearnUser reading loop-failure patterns as a second input class) is NOT built — see piece 3 item 3's built-status note |
| 2d | Wiki-links: prompt extension + promotion un-link rule | S | **PARTIAL** (2026-07-20) — the promotion un-link rule (Δ4) DONE; the prompt extension (`Learn`/`LearnUser` actually writing `[[links]]`) NOT built |
| 2e | Dashboard memory screen: list/read/delete + recent activity | M | not started (owned separately — the web dashboard) |
| 2f | Δ + ø gate | M | Δ green for everything landed above (see the Δ table's Status column); ø not run |

Order: 2b → 2a → 2d → 2c → 2e → 2f. 2b unblocks everything else being
meaningful; 2a/2d are cheap and mostly independent of 2c; 2c carries the
real design risk (threshold tuning); 2e can build against 2a alone and
backfill once 2c exists; 2f needs all of the above.

## Out of scope

- **Embeddings-based memory search** stays evidence-gated, per
  `memory-tools.md`'s existing decision — not reopened here.
- **No auto-ingestion of arbitrary user files.** The user tier is written
  only by `LearnUser`'s promotion and the manual `scope:"user"` hatch —
  never a filesystem sweep beyond what `internal/landscape` already does
  (names/paths, not content).
- **Nothing leaves the machine.** `journal.AssertLocalOnly` extends to the
  user tier and `LearnUser`'s registry reads — every project it touches is
  a local path from the local `projects.json`.
- **No relation database, no graph UI.** Piece 2 is text links in
  markdown, full stop — a node/edge visualization is a presentation layer
  over the same data, buildable later without touching storage.
- **No cross-machine sync.** Registry, both memory tiers, both journals
  stay single-machine, as today.
- **No auto-merge into project-tier notes.** Promotion only adds to the
  user tier; it never edits a project's own notes.

## Open questions for Derek

1. **Candidate-recurrence threshold. DECIDED conservatively, per the
   recommendation below, 2026-07-20.** `cmd/cortex/learn_user.go`'s
   `recurrenceMinSharedTerms = 3` (two notes from different projects must
   share ≥3 distinctive terms, each ≥`recurrenceMinTermLen` (5) runes,
   lowercase alphanumeric — a shared "the"/"test" doesn't count) — a
   placeholder starting point per its own doc comment, explicitly flagged
   to tune from real `LearnUser` receipts once the ø gate has live data, not
   from priors. Piece 1's overlap check needs a concrete threshold before
   Δ1 is testable. Recommendation: start conservative (likely
   under-promoting) since G2's precision floor punishes over-promotion
   harder than a missed lift punishes G1 — tune from real `LearnUser`
   receipts, not priors.
2. **Registry-scale ceiling for the no-cursor design.** Full-rescan-every-
   firing is fine at today's registry sizes. Recommendation: ship as
   designed; revisit only if `LearnUser`'s own G3 measurement shows it's
   already a problem.
3. **Same-tier-by-default link scoping. DONE per the recommendation,
   2026-07-20** — `unlinkAbsentTargets` ships exactly the un-link rule
   (deterministic, testable — Δ4); no `[[user:name]]` explicit cross-tier
   prefix syntax was implemented, per "leave it unimplemented until a real
   note needs it." This doc assumes a link resolves within its own note's
   tier unless explicitly prefixed (`[[user:name]]`), and promotion
   un-links unpromoted cross-tier targets.
4. **Dashboard delete scope for shadowed notes.** Same-name project/user
   notes: show as two tier-tagged rows, or only the shadowing (project)
   one with a note that a user-tier note exists underneath?
   Recommendation: two rows, tier-tagged — matches what `memory_search`
   already shows the model, so the human surface never shows less.
5. **Should `LearnUser` ever read raw `capture`, not just curated
   indices?** This doc keeps it off raw per-project capture entirely
   (cost). If real promotion misses turn out to be facts that never made
   it into any project's own memory, that changes. Recommendation: ship
   notes-only first; let the ø eval's G1 lift number answer this
   empirically before adding raw-journal reads.
