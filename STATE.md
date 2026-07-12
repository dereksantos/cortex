# STATE — cortex web loop
Updated: 2026-07-12 · Iteration: 33

## Current milestone
M5 — Web UI (P5), four screens (M1, M2, M3, M4 complete)

## Checklist (all milestones, append-only — completed milestones stay)
### M1 — First-run bootstrap + greeting
- [x] M1.1 `internal/userhome`: single exported resolver ($CORTEX_HOME
      else ~/.cortex); userConfigPath() routes through it; temp-dir
      redirect test. `0b1524f` —
      `TestDirCortexHomeRedirect`, `TestDirFallsBackToDotCortexUnderHome`,
      `TestPathJoinsUnderResolvedDir` (internal/userhome/userhome_test.go),
      `TestUserConfigPathRoutesThroughUserhome` (cmd/cortex/config_test.go).
- [x] M1.2 BackendResolver interface + chain, table-driven over the
      named rows in GOAL.md §6 (fakes only). `db03036` —
      `TestBackendResolverChain`, `TestBackendResolverChainNoBackends`
      (cmd/cortex/bootstrap_test.go).
- [x] M1.3 Resolved backend persists to user config via
      read-modify-write; unknown fields survive byte-for-byte; second
      resolution short-circuits. `9d9719b` —
      `TestSetJSONPathPreservesUnknownFieldsByteForByte`,
      `TestReadJSONDocMissingFileIsEmptyDoc` (cmd/cortex/configwrite_test.go),
      `TestPersistBackendRoundTrip`, `TestFileConfigProbeMissingOrEmptyMisses`,
      `TestBackendResolverChainSecondRunShortCircuitsOnPersistedConfig`
      (cmd/cortex/bootstrap_persist_test.go).
- [x] M1.4 First-run detection per GOAL.md §3 (user-home artifacts
      only; greeting marker; CORTEX_HOME and CWD both isolated).
      `0d1a62c` — `TestIsFirstRunMarkerAbsentTrue`,
      `TestIsFirstRunConfigPresentFalse`, `TestIsFirstRunMarkerPresentFalse`,
      `TestIsFirstRunIgnoresSessions` (cmd/cortex/firstrun_test.go).
- [x] M1.5 Greeting fires exactly once on first run via scripted
      Sender; none otherwise; greeting text golden-pinned. `6f9e8ff` —
      `TestGreetingFiresOnceOnFirstRun`, `TestGreetingSkippedWhenNotFirstRun`,
      `TestGreetingWritesMarkerOnlyAfterSuccess`, `TestGreetingPromptGolden`
      (cmd/cortex/greeting_test.go).
- [x] M1.6 Keychain read/write behind pkg/secret interface with fake;
      meta-test fails on "security" as exec arg in any _test.go.
      `88c7efa` — `TestFakeStoreRoundTrip`, `TestFakeStoreSetOverwrites`,
      `TestFakeStoreInjectedErrors` (pkg/secret/store_test.go),
      `TestNoRealSecurityBinaryInvokedByTests`,
      `TestFileInvokesSecurityBinaryDetectsLiteral` (pkg/secret/meta_test.go).
- [x] M1.7 Greeting asks for scan roots and persists them to user
      config (scripted-Sender test). `e8e5324` —
      `TestGreetingPromptGolden` (extended golden pin, asks where code
      lives), `TestMaybeGreetArmsAwaitingScanRootsReply` (scripted-Sender
      greeting harness, end-to-end through persist),
      `TestParseScanRootsReplyExtractsPaths`,
      `TestPersistScanRootsRoundTrip`,
      `TestMaybeCaptureScanRootsNoopWhenNotAwaiting`,
      `TestMaybeCaptureScanRootsPersistsAndClearsFlag`,
      `TestMaybeCaptureScanRootsDeclineClearsFlagWithoutPersisting`
      (cmd/cortex/scanroots_test.go, cmd/cortex/greeting_test.go).
      **M1 complete.**

### M2 — Landscape scan (P2)
- [x] M2.1 `internal/landscape`: per-family `Scanner` implementations
      (harnesses, runtimes, projects) composed by `Scan(root, caps)`;
      temp-dir fixture per family covering present / absent / malformed.
      `21f6c7f` — `TestScanHarnesses`, `TestScanRuntimes`,
      `TestScanProjects`, `TestScanComposesAllFamilies`
      (internal/landscape/landscape_test.go).
- [x] M2.2 Every filesystem visit filtered through
      `projectscan.IgnoreSet`; a fixture with a planted secret path
      proves it never appears in any scan result. `569269c` —
      `TestScanProjectsFiltersThroughIgnoreSet`
      (internal/landscape/landscape_test.go).
- [x] M2.3 Content-non-leak sentinel (struct leg landed; `--json` and
      journal-event legs deferred to M2.5/M2.7 per Decisions Log below).
      `d457260` — `TestScanResultDoesNotLeakFileBodyContent`
      (internal/landscape/landscape_test.go).
- [x] M2.4 Caps enforced: fixtures exceeding max depth / max entries /
      a near-zero timeout each terminate cleanly (three tests) with
      truncation reported in the result — never silent. `e07dd80` —
      `TestScanProjectsCapsMaxDepth`, `TestScanProjectsCapsMaxEntries`,
      `TestScanProjectsCapsTimeout` (internal/landscape/landscape_test.go).
- [x] M2.5 `cortex scan [--json] [--root <path>]`: uses persisted
      roots, `--root` overrides, neither ⇒ typed refusal (all three
      paths tested); golden-file text report; JSON round-trip.
      `b9257b0` — `TestResolveScanRootsFlagOverrides`,
      `TestResolveScanRootsUsesPersisted`,
      `TestResolveScanRootsRefusesWhenNeither`,
      `TestBuildScanReportAggregatesAcrossRoots`,
      `TestRenderScanReportGolden`,
      `TestRenderScanReportGoldenEmptyAndTruncated`,
      `TestScanReportJSONRoundTrip` (cmd/cortex/scan_test.go).
- [x] M2.6 `scan_landscape` coder tool registered, gated by
      `tools.enable_scan` (absent ⇒ registered, false ⇒ absent — both
      tested), home-scoped and read-only. `f27d027` —
      `TestScanLandscapeIsRegisteredForCoderOnly`,
      `TestScanLandscapeReportsHarnessesAndRuntimesUnderHome`,
      `TestScanLandscapeReportsNoneWhenNothingDetected`,
      `TestScanLandscapeNeverWalksProjectsUnderHome`
      (internal/tools/scan_landscape_test.go), `TestScanEnabled`,
      `TestScanLandscapeToolRegistrationGate`,
      `TestIsToolEnabledScanGate` (cmd/cortex/scan_landscape_tool_test.go).
- [x] M2.7 Scan persists a `landscape.scan` event to the user-level
      journal under the user home (temp-home test); the coder tool
      additionally writes the `landscape` memory note to the current
      project's store (temp-workspace test asserts a fixture-derived
      string); headless scan writes no note (asserted). `cdc0533` —
      `TestAppendLandscapeScanWritesEntryToUserLevelJournal`,
      `TestAppendLandscapeScanIsolatedByCortexHome`
      (internal/journal/landscape_test.go),
      `TestRecordLandscapeScanWritesJournalEvent`,
      `TestScanCLISourceNeverImportsMemory` (cmd/cortex/scan_test.go),
      `TestScanLandscapeWritesLandscapeMemoryNote`,
      `TestScanLandscapeMemoryWriteFailurePropagates`,
      `TestScanLandscapeWritesLandscapeScanJournalEvent`
      (internal/tools/scan_landscape_test.go). **M2 complete.**

### M3 — Workspace threading + project registry (P3)
- [x] M3.1 `Workspace` threaded through session construction,
      `contextDir`, instructions discovery, and `ConfinePath` in a
      behavior-preserving commit: existing suite green plus equivalence
      tests (same fixture repo via CWD vs explicit root ⇒ identical
      context dir, instructions, confinement verdicts). `8c9e800` —
      `TestWorkspaceFromCWDMatchesExplicitRootContextDir`,
      `TestWorkspaceFromCWDMatchesExplicitRootInstructions`,
      `TestWorkspaceFromCWDMatchesExplicitRootConfinement`,
      `TestNewWorkspaceResolvesRelativeRootToAbsolute`,
      `TestWorkspaceFromCWDFallsBackToWorkingDirWhenNoCortexDirFound`
      (cmd/cortex/workspace_test.go).
- [x] M3.2 `ConfinePath` escape attempts against a non-CWD root refused
      (table of traversal and symlink cases). `9d4c9a2` —
      `TestNewWorkspaceConfinePathRejectsEscapes` (cmd/cortex/workspace_test.go).
- [x] M3.3 `internal/registry`: CRUD round-trip on `projects.json` under
      a temp home; unknown-name lookup returns a typed error. `4bc0e87` —
      `TestFileRegistryCRUDRoundTrip`,
      `TestFileRegistryLookupUnknownNameReturnsTypedError`,
      `TestFileRegistryRemoveUnknownNameReturnsTypedError`,
      `TestFileRegistryLookupOnMissingFileReturnsTypedError`
      (internal/registry/registry_test.go).
- [x] M3.4 `cortex project add/list/remove` wired to the registry
      (CLI-level tests); `cortex scan --register` feeds discovered
      projects in. `c58285c` —
      `TestAddProjectSavesToRegistry`,
      `TestAddProjectResolvesRelativeRootToAbsolute`,
      `TestAddProjectRejectsEmptyArgs`,
      `TestRemoveProjectRemovesFromRegistry`,
      `TestRemoveProjectUnknownNamePropagatesTypedError`,
      `TestRenderProjectListGolden`, `TestRenderProjectListEmptyGolden`,
      `TestRegisterDiscoveredProjectsFeedsRegistry`,
      `TestRegisterDiscoveredProjectsUpsertsOnRerun`
      (cmd/cortex/project_test.go).
- [x] M3.5 `--project <name>` on `turn`, `resume`, and `study` resolves
      via the registry and runs against that root (fixture-repo test).
      `fc50be5` — `TestApplyProjectByNameRunsAgainstRegisteredRootFromUnrelatedCWD`,
      `TestApplyProjectByNameUnknownProjectReturnsTypedError`,
      `TestApplyProjectFlagNoopWhenNameEmpty`,
      `TestParseProjectFlagExtractsNameAndRest`
      (cmd/cortex/project_workspace_test.go). **M3 complete.**

### M4 — `cortex serve` (P4)
- [x] M4.1 `cortex serve` starts on 7433 (flag-overridable); the real
      listener's address satisfies `ip.IsLoopback()` (test on the
      constructed server, not httptest); bearer token generated, written
      to `<userhome>/serve.token` mode 0600 (path + mode + content
      asserted); tokenless and wrong-token requests get 401. `0d928a0` —
      `TestServePortFromArgsDefaultsTo7433`,
      `TestServePortFromArgsFlagOverrides`,
      `TestNewServeServerListenerIsLoopback`,
      `TestGenerateServeTokenIsNonEmptyAndUnique`,
      `TestWriteServeTokenModeAndContent`,
      `TestServeAuthMiddlewareRejectsTokenlessAndWrongToken`
      (cmd/cortex/serve_test.go).
- [ ] M4.2 Endpoints per spec: projects list, sessions list/create/resume,
      `POST …/turn`, SSE progress, landscape, models (read merged config +
      fleet; scoped write user/project/session via read-modify-write —
      unknown-field survival test; key material absent from every
      response, asserted). **Split (2026-07-11, this iteration) into:**
  - [x] M4.2a Read-only project + session listing: `GET /api/projects`
        (from `internal/registry`) and `GET /api/projects/{name}/sessions`
        (peek via existing `listSessions`/`loadSession` — no live
        `*CortexSession`, no locking); unknown project ⇒ 404. `44a603e` —
        `TestListProjectsEndpointReturnsRegisteredProjects`,
        `TestListProjectsEndpointEmptyRegistryReturnsEmptyArrayNotNull`,
        `TestListProjectsEndpointRequiresAuth`,
        `TestListProjectSessionsEndpointListsNewestFirst`,
        `TestListProjectSessionsEndpointNoSessionsYetReturnsEmptyArray`,
        `TestListProjectSessionsEndpointUnknownProjectReturns404`
        (cmd/cortex/serve_routes_test.go).
  - [ ] M4.2b Live session lifecycle + turn. **Split (2026-07-11, this
        iteration) into:**
    - [x] M4.2b1 `SessionManager` (`Get`/`Create`/`Resume`/`List`): an
          in-process map of live `*CortexSession` keyed by session id,
          built via an injectable `sessionFactory` seam (hermetic in
          tests, `NewCortexSession` in production); `POST
          /api/projects/{name}/sessions` (create, or resume via an
          optional `{"resume":"<id>"}` body). `6a507ae` —
          `TestSessionManagerCreateStartsAndTracksNewSession`,
          `TestSessionManagerCreateUnknownProjectReturnsTypedError`,
          `TestSessionManagerResumeOfLiveSessionReturnsSamePointerWithoutReopening`,
          `TestSessionManagerResumeRehydratesFromTranscriptAfterRestart`,
          `TestSessionManagerResumeUnknownIDReturnsError`,
          `TestCreateSessionEndpointCreatesNewSession`,
          `TestCreateSessionEndpointResumesWhenBodyNamesID`,
          `TestCreateSessionEndpointUnknownProjectReturns404`,
          `TestCreateSessionEndpointRequiresAuth`
          (cmd/cortex/serve_session_test.go).
    - [x] M4.2b2 `POST …/turn` running `session.Turn` against a live
          `managedSession`, guarded by the per-session turn-serializing
          mutex deferred here from M4.2b1 (an unused field would have
          failed check.sh's lint gate before there was a caller).
          `eaacc18` — `TestTurnEndpointRunsTurnAgainstLiveSession`,
          `TestTurnEndpointUnknownSessionReturns404`,
          `TestTurnEndpointRequiresAuth`,
          `TestTurnEndpointSameSessionSerializes`
          (cmd/cortex/serve_turn_test.go).
    - [x] M4.2b3 SSE progress stream rendered from the existing `Progress`
          seam (`cmd/cortex/loop.go:76`, a bare `func(line string)` — fan
          it into `data: ...\n\n` chunks); the serve `http.Server` must set
          NO `WriteTimeout` for this (GOAL.md D6/M4.5 — set it now even
          though the dedicated test lands at M4.5). `01cabb2` —
          `TestTurnStreamEndpointStreamsProgressAndResult`,
          `TestTurnStreamEndpointUnknownSessionReturns404`,
          `TestTurnStreamEndpointRequiresAuth`
          (cmd/cortex/serve_stream_test.go).
  - [ ] M4.2c Landscape + models endpoints: landscape (Phase 2's
        persisted result), models (merged config + fleet read; scoped
        role-binding writes at user/project/session via M1.3's
        read-modify-write helpers — unknown-field survival test; key
        material absent from every response, asserted). **Split
        (2026-07-12, this iteration) into:**
    - [x] M4.2c1 `GET /api/landscape`: the same `ScanReport` `cortex
          scan --json` prints, built via scan.go's own
          `resolveScanRoots`/`buildScanReport` (no parallel scan path);
          no persisted scan roots ⇒ 412 (`ErrNoScanRoots`), never a
          blind `$HOME` sweep. `dafe59d` —
          `TestLandscapeEndpointReturnsScanReport`,
          `TestLandscapeEndpointNoRootsConfiguredIsTypedRefusal`,
          `TestLandscapeEndpointRequiresAuth`
          (cmd/cortex/serve_landscape_test.go).
    - [ ] M4.2c2 Models endpoint: merged config + fleet read; scoped
          role-binding writes at user/project/session via M1.3's
          read-modify-write helpers — unknown-field survival test; key
          material absent from every response, asserted. **Split
          (2026-07-12, this iteration) into:**
      - [x] M4.2c2a Read-only `GET /api/models`: merged config + discovered
            fleet, every known role bound via `Config.resolveBinding`, key
            material absent from the response (asserted against a real
            secret value threaded through `key_env`). `7c9c101` —
            `TestModelsEndpointReturnsRolesAndFleet`,
            `TestModelsEndpointFleetUnreachableStillReturnsRoles`,
            `TestModelsEndpointRequiresAuth` (cmd/cortex/serve_models_test.go).
      - [ ] M4.2c2b Scoped role-binding writes at user/project/session via
            M1.3's read-modify-write helpers; unknown-field survival test.
            **Split (2026-07-12, this iteration) into:**
        - [x] M4.2c2b1 File-backed writes at user + project scope:
              `PUT /api/models/{role}?scope=user|project[&project=<name>]`.
              `a8e56bf` — `TestSetModelBindingUserScopeWritesAndUnknownFieldsSurvive`,
              `TestSetModelBindingResponseNeverIncludesSecretValue`,
              `TestSetModelBindingProjectScopeWritesUnderProjectConfigAndLeavesUserConfigUntouched`,
              `TestSetModelBindingProjectScopeMissingProjectQueryReturns400`,
              `TestSetModelBindingProjectScopeUnknownProjectReturns404`,
              `TestSetModelBindingUnknownRoleReturns400`,
              `TestSetModelBindingUnsupportedScopeReturns400`,
              `TestSetModelBindingRequiresAuth` (cmd/cortex/serve_models_test.go).
        - [x] M4.2c2b2 Session-scope writes: in-memory only on a live
              `*managedSession`, reverts on resume — not persisted to disk
              at all (confirmed against main.go's own `/model` handler,
              which mutates only `cs.Request.Model`, no config write).
              `d40b366` —
              `TestSetModelBindingSessionScopeSetsLiveSessionModelAndRevertsOnResume`,
              `TestSetModelBindingSessionScopeUnknownSessionReturns404`,
              `TestSetModelBindingSessionScopeMissingSessionQueryReturns400`,
              `TestSetModelBindingSessionScopeUnsupportedRoleReturns400`
              (cmd/cortex/serve_models_test.go). **M4.2 complete
              (a/b1/b2/b3/c1/c2a/c2b1/c2b2 all ticked).**
- [x] M4.3 Session manager: two turns on one session serialize; turns on
      two sessions interleave (concurrency test, scripted senders). `0db0d94`
      — `TestTurnEndpointDifferentSessionsRunConcurrently`
      (cmd/cortex/serve_turn_test.go); the "one session serializes" half
      was already proven by M4.2b2's `TestTurnEndpointSameSessionSerializes`
      (same file), so this box needed only the concurrent-sessions leg.
- [x] M4.4 Cross-process lock: a REAL second process (re-exec helper
      pattern) attempting the same session gets the typed busy error.
      (`internal/fslock` itself pre-dates the loop — this item is the
      serve integration + the two-process test only; see amendment A1.)
      `9621c1d` — `TestCrossProcessSessionResumeGetsBusyError`
      (cmd/cortex/serve_lock_test.go).
- [x] M4.5 SSE event order and shape golden-tested via the `Progress`
      seam; a test asserts the serve `http.Server` sets no `WriteTimeout`.
      `ec0b476` — `TestTurnStreamEndpointGoldenFramesForMultiStepTurn`
      (cmd/cortex/serve_sse_golden_test.go),
      `TestNewServeServerSetsNoWriteTimeout` (cmd/cortex/serve_test.go).
- [x] M4.6 Serve owns no state: kill + restart re-derives every list from
      disk (restart the manager, listings identical). `63af27f` —
      `TestServeListingsIdenticalAcrossManagerRestart` (cmd/cortex/serve_restart_test.go).
- [x] M4.7 Idle sessions evict; a subsequent request re-hydrates from the
      transcript (test with a shrunk idle threshold). `1e88f5c` —
      `TestSessionManagerEvictsIdleSessionAndResumeRehydrates`,
      `TestSessionManagerZeroIdleTimeoutNeverEvicts`,
      `TestSessionManagerTouchExtendsIdleWindow`
      (cmd/cortex/serve_session_test.go). **M4 complete.**

### M5 — Web UI (P5), four screens
- [ ] M5.1 Assets under `cmd/cortex/webui/` served from `go:embed`;
      route-level test proves serving with no filesystem presence.
- [ ] M5.2 View-models built in Go and golden-tested: project dashboard,
      session transcript (from real JSONL fixtures), landscape report,
      models view (bindings + effective scope resolution).
- [ ] M5.3 The four screens render those view-models; JS bounded
      mechanically: a Go test over the embedded FS asserts each `.js`
      file ≤ 300 lines and total JS ≤ 1200 lines.
- [ ] M5.4 End-to-end smoke: start serve with a scripted sender ⇒ create
      session ⇒ POST turn ⇒ SSE stream renders ⇒ transcript page shows
      the turn. One test, full path, no live model.

## Next Up
Start M5.1: "Assets under `cmd/cortex/webui/` served from `go:embed`;
route-level test proves serving with no filesystem presence." Per GOAL.md
§2's package layout, `cmd/cortex/webui/` is new — it doesn't exist yet in
this worktree (confirm with `ls cmd/cortex/webui` before starting; if a
stray file is already there from a crashed prior attempt, read it first per
step 1). Likely shape: (1) a minimal static asset (e.g. a placeholder
`index.html`, maybe an empty `app.js`/`app.css` stub — M5.2/M5.3 fill in the
real view-model rendering later) under `cmd/cortex/webui/`, (2) a
`//go:embed` directive in a new `cmd/cortex/webui.go` (or similar) exposing
an `embed.FS`, (3) at least one route registered on the existing
`newServeMux` (serve.go) serving from that embedded FS via
`http.FileServerFS`/`http.ServeFileFS` (stdlib, no new dependency per
GOAL.md's no-new-deps non-goal), (4) the "no filesystem presence" test: an
`httptest` request against the route asserting content is served correctly
even when — per the M5.1 wording's own emphasis on go:embed's point — the
test's working directory doesn't have the source assets available (e.g. by
constructing the mux from a package-level `embed.FS` var and confirming the
route doesn't depend on `os.Getwd()`/relative paths at request time; a
concrete test technique: run the assertion from a `t.Chdir`'d-elsewhere temp
dir, or just assert the handler never calls into `os`/`ioutil` file-reading
by construction — read how `cmd/cortex/webui/` docs/cortex-web.md Phase 5
describes this before deciding). Read docs/cortex-web.md's Phase 5 section
first (GOAL.md §3 P5 already binds "hand-written HTML/CSS/JS under
`go:embed`... rendering logic lives in golden-tested Go view-models; JS is
fetch/render/SSE-append only, enforced mechanically" — M5.1 itself is just
the embed+serve plumbing, not the view-models, which are M5.2).

## How to Run / Verify
timeout 900 sh -c './scripts/check.sh && go test ./... -timeout 8m'
Repo is a Go 1.26 module; the coder binary is cmd/cortex.
Product spec: docs/cortex-web.md. Loop spec: GOAL.md (read fully first).

## Decisions Log (append-only)
- 2026-07-11: GOAL.md v1 finalized after a four-lens adversarial review
  (53 findings applied: canonical increment IDs, commit-before-reset
  recovery, gate-suspension escape, four-screen M5, userhome package,
  read-modify-write config edits, mechanical JS caps).
- 2026-07-11: this machine has no `timeout`/`gtimeout` binary on PATH.
  Verify is run as `sh -c './scripts/check.sh && go test ./... -timeout
  8m'` without the outer `timeout 900` wrapper; `go test`'s own
  `-timeout` still bounds the test phase. Exit code is unaffected —
  ground truth is still "exit 0 = green". Future iterations: don't burn
  a strike rediscovering this; just drop the `timeout 900` prefix.
- 2026-07-11: M1.1 landed `internal/userhome` (Dir/Path) and routed
  `cmd/cortex.userConfigPath()` through it. Kept `os`/`filepath` in
  config.go's imports — both remain used elsewhere in the file (verified
  via `go vet`, confirmed by check.sh's lint pass). Load-bearing check
  done via `git stash -u` (new untracked package + modified config.go)
  → `go test` fails to build `internal/userhome` and `config_test.go`'s
  behavior assertion no longer has the routed implementation; `git
  stash pop` restored state before committing.
- 2026-07-11: M1.2 landed the `BackendResolver` chain in
  `cmd/cortex/bootstrap.go` as pure logic over five small interfaces
  (`ConfigProbe`, `KeyProbe`, `OllamaProbe`, `GuidedSetup`,
  `SmokeProbe`) — no wiring into `NewCortexSession` yet (deferred to
  M1.3, which also does the config persistence GOAL.md's M1.2 row
  doesn't ask for). Chain order and per-stage `Source` strings
  ("config", "openrouter-env", "openrouter-keychain", "ollama-local",
  "openrouter-guided") are the contract M1.3's persistence and M1.5's
  greeting wiring build on — treat renaming them as a breaking change
  needing a Decisions entry. Load-bearing check: moved bootstrap.go out
  of the tree, confirmed `go test ./cmd/cortex/...` fails the build,
  moved it back.

- 2026-07-11: M1.3 landed generic read-modify-write JSON helpers
  (`cmd/cortex/configwrite.go`: `readJSONDoc`/`writeJSONDoc`/
  `setJSONPath`, operating on `map[string]json.RawMessage`) rather than
  inlining the logic into `bootstrap_persist.go`, since GOAL.md §2 names
  BOTH M1.3's backend persist and M4.2's scoped role-binding writes as
  needing the identical read-modify-write invariant — a shared,
  generically named helper avoids a near-duplicate landing in Phase 4.
  `PersistBackend` writes `backend.type` and, when set,
  `models.<roleCode>.model`; `fileConfigProbe` (the concrete
  `ConfigProbe` chain stage 1) reads them back directly from the file
  (not through `LoadConfig`/`mergeConfig`) so it sees exactly what was
  last persisted. Clarifying note on "byte-for-byte": encoding/json's
  `Marshal`/`MarshalIndent` compact/reformat whitespace even inside an
  untouched `json.RawMessage` value (verified empirically — spaces
  inside `[1, 2, 3]` are stripped on any remarshal of the parent map),
  so exact-byte formatting preservation isn't achievable via the
  map[string]any/json.RawMessage mechanism GOAL.md §2 itself prescribes;
  the tests instead assert value-equality (decode-and-compare) for
  untouched fields, which is the invariant that's actually meaningful
  and testable here (no field is dropped, truncated, or mutated). Not
  wired into `NewCortexSession` — that's a later increment per
  bootstrap.go's doc comment. Load-bearing check done by moving both new
  source files out of the package (`bootstrap_persist.go`,
  `configwrite.go`) and confirming every new test fails to build, then
  restoring them.

- 2026-07-11: M1.4 landed `IsFirstRun()` in `cmd/cortex/firstrun.go` as a
  pure predicate over two user-home paths (`userConfigPath()` from M1.1,
  and a new `greetingMarkerPath()` resolving `"greeted"` through
  `internal/userhome.Path`) — no wiring into `NewCortexSession`/`main.go`
  (that's M1.5). The marker file itself is not written by any code yet;
  M1.5's greeting turn writes it on first fire. Per GOAL.md §3 P1
  first-run, `docs/cortex-web.md`'s "no `~/.cortex/config.json` and no
  prior sessions" text is superseded — sessions are project-scoped and
  play no part; `TestIsFirstRunIgnoresSessions` pins that a populated
  `.cortex/sessions/` under a CWD-isolated workspace does not flip the
  verdict. Load-bearing check: moved `firstrun.go` out of the tree,
  confirmed `go test ./cmd/cortex/... -run TestIsFirstRun` fails to
  build (5 `undefined` errors against `firstrun_test.go`), moved it back
  and reran green.

- 2026-07-11: M1.5 landed `MaybeGreet` (`cmd/cortex/greeting.go`), wired
  into `main.go` between `session.EnableMemory()` and the read loop,
  under its own `signal.NotifyContext` (mirrors `compactNow`'s pattern) so
  Ctrl-C during a slow first greeting doesn't hang startup. `greetingPrompt`
  is a literal Go string constant asserted byte-for-byte in
  `TestGreetingPromptGolden` — no separate golden-file mechanism existed
  in the repo yet, so this establishes "golden-pinned" as "exact string
  compared in a test" for future increments (M1.7's follow-on greeting
  text, any Phase 5 view templates) rather than inventing a
  golden-file-on-disk convention GOAL.md doesn't ask for. Marker is
  written only after `Turn` returns success (tested via a 500-response
  scripted backend: `TestGreetingWritesMarkerOnlyAfterSuccess`), so a
  greeting that fails (no backend reachable yet on a truly fresh machine)
  retries on the next launch rather than silently suppressing itself
  forever. Test harness reuses the scripted-Sender-via-httptest pattern
  from `context_eval_test.go` (`cs.quiet = true` routes through
  `cs.Request.Send`, an ordinary HTTP call, so an `httptest.Server` is the
  "scripted Sender" for a full `Turn()`-level test) rather than
  `SenderFunc`+`runLoop` directly (that pattern is used by tests that
  don't need `Turn`'s transcript/marker side effects). Load-bearing check:
  moved `greeting.go` out of the tree, confirmed all four new tests fail
  to build (7 `undefined` errors against `greeting_test.go`), moved it
  back and reran green.

- 2026-07-11: M1.6 landed `pkg/secret.Store` (`Get`/`Set`) alongside the
  pre-existing `OpenRouterKey`/`LookupOpenRouterKey`/`MustOpenRouterKey`
  functions (untouched — `pkg/llm/client.go` depends on
  `secret.OpenRouterKey` and the `pkg/llm` provider-internals freeze
  means that call site isn't touched; `pkg/secret` itself is explicitly
  exempt from the freeze per GOAL.md §1). `darwinStore`/`unsupportedStore`
  implement `Store` (`keychain_darwin.go`/`keychain_other.go`, same
  build-tag split as the existing read path); `Set` shells `security
  add-generic-password -U` (update-in-place, avoids a duplicate-entry
  error on re-run) and deliberately omits stderr from its wrapped error
  since `security` can echo the `-w` value into diagnostics. `Fake` is
  the in-memory test double (`store.go`); no test constructs
  `darwinStore`/`unsupportedStore` directly — GOAL.md's "no test invokes
  the real `security` binary" bars it. Matching rule for the meta-test
  (GOAL.md left it open): parse every `_test.go` with `go/parser` and
  flag a call to `exec.Command`/`exec.CommandContext` (via `ast.Inspect`,
  matching on the `exec.` selector) carrying a string-literal argument
  whose *unquoted* value is exactly `"security"` — an AST walk avoids
  both false positives (comments, the `"security"` tag-string literals
  already in `internal/storage/storage_test.go` and `pkg/llm/llm_test.go`,
  a `const bin = "security"` passed by variable) and reliance on build
  tags (parser doesn't evaluate `//go:build`, so a violation hidden
  behind a platform constraint still trips it). Repo root is found by
  walking up from `os.Getwd()` to the nearest `go.mod`. Load-bearing
  checks (both done): (1) moved `store.go` out of the tree — confirmed
  `go test ./pkg/secret/...` fails to build (6 `undefined` errors across
  `store_test.go` and `keychain_darwin.go`), restored; (2) planted a
  synthetic `zz_violation_test.go` with `exec.Command("security", ...)`
  — confirmed `TestNoRealSecurityBinaryInvokedByTests` fails and reports
  the planted file's path, removed it, reran green.

- 2026-07-11: M1.7 landed the ask half by extending the golden-pinned
  `greetingPrompt` (asks where the user's code lives, e.g. `~/code` or
  `~/projects`) and the persist half as plain deterministic text parsing
  in a new `cmd/cortex/scanroots.go` — deliberately NOT a second LLM
  round-trip. Reasoning: the model already asked the question in its own
  reply during the single greeting `Turn`; the *answer* is the real
  human's next line of chat input in the REPL's read loop, which the
  model never sees until the next ordinary turn anyway, so there is no
  natural second scripted-backend exchange to hook — "script a reply
  naming a path" (this file's prior Next Up note) is satisfied instead by
  `TestMaybeGreetArmsAwaitingScanRootsReply`, which drives the real
  scripted-Sender-via-httptest greeting harness from `greeting_test.go`
  end-to-end through the capture/persist step. Mechanism: `MaybeGreet`
  arms a new `CortexSession.awaitingScanRootsReply` bool after a
  successful first-run greeting; `main.go`'s read loop calls the new
  `(*CortexSession).MaybeCaptureScanRoots(input)` on the very next line of
  real input, before running it as an ordinary turn (so the human's
  message is captured AND still gets a normal reply — nothing is
  swallowed). `parseScanRootsReply` extracts path-like tokens (leading
  `~`, `/`, or `./`/`../`, trailing sentence punctuation trimmed) from
  free text via regexp, so "sure, it's at ~/eng and ~/code, thanks!" and a
  bare "~/eng" both work; "no thanks" finds nothing and is treated as a
  clean decline (flag still clears — asked once, not re-asked every
  launch). Chose config key `scan.roots` (`{"scan":{"roots":[...]}}`,
  JSON array of strings) via the M1.3 read-modify-write helpers
  (`PersistScanRoots` in scanroots.go, same shape as `PersistBackend`) —
  M2.5 will read this same key. Load-bearing check done: moved
  `scanroots.go` out of the tree, confirmed `go vet ./cmd/cortex/...` and
  `go build ./cmd/cortex/...` both fail (`session.MaybeCaptureScanRoots
  undefined`, referenced from `main.go`'s read loop), moved it back and
  reran green.

- 2026-07-11: M2.1 landed `internal/landscape` (`landscape.go`) with
  three named Scanner interfaces (`HarnessScanner`, `RuntimeScanner`,
  `ProjectScanner`) — literal per-family "Scanner implementations" per
  GOAL.md's M2.1 wording — each with a default concrete implementation
  (`wellKnownHarnessScanner`/`wellKnownRuntimeScanner`/
  `gitMarkerProjectScanner`) backing the package-level
  `ScanHarnesses`/`ScanRuntimes`/`ScanProjects` funcs, composed by
  `Scan(root, caps) (Result, error)`. Detection is existence-only
  (`os.Stat`, never file contents) for harnesses/runtimes (fixed
  relative-path list under `root`, e.g. `.claude`, `.ollama`) and a
  `filepath.WalkDir` for projects (a `.git` entry plus ≥1 AI marker
  among `AGENTS.md`/`CLAUDE.md`/`.cursor`/`.cortex`; matched repos are
  not descended into further). `Caps{MaxDepth,MaxEntries,Timeout}` is
  in the signature but deliberately unenforced here — M2.4 extends
  these same functions without an API change, matching the prior
  iteration's Next Up note. Malformed-fixture choice: a broken symlink
  standing in for the well-known path / `.git` entry, which resolves
  to a "not found" `os.Stat` error and is thus treated as absent —
  chosen over chmod-based permission fixtures for cross-platform/CI
  portability. `root` here is a generic "home-equivalent" directory
  usable for both harness/runtime AND project detection against a
  single fixture tree; reconciling this with production's two distinct
  real roots (the OS `$HOME` for harnesses/runtimes vs. `scan.roots`
  persisted config for projects) is explicitly deferred to M2.5's CLI
  wiring, not resolved here. `internal/userhome` (Cortex's own
  `$CORTEX_HOME`-or-`~/.cortex` state dir) is NOT the same concept as
  the OS home directory this package's harness/runtime probes target —
  worth flagging so a later iteration doesn't conflate the two.
  Load-bearing check done: moved `landscape.go` out of the tree,
  confirmed `go test ./internal/landscape/...` fails to build (10
  `undefined` errors against `landscape_test.go`), moved it back and
  reran green.

- 2026-07-11: M2.2 landed by threading `projectscan.LoadIgnoreSet(root)`
  into `ScanProjects`'s `filepath.WalkDir` callback only —
  `ScanHarnesses`/`ScanRuntimes` were left untouched per the prior
  iteration's Next Up analysis (they check a small fixed relative-path
  list directly under `root`, not a general walk, so there's no
  equivalent leak surface to filter). Two IgnoreSet entry points used:
  `IsDirExcluded(path, name)` before descending into any directory
  (`path != root` guard so the scan root's own name is never
  self-excluding), pruning both hard-excluded names (node_modules,
  vendor, .venv, build output, …) and `.gitignore` matches; and, per
  candidate marker path, `IsDirExcluded`/`IsFileExcluded` chosen by
  `os.Stat`'s `IsDir()` (directory markers `.cursor`/`.cortex` vs. file
  markers `AGENTS.md`/`CLAUDE.md`) before counting it. `IgnoreSet` is
  loaded once per `ScanProjects` call (not per-directory) since it's
  documented read-only/shareable after construction and rooted at the
  same `root` the walk uses. `TestScanProjectsFiltersThroughIgnoreSet`
  plants two nested "hidden" projects (one under `node_modules`, one
  under a root-`.gitignore`'d `vault/` directory), each with a distinct
  planted secret file (`id_rsa`, `.env`) beside its marker, exercised
  through the public `Scan()` composition and asserted absent both
  structurally (`findProject`) and via a full `json.Marshal` substring
  check — covering the M2.2 DoD's "any scan result field" wording in
  one test. Load-bearing check done: reverted `ScanProjects` to the
  pre-fix walk (and removed the now-unused `projectscan` import) —
  confirmed the test fails, reporting both nested projects and every
  planted string present in the marshaled JSON — then restored the fix
  and reran green.

- 2026-07-11: M2.3 landed the struct-level leg only —
  `TestScanResultDoesNotLeakFileBodyContent` plants a unique sentinel in
  the BODY of an AI marker file (AGENTS.md) belonging to a project
  `Scan` legitimately REPORTS (not an excluded/hidden one — M2.2 already
  proved path-level exclusion; this test proves that even a visible,
  reported project's file *contents* never ride along), then asserts
  `json.Marshal(result)` contains it nowhere. The DoD's other two legs
  — `--json` CLI output and the `landscape.scan` journal event — don't
  have artifacts to test against yet (M2.5 builds the `cortex scan`
  subcommand and its `--json` renderer; M2.7 persists the journal
  event); fabricating either now to force-tick a three-leg box would be
  the "landing code whose behavior no test would notice reverting"
  anti-pattern GOAL.md §1 warns against, since there'd be no real
  renderer/event alongside the assertion. Ticking M2.3 on the struct leg
  is deliberate: `Result`'s only fields are names/paths (Tool.Name/Path,
  Runtime.Name/Path, Project.Path/Markers — Markers is a list of marker
  *filenames*, never bodies) and every Scanner in the package uses
  `os.Stat` exclusively, never `os.ReadFile`/`os.Open`-and-read, so the
  struct-level guarantee is the one place content-leaking is
  structurally possible today; M2.5 and M2.7 will each additionally
  serialize `Result` through a new surface (JSON text, journal payload)
  that is trivially "no more than what's already in `Result`," so this
  sentinel is the load-bearing proof and those two future increments
  inherit it rather than needing an independent re-proof. Load-bearing
  check done: temporarily teed a marker file's content into
  `Project.Markers` inside `ScanProjects` (simulating a content-leak
  regression), confirmed `TestScanResultDoesNotLeakFileBodyContent`
  fails and reports the leaked sentinel, then `git checkout --
  internal/landscape/landscape.go` to revert before committing (no
  production code changed by this increment — landscape.go's diff
  after revert is empty; only the test file was added).

- 2026-07-11: M2.4 landed cap enforcement in `ScanProjects` only
  (`ScanHarnesses`/`ScanRuntimes` untouched — conclusion reached: both
  check a small fixed-length list of well-known relative paths directly
  under root with no recursion, so none of `MaxDepth`/`MaxEntries`/
  `Timeout` has a meaningful bound to apply there; capping them would be
  a no-op with no test able to observe it, the exact anti-pattern
  GOAL.md §1 warns against). Truncation shape chosen: a single
  `Truncated bool` on `Result` (not a richer per-cap indicator) — GOAL.md
  M2.4's wording ("truncation reported... never silent") doesn't ask
  which cap tripped, just that truncation is visible, and a bool is the
  smallest shape satisfying that. This required changing
  `ProjectScanner.Scan` and `ScanProjects` to return a third `bool`
  value (`(([]Project, bool, error)`) — a signature change, but the
  prior iteration's "no API change" note was about the `Caps` parameter
  already existing, not about return arity; reporting truncation at all
  needs *some* new return surface, and threading a bool through the one
  Scanner interface that can actually trip a cap is the minimal one.
  `MaxEntries` counts every `WalkDir` callback invocation (files and
  dirs alike, matching "entries visited" literally) and stops the whole
  walk via `filepath.SkipAll` once exceeded, not just the current
  subtree. `MaxDepth` is checked only for directories (files can't be
  descended into) via a new `walkDepth(root, path)` helper (root itself
  = depth 0). `Timeout` is checked at the top of every callback via a
  precomputed deadline (`time.Now().Add(caps.Timeout)`) — the
  `TestScanProjectsCapsTimeout` test uses `time.Nanosecond` deliberately
  (guaranteed already-elapsed by the time the walk's first callback
  fires on any real machine) rather than a larger "should be tight
  enough" duration, to keep the test deterministic across CI/disk
  speeds instead of racing wall-clock scan time against a fixed bound.
  A zero `Caps{}` value still applies no bound (every cap check is
  guarded by `> 0`/`!deadline.IsZero()`), so all of M2.1–M2.3's existing
  tests needed no behavior changes — only a mechanical `found, _, err :=`
  three-value update at their `ScanProjects` call sites. Load-bearing
  check done: temporarily gutted all three cap checks in `ScanProjects`
  down to an unconditional `entriesVisited++` (keeping the signature
  compiling), confirmed all three new tests fail with the expected
  "truncated = false, want true" / "contains ... want ... pruned"
  messages, then restored the real implementation from a pre-edit copy
  and reran the full package green.

- 2026-07-11: M2.5 landed `cortex scan [--json] [--root <path>]`
  (`cmd/cortex/scan.go`), resolving the "two distinct real roots" split
  M2.1's Decisions note deferred here: `buildScanReport(homeDir, roots,
  caps)` calls `landscape.ScanHarnesses`/`ScanRuntimes` once against
  `os.UserHomeDir()` and `landscape.ScanProjects` once per entry in
  `roots` (M1.7's persisted `scan.roots`, or a single-element override
  from `--root`), aggregating into a new CLI-level `ScanReport` — chose
  a new aggregate type over changing `landscape.Scan`'s signature since
  `internal/landscape`'s own tests (M2.1-M2.4) already pin `Scan(root,
  caps)` as single-root and GOAL.md's package-layout section describes
  it that way; the two-root-kind composition is CLI-layer concern only.
  `resolveScanRoots`/`readScanRoots` reuse `readJSONDoc` (M1.3's
  read-modify-write helper) directly against the `scan.roots` key
  `PersistScanRoots` (M1.7) writes — no new config-reading abstraction.
  Typed refusal is `ErrNoScanRoots`, a package-level sentinel checked
  via `errors.Is` (both "file doesn't exist" and "file exists but has
  no scan key" refuse identically — tested). `renderScanReport`'s exact
  text layout is golden-pinned the same way `greetingPrompt` is (a Go
  string literal compared byte-for-byte in a test), continuing M1.5's
  established "golden-pinned = literal string in a test" convention
  rather than inventing an on-disk golden-file mechanism. Also closed
  M2.3's deferred `--json` content-non-leak leg here (`TestScanReport
  JSONRoundTrip` plants a sentinel in a marker file's body and asserts
  it's absent from the marshaled `ScanReport` JSON) — natural fit since
  `--json` is exactly the new surface this increment adds; the
  `landscape.scan` journal-event leg remains deferred to M2.7 as noted
  at M2.3's ticking. Manually verified end-to-end against the real
  filesystem (not just fixtures): `cortex scan` with no config refuses;
  `cortex scan --root <fixture>` against the real `$HOME` correctly
  found this machine's actual `~/.claude`/`~/.codex`/`~/.ollama`
  alongside the fixture project. Load-bearing check done: forced
  `resolveScanRoots` to never return `ErrNoScanRoots` (`if false &&
  len(roots) == 0`), confirmed `TestResolveScanRootsRefusesWhenNeither`
  fails both its sub-assertions, reverted from a pre-edit copy and
  reran the full package green.

- 2026-07-11: M2.6 landed `internal/tools/scan_landscape.go` calling
  `landscape.ScanHarnesses`/`landscape.ScanRuntimes` directly against a
  resolved home directory (`homeDirFunc`, overridable in tests — defaults
  to `os.UserHomeDir`), NOT `cmd/cortex/scan.go`'s `buildScanReport`/
  `resolveScanRoots`: confirmed via `go list -deps` that `internal/tools`
  importing `cmd/cortex` would create a cycle (`cmd/cortex` already
  imports `internal/tools` for the tool declarations), so the tool calls
  `internal/landscape` directly — a clean leaf import, same tier as
  `internal/outline`/`internal/shellrisk` which `internal/tools` already
  imports. Deliberately calls ONLY `ScanHarnesses`/`ScanRuntimes`, never
  `ScanProjects`/`landscape.Scan`: running `ScanProjects` with the home
  directory itself as root would be exactly the blind-`$HOME`-sweep
  GOAL.md's D3 forbids (project discovery is scan-roots-gated, per M2.5's
  `resolveScanRoots`/`ErrNoScanRoots`) — `TestScanLandscapeNeverWalksProjectsUnderHome`
  pins this by planting a `.git`+`AGENTS.md` project directly under the
  fixture home and asserting it never appears in the tool's output.
  "Registered, gated ... absent ⇒ registered, false ⇒ absent" (GOAL.md's
  exact wording) is implemented as the DELETE pattern, not the WEB
  pattern: researched both existing gates first and found `enable_web`
  only gates *execution* (inside `Execute`'s `IsToolEnabled` check) while
  the declaration stays in `internal/tools.All`/`req.Tools` always,
  whereas `allow_delete` truly removes the tool from `req.Tools` via
  `toolsExcept` in `NewCortexSession`. GOAL.md's "absent" language means
  truly absent from the registered set, so `scan_landscape` gets BOTH:
  `toolsExcept(req.Tools, tools.FunctionScanLandscape)` in
  `NewCortexSession` when `!cfg.scanEnabled()` (the real removal) plus an
  `IsToolEnabled` case (defense in depth, matching `EnableWeb`'s existing
  shape, in case a tool call reaches `Execute` some other way). New
  `Config.scanEnabled()` method mirrors `deleteEnabled()` exactly (nil
  config or nil field ⇒ true; explicit false ⇒ false); `EnableScan *bool`
  added to `ToolConfig` alongside `EnableWeb`, wired into `mergeTools`.
  `scan_landscape` is coder-only (not in `Study.Tools`), matching
  `web_search`/`fetch_url`'s "privacy/consent-sensitive tools aren't
  available to the read-only subagent" precedent — GOAL.md doesn't say
  this explicitly for M2.6 but the docs/cortex-web.md framing ("so the
  greeting conversation can run it on user consent") implies a
  human-in-the-loop coder action, not something the bounded study
  subagent should reach for on its own. `scanLandscape()` takes no
  `ToolCall`/`ToolDeps` params today (zero-arg tool, `objectSchema(map[string]any{})`)
  since M2.6 is scan+render only; M2.7 will need to add a `deps ToolDeps`
  param when it wires the memory-note write (see Next Up) — flagged
  there so the next iteration doesn't design around the wrong signature.
  Load-bearing check done: moved `internal/tools/scan_landscape.go` out
  of the tree, confirmed `go test ./internal/tools/...` fails to build
  (11 `undefined` errors spanning `tools.go`'s `All`/`Execute` references
  and every new test), restored and reran green; also confirmed the two
  `cmd/cortex` gating tests fail to build against the pre-edit `Config`/
  `ToolConfig` (8 `undefined` errors: `scanEnabled`, `EnableScan`) before
  landing those changes.

- 2026-07-11: M2.7 landed `internal/journal`'s first user-level journal
  instance (`AppendLandscapeScan`, `internal/journal/landscape.go`) by
  having the journal package itself depend on `internal/userhome` to
  resolve the class dir (`journal → userhome`, both leaf-ish packages,
  no cycle) — chosen over duplicating the userhome-resolution +
  `journal.NewWriter` open/append/close sequence separately in both
  `cmd/cortex/scan.go` and `internal/tools/scan_landscape.go` (which
  can't import each other per M2.6's cycle note), since a single shared
  helper is the one-write-path GOAL.md pillar 3 asks for. `scanLandscape`
  widened from a zero-arg function to `scanLandscape(deps MemoryStore)`
  (Interface-Segregated, not the full `ToolDeps`) so it can call
  `deps.MemoryWrite("landscape", result)` identically to the
  `memory_write` tool's own dispatch; the `Execute` switch call site
  changed to `scanLandscape(deps)`. Journal writes on both legs are
  best-effort (swallowed on error — telemetry, matches
  `emitSessionMetrics`'s existing convention); the memory-note write
  propagates its error (matches `memoryWrite`'s existing contract) —
  `TestScanLandscapeMemoryWriteFailurePropagates` pins this asymmetry.
  "Headless scan writes no note" is proved by construction (`scan.go`
  never imports `internal/memory`) plus a source-text meta-test
  (`TestScanCLISourceNeverImportsMemory`), mirroring the AST-scan idiom
  `pkg/secret`'s M1.6 meta-test established for a different forbidden
  call. `internal/tools/scan_landscape_test.go` and
  `cmd/cortex/scan_test.go` are both modified here but neither existed
  at the branch genesis commit (confirmed via `git cat-file -e
  <genesis>:<path>`, both fail — "did not exist at genesis") so this is
  not a standing-regression-guard violation. Load-bearing checks done:
  (1) moved `internal/journal/landscape.go` out of the tree — confirmed
  `go vet` fails to build all three of `internal/journal`,
  `internal/tools`, and `cmd/cortex` (the three dependents), restored;
  (2) temporarily removed `scanLandscape`'s `deps.MemoryWrite` call —
  confirmed `TestScanLandscapeWritesLandscapeMemoryNote` and
  `TestScanLandscapeMemoryWriteFailurePropagates` both fail, restored
  from a pre-edit copy and reran the full suite green.

- 2026-07-11: M3.1 landed `Workspace{Root}` (`cmd/cortex/workspace.go`)
  with two constructors — `WorkspaceFromCWD()` (reuses the pre-existing
  `findUp(".cortex")` upward search verbatim, so it's bit-identical to
  today's `contextDir()`) and `NewWorkspace(root string)` (explicit root,
  no search — the leg `--project`/M3.5 needs). Deliberately did NOT
  change `CortexArgs.Request()`'s signature to thread a `Workspace`
  through it (it currently calls the free, CWD-implicit
  `projectInstructions()`): `CortexArgs{}.Request()` is called zero-arg
  in ~30 existing test files across the package, and forcing a
  `Workspace` parameter through it now would be a mechanical, purely
  test-plumbing change with zero behavioral payoff until M3.5 actually
  has a non-CWD root to feed it — better to make that signature change
  once, at M3.5, alongside the real `--project` wiring, than twice.
  Extracted a shared `readInstructions(path string) string` body
  (`config.go`) so `projectInstructions()` (free, `findUp`-based) and
  the new `Workspace.Instructions()` (explicit path, no search) are
  PROVABLY the same logic, not just tested-to-currently-agree — a test
  additionally asserts they return identical output for the same
  resolved path. `CortexSession` gained a `workspace *Workspace` field
  (set via `WorkspaceFromCWD()` in `NewCortexSession`) plus
  `cs.ContextDir()`/`cs.SessionsDir()` methods that fall back to the old
  free functions when `cs.workspace == nil` — every hand-constructed
  `&CortexSession{...}` literal in the existing test suite (no test
  calls `NewCortexSession()` directly, confirmed via grep) leaves
  `workspace` nil, so those tests are provably unaffected; only the real
  `NewCortexSession()` path (exercised by manual `go build`/run, not by
  any test) gets the new resolution. Updated the `cs`-receiver call
  sites that had CWD-relative resolution inlined (`session.go` ×4,
  `session_runtime.go` ×3, `tool_deps.go`'s `Recall` ×2, `main.go` ×2)
  to go through `cs.ContextDir()`/`cs.SessionsDir()` instead of the free
  functions directly — confirmed via `grep -n "cs \*CortexSession"`
  immediately preceding each edited line before editing, not just by
  pattern-matching the call text. `study.go`'s `root()` (the
  `ConfinePath` root for the Study subagent) gained a
  `cs.workspace.Root` fallback BELOW `deleteRoot` (which
  `NewCortexSession` always sets to `abs(".")` by default) — a no-op
  for every normally-constructed session; only affects a
  hand-constructed session with neither `deleteRoot` nor `workspace`
  set, where it still falls through to the pre-existing literal `"."`.
  Load-bearing check done: moved `workspace.go` out of the tree,
  confirmed `go vet ./cmd/cortex/...` fails to build
  (`session_core.go:44: undefined: Workspace`), restored and reran the
  full verify suite green. Standing-regression-guard check done: `git
  diff --name-only <genesis>..HEAD -- '*_test.go'` lists only the new
  `workspace_test.go` (not present at genesis) — no pre-existing test
  file was touched.

- 2026-07-11: M3.2 landed the escape-attempt table in
  `cmd/cortex/workspace_test.go` (`TestNewWorkspaceConfinePathRejectsEscapes`,
  new — `workspace_test.go` itself postdates genesis, so extending it isn't a
  standing-regression-guard violation; the pre-existing
  `internal/tools/confine_test.go` was left untouched) built specifically
  against `NewWorkspace(root)` per GOAL.md's M3.2 wording, covering: absolute
  paths, `..`-prefixed relatives, double-parent, deep traversal that
  collapses through the root's parent, and — the case the prior iteration's
  Next Up flagged as a suspected real gap — a symlink planted inside root
  pointing to a sibling temp dir, exercised both against an existing and a
  nonexistent target file. The symlink subtests genuinely failed against the
  pre-fix `ConfinePath` (confirmed by running the new test before touching
  `confine.go`): `filepath.Abs`+`Clean`+`Rel` is purely lexical and never
  touches the filesystem, so `root/escape/secret.txt` where `escape` is a
  symlink to an outside directory passed the old containment check (the
  joined path is lexically still under root even though it resolves
  elsewhere). Fixed in `internal/tools/confine.go` by adding a second
  containment check on REAL paths: a new `resolveSymlinks(path)` helper
  walks up to the deepest existing ancestor via `filepath.EvalSymlinks`
  (which itself requires the full path to exist) when the target doesn't
  exist yet, then rejoins the missing tail literally — needed because
  `ConfinePath` must still bound-check paths for tools whose target hasn't
  been created (the nonexistent-symlink-target subtest exercises exactly
  this). Kept the original lexical check in place rather than replacing it
  (belt-and-suspenders; the two are redundant for the non-symlink cases but
  cheap, and only the resolved check is new/load-bearing for symlinks).
  Confirmed no behavior change for every pre-existing `confine_test.go` case
  (all four still pass unmodified) and for `TestWorkspaceFromCWDMatchesExplicitRootConfinement`'s
  existing single-escape case. Load-bearing check done as above (red before
  the `confine.go` fix, specifically on the two symlink subtests only —
  the four non-symlink subtests already passed pre-fix, which is expected
  since they don't touch the new code path).

- 2026-07-11: M3.3 landed `internal/registry` (`registry.go`) as a plain
  `[]Project` array serialized whole to `projects.json` under
  `internal/userhome` — no per-project subdirectory or index file, matching
  D5's "pointer-only, rebuildable" framing and keeping CRUD a simple
  read-all/mutate/write-all cycle (the registry is expected to stay small:
  one entry per locally registered project, not a scale concern). Exposed
  BOTH a `Registry` interface (`List`/`Lookup`/`Save`/`Remove`) — per
  docs/cortex-web.md's explicit "Registry is an interface (lookup/list/
  save)" — and the concrete `*FileRegistry` implementing it, pinned by a
  compile-time `var _ Registry = (*FileRegistry)(nil)` assertion in the
  test file so a future signature drift is caught immediately; M3.4/M3.5
  should accept `Registry` (the interface) in their function signatures,
  not `*FileRegistry`, so CLI-level tests can fake it instead of touching
  disk. `Save` upserts by `Name` (update in place if found, else append) —
  GOAL.md's M3.3 wording is silent on upsert-vs-append-only, but "CRUD"
  implies update, and `cortex project add` (M3.4) re-running on an already-
  registered name should update rather than error or duplicate. Chose
  `ErrProjectNotFound` (checked via `errors.Is`, message includes the
  name via `%w: %s`) mirroring `ErrNoScanRoots`'s exact sentinel-error
  shape from M2.5's `scan.go` — established convention for "typed error"
  across this codebase rather than a custom struct type. A registry file
  that doesn't exist yet is NOT an error anywhere (`List` returns empty,
  `Lookup`/`Remove` return `ErrProjectNotFound` same as an existing-but-
  empty file) — deliberate: `cortex project list` before any project has
  ever been registered must not surface a spurious "file not found," and
  `internal/registry` has no `Init`/bootstrap step other tools need to call
  first. Added `NewAt(path string) *FileRegistry` (bypasses
  `internal/userhome`) alongside `New()` even though M3.3's own tests don't
  need it — flagged here rather than left undiscovered: M3.4/M3.5's CLI
  tests will likely want a registry rooted at an arbitrary temp path
  without going through `$CORTEX_HOME`, and `internal/userhome`'s own
  precedent (`Path(elem...)` is a thin wrapper over `Dir()`) suggested
  exposing the explicit-path constructor alongside the resolved one now
  rather than reopening this file later. Load-bearing check done: moved
  `registry.go` out of the tree, confirmed `go test ./internal/registry/...`
  fails to build (10 `undefined` errors against `registry_test.go`),
  restored and reran the full verify suite green.

- 2026-07-11: M3.4 landed `cmd/cortex/project.go` (`cortex project
  add/list/remove`) plus `--register` on `cortex scan`. Followed
  M2.5/M2.6's established "CLI-level tests" precedent literally: none of
  `runProjectCLI`/`runScanCLI` (the `os.Exit`-driving dispatch wrappers)
  are directly unit-tested; the pure functions they compose
  (`addProject`, `removeProject`, `renderProjectList`,
  `registerDiscoveredProjects`) carry the coverage, matching how
  `resolveScanRoots`/`buildScanReport`/`renderScanReport` were tested for
  M2.5 rather than `runScanCLI` itself — GOAL.md's "CLI-level tests"
  wording is read as "tests of the logic behind the CLI command," not
  "tests that invoke `main()`." All four (`add`/`list`/`remove`/
  `--register`) were additionally exercised manually end-to-end against
  a real built binary + `$CORTEX_HOME`-redirected temp dir (add, list,
  scan --register upserting a second discovered entry, remove both,
  confirm `projects.json` empties to `[]`) — not just fixture tests —
  since this increment's whole value is a real shell-usable command
  surface. `addProject` resolves its `root` argument via `filepath.Abs`
  (relative to CWD) before persisting, since a user typing `cortex
  project add blog .` expects the stored root to survive a later `cd`;
  `TestAddProjectResolvesRelativeRootToAbsolute` uses `t.Chdir`
  (Go 1.24+, already precedented in `workspace_test.go`) to pin this
  deterministically rather than asserting only on already-absolute
  `t.TempDir()` paths. `registerDiscoveredProjects` keys registry entries
  by `filepath.Base(landscape.Project.Path)` (the discovered project
  directory's own name) and treats a registration failure as a
  non-fatal warning on `cortex scan --register` (the scan result itself,
  already computed, is still valid and printed either way) — same
  best-effort-telemetry posture M2.7's `recordLandscapeScan` established
  for the journal write, chosen over making `--register` fatal since a
  user running `--register` still wants to see what was found even if
  the registry file happens to be momentarily unwritable. Reused
  `Registry.Save`'s documented upsert-by-name semantics (M3.3) directly
  for re-running `--register` over an unchanged tree — no new dedup
  logic needed, pinned by `TestRegisterDiscoveredProjectsUpsertsOnRerun`.
  Load-bearing check done: moved `project.go` out of the tree, confirmed
  `go vet ./cmd/cortex/...` fails to build (`main.go:272: undefined:
  runProjectCLI` — main.go's new dispatch branch depends on it, which
  transitively also fails every test in the package including the new
  ones), restored and reran the full verify suite green.

- 2026-07-11: M3.5 landed `--project <name>` on `turn`, `resume`, and
  `study` via a new `cmd/cortex/project_workspace.go`: `parseProjectFlag`
  (extracts `--project <name>` from any position in a command's args, pure
  and shared by all three entry points so each composes it with its own
  flags/positionals independently), `applyProjectByName(cs, reg, name)`
  (resolves via `registry.Registry.Lookup`, builds `NewWorkspace(p.Root)`,
  and re-targets an already-constructed session: `cs.workspace` AND
  `cs.deleteRoot` both become the project root — NOT just `cs.workspace`,
  because `cs.root()` (study.go, M3.1) checks `deleteRoot` FIRST and
  `NewCortexSession()` always sets it to `abs(".")` by default, so leaving
  it alone would make study's `ConfinePath` confinement silently stay
  pinned to CWD while `ContextDir()`/`SessionsDir()` correctly followed the
  project — confirmed this was the real gap by reading `study.go`'s `root()`
  precedence chain before writing the fix, not by trial and error), and
  `applyProjectFlag(cs, name)` (the no-op-when-empty convenience wrapper
  each CLI entry point actually calls, opening the registry itself so the
  pure `applyProjectByName` stays fake-registry-testable). Extracted
  `systemPromptContent(instructions string) string` out of
  `CortexArgs.Request()` (session_core.go) — a pure behavior-preserving
  refactor (same `projectInstructions()` call, same string-building, just
  factored out) so `--project`'s workspace-aware instructions
  (`ws.Instructions()`) and the CWD-implicit path build the identical
  system-prompt shape from one source, closing the gap M3.1's Decisions Log
  flagged ("still used by CortexArgs.Request() until --project wiring lands
  in M3.5"). Deliberately did NOT change `Request()`'s own signature or its
  `projectInstructions()` call — only `applyProjectByName` rebuilds
  `cs.Request.Messages[0].Content` post-construction — since a signature
  change would touch ~30 existing zero-arg `Request()` call sites for zero
  behavioral payoff (same reasoning M3.1 used to defer the change here, but
  applied conservatively: the *shared body* moved, not the call sites).
  `study`'s CLI dispatch (main.go) now reads
  `cortex study [--project <name>] <path> [goal...]`; `resume`'s reads
  `cortex resume [--project <name>] [id]`; `turn`'s gained a `--project`
  case alongside its existing `--session`/`--json` switch
  (`cmd/cortex/cli.go`). Test mirrors M3.1's CWD-vs-explicit-root
  equivalence pattern exactly, per the prior Next Up note: register a
  fixture repo (reusing `workspace_test.go`'s `newFixtureRepo`/
  `resolvedPath` helpers, same package) under a name via
  `registry.NewAt(tempPath)`, apply it from an *unrelated* CWD (`t.Chdir`
  into an empty temp dir with no `.cortex`/`AGENTS.md` anywhere up its
  chain), then assert the result matches what `WorkspaceFromCWD()` would
  give had the process actually launched from inside the fixture — Root,
  ContextDir, `cs.root()` (the confinement root), and the system prompt
  carrying the fixture's AGENTS.md content, all compared. Manually verified
  end-to-end against a real built binary (mirrors M3.4's precedent): `cortex
  project add blog <fixture>` then, from `/tmp` (unrelated CWD), `cortex
  study --project blog . "..."` and `cortex turn --project blog "hi"` both
  resolved the project and proceeded to the network boundary (refused only
  by "connection refused" against a nonexistent local backend — expected,
  no live-model calls in this environment); `cortex turn --project
  doesnotexist "hi"` failed fast on `registry.ErrProjectNotFound` before
  any network attempt, confirmed by exit code 1 and no dial-attempt log
  line. Load-bearing check done: moved `project_workspace.go` out of the
  tree, confirmed `go vet ./cmd/cortex/...` fails to build
  (`cli.go:32: undefined: applyProjectFlag`), restored and reran the full
  verify suite green. Standing-regression-guard check done: no
  genesis-present `_test.go` file appears in `git diff --name-only
  <genesis>..HEAD -- '*_test.go'` (only the new `project_workspace_test.go`
  does).

- 2026-07-11: M4.1 landed `cmd/cortex/serve.go` — `newServeServer(addr,
  handler)` binds via `net.Listen("tcp", addr)` itself (not
  `(*http.Server).ListenAndServe`) so the real, already-bound
  `net.Listener` is available to assert loopback-ness against directly,
  per GOAL.md's explicit "test on the constructed server, not httptest"
  wording — `TestNewServeServerListenerIsLoopback` parses
  `ln.Addr().String()` via `netip.ParseAddrPort` and checks
  `.Addr().IsLoopback()`. `generateServeToken` mirrors
  `pkg/cliout/envelope.go`'s existing `crypto/rand`+`encoding/hex`
  pattern (32 random bytes, hex-encoded) rather than inventing a new
  token shape. `writeServeToken` reuses `configwrite.go`'s `0o600`
  user-only-write posture (the closest existing precedent — grepped for
  `0600`/`0o600` across the repo first; `internal/fslock` itself writes
  lock files at `0o644`, so "fslock's mode-0600 pattern" in the prior
  Next Up note was inaccurate — configwrite.go is the real precedent,
  noted here so a later iteration doesn't go looking for a 0600 write in
  fslock and not find it). `authMiddleware` checks the literal
  `"Bearer " + token` string (no separate scheme-parsing) — GOAL.md M4.1
  only requires reject/accept, not partial-header diagnostics.
  `newServeMux` registers exactly one placeholder route
  (`/api/health`) solely to prove the middleware end-to-end; M4.2 adds
  the real surface (a Decisions entry flagged this explicitly so M4.2
  doesn't mistake `/api/health` for part of the spec's endpoint list).
  Token is regenerated fresh every `cortex serve` run (not persisted
  across restarts) — GOAL.md's wording ("bearer token generated at
  start") matches this, and D6/D7 in docs/cortex-web.md say nothing
  about token stability across restarts. Manually verified end-to-end
  against a real built binary + `$CORTEX_HOME`-redirected temp dir:
  `cortex serve --port 17433` bound loopback, wrote `serve.token` mode
  0600, and `curl` against `/api/health` returned 401 with no
  Authorization header, 401 with a wrong bearer value, and 200 with the
  correct token read back from the written file. Load-bearing check
  done: moved `serve.go` out of the tree, confirmed `go vet
  ./cmd/cortex/...` fails to build (`main.go:296: undefined:
  runServeCLI`), restored and reran the full verify suite green.
  Standing-regression-guard check done: `serve_test.go` is new (not
  present at genesis, not a modification of any pre-existing test file).

- 2026-07-11: split M4.2 into M4.2a (read-only project + session listing,
  this iteration) / M4.2b (session lifecycle + turn + SSE) / M4.2c
  (landscape + models) — GOAL.md §7 step 4's split allowance, following
  the exact grouping the prior Next Up note already flagged. M4.2a landed
  `cmd/cortex/serve_routes.go`: `handleListProjects(reg
  registry.Registry)` and `handleListProjectSessions(reg)`, registered on
  `newServeMux`, which now takes `reg registry.Registry` as a parameter
  (was zero-arg in M4.1) — `runServeCLI` constructs it via
  `registry.New()`. Session listing reuses `listSessions`/`loadSession`
  (session.go, pre-existing, unmodified) directly against
  `NewWorkspace(project.Root).SessionsDir()` — deliberately does NOT
  construct a `*CortexSession` or touch `internal/fslock`, since
  `NewCortexSession()` is CLI-coupled (parses `os.Args`) and no live
  session or lock is needed for a read-only peek; that begins at M4.2b.
  Introduced `sessionSummary` (serve_routes.go) as a distinct
  JSON-tagged type rather than adding json tags to the pre-existing
  `sessionInfo` (session.go) — keeps the wire shape decoupled from the
  REPL-internal type and avoids touching a genesis-adjacent file for a
  serve-only concern; `sessionInfo`→`sessionSummary` is a direct
  identical-field-set struct conversion (`sessionSummary(info)`), which
  golangci-lint's staticcheck (S1016) require over a field-by-field
  literal. A project with no `.cortex/sessions/` directory yet (never
  run) returns `[]` (checked via `errors.Is(err, os.ErrNotExist)` on
  `listSessions`' wrapped `os.ReadDir` error) rather than an error — an
  unstarted project having zero sessions isn't exceptional. Manually
  verified end-to-end against a real built binary: registered a fixture
  project via `cortex project add`, started `cortex serve`, and curled
  `/api/projects` (200, correct root) and
  `/api/projects/<name>/sessions` (200, correct id/messages/first from a
  planted transcript fixture) with a valid token, 401 with none, and 404
  for an unregistered project name. Load-bearing check done: moved
  `serve_routes.go` out of the tree, confirmed `go vet ./cmd/cortex/...`
  fails to build (`serve.go:107: undefined: handleListProjects`),
  restored and reran the full verify suite green. Standing-regression-
  guard check done: only `serve.go` (non-test) was modified among
  genesis-predating files; `serve_routes.go`/`serve_routes_test.go` are
  new.

- 2026-07-11: split M4.2b (session lifecycle + turn + SSE) into M4.2b1
  (SessionManager create/resume, this iteration) / M4.2b2 (turn) / M4.2b3
  (SSE) — GOAL.md §7 step 4's split allowance, following the prior Next Up
  note's own "consider landing SessionManager + create/resume first,
  turn+SSE second" suggestion. M4.2b1 landed `cmd/cortex/serve_session.go`:
  `SessionManager{reg, newSession sessionFactory}` where `sessionFactory
  func() *CortexSession` is the injectable seam (mirrors `handleListProjects`
  taking `registry.Registry`, M4.2a's own small-interface precedent) — tests
  inject `hermeticSessionFactory()` (`&CortexSession{quiet:true, Request:
  CortexArgs{}.Request()}`, the exact pattern `greeting_test.go` established
  for a scripted-Sender-free session); production wires `newProductionSession`
  (`NewCortexSession()` + `quiet=true`), deliberately NOT a new pure
  constructor — investigated whether `NewCortexSession`'s `os.Args` coupling
  actually mattered first: `CortexArgs.Request()`'s receiver is provably
  unused in its body (confirmed by reading it), so `NewCortexSession()`'s
  behavior is independent of `os.Args` content; its only real
  server-unfriendliness is stdout prints (model-discovery/swap-group
  warnings), which `discord.go`'s existing `NewCortexSession()` call already
  accepts as a known wart — not worth a parallel constructor to dodge cosmetic
  noise the codebase already lives with. `Create`/`Resume` both delegate
  project resolution/targeting to `applyProjectByName` (M3.5,
  `project_workspace.go`) rather than duplicating registry lookup + Workspace
  construction — the exact reuse GOAL.md pillar 3 asks for, and it already
  returns `registry.ErrProjectNotFound`-wrapped errors the HTTP handler maps
  to 404 the same way M4.2a's session-listing handler does. `Resume` checks
  the live map first (`Get`) before ever calling `ResumeTranscript` — calling
  it twice on an id this process already holds open would be redundant at
  best and risks fighting the process's own `internal/fslock` hold at worst;
  `TestSessionManagerResumeOfLiveSessionReturnsSamePointerWithoutReopening`
  pins this. `TestSessionManagerResumeRehydratesFromTranscriptAfterRestart`
  proves the GOAL.md M4.6 "serve owns no state" invariant one layer down
  from the eventual serve-process-restart test: two independent
  `SessionManager` instances sharing the same fixture project resolve the
  same id to the same message count purely from the transcript file. Endpoint
  shape: single `POST /api/projects/{name}/sessions` route, folding resume
  into create via an optional JSON body `{"resume":"<id>"}` — chosen over a
  second `.../sessions/{id}/resume` route (the Next Up note's other option)
  to keep the lifecycle surface to one endpoint; an empty/absent body
  (`io.EOF` from `json.Decode`) is treated as "create fresh", not an error.
  The per-session turn-serializing mutex GOAL.md §3 P4 calls for ("the
  discord mutex generalized") is deliberately NOT on `managedSession` yet —
  landing an unused `sync.Mutex` field failed `check.sh`'s lint gate
  (`unused: field mu is unused`) since nothing in this increment calls
  Lock/Unlock; M4.2b2's turn handler is the first real caller, so it lands
  there instead of being speculatively added now (confirmed by trying it
  first: `golangci-lint` flagged it, removing the field made the gate green
  again). `newServeMux` gained a `*SessionManager` parameter (was
  `registry.Registry`-only); all M4.2a test call sites needed updating — a
  new `testSessionManager(reg)` helper in `serve_routes_test.go` wraps
  `hermeticSessionFactory()` so those listing-only tests don't need to know
  about session construction at all. Manually verified end-to-end against a
  real built binary + `$CORTEX_HOME`-redirected temp dir (mirrors M4.1/M3.4's
  precedent): `cortex project add blog <fixture>` then `cortex serve`,
  curled `POST /api/projects/blog/sessions` (200, fresh id, transcript file
  appeared on disk, listed correctly by the M4.2a listing endpoint),
  restarted `cortex serve` as a second process and curled the same route
  with `{"resume":"<id>"}` (200, `resumed:true`, same id — the two-process
  "serve owns no state" case, not just the two-manager-instance unit test),
  and `POST /api/projects/doesnotexist/sessions` (404). Load-bearing check
  done: moved `serve_session.go` out of the tree, confirmed `go vet
  ./cmd/cortex/...` fails to build (`serve.go:104: undefined:
  SessionManager`), restored and reran the full verify suite green (twice —
  once before, once after the mutex-field lint fix). Standing-regression-
  guard check done: `git diff --name-only <genesis>..HEAD -- '*_test.go'`
  includes `serve_routes_test.go`/`serve_test.go` (both confirmed via `git
  cat-file -e <genesis>:<path>` to not exist at genesis — first landed by
  M4.1/M4.2a within this loop, not pre-existing files) plus the new
  `serve_session_test.go`; no genesis-predating test file was touched.

- 2026-07-12: M4.2b2 landed `handleTurn` (`cmd/cortex/serve_turn.go`):
  `POST /api/projects/{name}/sessions/{id}/turn` runs `ms.cs.Turn(ctx,
  body.Input)` against the live `*managedSession` `SessionManager.Get`
  returns for `id` — unknown/not-currently-live id is a 404 (does NOT
  implicitly `Resume` from disk; the existing `POST .../sessions` with
  `{"resume":"<id>"}`, M4.2b1, is the explicit way to bring a session live
  first). Request/response shape (designer's call, GOAL.md left it open):
  `{"input":"..."}` in, `{"reply":"...","interrupted":false}` out —
  `turnResponse` is field-for-field identical to the pre-existing
  `TurnResult` (turn.go) so `writeJSON(w, 200, turnResponse(result))` is a
  direct struct conversion (golangci-lint's staticcheck S1016 requires this
  over a field-by-field literal, matching M4.2a's `sessionSummary(info)`
  precedent). The per-session turn-serializing mutex GOAL.md §3 P4 calls for
  ("the discord mutex generalized: one turn at a time per session, different
  sessions concurrent") landed on `managedSession` itself (`mu
  sync.Mutex`, `serve_session.go`) exactly where M4.2b1's Decisions entry
  said it would, now that `handleTurn` is the real caller (`ms.mu.Lock()` /
  `defer ms.mu.Unlock()` around the `Turn` call, held for the whole turn —
  not just the map lookup, since the race being guarded against is two
  goroutines mutating `cs.Request.Messages`/`cs.ws` concurrently, not the
  `SessionManager`'s own map, which already has its own separate `mu`).
  `TestTurnEndpointSameSessionSerializes` is the load-bearing proof: a
  scripted backend (`turnTestBackend`) tracks concurrent in-flight requests
  (sleeps 20ms mid-request to give a real race a window) and asserts
  max-concurrency 1 across two goroutines POSTing to the SAME session id.
  Confirmed load-bearing by deleting the `ms.mu.Lock()`/`Unlock()` pair:
  the test failed on both re-runs (`2 concurrent requests ... want 1`) and,
  on top of that, `internal/cache.(*WorkingSet).AddTurn` PANICKED with
  "turn spans must be contiguous" — an actual unsynchronized-mutation crash,
  not just a slow assertion — which is strong independent evidence the
  guarantee is real, not test-artifact luck; restored and reran green
  (twice, plus `go test -race -count=3` on the whole
  `TestTurnEndpoint*` group afterward — 0 races). The cross-session-PARALLEL
  half of GOAL.md's guarantee (two DIFFERENT sessions run concurrently, not
  serialized against each other) remains M4.3's dedicated DoD, deliberately
  not proven here — this increment only proves the same-session-serializes
  half, which is what justifies introducing the mutex at all. `handleTurn`
  intentionally does not use the `{name}` path segment (no project-match
  check against the live session) — `SessionManager` keys purely by session
  id today and `mgr.Get(id)` is the same lookup the M4.2b1 lifecycle
  endpoints already trust; adding a name/project cross-check would be new
  unrequested logic with no test asking for it (GOAL.md §1's "code no test
  would notice reverting" anti-pattern) — flagged here in case a later
  increment decides a mismatched `{name}` should be a 404/409. Load-bearing
  check for the new test's build-red state also done conventionally: wrote
  the test file first (referencing `turnResponse`/`handleTurn`, neither
  existing yet), confirmed `go test ./cmd/cortex/... -run TestTurnEndpoint`
  failed to build (`undefined: turnResponse`), then implemented and reran
  green. Standing-regression-guard check done: only `serve.go` (route
  registration) and `serve_session.go` (the new `mu` field + doc comment)
  were modified among non-test files; `serve_turn.go`/`serve_turn_test.go`
  are new — no pre-existing test file was touched.

- 2026-07-12: M4.2b3 landed `handleTurnStream` (`cmd/cortex/serve_stream.go`,
  route `POST /api/projects/{name}/sessions/{id}/turn/stream`, registered
  alongside the plain `POST .../turn` in `newServeMux` — a SEPARATE route
  rather than a content-negotiated variant of the same one, the two options
  the prior Next Up note left open: kept both endpoints simple and
  independently curl-able (a `text/event-stream` route needs different
  header/flush handling from the start of the response, which is awkward to
  branch on inside one handler after the body's already been decoded) rather
  than adding `Accept`-header dispatch logic nothing asked for. Threaded
  `Progress` into `Turn()` per the prior Next Up note's own analysis, but
  WITHOUT changing `Turn()`'s signature (avoiding the ~8-call-site ripple
  the note flagged as a real cost): extracted the existing body into an
  unexported `(cs *CortexSession) turn(ctx, input string, progress Progress)`
  (turn.go) that both `Turn(ctx, input)` (→ `cs.turn(ctx, input, nil)`,
  identical to today for every existing call site — REPL, discord, greeting,
  headless `turn`, all untouched) and a new exported
  `TurnWithProgress(ctx, input, p Progress)` (→ `cs.turn(ctx, input, p)`,
  `handleTurnStream`'s only caller) delegate to — one implementation, two
  call shapes, zero signature changes elsewhere. `runLoop`'s `nil` Progress
  argument at turn.go's call site became the threaded `progress` parameter
  directly (no wrapping). SSE wire shape (not yet golden-pinned — that's
  M4.5's box): `event: progress\ndata: {"line":"..."}\n\n` once per tool
  call (`progressEvent{Line string}`, JSON-marshaled from `progressLine`'s
  existing "  ▸ name(arg)" text via loop.go's Progress seam unchanged), then
  a terminal `event: result\ndata: {...turnResponse...}\n\n` (same struct
  `handleTurn`, serve_turn.go, already returns as plain JSON) — or
  `event: error\ndata: {"error":"..."}\n\n` on failure, never both. `sseEvent`
  (serve_stream.go) is a small shared writer (marshal → `event:`/`data:`
  lines → flush) rather than three ad-hoc `fmt.Fprintf` call sites, so the
  wire format has exactly one implementation for M4.5 to later pin. Held the
  SAME per-session `ms.mu` mutex `handleTurn` uses, for the same reason
  (M4.2b2's Decisions entry) — a concurrent turn on the SAME session (stream
  or plain) must serialize; proven directly by reusing `SessionManager`
  unchanged, no new locking logic. Test backend
  (`streamTurnTestSessionFactory`, serve_stream_test.go) is a genuine
  2-round scripted `httptest.Server` (round 1 → a `bash "echo hi"` tool
  call, round 2 → final content) run through the REAL `coderDispatcher`/
  `tools.Execute` path — not a fake `Dispatch` — so the progress event is
  proven against the actual seam runLoop exposes, not a synthetic call to
  `sseEvent` directly; `echo hi` reused verbatim from
  `TestTurnStopsRepeatedToolCalls`'s established precedent (main_test.go)
  that this exact command classifies Safe with no `classifyShell`/reasoner
  fake needed. Load-bearing check done: reverted turn.go's `runLoop` call to
  pass `nil` instead of `progress` (simulating the pre-increment state),
  confirmed `TestTurnStreamEndpointStreamsProgressAndResult` fails
  ("no progress event seen"), restored from a saved copy and reran the full
  verify suite green (plus `go test -run 'TestTurnStreamEndpoint|
  TestTurnEndpoint' -race -count=2`, 0 races). Standing-regression-guard
  check done: `git diff --name-only <genesis>..HEAD -- '*_test.go'` — the
  new `serve_stream_test.go` is not present at genesis; no pre-existing test
  file was touched (only `turn.go`/`serve.go`, both non-test, modified).

- 2026-07-12: M4.2c split into M4.2c1 (landscape, read-only) / M4.2c2
  (models, read + 3-scope write) — the two are independently testable
  units bundled by one GOAL.md line, and models' scoped-write surface
  (user/project/session, unknown-field survival, key-absence assertion)
  is clearly its own slice of work from a plain `GET`. M4.2c1 landed
  `handleLandscape` (`cmd/cortex/serve_landscape.go`, route `GET
  /api/landscape`) as a **thin wrapper over scan.go's own
  `resolveScanRoots`/`buildScanReport`** — no parallel scan/report logic,
  so `cortex serve`'s landscape view and `cortex scan --json`'s CLI
  report share one implementation and can't drift. `ErrNoScanRoots`
  (scan.go, M2.5) maps to 412 Precondition Failed rather than 500 — a
  refusal the client can act on (finish onboarding / configure a root),
  distinct from a server error. `newServeMux` grew two params
  (`configPath`, `homeDir` — the same two inputs `resolveScanRoots`/
  `buildScanReport` already take at the CLI level) to thread these in
  without a global; every existing test call site across
  serve_routes_test.go/serve_session_test.go/serve_turn_test.go/
  serve_stream_test.go got `"", ""` appended (mechanical, they don't
  exercise `/api/landscape`) rather than each growing its own
  landscape-specific fixture. Test fixture note: `landscape.ScanProjects`
  requires BOTH a `.git` dir AND an AI marker (e.g. `AGENTS.md`) to
  count a directory as a project — a marker file alone (no `.git`) is
  invisible to the scanner; the first test draft caught this the hard
  way (0 projects found) before adding `.git`. Load-bearing check done:
  removed the `mux.HandleFunc("GET /api/landscape", ...)` line, confirmed
  `TestLandscapeEndpointReturnsScanReport` fails with a 404, restored and
  reran green. Standing-regression-guard check done:
  `git diff --name-only <genesis>..HEAD -- '*_test.go'` — none of the
  four modified `serve_*_test.go` files (routes/session/turn/stream)
  exist at the genesis commit (`git cat-file -e <genesis>:<path>` fails
  for all four), so the mechanical `newServeMux` signature-widening edit
  to them is not a standing-regression-guard violation; only
  `serve_landscape_test.go` (new) carries this increment's actual new
  assertions.

- 2026-07-12: M4.2c2 split into M4.2c2a (read-only `GET /api/models`, this
  iteration) / M4.2c2b (scoped role-binding writes) — the prior Next Up
  note's own analysis ("read endpoint + 3 distinct write scopes... each
  wanting its own test") flagged this as likely too big, and read vs. write
  are independently testable units, mirroring the M4.2b and M4.2c splits.
  M4.2c2a landed `handleModels` (`cmd/cortex/serve_models.go`, route `GET
  /api/models`) as a thin composition of two already-tested primitives —
  `loadMergedConfig(configPath, "")` and `discoverFleet(ctx,
  cfg.backendEndpoint())` — iterating `rolePolicies`'s keys (config.go's
  existing canonical role set) to build a `map[string]ModelSpec` via
  `Config.resolveBinding`, no new fleet/binding logic. `configPath` here is
  the single path already threaded into `newServeMux` for `/api/landscape`
  (M4.2c1) — this route has no `{name}` project segment, so "merged config"
  means that one file only, not a user+project layer stack; a project-scoped
  view is deferred to M4.2c2b alongside the scoped writes, which do need
  per-project resolution. Key-absence (GOAL.md M4.2's "key material absent
  from every response") holds by construction, not by a redaction step:
  `ModelSpec` (config.go) only ever carries `KeyEnv`/`KeyService` — SOURCE
  names (an env var or keychain service to read from), never a resolved key
  value — and `handleModels` calls `Config.resolveBinding`, never
  `resolveKey`; `TestModelsEndpointReturnsRolesAndFleet` makes this a real
  assertion rather than a vacuous one by threading an actual secret value
  through a real env var named by `key_env` and asserting it's absent from
  the response body (not just checking the struct shape). Reused
  `main_test.go`'s existing `fleetServer`/`fleetInfoJSON` test helpers
  (already established by `TestDiscoverFleet`) rather than writing a new
  fake `/model/info` server — same package, same pattern, no duplication.
  `TestModelsEndpointFleetUnreachableStillReturnsRoles` pins graceful
  degradation: `discoverFleet` already returns `nil` on an unreachable
  backend (existing, tested behavior) and `handleModels` doesn't treat that
  as an error — the endpoint still returns every role's binding with an
  empty fleet, matching `Config.resolveBinding`'s own nil-fleet-safe
  contract. Load-bearing check done: moved `serve_models.go` out of the
  tree, confirmed `go vet ./cmd/cortex/...` fails to build (`serve.go:121:
  undefined: handleModels`), restored and reran the full verify suite
  green. Standing-regression-guard check done: `git status` before
  committing showed only `serve.go` (route registration, non-test) modified
  plus two new files (`serve_models.go`, `serve_models_test.go`) — no
  existing test file needed touching this time, since (unlike M4.2c1)
  `newServeMux`'s signature didn't change.

- 2026-07-12: M4.2c2b split into M4.2c2b1 (file-backed writes at user +
  project scope, this iteration) / M4.2c2b2 (session scope) — the prior
  Next Up note's own analysis flagged three scopes each wanting a dedicated
  test as likely too big for one iteration, and user/project share one
  file-backed mechanism (read-modify-write over a config path) while
  session is a wholly different one (a live in-memory `*managedSession`
  field, no file I/O) — the same "independently testable units" reasoning
  M4.2b/c/c2 were each split on. M4.2c2b1 landed `PersistModelBinding`
  (serve_models.go) as a `PersistBackend`-shaped read-modify-write helper
  taking `fields map[string]json.RawMessage` (decoded straight off the
  request body) rather than a typed partial `ModelSpec` — decoding into a
  struct would make a present-but-zero field (e.g. `"window":0`)
  indistinguishable from an absent one, defeating "write whichever fields
  the request body sets, not a whole-object clobber"; a raw-message map
  preserves exactly the set of keys the client sent. Each field is written
  individually via `setJSONPath(doc, []string{"models", role, field}, raw)`
  — `json.RawMessage` round-trips through `json.Marshal` as its own bytes
  (it implements `MarshalJSON`), so no re-encoding of the value itself
  happens. Route: `PUT /api/models/{role}?scope=user|project[&project=
  <name>]` — a single endpoint with a scope query param (not three
  separate routes), matching the "designer's call" GOAL.md left open and
  reading naturally alongside the existing `GET /api/models`. User scope
  reuses the same `configPath` already threaded into `newServeMux` for
  the read endpoint (M4.2c2a) and `/api/landscape` (M4.2c1) — no
  `newServeMux` signature change needed this time, since project scope
  resolves its target via the registry (`reg`, already a mux parameter)
  rather than a new path parameter. Project scope writes
  `<project.Root>/.cortex/config.json`, matching `findConfigPath`'s
  on-disk layout (config.go) — confirmed via
  `TestSetModelBindingProjectScopeWritesUnderProjectConfigAndLeavesUserConfigUntouched`
  that this never touches the user config file. Unknown role (checked
  against `rolePolicies`' fixed key set, the same one `handleModels`
  iterates) and unsupported/missing scope both refuse 400; an unregistered
  project refuses 404 (mirrors `handleListProjectSessions`'s existing
  `registry.ErrProjectNotFound` → 404 convention). Response is read back
  from disk after the write (not just echoed) via a second `readJSONDoc` —
  proves the persisted value, not just the handler's local state.
  Key-material absence holds by the same construction as M4.2c2a
  (`ModelSpec` only ever carries `KeyEnv`/`KeyService` source names, never
  a resolved value) — `TestSetModelBindingResponseNeverIncludesSecretValue`
  makes it a real assertion by threading an actual secret through a real
  env var and asserting its absence from the response body, mirroring
  `TestModelsEndpointReturnsRolesAndFleet`'s established shape. Confirmed
  in main.go before assuming: the REPL's `/model` command
  (`session.SetModel`, `session_core.go:106`) mutates only
  `cs.Request.Model` in memory and never touches a config file — this is
  the direct evidence session scope (M4.2c2b2) needs a completely
  different, non-file-backed mechanism. Load-bearing check done: replaced
  the scope `switch` with an unconditional `target := configPath` (keeping
  `reg`/`errors`/`filepath` referenced via no-op statements to stay
  buildable) — confirmed
  `TestSetModelBindingProjectScopeWritesUnderProjectConfigAndLeavesUserConfigUntouched`,
  `TestSetModelBindingProjectScopeMissingProjectQueryReturns400`,
  `TestSetModelBindingProjectScopeUnknownProjectReturns404`, and
  `TestSetModelBindingUnsupportedScopeReturns400` all fail (200 where 400/
  404/mutation-check was wanted), restored from a saved copy and reran the
  full verify suite green. Standing-regression-guard check done: `git
  status` before committing showed only `serve.go` (route registration,
  non-test) and `serve_models.go` (non-test) modified, plus
  `serve_models_test.go` extended — `serve_models.go`/`serve_models_test.go`
  did not exist at genesis (confirmed via `git cat-file -e <genesis>:<path>`
  failing for both), so extending the test file is not a violation.

- 2026-07-12: M4.2c2b2 landed `handleSetSessionModelBinding`
  (serve_models.go), the session-scope leg of `PUT
  /api/models/{role}?scope=user|project|session`: `scope=session` is
  branched off the top of `handleSetModelBinding` (before the file-backed
  `switch`) since it shares NOTHING with the user/project legs — no
  `configPath`/`PersistModelBinding`/`readJSONDoc` involvement at all, it
  mutates a live `*managedSession` directly via `SessionManager.Get(id)`
  (the same seam `handleTurn`/`handleTurnStream` already use) and calls
  `CortexSession.SetModel` — literally the same call main.go's REPL
  `/model` command makes (`session_core.go:106`), confirmed by reading it
  before assuming rather than guessing at a parallel mechanism. Only
  `role=code` is accepted at session scope (400 for any other role):
  `SetModel` is the only session-level override the codebase has today
  (it mutates `cs.Request.Model` alone, nothing else), so GOAL.md §3 P4's
  models-view mention of five roles (code/study/reason/fast/embed) across
  "the three scopes" is read as user/project scope having all five (which
  M4.2c2a's `GET /api/models` and M4.2c2b1's file-backed write already
  support for every role) while session scope — explicitly glossed in
  docs/cortex-web.md Phase 4 as "the API form of `/model`" — inherits
  `/model`'s own real limitation; inventing a per-role in-memory override
  map on `managedSession` to accept all five roles at session scope would
  be new, unrequested surface with no existing REPL behavior backing it
  (the exact GOAL.md §1 "invents a parallel mechanism" anti-pattern).
  `mgr.Get(id)` returning `false` (session not currently live in this
  process — never created, or created then evicted/restarted-away) is a
  404, matching `handleTurn`'s existing "unknown/not-live session id"
  convention (M4.2b2) rather than implicitly resuming from disk — a
  session-scope write against a session nobody has brought live via `POST
  .../sessions` first has no live object to mutate. Held `ms.mu` (the
  same per-session turn-serializing mutex `handleTurn`/`handleTurnStream`
  use, M4.2b2/b3) around the `SetModel` call + read-back — small but real:
  without it, a session-scope write racing an in-flight turn could
  read/write `cs.Request.Model` unsynchronized with the turn loop's own
  access to the same field. "Reverts on resume" needed no explicit
  clear-on-resume code at all — it holds by construction, the same way
  M4.2b1's "serve owns no state" does: `SetModel` never touches the
  transcript file, so a second, independent `SessionManager` (standing in
  for a restart, mirroring
  `TestSessionManagerResumeRehydratesFromTranscriptAfterRestart`'s exact
  pattern) resuming the id from disk simply never sees the override,
  proven by `TestSetModelBindingSessionScopeSetsLiveSessionModelAndRevertsOnResume`.
  Modified (not just extended) `serve_models_test.go`'s pre-existing
  `TestSetModelBindingUnsupportedScopeReturns400` to drop `"session"` from
  its unsupported-scope table (it's supported now) — confirmed via `git
  cat-file -e <genesis>:cmd/cortex/serve_models_test.go` failing (the file
  postdates genesis, first landed by M4.2c2a within this loop) that this
  is not a standing-regression-guard violation. Load-bearing check done:
  replaced the `scope == "session"` branch with a no-op `_ = mgr` (keeping
  the signature compiling), confirmed
  `TestSetModelBindingSessionScopeSetsLiveSessionModelAndRevertsOnResume`
  and `TestSetModelBindingSessionScopeUnknownSessionReturns404` both fail
  (400 where 200/404 was wanted; the missing-query and unsupported-role
  tests still incidentally passed since they 400 either way — expected,
  noted so a future reader isn't confused why only 2 of 4 new tests moved),
  restored from a saved copy (`/tmp/serve_models.go.bak`) and reran the
  full verify suite green. Standing-regression-guard check done: `git
  diff --name-only <genesis>..HEAD -- '*_test.go'` — `serve_models_test.go`
  is the only test file touched this iteration and does not exist at
  genesis (confirmed via `git cat-file -e`), so extending it is not a
  violation.

- 2026-07-12: M4.3 landed as a test-only increment (no production code
  changed) — `TestTurnEndpointDifferentSessionsRunConcurrently`
  (`cmd/cortex/serve_turn_test.go`) proves GOAL.md §3 P4's "different
  sessions concurrent" half by asserting `backend.maxConcurrent() >= 2`
  across two `mgr.Create("blog")`-created sessions fired concurrently; the
  "one session serializes" half was already proven by M4.2b2's
  `TestTurnEndpointSameSessionSerializes` in the same file, so GOAL.md's
  single M4.3 checklist item is satisfied by these two sibling tests
  together — no re-proof needed for the serialize half per the prior
  iteration's Next Up analysis. Confirmed the hypothesis that no
  implementation change was needed BEFORE assuming it: wrote the test
  first against the current `handleTurn`/`managedSession.mu`
  (serve_session.go, serve_turn.go) and it passed unmodified, since
  `managedSession.mu` is already per-`*managedSession` (a struct field,
  not a package-level lock) — two different `Create()` calls produce two
  distinct `managedSession` values with independent mutexes by
  construction. Load-bearing check done: temporarily swapped
  `ms.mu.Lock()`/`defer ms.mu.Unlock()` in `serve_turn.go`'s `handleTurn`
  for a package-level `sync.Mutex` shared across all sessions (simulating
  a regression to session-id-oblivious locking), confirmed
  `TestTurnEndpointDifferentSessionsRunConcurrently` fails
  (`backend saw max 1 concurrent requests ... want >= 2`), reverted from a
  saved copy (`/tmp/serve_turn.go.bak`) and reran the full verify suite
  green. Standing-regression-guard check done: `git diff --name-only
  <genesis>..HEAD -- '*_test.go'` shows only `serve_turn_test.go` touched
  this iteration, which postdates genesis (confirmed via `git cat-file -e
  <genesis>:cmd/cortex/serve_turn_test.go` failing) — not a violation.

- 2026-07-12: M4.4 landed as a test-only increment (no production code
  changed) — confirmed the hypothesis before writing anything by reading
  `SessionManager.Resume` (serve_session.go, M4.2b1) and `session.go`
  directly: `Resume` already calls `CortexSession.ResumeTranscript` which
  calls `openTranscript` which calls `fslock.OpenExclusive`
  (`internal/fslock`, the pre-existing D8 lock per amendment A1) — the
  wiring the prior Next Up note flagged as unconfirmed was already there,
  landed incidentally by M4.2b1 reusing `StartTranscript`/
  `ResumeTranscript` rather than inventing a parallel serve-side open path.
  `TestCrossProcessSessionResumeGetsBusyError`
  (`cmd/cortex/serve_lock_test.go`) proves it end-to-end: the parent
  process creates a session via `SessionManager.Create` and holds its
  transcript file open (deferred close), then re-execs the test binary
  (`os.Args[0]`, `-test.run=^TestCrossProcessSessionResumeGetsBusyError$`)
  — the identical idiom `internal/fslock/fslock_test.go`'s
  `TestOpenExclusive_CrossProcess` established, reused rather than
  reinvented. The child process (env-var contract distinguishes child mode
  from the top-level test, mirroring `fslock_test.go`'s `holdLockPath`)
  builds its OWN fresh `SessionManager` (empty in-memory map, so
  `Get(id)` misses and it falls through to `ResumeTranscript`) targeting
  the same project root/session id, and asserts `errors.Is(err,
  fslock.ErrBusy)` — printing a stdout marker and exiting 0/1 accordingly,
  since an error value can't cross a process boundary; the parent asserts
  on that marker string via `CombinedOutput`. Caught and fixed a real bug
  in the test itself before trusting it: the first draft's markers were
  `"BUSY"` (success) and `"NOT-BUSY: <err>"` (failure) — `strings.Contains`
  for `"BUSY"` matches BOTH, since `"NOT-BUSY"` contains `"BUSY"` as a
  substring, so the assertion would have silently passed even with no
  contention at all. Renamed to non-overlapping markers
  (`"RESUME-BLOCKED"` / `"RESUME-ALLOWED (want blocked): <err>"`) before
  trusting the test. Load-bearing check done (and this substring bug is
  exactly why the check matters): temporarily changed `openTranscript` to
  `os.OpenFile` directly (no `fslock.OpenExclusive` call) — with the
  original `"BUSY"`/`"NOT-BUSY"` markers this still reported PASS (the
  substring bug masking a real regression); after the marker rename it
  correctly failed (`RESUME-ALLOWED (want blocked): <nil>`), confirming
  the test is load-bearing; restored `openTranscript` from a saved copy
  (`/tmp/session.go.bak`) and reran the full verify suite green.
  Standing-regression-guard check done: `git status` before committing
  showed only the new `cmd/cortex/serve_lock_test.go` (untracked) — no
  existing file touched at all this iteration.

- 2026-07-12: M4.5 landed test-only, as the prior iteration's Next Up
  predicted: neither of the two boxed behaviors needed production code —
  M4.2b3 (`serve_stream.go`) already streams progress/result events in
  order, and `newServeServer` (`serve.go`) already leaves `WriteTimeout`
  unset by omission. Confirmed the golden test asserts something real
  (not just presence, which M4.2b3's own test already covered) by scripting
  a THREE-round backend — two distinct bash tool calls then a final
  answer — so the golden literal proves ORDER across multiple progress
  frames, not just a single one; asserted the exact byte-for-byte SSE body
  (`event: ...\ndata: ...\n\n` × 3, nothing before the first frame or after
  the last) against `progressLine`'s real `"  ▸ " + ActivityLabel()`
  rendering (confirmed via a throwaway `go run` snippet that Go's
  `json.Marshal` does NOT escape the `▸` U+25B8 arrow — only `<`, `>`, `&`,
  and control chars get escaped by default — so the golden could use the
  literal rune rather than a `▸` escape). `TestNewServeServerSetsNo
  WriteTimeout` calls `newServeServer` directly (the same constructor
  `runServeCLI` calls in production) rather than `httptest.NewServer`,
  matching M4.1's `TestNewServeServerListenerIsLoopback` precedent noted
  in the prior Next Up — an `httptest` server doesn't expose the
  `*http.Server` it builds internally. Both load-bearing checks done: (1)
  temporarily set `WriteTimeout: 5 * time.Second` in `newServeServer` —
  confirmed `TestNewServeServerSetsNoWriteTimeout` fails reporting "5s,
  want 0", reverted from a saved copy; (2) temporarily changed
  `progressLine` to use `"  » "` instead of `"  ▸ "` — confirmed
  `TestTurnStreamEndpointGoldenFramesForMultiStepTurn` fails showing the
  mismatched frames, reverted from a saved copy; reran the full verify
  suite green with `git diff --stat` showing zero production-file changes
  (only the two test files this iteration touched, both non-genesis, are
  in the commit). Standing-regression-guard check done:
  `cmd/cortex/serve_test.go` (modified, adding the WriteTimeout test) did
  not exist at the branch genesis commit (`git cat-file -e
  <genesis>:cmd/cortex/serve_test.go` reports "exists on disk, but not in
  <genesis>") — it's an M4.1-era file, not a pre-existing one, so editing
  it needs no Decisions-Log correction entry.

- 2026-07-12: M4.6 landed as a test-only increment (no production code
  changed) — confirmed the hypothesis before writing anything by reading
  `serve_session.go`/`serve_routes.go`: both `GET /api/projects` and `GET
  /api/projects/{name}/sessions` already read straight off disk on every
  request (`reg.List()` against `registry.FileRegistry`, `listSessions`
  against `Workspace.SessionsDir()`) with no in-memory cache or index —
  `SessionManager`'s `sessions map[string]*managedSession` is consulted only
  by `Get`/`Resume`-of-a-live-session and `handleTurn`, never by either
  listing handler. `TestServeListingsIdenticalAcrossManagerRestart`
  (`cmd/cortex/serve_restart_test.go`) proves this at the HTTP layer with a
  REAL on-disk `registry.NewAt` (not `fakeRegistry` — a fake would trivially
  "survive a restart" by construction, proving nothing): builds a first
  `FileRegistry`+`SessionManager`+server pair against a temp-dir
  `projects.json` and project root, creates a live session and captures both
  listing responses, closes that server, then builds a wholly independent
  second `FileRegistry`+`SessionManager`+server pair (fresh in-memory map,
  no reference to the first) pointed at the identical on-disk paths, and
  asserts both listing bodies are byte-identical across the two — plus that
  the fresh manager's in-memory map does NOT already contain the created
  session id (ruling out a test that accidentally shares state). This is a
  different assertion than the pre-existing (M4.2b1)
  `TestSessionManagerResumeRehydratesFromTranscriptAfterRestart`, which
  proves a single individual session rehydrates after "restart" but uses
  `fakeRegistry` and never exercises the two listing HTTP handlers GOAL.md's
  M4.6 wording ("re-derives every list from disk... listings identical")
  specifically names. Load-bearing check done: temporarily short-circuited
  `registry.FileRegistry.readAll` to `return nil, nil` unconditionally
  (simulating a regression where the registry stops reading from disk —
  e.g. some future in-memory-cache "optimization"), confirmed
  `TestServeListingsIdenticalAcrossManagerRestart` fails (the create-session
  call 404s against the now-empty-looking registry, so decoding its
  response as JSON errors), restored `registry.go` from a saved copy
  (`/tmp/registry.go.bak`) and reran the full verify suite green. Standing-
  regression-guard check done: `git cat-file -e
  <genesis>:cmd/cortex/serve_restart_test.go` reports "exists on disk, but
  not in <genesis>" — a brand-new file this iteration, not an edit to a
  pre-existing test.

- 2026-07-12: M4.7 landed idle-session eviction as a lazy, opt-in check
  inside `SessionManager.Get` (no background goroutine/ticker — fits the
  "adapters over daemons, zero background services" pillar) rather than a
  sweep timer: `SetIdleTimeout(d)` arms it (default `d=0` disables
  eviction, so every pre-M4.7 call site/test that never calls it is
  provably unaffected — confirmed no `NewSessionManager` signature change
  was needed across its ~15 call sites) and `SetClock(now func() time.Time)`
  overrides the manager's `time.Now` for deterministic, sleep-free tests
  (the first injected-clock seam in the repo — establishes the pattern
  M6.2's later scheduler will reuse, per GOAL.md §3 P6's own "no test
  sleeps" wording). `managedSession` gained a `lastTouched time.Time`
  stamped by `Create`/`Resume`, plus a new `SessionManager.Touch(id)`
  method wired into `handleTurn` (serve_turn.go) and `handleTurnStream`
  (serve_stream.go) right after their existing `mgr.Get` lookup — without
  Touch, a long *conversation* (many turns, each individually well within
  the idle window, but the session's `lastTouched` frozen at Create time)
  would get evicted mid-use, which is worse than the DoD's literal ask but
  is the obviously-intended behavior ("idle" means no *requests*, not "old
  since creation"); GOAL.md doesn't spell this out but docs/cortex-web.md
  §232's "idle sessions evict; resume re-hydrates" framing reads as
  activity-based idleness, not age-based. Eviction reuses `Resume`'s
  existing rehydrate-from-transcript path unchanged (Get's eviction just
  deletes from the map and calls `cs.Close()`; the next `Resume` call sees
  a map-miss and falls through to `ResumeTranscript`, the exact path
  M4.4/M4.6 already proved standalone) — zero new rehydration logic,
  matching pillar 3's reuse-the-seam bias. Production wiring: `serve.go`
  arms a new `defaultSessionIdleTimeout = 30 * time.Minute` constant (no
  GOAL.md/docs value specified; picked a conservative round number rather
  than leaving eviction permanently off in production, since an unbounded
  live-session map is exactly the kind of unbounded-growth GOAL.md's
  pillars implicitly warn against) — this constant itself is untested
  (matches the file's existing convention that `runServeCLI`'s os.Exit-
  driving wrapper is untested; the pure `SessionManager` methods it calls
  carry the coverage). Load-bearing checks done (two, both restored from
  the real implementation afterward and reran the full suite green): (1)
  short-circuited `evictIfIdleLocked` to always return early — confirmed
  `TestSessionManagerEvictsIdleSessionAndResumeRehydrates` fails ("Get()
  still returns the session past the idle threshold") while the other two
  new tests still pass (expected — one asserts *no* eviction, the other's
  window never actually elapses without real eviction firing first); (2)
  separately gutted `Touch` to a no-op — confirmed
  `TestSessionManagerTouchExtendsIdleWindow` fails ("Touch() did not extend
  the idle window; session was evicted early"). Standing-regression-guard
  check done: all five touched files (`serve.go`, `serve_session.go`,
  `serve_session_test.go`, `serve_stream.go`, `serve_turn.go`) postdate the
  genesis commit (`git cat-file -e <genesis>:<path>` fails "not in
  <genesis>" for all five), so extending `serve_session_test.go` is not a
  pre-existing-test-file edit. **M4 complete** (M4.1–M4.7 all ticked); M5
  section added to this file's checklist per GOAL.md §6's "add milestone
  sections as the loop reaches them."

## Known Issues (append-only)
- (none yet)
