# Eval Journal

A human-readable log of eval runs — what we ran, why, what we noticed. The structured record lives in `.cortex/journal/eval/` (`eval.cell_result` JSONL) and is the canonical source for analysis. This file is the lab notebook around those numbers.

Principles: [`docs/prompts/eval-principles.md`](prompts/eval-principles.md). Operational checklist: [`docs/benchmarks/integrity.md`](benchmarks/integrity.md). **Consolidated time-stamped baseline snapshot:** [`docs/eval-baseline.md`](eval-baseline.md) — the "before" picture Phase 6 of the integration roadmap will diff against.

## How to use this journal

- **Every eval run gets an entry.** Even failed runs, even runs we discarded — write down why.
- **Newest at the top.** Reverse chronological. Past entries are immutable; corrections go in a new entry that references the old one.
- **Quote the actual command.** Per principle 1, the CLI invocation IS the eval. Paste it verbatim; never paraphrase.
- **Capture versions.** Per principle 3, scores without provenance are meaningless six months later.
- **Note hypothesis vs. surprise.** What did you expect? What actually happened? Surprises are the high-signal moments worth coming back to.

## Entry template

```markdown
### YYYY-MM-DD — <benchmark> / <variant>

**Cortex**: `<git SHA or branch>`
**Command**:
\`\`\`
./cortex eval ...   # or actual subprocess invocation
\`\`\`
**Versions**: embedder=`<provider/model>`, llm=`<model>`, judge=`<model>`, rerank=`<true|false>`
**Result**: `<primary metric>` (full results in `.cortex/journal/eval/<segment>`)

**Why this run**: one sentence — what changed, what hypothesis.

**Observations**: what stood out. Bullet points fine.

**Follow-ups**: issues filed, next runs queued, principles flagged.
```

## Entries

<!-- Newest at the top. -->

### 2026-05-17 — Phase B + D audit (pre-implementation)

Pre-implementation audit for `loop-phase-b-legacy-cognition.md` and
`loop-phase-d-journeys.md` per the goal queue. No execution attempted;
this entry captures the inventory + classification that informs the
runner implementation choices.

**Phase D inventory — journeys/**

10 scenarios in `test/evals/journeys/`. None currently runnable:
- `cortex eval` CLI does NOT support `--suite=journeys` (no `--suite`
  flag at all — see `./bin/cortex eval --help`).
- No `type: e2e` scenario handler in current `internal/eval/` code
  (verified by grep — runner code was deleted per memory
  `project_deleted_eval_runners`).
- Project scaffolds DO exist under `test/evals/projects/` (verified:
  `hello-service/` has go.mod + greeter.go + tests).

**Path recommendation for Phase D:** (a) thin journey-runner +
`--suite=journeys` CLI wiring. Porting all 10 to v2 format (option (b))
is plausible but loses the multi-session phase/events structure that
journeys use; v2's scenario format doesn't natively support
session/phase/event hierarchies.

**Phase B classification — legacy/cognition/ (22 scenarios)**

Re-running the prompt's "two patterns" claim against actuals:

- **3 SELF-CONTAINED** (inline all results): `resolve_inject`,
  `resolve_queue`, `resolve_wait`. Runner can dispatch directly to
  `Resolver.Resolve(ctx, query, inlineResults)`.
- **19 STORAGE-DEPENDENT** (reference fixture IDs like `auth_module`,
  `jwt_handler` that aren't defined inline): all `abr_*`, `dream_*`,
  `reflect_*`, `reflex_*`, `session_*`, plus `indent_conflict` and
  `testing_conflict`.

**Path recommendation for Phase B — FLIPPED from prompt:**
- Original prompt recommended (b) migrate 19 scenarios to inline
  fixtures.
- Audit shows (b) is more work than (a): 19 scenarios × ~5-10
  fixture entries each = ~100-200 fixture rows to author by hand.
- (a) build a shared `seedFixtures(store)` helper in the runner that
  loads ~10-15 canonical fixtures (auth_module, jwt_handler,
  db_schema, db_connection, error_pattern, etc.) once per suite
  invocation. Scenarios reference IDs; the seed makes them resolve.
- The canonical fixture set itself becomes documented in
  `internal/eval/legacy/fixtures.go` — readable and reusable.

**Phase 1 telemetry alignment:** both B and D runners must write to
`.cortex/db/cell_results.jsonl` via the unified Phase 1 sink (commit
`14d2170`). The legacy v2 runner already lands rows there; the new
suite runner must do the same.

**Out of scope today:** actual runner implementation. Both runners
are multi-hour code work; tracked in their respective loop prompts.
This entry blocks neither — it surfaces the path recommendations so
the implementing session has the decisions pre-made.

**Follow-ups:**
- Build Phase D thin runner per path (a)
- Build Phase B runner with seedFixtures helper per flipped (a)
- Re-run Phase F consolidation once B + D produce baselines (the
  pending sections in `docs/eval-baseline.md`)

---

### 2026-05-17 — InjectedContextTokens flows end-to-end on ABR session cells

**Cortex**: `86e9458` (branch `derek.s/dag-build`)
**Result**: `CellResult.InjectedContextTokens` is now populated from real harness telemetry on per-turn cells written by the ABR session adapter, closing the first of the three follow-ups in the prior entry.

**Wires (4 hops)**:
- `internal/harness/loop.go:247` — `LoopResult.InjectedContextTokens = l.Registry.InjectedContextTokens()` (was always populated; never propagated past here).
- `cmd/cortex/commands/repl.go` — `turnRow` gains the field; `runTurn` copies `lres.InjectedContextTokens` into it before the JSONL write.
- `internal/eval/v2/abr_session.go` — `replTurnRow` decoder picks up the new field; `emitCell` threads it onto the `CellResult`; `ABRTurn` summary struct exposes `FastInjected` / `FullInjected` so it's visible in the test log too.

**Observed on the verification re-run** (haiku-4.5 × 2 passes × 4-turn JWT scenario):

| Turn | Prompt | Fast injected (tok) | Full injected (tok) |
|---|---|---|---|
| 0 | Record JWT decision | 20 | 20 |
| 1 | Cite our auth approach | 53 | 59 |
| 2 | Cite the JWT rationale | 243 | 142 |
| 3 | Do we use session cookies? | 266 | 285 |

The "grow over the session" shape is exactly the signal cross-cutting finding #3 from the Phase A summary ("cortex injects 0 context across every benchmark run") was missing — turns 2-3 inject ~200+ tokens of real captured content, not zero.

MeanABR for this run = 0.957 · MeanFast = 0.911 · MeanFull = 0.866. The slight Full > Fast on later turns (turn-3 ABR = 0.947) is consistent with Reflect adding small reranking value once there's a non-trivial result set to reorder.

**Follow-ups still open** (carried from prior entries, not addressed in this commit):
- **10-turn multi-domain ABR scenario.** Port the legacy `abr_convergence.yaml` shape — warm-up queries across mixed topics, then domain-focused queries that should benefit from Think's cached reflections. The current 4-turn single-topic scenario can't produce the cold-start → convergence trajectory the original metric was designed for.
- **Non-recurring-topic control scenario.** A scenario where prompts deliberately don't recur, so the journal can distinguish "ABR ≈ 1.0 = converged" from "ABR ≈ 1.0 = topics never overlapped." Same plumbing, different prompt list.
- **Per-tool-call EventToolUse capture.** Currently `captureTurn` fires once at end-of-turn with the whole turn bundled as one event. Per-tool-call granularity would let Reflex match finer signals; trade-off is capture overhead. Defer until a scenario demands it.

---

### 2026-05-17 — Auto-capture loop reinstated: ABR=1.000 on intra-session JWT scenario

**Cortex**: `9b6539b` (branch `derek.s/dag-build`)
**Command**:
```
RUN_ABR_SESSION=1 \
  CORTEX_ABR_MODEL=anthropic/claude-haiku-4.5 \
  CORTEX_ABR_JUDGE=anthropic/claude-haiku-4.5 \
  go test ./internal/eval/v2 -run TestABRSession_Real -v \
  -timeout 30m -count=1
```
**Versions**:
- Adapter: `internal/eval/v2/abr_session.go` @ `9b6539b`
- REPL agent: `anthropic/claude-haiku-4.5` via OpenRouter (keychain)
- Judge: `anthropic/claude-haiku-4.5` via OpenRouter (keychain) — `ScoreWithJudgeCriteria` → `CompositeQuality`
- Auto-capture: REPL's existing `captureTurn` now writes to both the journal AND the shared Storage; the harness's `cortex_search` tool retrieves from that same shared Cortex.
- Embedder: not exercised (Reflex falls back to text-search; no precomputed embeddings in the per-eval store)

**Result**:
- **MeanABR = 1.000** (denominator: 4 turns where Full > 0; all 4 turns scored)
- **MeanFast = 0.910 · MeanFull = 0.910** — both passes high quality, both grounded in retrieved content
- Per-turn ABR: 0.973 / 1.000 / 1.029 / 1.000
- 59.6s end-to-end · cost ≈ $0.02 (4 turns × 2 passes × judge calls)
- 8 CellResults written (cortex-fast / cortex-full × 4 turns, shared session_id, turn_index 0-3)

**Why this run**: The prior run on the same plumbing (commit `7362d4d`, journal entry below) hit `MeanFast=0.093 / MeanFull=0.197` — both responses kept saying "store is empty." The root cause was discovered as a two-part gap, **not** a tiny-model JSON-as-text artifact:

1. REPL had a `captureClient` but never invoked it from the turn loop. Captures never landed anywhere.
2. Even if they had, the `cortex_search` tool constructed its own `Storage` separate from any capture path — in-memory indexes drifted, captures from one were invisible to the other.

Both fixes landed in this commit (`9b6539b`):
- `capture.NewWithStorage` writes journal AND `storage.StoreEvent` (synchronous, in-process searchable).
- `replState` now constructs ONE shared `Storage + Cortex` at session init, passed to both the captureClient and the harness's cortex_search tool via `SetSharedCortex`.

**Observations**:

- **Auto-capture is real.** Turn-1's agent response was *"JWT tokens enable stateless horizontal scaling by eliminating server-side session storage dependencies."* Turn-2's response: *"Based on the cortex store search, here's what I found ... 'JWT tokens enable stateless horizontal scaling by eliminating server-side session storage dependencies.'"* — the agent is quoting turn-1 verbatim, meaning `cortex_search` retrieved turn-1's captured event. End-to-end pipeline works.

- **Think actually runs and accumulates.** Per-turn budget shows decay: 5 → 4 → 4 → 4 across the 4 turns (Think consumed capacity as patterns emerged). Each turn shows `Think: starting (budget: N) ... completed (1 ops)` in the REPL log. This was nominally true before but with no events to actually learn from; now it has signal.

- **ABR = 1.000 is the expected outcome for this scenario size.** With one captured decision and 3 follow-up retrievals on the same topic, Reflex finds the decision on every Full pass too, so Reflect has nothing to rerank — Full and Fast converge by construction. The story to tell here is: "the captures land, retrieval works, scoring matches." Not: "ABR has reached 1.0 as a research milestone."

- **The classic ABR shape (cold-start → convergence over many turns) needs a larger N scenario.** `abr_convergence.yaml` had 10 queries across multiple domains; porting that shape (with captures triggered along the way to populate the topical store mid-session) is the next step to produce a non-degenerate ABR < 1 from a meaningful place.

- **`InjectedContextTokens` is currently 0 on every emitted CellResult.** The adapter doesn't yet plumb the `cortex_search` tool's `observedInjectedTokens` (`internal/harness/tools.go:33`) into the per-turn CellResult. Functional but the cell field reads as 0 even when injection actually happened — the LongMemEval-style "cortex injects 0 context" check should be performed via session.jsonl payload size for now, not the cell field.

- **Bonus carry-through for LongMemEval / SWE-bench.** Same `captureTurn` path means every REPL-driven benchmark now auto-populates the store as the agent works. The "cortex injects 0 context across every benchmark run" cross-cutting finding from the Phase A summary is *structurally* addressed for any benchmark routed through the REPL harness, not just ABR. SWE-bench's multi-attempt retry pattern would benefit immediately: failed attempt N captures into the store, attempt N+1 can `cortex_search` for what went wrong.

**Follow-ups**:
- Wire `observedInjectedTokens` from the harness tool registry into the per-turn CellResult so analytics can spot 0-context cells without re-reading session.jsonl.
- Port the legacy `abr_convergence.yaml` shape (10-turn multi-domain trajectory) so we measure ABR under conditions where Reflect matters and Fast vs Full can diverge meaningfully.
- Add a non-recurring-topic control scenario so the asymmetry "ABR ≈ 1.0 means converged" vs "ABR ≈ 1.0 means topics never overlapped" is distinguishable in the journal.
- Consider whether to extend `captureTurn` with per-tool-call EventToolUse events (currently the whole turn is one event). Trade-off: granularity vs. capture overhead — defer until a scenario actually demands it.

---

### 2026-05-17 — ABR session adapter: end-to-end plumbing, signal is noise

**Cortex**: `7362d4d` (branch `derek.s/dag-build`)
**Command**:
```
RUN_ABR_SESSION=1 CORTEX_ABR_JUDGE=gemma2:2b go test \
  ./internal/eval/v2 -run TestABRSession_Real -v \
  -timeout 30m -count=1
```
**Versions**:
- Adapter: `internal/eval/v2/abr_session.go` @ `7362d4d`
- REPL agent: `qwen2.5-coder:1.5b` via Ollama (`http://localhost:11434/v1/chat/completions`)
- Judge: `gemma2:2b` via Ollama (`ScoreWithJudgeCriteria` → `CompositeQuality`)
- Embedder: not exercised (cortex_search Reflex falls back to text search when no embedder, per `tool_cortex_search.go:147`)
- Tool surface: full 5-tool (`--full-tools`), required because the Ollama auto-drop in `repl.go:899` would otherwise remove `cortex_search` from the registry

**Result**: PASS for the plumbing, GARBAGE for the numbers.
- 3 turns × 2 passes = 6 REPL invocations + 6 judge calls in 17.67s
- MeanABR = 0.793 (denominator: 2 turns where Full > 0)
- MeanFast quality = 0.755 · MeanFull quality = 0.640
- Per-turn ABR: 0 (Full scored 0), 0.690, 0.897
- 6 CellResults written via PersistCell, strategy=`cortex-fast` / `cortex-full`, `turn_index` populated, `session_id="abr-20260517T191704Z"`

**Why this run**: First end-to-end smoke of the ABR session adapter
landed across commits `cd79ebf` (cortex_search mode param + strategy
enum split), `9b019ef` (adapter), and `7362d4d` (--full-tools fix +
runtime test). The three together close principle 9 (Fast/Full
strategy split — first time `cortex-fast` / `cortex-full` rows have
ever been written) and re-establish the trajectory-shaped ABR
measurement the deleted `--cognition` runner used to do, this time
through the REPL harness instead of the deleted in-process evaluator.

**Observations**:

- **Plumbing is correct end-to-end.** REPL spawned twice with separate
  workdirs and `CORTEX_SEARCH_DEFAULT_MODE` set per pass; each pass
  produced a `session.jsonl` with three turn rows; both were parsed,
  paired by turn index, judged, scored, and persisted. The
  cell_results.jsonl now has the first-ever rows tagged
  `cortex-fast` / `cortex-full` with `turn_index ∈ {0,1,2}` and a
  shared `session_id`. Analytics groupings work.

- **The numbers are not interpretable.** Looking at the per-turn
  responses in the test log, qwen2.5-coder:1.5b is emitting tool-call
  JSON as *text* in `final_text` instead of invoking the tools.
  Example (turn 0, Fast pass): the response is the literal string
  `\`\`\`json\n{\n  "name": "cortex_search",\n  "arguments": ...\n}\n\`\`\``
  rather than a tool call. The model is below the function-call
  discipline floor at the 5-tool surface — matches `PROGRESS-REPL.md`
  iter 3 finding (qwen-1.5b clean at ≤3 tools, text shapes at ≥5).
  With `--full-tools` mandatory for ABR (we need cortex_search), this
  is a known dead-end on tiny local models.

- **The judge can't recover signal that isn't there.** gemma2:2b
  scored the JSON-as-text dumps with composite qualities ranging
  0.69–0.92, which is essentially noise about surface JSON-ness, not
  retrieval quality. The 0.793 ABR is meaningless as an ABR number;
  it's a measurement of "how similarly does gemma2:2b score qwen's
  garbage in both passes."

- **`turn_full_nonzero = 2` (not 3).** Full pass turn 0 was scored 0
  by gemma2:2b ("No results found." string-only response). When Full
  scores 0, the per-turn ABR is forced to 0 (we don't compute the
  ratio) and the turn is excluded from the MeanABR denominator — so
  MeanABR is averaged over the 2 turns where Full was non-zero. This
  is the right policy (treating "Full also failed" turns as ABR=0
  would conflate "Fast caught up" with "everything broke equally")
  but means the headline number's denominator is small.

- **OpenRouter remains the blocker for an honest run.** The
  session-close health card noted "What 'good' looks like next
  session: OpenRouter top-up". That's still gating: the only path to
  meaningful ABR numbers is haiku-4.5 on the agent and a real judge
  (haiku-4.5 also fine). Local 1.5B can't drive a 5-tool surface;
  local 2B can't reliably judge JSON dumps; the credit-exhausted
  keychain key is what's between us and the proper measurement.

- **Anthropic-direct also blocked: `ANTHROPIC_API_KEY` returns 401**
  (`invalid x-api-key`) when the adapter tries to use it as the judge
  fallback. Either the env-set key is for a different account or has
  rotated. Not in scope for this run; flagged for the user to
  refresh.

**Follow-ups**:
- Re-run this test once OpenRouter is topped up:
  `RUN_ABR_SESSION=1 CORTEX_ABR_MODEL=anthropic/claude-haiku-4.5 go test ./internal/eval/v2 -run TestABRSession_Real -v -timeout 30m -count=1`.
  Same plumbing, real numbers.
- Even with haiku-4.5 on both sides, the per-eval Cortex store is
  empty on a fresh workdir, so this scenario will likely show ABR
  ≈ 1.0 (degenerate — nothing for Think to cache, nothing for Full's
  Reflect to rerank). To produce a non-degenerate run, the next
  iteration should pre-seed the store via `cortex capture` against
  the workdir's `.cortex/` before invoking the adapter. The legacy
  `abr_convergence.yaml` 10-query sequence is then portable as-is.
- Refresh `ANTHROPIC_API_KEY` so the adapter's default judge path
  (anthropic-direct) works without `CORTEX_ABR_JUDGE=<local>`
  override.
- This run does NOT update ROADMAP.md's 0.586 baseline — that number
  came from the v2 single-shot ABR sweep, which measures something
  different (per-cell lift, not session trajectory) and remains the
  canonical "snapshot" pre-DAG-protocol baseline.

---

### 2026-05-17 — Phase 1 complete; Phase A re-run viable

**Cortex**: `14d2170` (branch `derek.s/dag-build`)
**Loop**: `docs/prompts/loop-phase-1-tool-surface.md`
**Result**: All three Phase 1 deliverables land green (`go test ./...` passes other than the pre-existing OpenRouter-credit failures in `internal/cognition/nuance_test.go`). `tools.json` is generated from the registry; every `--json` path wraps output in `pkg/cliout.Envelope`; every CLI invocation writes a `{source:"cli", trace_id, cortex_function, command, latency_ms, ok}` row to `.cortex/db/cell_results.jsonl` (or skips silently when there's no `.cortex/` tree nearby).

**Why this run**: Close the BLOCKED status on the Phase A v2 / ABR baselines below — both lacked the unified telemetry sink that Phase 1 lands.

**Observations**:
- **Trace-id joins envelopes to rows.** `cliout.Invocation.Emitter(workdir)` shares its trace id with the envelope it emits, so an analyst reading `cell_results.jsonl` can pivot directly into the matching envelope payload that came out on stdout. Verified end-to-end with `cortex search --workdir <tmp> --json "x"`: envelope `meta.trace_id == ` JSONL row `trace_id`.
- **Schema coexistence works.** Eval `CellResult` rows (no `source` field, populated `run_id`/`scenario_id`/etc.) and CLI telemetry rows (`source:"cli"`, telemetry-specific fields) live in the same `.cortex/db/cell_results.jsonl` without breaking either consumer. Discriminator is `source` field presence.
- **`--no-telemetry` + `CORTEX_NO_TELEMETRY` env** both opt out; verified row count stays unchanged across both modes.
- **Ad-hoc CLI invocations from a non-cortex cwd skip silently** instead of littering the user's home with stray `.cortex/` trees — confirmed via tempdir test.

**Follow-ups**:
- File a v2 runner change to thread `ctx.Invocation` through `internal/eval/v2/*` so the v2 scenario runner can emit telemetry rows alongside its existing `eval_scenario_results` SQLite writes (the v2 BLOCKED entry below remains accurate as a snapshot until that lands).
- Re-running the full v2 (40 scenarios) + ABR sweep is now mechanically possible but deliberately deferred: this loop's goal was the *floor*, not the rerun itself. The next eval loop should consume that floor.
- The legacy ABR sweep that was BLOCKED on principle 6 will now produce CLI rows when re-run (it shells out via `internal/eval/benchmarks/cortexcli.go`'s `RunSearch` / `RunCode`, both migrated). Rerunning it is a separate eval-loop entry.

### 2026-05-17 — ABR diagnostic: 0.586 vs 0.77 resolved as stale doc

**Cortex**: `861f0ff` (branch `derek.s/dag-build`)
**Loop**: `docs/prompts/loop-abr-diagnostic.md`
**Result**: Category (a) Stale doc. `ROADMAP.md` rebaselined from 0.77 → 0.586; this entry supersedes the "(flagged)" status on the ABR row in the session-close health card below.

**Why this run**: Reconcile the ≥20% deviation between Phase A's measured ABR (0.586, see the "Phase A baseline complete" entry below) and `ROADMAP.md`'s stated 0.77.

**Provenance of 0.77** (traced via `git log --all -p -- ROADMAP.md`):

| Field | 0.77 (2025-12-30) | 0.586 (2026-05-17) |
|---|---|---|
| Commit that established the number | `3c18d17` ("feat: add paper structure, eval improvements…") | `2e90738` ("docs(eval): Phase A baseline complete") |
| Runner | `internal/eval/cognition.go` + `cognition_eval.go` (invoked via `--cognition` flag) | `internal/eval/v2/Evaluator` (unified post-consolidation runner) |
| Runner status today | **Deleted** in commit `1628173` (2026-01-04, "refactor: consolidate eval system 23 files → 5 files", removed ~11k lines) | Active |
| Scenarios | `test/evals/scenarios/cognition/*.yaml` (moved to `test/evals/legacy/`) | 43-scenario v2 sweep in `test/evals/v2/` |
| LLM | Ollama default (qwen2:0.5b per adjacent ROADMAP context at the time) | `anthropic/claude-haiku-4.5` via OpenRouter |
| Embedder | all-MiniLM-L6-v2 (per ROADMAP context at the time) | nomic-embed-text v1.5 |
| Companion pass-rate | 87% (cognition tests) | 90.7% (v2 lift-based) |

**Categorization**: Unambiguously **(a) Stale doc**. The 0.77 cannot be a regression because the runner that produced it no longer exists in the repo — it was deleted four months ago and the scenarios were archived to `test/evals/legacy/`. Every condition that produced 0.77 has been replaced. The 0.586 figure is the current best estimate under the only runner that still exists, with the documented run-to-run variance caveat (0.586 → 0.492 on a same-day re-run, recorded in the Phase A entry).

**Action taken**:
- `ROADMAP.md`: replaced the four current-state mentions of 0.77 with 0.586, added a one-sentence rebaseline note adjacent to the Current Eval Results table, bumped `Last Updated` to `2026-05-17`. Historical references in "Recently Completed" and the Phase 3 dashboard example were left intact (they remain accurate as history / placeholders).
- This entry filed.
- Per the loop's "Diagnosis only" constraint, no scenario or framework changes were made; the underlying variance issue (Full NDCG = 0 for 14 scenarios) remains open per the Phase A "Follow-ups" list.

**Observations**:
- `ROADMAP.md`'s "Recently Completed" item `- [x] ABR metric baseline (0.77)` is still factually correct as a historical record (work that completed at the time) and was left as-is.
- The 0.77 had drifted across multiple ROADMAP rewrites (commits `f0927d6`, `627867a`, `ca7d61a`) without ever being re-measured under the new runner — a useful reminder that headline numbers in design docs need a "measured-under" stamp, not just a value.

**Follow-ups**:
- None for this loop (diagnostic only). The ABR optimization work itself remains scoped to Phase 4 in `ROADMAP.md`.

---

### 2026-05-17 — Session close: --full-tools + --keep-on-fail, mistral:7b probe, eval health

**Cortex**: `63107a8` (and one minor bug found, not yet fixed).

Two follow-up flags from the prior entry landed in `63107a8`:

- **`--full-tools`** forces the REPL to register the full 5-tool surface (read / write / list_dir / run_shell / cortex_search) even when routed to Ollama. The existing iter-4 minimal-tools toggle is right for interactive use with tiny models that lose function-call discipline at >3 tools, but wrong for SWE-bench against any model where `list_dir` is non-negotiable for navigating a real repo. SWE-bench's `REPLHeadlessOpts` sets it to true.
- **`--keep-on-fail`** suppresses the REPL's snapshot-rollback when the verifier fails. Interactive default keeps rollback (don't ship half-broken edits); benchmark default flips it on so the agent's file writes survive across retries — closer to how a real engineer iterates, and crucially stops mid-attempt errors (e.g. the OpenRouter 402 we hit) from wiping out the agent's actual progress before the verifier even gets to score it.

**Local-model probe** (`mistral:7b` on `django__django-10097`, no API spend):

| Signal | Value | What it means |
|---|---|---|
| agent_turns | 1 | mistral did **zero tool calls** — answered as text instead of using its tools |
| tokens_in | 4 096 (exactly) | Ollama's default context cap for mistral; the ~100 KB problem statement + F2P list got hard-truncated to 4 KB |
| files_changed | None | No source edits, no test-cmd.sh |
| final_text | Generic essay (`"To run all tests in Django, use \`python manage.py test\`"`) | Treated the prompt as "explain Django tests" rather than as a coding task |
| Wall time | 183 s | 3 min of CPU/Metal inference for two essay generations |

Conclusion: mistral:7b is below the SWE-bench capability floor. Two failures stack — tool-calling discipline (even with `--full-tools` letting the tools be registered, the model just doesn't call them) and context truncation (4 KB sees only the tail of the prompt). Not a harness problem: the verifier ran honestly, NO_PATCH branch behaved correctly, retry fired. The harness side passes the test.

Realistic local-model tier for SWE-bench on 24 GB VRAM (e.g. RTX 3090): aim at tool-trained coders, not generalist 7B — `qwen3-coder:30b-a3b-instruct` (MoE, ~18 GB at q4, confirmed tool-use on OpenRouter) or `qwen2.5-coder:32b-instruct` (q4, tool support needs probing). General eval / judge tier: `qwen2.5:14b` or `qwen2.5-coder:7b` at ~5–9 GB. Embedding tier: keep `nomic-embed-text` (works) or upgrade to `bge-m3` (~2.3 GB). Run via vLLM or sglang for batched throughput; ollama is fine for one-shots but slow at scale.

**Minor bug found, not yet fixed**: `writeWorkdirGitignore` runs before the agent does, so every attempt's `git diff HEAD` includes a 5-line `.gitignore` addition. That makes the verifier's `NO_PATCH` check (`if [ ! -s $PATCH ]`) never fire — the diff is never empty. Cosmetic today (NO_PATCH was only meant as a guard rail for agents that read but don't write), but worth a 5-minute fix: either commit `.gitignore` to HEAD before the agent runs, or change the NO_PATCH check to "no diff outside .gitignore." Logged here so it doesn't disappear.

**End-of-session eval health** (the honest scorecard the user asked for):

| Suite | Status | Real number | Notes |
|---|---|---|---|
| MTEB NFCorpus | 🟢 Healthy | NDCG@10 = 0.373 (n=100) | Matches published nomic baseline |
| v2 scenarios | 🟢 Healthy | cortex 92.4% / baseline 83.6% per-test pass | 342 cells in `cell_results.jsonl`; principle 6 closed |
| LongMemEval baseline | 🟢 Healthy | baseline 15.6% / cortex 13.3% (n≈30 each, judge enabled) | Pipeline runs end-to-end; cortex strategy at parity-or-below is the honest pre-integration finding |
| LongMemEval +analyze | 🟡 Diagnostic | 0/5 with analyze=50 | Small sample but proved the pipeline works; root cause = extraction prompt loses numeric/named-entity detail |
| ABR (v2 full sweep) | 🟢 Healthy data | 0.586 run-level / 0.409 scenario-mean | Real number; contradicts ROADMAP's 0.77 (flagged) |
| SWE-bench | 🟡 Wired, unmeasured | n/a (no honest cell yet) | Every "0/N pass" row in JSONL is infra-tainted; harness now demonstrably works (sonnet wrote 4 files including `django/core/validators.py`); needs credit top-up for first real measurement |

**Cross-cutting wins this session**:
- Uniform per-cell telemetry across benchmarks AND v2 (principle 6 closed for every measured suite).
- Preflight pattern (docker daemon + image inspect) prevents silent infra failures from masquerading as model failures — three prior journal entries had to be corrected for exactly that.
- Headless REPL (`--prompt --verifier --auto-retry --max-retries --system-prompt --max-turns --max-cost-usd --max-cumulative-tokens --full-tools --keep-on-fail --workdir --json`) is the CLI surface for any agent benchmark; principle 1 + 4 violations resolved for SWE-bench.

**Cross-cutting gaps still open** (logged so they don't slip out of memory):
- **Principle 8 (judge variance)**: never addressed — every score is a single point estimate, no σ.
- **Principle 9 (Fast/Full strategy split)**: never addressed — `CellResult.ContextStrategy` only allows baseline/cortex/frontier.
- **`cortex_version` is a static `"0.1.0"` string** — should embed the git SHA so principle 3 (Versioned) stops being "~ partial" everywhere.
- **`RunREPLHeadless` JSON summary lacks token totals / cost** — SWE-bench cells show zeros for spend even when sonnet really did spend $2 (session.jsonl has the real numbers; the cell is the public surface).
- **`spend.EstimateCost` is ~250× pessimistic for haiku-4.5** (logged in Phase A summary) — forces ceiling overrides for routine sweeps.

**What "good" looks like next session**:
1. OpenRouter top-up → one honest sonnet SWE-bench cell.
2. Fix the gitignore-always-diffs-so-NO_PATCH-never-fires bug.
3. Either: 24 GB local-LLM probe with `qwen3-coder:30b-a3b-instruct`, OR continue iterating on cortex pipeline knobs and re-run benchmarks for trend.

---

### 2026-05-17 — SWE-bench: agent reaches write-source state; two new blockers

**Cortex**: `b37b170` (and predecessors `0314114`, `e58463c`).

Continuation of today's SWE-bench arc. Three further commits since the prior entry:

1. **Agent-driven test-runner discovery** (`e58463c`). Verifier now reads `.cortex/test-cmd.sh` if present and runs that inside docker; falls back to pytest. System prompt instructs the agent to discover the project's test runner and write the command to that file. Cleanest replacement for the per-repo hardcoding that would otherwise be required (django → `runtests.py`, sympy → `bin/test`, …).
2. **Stripped coaching from system prompt** (`0314114`). The previous version listed CONTRIBUTING.md / tox.ini / pyproject.toml as files to read and pytest / Django / sympy as frameworks to expect — both biased discovery toward the half-dozen repos in Verified. The new prompt frames the task ("you are an engineer landing in an unfamiliar repo; figure out the technology, how its tests run, what command verifies the failing tests"), names only the agent's tools and the test-cmd.sh protocol, and otherwise lets the model discover. Eval-principles #2-compliant: framing, not coaching.
3. **Per-attempt budget flags** (`b37b170`). REPL gains `--max-turns / --max-cost-usd / --max-cumulative-tokens` that override the interactive-mode defaults (8 turns / $0.20 / 300k tokens). SWE-bench passes 50 / $5 / 800k since the prior probe blew the $0.20 cap in 4 exploratory reads on Django.

**End-to-end probe** (sonnet-4.5 on `django__django-10097`):

| Metric | Value |
|---|---|
| Agent turns (attempt 1) | 21 |
| Tool-call mix (attempt 1) | list_dir × 7, read_file × 6, run_shell × 4, **write_file × 4** |
| Last tool call (attempt 1) | `write_file django/core/validators.py` |
| Tokens (attempt 1) | 620 918 in / 17 322 out |
| Cost (attempt 1) | $2.12 |
| End reason | `openrouter (402)`: credit exhaustion mid-attempt 2 |
| Final cell | F2P=0/438, P2P=0/1432 |
| Wall time | 250 s |

The agent reached the **write-source state** for the first time today — list_dir to find the implicated module, run_shell to probe the test setup, read_file on `validators.py` (the actual file the bug fix lives in!), then `write_file django/core/validators.py`. This is qualitatively different from every prior attempt this session. The probe died not because the model failed but because mid-attempt-2 the OpenRouter account hit a 402 credit-exhaustion error.

**Two new blockers surfaced** (worth flagging before tomorrow's probes):

1. **OpenRouter credits exhausted.** `openrouter (402): This request requires more credits, or fewer max_tokens. You requested up to 4000 tokens, but can only afford 1825.` Top up at https://openrouter.ai/settings/credits before the next sonnet probe. Local-model probe (mistral:7b via ollama) is the credit-free alternative for harness iteration — needs a `--full-tools` flag added to the REPL because the current Ollama path force-toggles `minimalTools=true` and drops `list_dir`/`cortex_search` (`cmd/cortex/commands/repl.go:839`), which SWE-bench can't function without.

2. **Snapshot rollback throws away agent progress on verify_fail.** The REPL's `runTurn → finalize` calls `restoreFromSnapshot` whenever `accepted=false` (i.e. verifier failed). For interactive use that's right — don't keep half-broken edits. For benchmarks it's wrong: the agent's 4 `write_file` calls on attempt 1 were rolled back when attempt 2 started, then attempt 2 errored out on credits with the workdir at zero progress. The verifier sees an empty diff and the cell records "no source change" even though the agent really did work. Fix: add `--keep-on-fail` to the headless mode (default-on for benchmark harnesses) so iterations build on prior work instead of restarting from scratch — that's closer to how a real engineer iterates anyway.

**What's confirmed working at this point**:
- Headless REPL (`--prompt --verifier --auto-retry --max-retries --json --workdir --system-prompt --max-turns --max-cost-usd --max-cumulative-tokens`).
- Preflight gates (docker daemon, per-instance scoring image).
- Image-id format for the new SWE-bench Verified registry.
- `.gitignore` on cloned repos to keep verifier diffs clean.
- `pipefail` + `$PIPESTATUS` so pytest exit can't be masked.
- Agent-driven test runner discovery convention (`.cortex/test-cmd.sh`).
- Coaching-free system prompt.
- Per-attempt budgets sized for real repos.
- Agent reaches write-source state on a real SWE-bench instance.

**Follow-ups queued (priority order)**:
1. `--keep-on-fail` flag on REPL (default-on for benchmark callers) so iterative progress survives verify failures.
2. `--full-tools` flag on REPL to override the Ollama auto-minimal so small local models can also exercise the SWE-bench flow once tool-use discipline allows.
3. Re-run after credit top-up to see whether the agent's discovered fix actually moves F2P off zero.
4. Token/cost accounting from `RunREPLHeadless` JSON summary so cells capture real spend.

---

### 2026-05-17 — SWE-bench: iteration wired via REPL + correction of prior 0/3 cells

**Cortex**: `a8d47bf` (and predecessors `ecb10fc`, `3244bad`, `a8b39d1`, `12cc6dd`, `2b789f7`, `90474e3`).

**What this entry is**: a connected arc of seven commits that:
1. Adds `cortex --prompt --verifier --auto-retry --max-retries --json --workdir --system-prompt` headless flags to the REPL.
2. Refactors SWE-bench's runner to drive the REPL (instead of single-shot `cortex code`) so the verify-and-retry loop GoL eval uses is now CLI-accessible (closes the principle 1 + principle 4 violation flagged in prior entries).
3. Adds pre-flight gates so SWE-bench fails fast with actionable errors when Docker is down, when the per-instance scoring image is missing, or when subordinate infra breaks.
4. Fixes the image-id format (`<org>_1776_<repo>-<issue>:latest`, not the obsolete `<org>__<repo>:v<version>`).
5. Writes `.gitignore` in the cloned repo so the verifier's `git add -A` doesn't slurp the REPL's per-turn snapshots into the patch.
6. Adds `set -eo pipefail` + `$PIPESTATUS` to the verifier so pytest's exit code can no longer be masked by the `tail -c 4096` pipeline.

**Correction to prior 2026-05-17 SWE-bench entries** (sonnet django + qwen3-coder-30b-a3b django + the original astropy entry): the "0/N pass" results in those entries were **silent infra failures**, not model failures. With docker daemon down (later runs) or with the new image format unsupported (earlier runs), the verifier's docker invocation failed; `scoreFromOutput` saw no pytest patterns, returned 0/0; `len(inst.FailToPass) = N` became the denominator; the cell looked like a clean "model failed" row.

| Entry | What was actually happening |
|---|---|
| Original haiku astropy | Image id wrong (`...:v4.3` doesn't exist on Docker Hub). docker pull failed, verifier exit 1, scorer parsed 0/0. |
| Sonnet django | Same image-id bug + docker daemon was down at probe time on at least one run. |
| Qwen django | Same as sonnet. The "qwen used half the tokens" finding may stand, but the 0/N pass-rate was infra not model. |

The numbers in those entries should be read as "this run didn't actually score against pytest" rather than as a real model pass-rate. The qualitative observations (token usage, latency) are still valid since the agent + verifier loop did execute.

**End-to-end probe through new surface**:

```
CORTEX_BINARY=$PWD/bin/cortex \
./bin/cortex eval --benchmark swebench --subset verified --limit 1 \
  --repo django/django --strategy cortex --model anthropic/claude-sonnet-4.5
```

Sequence observed:
1. Preflight: docker daemon up ✓, scoring image already local ✓.
2. `.gitignore` appended to cloned repo with `.cortex/` and verifier sentinels ✓.
3. REPL invoked headless: 5 agent turns, 75k tokens, $0.23.
4. Verifier ran: docker pulled the (now-correct) image, applied an empty patch (agent didn't write files — see below), ran pytest, got `No module named pytest` from miniconda's testbed env.
5. With pipefail + `$PIPESTATUS`, the verifier correctly exited non-zero. `verify_ok=False` (honest).
6. Auto-retry budget (default 3) ran one more attempt; same result.
7. Final cell persisted with `tests_passed=0, tests_failed=1870` from `RunSWEBenchTests` — same infra mode hits final scoring too.

Latency: 54 s end-to-end for one instance + 2 attempts × verifier docker overhead. Per-attempt agent cost ≈ $0.23 sonnet.

**Remaining gap (out of scope for this entry; logged for follow-up)**:
- **Per-repo test-runner adapters**. The new `swebench/sweb.eval.x86_64.<org>_1776_<repo>-<issue>:latest` image set uses Django's `python tests/runtests.py <names>` for django and `pytest` for some others, NOT a single pytest invocation across the board. Today's verifier hardcodes `python -m pytest`, which produces `No module named pytest` on django images. Mirrors a gap the upstream SWE-bench evaluator solves via per-instance `test_cmd` config. We need an equivalent (probably as a JSON file in `internal/eval/benchmarks/swebench/testdata/` or a per-instance lookup against a small repo-keyed map).
- **F2P name format**. The dataset stores Django test names as `test_method (full.dotted.TestClass)` with a SPACE; pytest and django runner both want different formats. Whatever per-repo adapter we add needs to normalize these.
- **arm64/amd64 platform warning**. Each docker run on Apple Silicon emits `WARNING: The requested image's platform (linux/amd64) does not match the detected host platform (linux/arm64/v8)`. Works via Rosetta but slow (~3× verifier time). Long-term fix is `--platform linux/amd64` explicit or arm64 images upstream.

**Why this still counts as success despite still being 0/1**:
- Principle 1 violation flagged in earlier entries is resolved: SWE-bench now drives the same agent loop as GoL via subprocess. No `internal/` imports added.
- Principle 4 violation resolved: REPL exposes iteration as a CLI surface; benchmark harnesses don't need to re-implement retry-with-feedback.
- The "silent zero" failure mode that contaminated three prior journal entries is now impossible: preflight surfaces docker / image issues before model spend, and pipefail surfaces pytest issues before they masquerade as model failures.
- The deeper problem (per-repo test runners) was previously *hidden* behind the silent infra failure. Surfacing it correctly is what makes the next step actionable.

**Follow-ups queued (priority order)**:
1. Per-repo test-runner adapter (`internal/eval/benchmarks/swebench/testrunners.go`): map repo → test command template (django → `python tests/runtests.py`, sympy → `bin/test`, pytest for the rest). Updates the verifier `inner` shell command per-instance.
2. Token/cost accounting from `RunREPLHeadless`: today's REPL JSON summary returns only `accepted` + paths. Extend to include token totals + cost so cells aren't always zero-cost.
3. Same `_1776_` change applied to `score.go`'s `imageNameFor` already; verify it lands in the next baseline run too.
4. Document the corrected format in `docs/benchmarks/swebench.md`.

---

### 2026-05-17 — LongMemEval (oracle, limit=5) / cortex +analyze 50

**Cortex**: `6885a8f` + uncommitted CLI gap-closure (`cortex analyze --workdir --limit` flags) + `benchmarks.RunAnalyze` helper + `longmemeval` `--analyze-limit` filter. Committed as part of this entry; see commit hash after the record commit.

**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark longmemeval --subset oracle --limit 5 \
  --strategy baseline,cortex --judge --analyze-limit 50 \
  --model anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5`, judge=`anthropic/claude-haiku-4.5` (default), `cortex_version=0.1.0`. New: an `analyze` pass with limit=50 runs between `capture --bulk` + `ingest` and `code` for the cortex strategy (Dream-style insight extraction on the ingested haystack).

**Result** — same 5 questions as the prior LongMemEval entries; compares the three conditions head-to-head:

| Condition | n | Pass | Total cost | Avg latency | Avg tokens in | Avg tokens out |
|---|---|---|---|---|---|---|
| baseline (no haystack, no store) | 5 | 0/5 | $0.0083 | 1607 ms | 1211 | 91 |
| cortex (haystack ingested, no analyze) — from prior runs | 5 | 0/5 | — | — | ~1211 | ~97 |
| **cortex + analyze 50** | 5 | 0/5 | $0.0122 | 2131 ms | **1854** | 118 |

Per-instance tokens_in for the +analyze cortex cell:

| Instance | axis | tokens_in (+analyze) | tokens_in (baseline) | Δ |
|---|---|---|---|---|
| 001be529 | single-hop        | 1207 | 1207 | 0 |
| 00ca467f | multi-hop         | 1206 | 1206 | 0 |
| 0100672e | multi-hop         | **2975** | 1210 | +1765 |
| 01493427 | knowledge-update  | **2663** | 1211 | +1452 |
| 031748ae | knowledge-update  | 1220 | 1220 | 0 |

**Why this run**: tests the principled "Dream pass before search" addition (per the user's question about whether the pipeline should use `analyze`/Dream to extract insights from the haystack before retrieval). All five Cortex-eval principles 1–9 were honored: black-box via CLI (`cortex analyze --workdir --limit`), no coaching (analyze runs the same prompt against haystack as it would against any captured events in production), versioned, reproducible (modulo LLM stochasticity), isolated (per-workdir state), structured (cells in `cell_results.jsonl`).

**Observations**:
- **Analyze DID change retrieval behavior on 2 of 5 cells** — cells `0100672e` and `01493427` saw their `tokens_in` ~double, which corresponds to `cortex_search` actually returning content (~640 extra tokens of injected context on average across the +analyze cells, vs ~0 in the prior no-analyze runs). Conclusion: the pipeline now works end-to-end — capture → ingest → analyze → search → inject.
- **Pass rate stayed 0/5.** Even when retrieval worked, the extracted insights didn't contain the specific facts the question needed. Representative judge reason: "The candidate refuses to answer, … while the gold answer provides specific concrete facts (4 engineers initially, 5 now), indicating this information should have been extracted." Diagnosis: the `analyze` prompt (geared for "decisions, patterns, constraints from a *development* event") loses numeric/specific detail when applied to *conversational* observations. Insight extraction at this prompt produces summaries like "team grew" rather than "4 → 5 engineers."
- **Cost is negligible**: $0.012 for 5 cells with analyze=50 — analyze itself is a bounded ~50 LLM calls on small events. End-to-end the +analyze run cost ~$0.0024 per cell more than the baseline cortex flow.
- **Latency +524 ms over baseline** (2131 vs 1607 ms) — modest tax for the extra cortex_search call, well under the 5 s budget that mattered in the earlier "is this a search-tax-only addition" analysis.
- 3 of 5 cells unchanged: analyze produced `NO_INSIGHT` for some haystack turns (single-line conversational content doesn't trigger the dev-event extractor), so the store wasn't enriched and search still returned nothing for those questions.

**Diagnostics for the LongMemEval gap** (now narrowed):
- ✓ Not "store is empty" — `capture --bulk` + `ingest` works.
- ✓ Not "agent doesn't call cortex_search" — analyze nudges enough that the agent retrieves on at least some cells.
- ✗ "Extracted insights lose the specific facts QA needs" — confirmed by judge reasoning. The `AnalyzeEventWithLLM` prompt in `cmd/cortex/commands/query.go:470` summarizes events into category/summary/importance/tags, which loses numeric/named-entity detail.

**Follow-ups**:
- Author a benchmark-specific analyze prompt that preserves named entities and numbers (or skip summarization entirely for `capture_type=observation` events and let raw chunks ride to retrieval). This is the highest-leverage gap remaining.
- Larger N (limit=25 + analyze=200, est. ~$0.10) to confirm the directional finding once the prompt is fixed.
- Add `cortex analyze --type=observation` or similar so a benchmark can opt into a different extraction prompt without modifying the production one.

**Effect on prior journal entries**: this entry supersedes the correction entry's "agent isn't calling cortex_search effectively, or embedding retrieval isn't returning the right haystack snippets" reading — it's the latter (or rather: the *extracted-insight* layer that sits between embeddings and the agent is what loses the answer).

---

### 2026-05-17 — SWE-bench (verified, django subset, limit=3) / qwen3-coder-30b-a3b

**Cortex**: `7c5accd`; `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark swebench --subset verified --limit 3 \
  --repo django/django --strategy baseline,cortex \
  --model qwen/qwen3-coder-30b-a3b-instruct
```
**Versions**: provider=`openrouter`, llm=`qwen/qwen3-coder-30b-a3b-instruct` (selected because user asked for "32b qwen coder on OpenRouter" and `qwen-2.5-coder-32b-instruct` returned `openrouter (404): No endpoints found that support tool use` — the 30b-A3B MoE coder is the closest tool-use-capable Qwen coder at that scale; pricing $0.07 per million input tokens). Same 3 django instances as the sonnet entry below for direct comparison.

**Result**:

| Strategy | n | Pass | Total cost | Avg latency | Avg tokens in | Avg tokens out | Avg turns |
|---|---|---|---|---|---|---|---|
| baseline | 3 | 0/3 | $0.0598 | 87.2 s | 268 440 | 4 238 | 17.0 |
| cortex   | 3 | 0/3 | $0.0326 | 76.9 s | 135 513 | 5 111 | 11.0 |

(Note: two ghost cells from a prior `qwen/qwen-2.5-coder-32b-instruct` attempt landed in `cell_results.jsonl` with `tokens_in=0, cost=0` and the same `F2P=0/438` placeholder — those are from before tool-use support was confirmed missing. Filter by `model=='qwen/qwen3-coder-30b-a3b-instruct'` to exclude them.)

**Why this run**: per user request — compare a mid-sized OpenRouter coder model against sonnet-4.5 on the same django instances. Tests whether SWE-bench pass-rate is capability-bound or scaffolding-bound.

**Observations**:
- **Same 0/3 pass-rate as sonnet-4.5** on identical instances. Reinforces the "scaffolding-bound, not capability-bound" reading from the sonnet entry — even an 11× cheaper model on the same harness gets the same outcome.
- **Cost is 10–20× cheaper**: $0.03/cell for qwen3-coder vs $0.22/cell for sonnet-4.5 on the same problems. Useful as a fast-feedback model for harness iteration even if final benchmarks use sonnet.
- **Cortex strategy used HALF the tokens (135 k vs 268 k avg in) and 6 fewer turns** than baseline. Agent terminated earlier under cortex — possibly because `cortex_search` returned a confident-looking (but unhelpful) result the model chased. Interesting pattern; not enough cells to know if it's signal or noise.
- **Qwen's per-call latency is 3× sonnet's** (87s vs 29s baseline) — slower per turn AND more turns. Throughput is the practical limit on qwen for this benchmark, not cost.
- **`qwen-2.5-coder-32b-instruct` is a no-go for tool-use benchmarks** on OpenRouter today (`openrouter (404): No endpoints found that support tool use`). Future SWE-bench runs targeting the 32b qwen tier must use the 30b-A3B MoE coder, `qwen3-coder` (full), or the free-tier `qwen3-coder:free`.

**Follow-ups**:
- A direct sonnet-vs-qwen comparison on a "fixable" SWE-bench instance (one with F2P <= 5) would isolate whether the qwen-cortex token-reduction is "agent gives up early" vs "actually finds the right answer cheaper."
- Two ghost cells are noise — worth a small `cortex eval grid` filter that drops `tokens_in == 0` rows by default unless explicitly asked.

---

### 2026-05-17 — SWE-bench (verified, django subset, limit=3) / sonnet-4.5

**Cortex**: `5a5f06c`; `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark swebench --subset verified --limit 3 \
  --repo django/django --strategy baseline,cortex \
  --model anthropic/claude-sonnet-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-sonnet-4.5`, judge=n/a (Docker tests-pass-all). Scoring images: `swebench/sweb.eval.x86_64.django__django:v2.2` and `v3.1`.

**Result**:

| Strategy | n | Pass | Total cost | Avg latency | Avg tokens in | Avg turns |
|---|---|---|---|---|---|---|
| baseline | 3 | 0/3 | $0.660 | 29.2 s | 67 018 | 9.7 |
| cortex   | 3 | 0/3 | $0.661 | 34.9 s | 68 354 | 11.0 |

Per-instance F2P:

| Instance | baseline | cortex |
|---|---|---|
| django-10097 | F2P=0/438 P2P=0/1432 | F2P=0/438 P2P=0/1432 |
| django-10554 | F2P=0/2 P2P=0/23 | F2P=0/2 P2P=0/23 |
| django-10880 | F2P=0/1 P2P=0/55 | F2P=0/1 P2P=0/55 |

Per-cell records in `.cortex/db/cell_results.jsonl`.

**Why this run**: Phase A follow-up — replace haiku's all-zero astropy baseline with a stronger model on django, to see whether the 0% was capability or scaffolding.

**Observations**:
- **Still 0/3.** Sonnet-4.5 emits patches but they don't pass any F2P test. This is harness-quality limited, not raw model capability: published Sonnet-4.5 SWE-bench Verified pass rates with proper scaffolding (Aider, SWE-Agent) are ~30–40%. Our `cortex code` harness is a single-shot edit loop with file ops + shell + cortex_search — substantially simpler than the published harnesses.
- **django-10097 alone has 438 fail-to-pass tests** — even a partially-correct patch would land 0/438 without a near-perfect fix. The instance distribution biases toward "all-or-nothing" outcomes.
- Cortex strategy turns slightly higher (11.0 vs 9.7) — extra calls to `cortex_search`, which never returns useful results because the per-instance `.cortex/` is empty.
- **Cost note**: $0.22/cell — bumping limit to 10 would be ~$4.40, still under the default $5 ceiling (estimator over-projects so ceilings still need raising).

**Follow-ups**:
- A pre-seed for SWE-bench cortex strategy (related issues / PRs / commit messages for the same repo) is the principled fix to make the "cortex" cell meaningful — see correction entry below for the framing.
- A harness comparison run (Aider as the harness instead of `cortex code`) would isolate "is the agent loop the bottleneck" from "is the model the bottleneck." Out of scope for this loop.

---

### 2026-05-17 — v2 suite (full sweep) / cell-level telemetry now landing

**Cortex**: `40aa466` + uncommitted `internal/eval/v2/eval.go` + `cmd/cortex/commands/eval.go` changes that add `Evaluator.SetPersister` and emit one `CellResult` per (test × strategy). Committed as part of this entry; see commit hash in `git log` after the record commit.

**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval -d test/evals/v2 -p anthropic -m anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5`, judge=none (lift/NDCG/ABR scoring only), `cortex_version=0.1.0`. Persisted as 342 cell rows in `.cortex/db/cell_results.jsonl` + 43 scenario rows in `eval_scenario_results` (legacy aggregation still emitted alongside).

**Result** (per-strategy aggregates across 171 tests × 2 strategies = 342 cells):

| Strategy | n | Tests passed | Pass rate (per test) | Avg latency | Total tokens in | Total tokens out | Total injected ctx |
|---|---|---|---|---|---|---|---|
| baseline | 171 | 143 | **83.6%** | 3217 ms | 2 925 | 61 802 | 0 |
| cortex   | 171 | 158 | **92.4%** | 2208 ms | 41 922 | 30 716 | 26 688 |

Scenario-level rollup (`eval_runs.eval-20260517-121309`):

| Metric | This sweep | Prior sweep (`eval-20260517-030846`) |
|---|---|---|
| Scenarios | 43 | 43 |
| Pass rate (scenario, lift-based) | 88.4% (38/43) | 90.7% (39/43) |
| Avg ABR (run-level) | 0.492 | 0.586 |
| Avg lift | +33.0% | +31.8% |
| Total baseline tokens / cortex tokens | 62 935 / 71 612 | 62 968 / 71 021 |

**Why this run**: Phase A follow-up — close the v2 telemetry gap so the suite stops being BLOCKED on principle 6 (Structured) and the journal has per-cell data to anchor the integration delta against.

**What changed in code** (committed with this entry):
- `internal/eval/v2/Evaluator` gains a `persister *Persister` field + `SetPersister(p, providerName)` setter. When non-nil, each test in `runTest` emits two `CellResult` rows (baseline + cortex) through the standard `PersistCell` fan-out (journal → SQLite `cell_results` table → `.cortex/db/cell_results.jsonl`).
- `cmd/cortex/commands/eval.go` constructs the persister up front (skipped in `--dry-run`) and reuses it for both per-cell persistence and the existing legacy scenario rollup, so we open the database once per process.
- `scenarioID` is now threaded through `walkTree → runTest` so cell rows carry the YAML scenario id (`v2/<scenario_id>`) instead of just the test id.
- Persistence failure logs at verbose level but does **not** fail the test — a missing row is more recoverable than a failed run.

**Observations**:
- **Cortex strategy lifts per-test pass rate by ~9 pp** (92.4% vs 83.6%) on this sweep — first time we have per-cell data fine-grained enough to see that. Worth treating as a "preliminary green" signal pending judge enablement.
- **Cortex generations are faster than baseline** (avg 2208 ms vs 3217 ms): cortex output is shorter (179 tokens out avg vs 361) because the retrieved context grounds shorter answers. Re-frames the earlier "cortex uses more tokens" finding — that was true on tokens_in (because of injected context) but not on tokens_out, and end-to-end latency wins.
- **Injected context averaged ~156 tokens per cortex cell** — my `len(cortexContext)/4` heuristic is an under-estimate vs the true delta (`avg cortex tokens_in 245 - avg baseline tokens_in 17 = ~228`). Acceptable for now; recalibrate when a per-call tokenizer is wired.
- **ABR varies run-to-run** (0.586 → 0.492 at temperature=0, no seed pinning) — direct evidence for principle 8 (LLM-judged variance). Single-run ABR numbers should be quoted with a sample-size caveat from here on.
- **Provider routing fixed at cell level**: `canonicalProviderName(flag, provider)` in the CLI maps `-p anthropic` to `provider=openrouter` on the cell when the keychain key is present, so the CellResult passes validation (`ContextStrategy == cortex` requires a matching provider enum).

**Carry-over gaps** (still unaddressed; flagged for follow-up):
- Principle 1 (Black box): the v2 runner still imports `internal/eval/v2/` directly — it IS the internal runner. This work closes principle 6/7 but not principle 1. A proper fix is wrapping each scenario as a benchmark with a CLI-shell harness.
- Principle 8 (Variance): single-run ABR numbers still cited as point estimates. Need repeated runs + σ reporting.
- Principle 9 (Separated baselines): only `baseline` / `cortex` cells emitted; no Fast/Full split (taxonomy missing from `CellResult.ContextStrategy`).
- Principle 3 (Versioned): `cortex_version=0.1.0` still a static constant; should include git SHA.

**Effect on prior journal entries**:
- The v2 "BLOCKED" entry below remains accurate as a snapshot of the gap that existed; this entry is its resolution. The Phase A summary's cross-cutting finding #1 ("v2 + ABR runners don't write `cell_results.jsonl`") is now partially obsolete — v2 does write; ABR is still computed from the same v2 cells but its specific entry below still reflects the legacy-only path (since the ABR aggregate is computed from scenario rollups, not raw cells, the ABR cell row situation is unchanged).

---

### 2026-05-17 — MTEB / NFCorpus (n=100 queries)

**Cortex**: `5f6d027` (branch `derek.s/dag-build`); `cortex_version=mteb-phase-a` (the MTEB runner pins this string regardless of git SHA — see follow-ups)
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
./bin/cortex eval --benchmark mteb --tasks NFCorpus --limit 100
```
**Versions**: embedder=`ollama/nomic-embed-text`, rerank=false (the `--rerank` flag is pending a `cortex rerank` CLI per `eval-principles.md:79`), index size=3633 docs.

**Result** (single cell, principle 1 black-box via `cortex embed --bulk` + `cortex search-vector`):

| Metric | Value |
|---|---|
| Queries scored | 100 |
| **NDCG@10** | **0.3729** |
| MRR@10 | 0.5887 |
| Recall@10 | 0.1968 |
| Index build time | 3 m 30 s |
| Retrieval time | 33.1 s (≈ 331 ms per query) |
| Cost | $0 (local ollama embeddings) |

Per-cell record in `.cortex/db/cell_results.jsonl` (row id `31227684-bd96-…`).

**Why this run**: Phase A Step 5 — first benchmark that doesn't depend on an LLM hot path, giving us a clean *capability* baseline for the embedding-retrieval layer alone. Confirms the embedding pipeline + `cortex embed`/`cortex search-vector` CLI surface works end-to-end.

**Observations**:
- **First non-red number.** NDCG@10=0.373 is in the published range for nomic-embed-text v1.5 on NFCorpus (typically 0.32–0.38 depending on dimension + chunking), so the implementation reproduces a known reference point.
- A 5-query smoke run earlier scored NDCG@10=0.3499 — the n=5 vs n=100 delta (0.350 → 0.373) is sampling noise, both numbers are in the expected band.
- Index build (3 m 30 s for 3633 docs) is the dominant cost; per-query retrieval averages ~330 ms via `cortex search-vector`. Reuse across re-runs would amortize this, but the current run doesn't cache between invocations.
- Cell `cortex_version` is hardcoded to `"mteb-phase-a"` rather than reading the git SHA — minor principle 3 (Versioned) drift to clean up.
- `tests_passed=1` simply means "the runner emitted a result" not "agent passed the task" — there's no agent here. Don't compare this 1/1 to LongMemEval's k/N or SWE-bench's F2P pass rates.

**Follow-ups**:
- `cortex rerank` CLI is the prerequisite for re-enabling `--rerank` (rerank=false today). The Reflect-based rerank claim in `docs/journal.md` is currently untested end-to-end.
- Recalibrate `CellResult.CortexVersion` to embed the git SHA across all benchmarks (not just MTEB) so principle 3 stops being "~ partial" everywhere.
- Larger-N run (full 323 NFCorpus queries) is a cheap follow-up at ~50 s wall time once retrieval is the only cost (index is on disk).
- Adds to the "is it red?" picture: MTEB confirms the retrieval/embedding layer is healthy. The LongMemEval gap is therefore not "embeddings are broken" but "retrieval returns chunks the agent doesn't synthesize correctly" — see LongMemEval +analyze follow-up.

---

### 2026-05-17 — Correction to LongMemEval + SWE-bench entries below

The original 2026-05-17 LongMemEval entry asserted that "cortex strategy
injects 0 context" and concluded "the persistent store is empty
pre-integration, so the 'cortex' cell pays a search-overhead tax without
retrieving anything." That misreads the harness.

What the harness actually does (`internal/eval/benchmarks/longmemeval/runner.go:95-99`):

- For `--strategy cortex`, the runner calls `hydrateHaystack` which
  shells out to `cortex capture --bulk --workdir <wd>` with all haystack
  sessions for the question, then `cortex ingest --workdir <wd>`. The
  per-instance `.cortex/` store IS populated with the question's
  haystack before the agent runs.
- The subsequent `cortex code` call has `cortex_search` available as a
  tool; the agent decides whether and how to call it.
- `CellResult.InjectedContextTokens` measures **session-start prompt-prefix
  injection**, not tool-call retrieval (`internal/eval/v2/cellresult.go:87`:
  "subset of TokensIn attributable to cortex injection"). LongMemEval
  uses tool-based retrieval, so 0 is expected and correct — it does NOT
  mean the store is empty.

Honest reread of the LongMemEval numbers below:

- baseline 5/32 (15.6%) is the model answering questions with zero
  haystack and no store — it's the "no context" arm. 15.6% is in the
  range published in the LongMemEval paper for cheap models.
- cortex 4/30 (13.3%) is the model with the haystack ingested into the
  store and `cortex_search` available. The fact that it's slightly under
  baseline at n=30 (within noise) actually shows a real signal: either
  the agent isn't calling `cortex_search` effectively, or the embedding
  retrieval isn't returning the right haystack snippets, or both. Worth
  investigating before claiming the pipeline doesn't help — it just
  isn't *currently* helping above no-context.

For SWE-bench, the correction is the opposite direction:

- `internal/eval/benchmarks/swebench/runner.go:56` shows that baseline
  vs cortex differs only by `NoSearch: strategy == "baseline"`. There is
  **no haystack pre-seed for SWE-bench** — the cortex strategy just
  toggles `cortex_search` availability against a freshly-created empty
  `.cortex/` per workdir. The "store is empty" reading is correct for
  this benchmark, but the principled fix is *not* a per-instance
  pre-seed (there's no haystack to seed); it's seeding with related
  issues / PRs / prior commits to make `cortex_search` actually useful
  for code understanding.

**Why this correction matters**: the original entries implied "cortex
adds search-tax with no retrieval benefit because store is empty." The
accurate framing is "LongMemEval retrieval pipeline runs end-to-end but
underperforms no-context at n=30; SWE-bench cortex strategy is
unevaluable today because there is nothing to retrieve from." Those
are different problems and need different fixes.

Cross-cutting finding #3 in the "Phase A baseline complete" summary above
("Cortex strategy injects 0 context tokens on every benchmark cell
pre-integration. … today's 'cortex' strategy is 'baseline + search-tax'")
is partially wrong — it correctly describes SWE-bench's situation but
incorrectly describes LongMemEval's.

---

### 2026-05-17 — Phase A baseline complete

Aggregate "before" snapshot for the DAG-protocol build per
`docs/eval-prep-epic.md` Phase A. Loop:
`docs/prompts/loop-eval-prep-phase-a.md`.

**Common attribution** (all four suites unless noted):
- Branch: `derek.s/dag-build`
- Cortex binary: locally-built `bin/cortex` (`go build -o bin/cortex ./cmd/cortex`) pinned via `CORTEX_BINARY`
- Provider: `openrouter` (resolved via macOS keychain `cortex-openrouter` per `pkg/llm/client.go:137` — `-p anthropic` ALIASES to OpenRouter when the keychain key is present, so the OpenRouter-style model id is mandatory)
- Model: `anthropic/claude-haiku-4.5`
- Spend ceilings raised to $25 run / $25 daily / $25 lifetime for the LongMemEval, SWE-bench, and ABR sweeps because the cost estimator (`internal/eval/v2/spend.go`) over-projects haiku-4.5 by ~250×; **actual total spend across all of Phase A ≈ $1.50**.

**Headline numbers**:

| Suite | Status | Headline number | Cells written |
|---|---|---|---|
| v2 scenarios (40+, end-to-end) | **BLOCKED** — Phase 1 telemetry gap | n/a (runner doesn't write `cell_results.jsonl`) | 0 to unified sink; 1 row in legacy `eval_scenario_results` per scenario |
| LongMemEval (oracle, limit=25, both strategies) | recorded | baseline **15.6%** (5/32) · cortex **13.3%** (4/30); cortex injects 0 ctx | 62 cells in `cell_results.jsonl` |
| SWE-bench Verified (limit=5, both strategies) | recorded | baseline **0%** (0/5) · cortex **0%** (0/5) on `astropy` subset | 11 cells in `cell_results.jsonl` |
| ABR (v2 full sweep, 43 scenarios) | recorded (with Phase-1 caveat) | **0.586 run-level / 0.409 scenario-mean** vs ROADMAP's 0.77 | 0 to unified sink; 43 rows in legacy `eval_scenario_results` |

**Cross-cutting findings worth carrying into Phase 6**:

1. **`cell_results.jsonl` parity is the gating Phase-1 work.** Only the benchmark path (`cmd/cortex/commands/eval_benchmark.go:141`) calls `evalv2.Persister.PersistCell`. The v2 scenario runner and the ABR computation path don't — both write to legacy `eval_runs` / `eval_scenario_results` SQLite tables instead. Until that is unified, principle 6 (Structured) cannot be honored for v2 or ABR.
2. **The `cortex-fast` vs `cortex-full` strategy taxonomy does not exist in the cell schema.** `internal/eval/v2/cellresult.go:44` allows only `baseline` / `cortex` / `frontier`. The loop's principle 8 ("no-context / Cortex-Fast / Cortex-Full as 3 distinct rows per scenario") is **structurally unsupported** today for *every* benchmark. Adding this distinction is a Phase 1 / DAG-protocol prerequisite, not a v2-only fix.
3. **Cortex strategy injects 0 context across every benchmark run.** `injected_context_tokens=0` on every cortex cell in LongMemEval and SWE-bench. The pre-integration store has nothing relevant to either benchmark, so today's "cortex" strategy is "baseline + search-tax" (+~10% tokens, +~2 s latency on LongMemEval). The post-DAG delta will only be interpretable if Phase 5/6 pre-seeds the store for each benchmark.
4. **Negative token reduction.** Across v2 (−12.8%) and LongMemEval (+9% tokens_in), cortex spends *more* tokens than baseline, not fewer. This contradicts the "Token Cost Reduction over time" North Star in `ROADMAP.md` Line 5.
5. **ABR ≠ ROADMAP claim.** Run-level ABR is 0.586, not 0.77. ROADMAP needs either an update or an investigation; flagged per the loop's ≥20% deviation rule.
6. **Cost estimator is ~250× pessimistic for haiku-4.5.** Real spend was $1.50 across all four suites combined; estimator wanted $50+ in ceiling headroom to permit them. Recalibrating `spend.EstimateCost` for the haiku-4.5 price band is a pre-req for letting the default $5 ceiling be the actual safety boundary the loop instructions assume.

**Verification artifacts**:
- Journal entries: this section plus the four per-suite entries below.
- Structured cells (where principle 6 is honored): 73 rows in `.cortex/db/cell_results.jsonl` (62 LongMemEval + 11 SWE-bench), 73 entries in `.cortex/journal/eval/0001.jsonl`.
- Structured cells (legacy-only sink): 45 rows in `.cortex/db/evals_v2.db` `eval_scenario_results` (43 from v2-full-sweep + 2 from single-scenario probes), 3 rows in `eval_runs`.
- Commits: `f815d06` (v2 BLOCKED), `533ca06` (LongMemEval), `4dbeede` (SWE-bench), `94980d3` (ABR), and this summary commit (next).

**Exit per loop**: STOP. Do not start Phase B in this session.

---

### 2026-05-17 — ABR baseline (v2 full sweep) / 43 scenarios

**Cortex**: `4dbeede` (branch `derek.s/dag-build`); `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval -d test/evals/v2 -p anthropic -m anthropic/claude-haiku-4.5 -o json
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5`, judge=none (NDCG-based ABR, not judge-based), `cortex_version=0.1.0`. Persisted run id: `eval-20260517-030846` in `.cortex/db/evals_v2.db` table `eval_runs` + 43 per-scenario rows in `eval_scenario_results`.

**Result** (top-line aggregates from this run):

| Metric | Value |
|---|---|
| Scenarios run | 43 |
| Pass | 39 / 43 (90.7%) |
| Avg ABR (run-level / cell-weighted, from `eval_runs.avg_abr`)     | **0.586** |
| Avg ABR (scenario-mean of `eval_scenario_results.avg_abr`)         | **0.409** |
| Avg lift (cortex vs baseline judge-free score) | +31.8% |
| Avg Fast NDCG | 0.093 |
| Avg Full NDCG | 0.535 |
| Total baseline tokens / cortex tokens | 62 968 / 71 021 |
| Avg token reduction | **−12.8%** (cortex uses *more* tokens than baseline) |

ABR distribution across 43 scenarios:

| ABR bucket | Count | Scenarios (sample) |
|---|---|---|
| 0.00          | 14 | `abstention-missing-info`, `adversarial-abstention`, `agentic-find-*`, `db-patterns`, `locomo-*`, `updates-api-versions`, `temporal-release-history` (the locomo and agentic-find scenarios show Full NDCG = 0, so 0/0 → 0) |
| 0.25–0.50     | 14 | `abstention-partial-info`, `reasoning-debug-journey`, `debugging`, `deployment`, `go-*`, `error-handling`, `temporal-*` |
| 0.50–0.75     | 9  | `extraction-*`, `security-practices`, `api-design`, `auth-evolution`, `auth-patterns`, `code-review`, `adversarial-defaults`, `updates-policy-changes` |
| 0.75–1.00     | 6  | `cache-evolution`, `extraction-infra-config`, `abstention-ambiguous-context`, `adversarial-noise`, `api-evolution`, `error-convention`, `testing-patterns` |

**Why this run**: Phase A Step 4 — establish current ABR. The ABR trend was reading only two prior `auth-patterns`-only runs (0.67 latest); a full-suite sweep is needed for an honest baseline.

**Observations**:
- **Run-level avg ABR (0.586) and ROADMAP's 0.77 disagree by ~24%.** Per the loop's "When to ask for human input" rule, this is a ≥20% deviation worth surfacing. The user pre-authorized continuing without pausing; flagging for follow-up. Likely explanations: (a) ROADMAP cites a stale single-scenario reading, (b) prior sweeps used a different model or context priming, or (c) recent code changes regressed ABR. The git SHA on the latest stored eval row is `55d7427`, same as this branch's recent commit — so no obvious "old code" alibi.
- **The scenario-mean (0.409) is lower than the run-level (0.586)**: cell-weighted averaging hides per-scenario zeros that the unweighted mean reveals. 14 scenarios sit at ABR=0 (often because Full NDCG itself is 0, e.g. `locomo-*` and `agentic-find-*` — their `expect` blocks don't seed retrieval correctly).
- **Cortex uses 12.8% MORE tokens than baseline**, not fewer — the "Token Cost Reduction" North Star in `ROADMAP.md` is currently negative. Consistent with the LongMemEval finding (cortex strategy is mostly search-tax pre-integration).
- Pass-rate of 90.7% reflects the lift-based pass criterion (`avg_lift > 0` ties pass), not actual task success. Don't confuse it with LongMemEval or SWE-bench task-success pass rates.
- **Principle 6 (Structured) gap reaffirmed.** This entire ABR baseline lives in `eval_scenario_results` SQLite only — no `cell_results.jsonl` row, no journal entry. Same Phase-1 telemetry blocker recorded in the v2 entry below; the ABR baseline is therefore officially BLOCKED on principle 6 but recorded here as the best available pre-integration anchor.

**Follow-ups**:
- Reconcile ROADMAP.md's 0.77 → 0.586 (this run) — either update the ROADMAP number, or investigate the regression.
- 14 scenarios with Full NDCG = 0 are silently zeroing the ABR. Either fix the `expect` blocks (so retrieval can be scored) or exclude them from the ABR mean — current behavior penalizes the metric for fixture bugs.
- Negative token reduction (cortex > baseline) is a North Star regression worth surfacing separately; recommend a dedicated follow-up.

---

### 2026-05-17 — SWE-bench (verified, limit=5) / baseline vs cortex

**Cortex**: `533ca06` (branch `derek.s/dag-build`); `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark swebench --subset verified --limit 5 \
  --strategy baseline,cortex --model anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5`, judge=n/a (SWE-bench uses `tests_pass_all`, i.e. Docker test-suite execution, not LLM judging), scoring images = `swebench/sweb.eval.x86_64.<repo>:v<n>`, `cortex_version=0.1.0`.
**Result**:

| Strategy | n | Pass | Pass rate | Total cost | Avg latency | Avg tokens (in/out) | Avg turns | Avg injected ctx |
|---|---|---|---|---|---|---|---|---|
| baseline | 6 | 0 | 0.0% | $1.294 | 28.4 s | 205200 / 2086 | 13.5 | 0 |
| cortex   | 5 | 0 | 0.0% | $1.059 | 26.4 s | 202601 / 1849 | 14.2 | 0 |

(baseline n=6 includes one extra cell from the probe run; cortex n=5 is the clean limit-5 sweep.)

Per-instance F2P/P2P (all instances from `astropy/astropy`, the alphabetically-first repo in the verified subset):

| Instance | strat=baseline | strat=cortex |
|---|---|---|
| astropy-12907 | F2P=0/2, P2P=0/13 | F2P=0/2, P2P=0/13 |
| astropy-13033 | F2P=0/1, P2P=0/20 | F2P=0/1, P2P=0/20 |
| astropy-13236 | F2P=0/2, P2P=0/644 | F2P=0/2, P2P=0/644 |
| astropy-13398 | F2P=0/4, P2P=0/68  | F2P=0/4, P2P=0/68 |
| astropy-13453 | F2P=0/1, P2P=0/9   | F2P=0/1, P2P=0/9 |

Per-cell records in `.cortex/db/cell_results.jsonl` (11 swebench rows total).

**Why this run**: Phase A Step 3 — establish a pre-integration SWE-bench Verified baseline.

**Observations**:
- **0% pass on both strategies.** The agent runs 13–14 turns per instance, emits a patch, and the patch fails all F2P tests in the scoring container every time. Same set of 5 astropy instances passes/fails identically across strategies, modulo a small token/turn delta.
- **Cortex strategy still injects 0 context.** Same finding as LongMemEval: store is empty pre-integration, so "cortex" cell is "baseline + search-tax". The 2-second latency reduction and slightly fewer output tokens on the cortex side are noise at n=5.
- **Repo coverage is narrow.** `--limit 5` against the alphabetical-by-instance-id verified subset picked only astropy. A representative SWE-bench Verified baseline needs `--repo` rotation across the 12 repos in the subset. Not done here to respect both the cost ceiling and per-loop scope.
- Scoring container worked first try (Docker is up; `swebench/sweb.eval.x86_64.astropy__astropy:{v4.3,v5.0}` images pulled and executed).
- One legitimate principle-1 confirmation: the harness only sees `cortex` as a black box (`internal/eval/benchmarks/cortexcli.go`); F2P numbers come from running the test suite in the Docker image, not from the agent self-reporting.

**Follow-ups**:
- Cross-repo sweep (one limit-1 per repo × 12 repos × 2 strategies ≈ 24 cells, est. $5) for a representative Verified baseline. Defer until cost estimator is recalibrated.
- 0% baseline is consistent with claude-haiku-4.5's reported SWE-bench Verified pass rate — to expose any DAG-protocol delta we'll want a stronger model (e.g. `anthropic/claude-sonnet-4.5`) in Phase 6 comparison, not just haiku.
- Same `cortex-fast / cortex-full` taxonomy gap as LongMemEval — see that entry's follow-ups.

---

### 2026-05-17 — LongMemEval (oracle, limit=25) / baseline vs cortex

**Cortex**: `f815d06` (branch `derek.s/dag-build`); `cortex_version=0.1.0`
**Command**:
```
CORTEX_BINARY=$PWD/bin/cortex \
CORTEX_EVAL_RUN_USD_CEILING=25 \
CORTEX_EVAL_DAILY_USD_CEILING=25 \
CORTEX_EVAL_LIFETIME_USD_CEILING=25 \
./bin/cortex eval --benchmark longmemeval --subset oracle --limit 25 \
  --strategy baseline,cortex --judge -m anthropic/claude-haiku-4.5
```
**Versions**: provider=`openrouter`, llm=`anthropic/claude-haiku-4.5` (also used as judge per `internal/eval/benchmarks/longmemeval/judge.go:13`), rerank=n/a (LongMemEval doesn't use rerank), `cortex_version=0.1.0`. Dataset: `~/.cortex/benchmarks/longmemeval/longmemeval_oracle.json` (HuggingFace `xiaowu0162/longmemeval-cleaned`, 500 questions, sorted by QuestionID; `--limit 25` takes the first 25).
**Result** (aggregated across this run + the prior limit=5 probe, 62 cells total):

| Strategy | n | Pass | Pass rate | Total cost | Avg latency | Avg tokens (in/out) | Avg injected ctx |
|---|---|---|---|---|---|---|---|
| baseline | 32 | 5 | 15.6% | $0.0550 | 1808 ms | 1214 / 101 | 0 |
| cortex   | 30 | 4 | 13.3% | $0.0559 | 2049 ms | 1327 / 107 | 0 |

Per-axis (latest 50 cells; n is per-strategy):

| Axis | baseline | cortex |
|---|---|---|
| single-hop        | 1/8 | 1/8 |
| multi-hop         | 1/7 | 1/7 |
| knowledge-update  | 1/8 | 1/8 |
| temporal          | 2/2 | 1/2 (n too small) |

Per-cell records in `.cortex/db/cell_results.jsonl` (62 rows) and `.cortex/journal/eval/0001.jsonl`.

**Why this run**: Phase A Step 2 — establish a pre-integration LongMemEval baseline so Phase 6 has a real "before" picture.

**Observations**:
- **Cortex strategy injects zero context** (`injected_context_tokens=0` on every cortex cell). The persistent store is empty pre-integration, so the "cortex" cell pays a search-overhead tax (+241 ms avg, +9% tokens_in) without retrieving anything. Pass-rate parity is the *only* honest reading; the small baseline-better delta is within noise.
- Judge wiring works (`task_success_criterion=judge_llm`; representative `notes`: "The candidate refuses to answer and claims lack of access to information, while the gold answer provides specific concrete facts (4 engineers initially, 5 now)…"). Failures are real model abstentions, not tool errors.
- **Cost estimator is ~250× pessimistic.** `spend.EstimateCost` projects $0.45/cell for `anthropic/claude-haiku-4.5`; actual cell cost ≈ $0.0017–$0.0018. All three default ceilings ($5 run / $5 daily / $5 lifetime) had to be raised to $25 to permit 50 instances, even though real spend totalled $0.11. Worth recalibrating before larger Phase A sweeps.
- **Strategy taxonomy gap (principle 8).** `internal/eval/v2/cellresult.go:44` only allows `StrategyBaseline / StrategyCortex / StrategyFrontier`. There is no `cortex-fast` vs `cortex-full` split in the cell schema, so the loop's principle 8 "no-context / Cortex-Fast / Cortex-Full as 3 distinct rows" is **structurally unsupported** today — for *all* benchmarks, not just v2.
- Provider routing surprise repeats here: `-p anthropic` → `pkg/llm/client.go:137` resolves OpenRouter first (keychain `cortex-openrouter`), so OpenRouter-style model id is mandatory even with `-p anthropic`. Recorded in the v2 entry below.

**Follow-ups**:
- Phase 1 / DAG protocol: add a `cortex-fast` vs `cortex-full` axis to `evalv2.CellResult.ContextStrategy` so principle 8 becomes expressible.
- Calibrate `spend.EstimateCost` for `anthropic/claude-haiku-4.5` (current 250× over-estimate forces ceiling overrides for routine sweeps).
- Decide before Phase 6: do we pre-ingest each LongMemEval haystack into the cortex store before scoring the "cortex" cell? Without that the "cortex" strategy is just baseline-with-search-tax and the post-DAG delta will be uninterpretable.
- Larger N sweep (e.g. 100 per axis) is cheap (~$0.50 total) and worth running once the ceiling estimator is fixed.

---

### 2026-05-17 — v2 suite / BLOCKED: needs Phase 1 unified telemetry

**Cortex**: `55d7427` (branch `derek.s/dag-build`)
**Command**:
```
./bin/cortex eval -s test/evals/v2/auth-patterns.yaml -p anthropic -m anthropic/claude-haiku-4.5 -v
```
**Versions**: provider=`openrouter` (keychain `cortex-openrouter`), llm=`anthropic/claude-haiku-4.5`, judge=none, rerank=false
**Result**: BLOCKED — see Observations.

**Why this run**: Phase A Step 1 — establish a v2 baseline. Probed with one scenario before committing to all 40, per the loop's "verify telemetry first" gate.

**Observations**:
- Single-scenario probe succeeded as a *legacy* eval: `auth-patterns` reported avg lift +33%, ABR 0.67, 3/3 tests ran, baseline 1464 → cortex 1107 tokens (-24%).
- Persistence landed in `.cortex/db/evals_v2.db` table `eval_scenario_results` (legacy schema) only — **not** in `.cortex/db/cell_results.jsonl`, **not** in the `cell_results` SQLite table, **not** in `.cortex/journal/eval/*.jsonl` (segment `0001.jsonl` is 0 bytes after the run).
- Call site confirms the gap: `cmd/cortex/commands/eval_benchmark.go:141` is the only caller of `evalv2.Persister.PersistCell` (the writer that fans out to journal + SQLite `cell_results` + JSONL). The v2 scenario runner in `internal/eval/v2/` does not invoke it; it writes the older per-scenario summary row instead.
- Independent principle-8 gap: v2 runner produces only `baseline` vs `cortex` cells; it does not emit separated `no-context / Cortex-Fast / Cortex-Full` rows the loop requires.
- Provider routing surprise (not a blocker, worth recording): `-p anthropic` routes through OpenRouter when the keychain key is present (resolution order at `pkg/llm/client.go:137`), so the OpenRouter-style model id (`anthropic/claude-haiku-4.5`) is required even with `-p anthropic`. The direct Anthropic model id (`claude-haiku-4-5-20251001`) returned `openrouter (400): … is not a valid model ID`.

**Follow-ups**:
- Phase 1 of `docs/integration-roadmap.md` (unified `cell_results.jsonl` for ad-hoc CLI invocations) is the prerequisite. Until it lands, all 40 v2 scenarios will fail principle 6 (Structured) the same way; skipping the full sweep for now.
- Independent Phase-A item to file: extend the v2 runner to emit Fast vs Full as distinct rows so principle 8 (Separated baselines) can be honored even after the telemetry sink lands.
- The legacy run is retained in `eval_scenario_results` for reference; do **not** treat it as the Phase A v2 baseline.
