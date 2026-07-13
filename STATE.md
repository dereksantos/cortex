# STATE — cortex web loop
Updated: 2026-07-13 · Iteration: 60

## Current milestone
M6 — Loops (P6) (M1, M2, M3, M4, M5 complete)

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
- [x] M5.1 Assets under `cmd/cortex/webui/` served from `go:embed`;
      route-level test proves serving with no filesystem presence. `443a7ea`
      — `TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence`
      (cmd/cortex/webui_test.go).
- [ ] M5.2 View-models built in Go and golden-tested: project dashboard,
      session transcript (from real JSONL fixtures), landscape report,
      models view (bindings + effective scope resolution). **Split
      (2026-07-12, this iteration) into:**
  - [x] M5.2a Project dashboard view-model: `buildDashboardViewModel`
        composes `internal/registry`'s project list, the M4.2a session-list
        seam (`listSessions`/`sessionSummary`), and a new dir-scoped git
        change-status helper (`change.go`'s `changeStatusFor`), sorted by
        project name; a project whose git status can't be read still
        renders with the error surfaced per-row. `4946ee3` —
        `TestBuildDashboardViewModelGolden`,
        `TestBuildDashboardViewModelEmptyRegistryReturnsEmptyProjectsNotNull`,
        `TestBuildDashboardViewModelNonGitProjectSurfacesChangeError`
        (cmd/cortex/webui_dashboard_test.go),
        `TestChangeStatusForReportsBranchActiveAndClean`,
        `TestChangeStatusForNotAGitRepoReturnsError`
        (cmd/cortex/change_status_test.go).
  - [x] M5.2b Session transcript view-model: `buildTranscriptViewModel`
        reads a real session JSONL transcript via the existing `loadSession`
        (session.go), rendering ordered role/content entries with
        tool-call/tool-result turns carried alongside plain ones; the seed
        system-prompt message (always index 0 of a fresh session) is
        omitted, matching display.go's REPL rendering convention. `286e14e`
        — `TestBuildTranscriptViewModelGolden`,
        `TestBuildTranscriptViewModelOmitsSeedSystemMessage`,
        `TestBuildTranscriptViewModelMissingFileReturnsError`
        (cmd/cortex/webui_transcript_test.go).
  - [x] M5.2c Landscape report view-model, wrapping M2's `ScanReport`
        (the same shape `cortex scan --json` / `GET /api/landscape`
        already produce). `b8d4b8a` —
        `TestBuildLandscapeViewModelGolden`,
        `TestBuildLandscapeViewModelNoRootsConfiguredReturnsTypedRefusal`
        (cmd/cortex/webui_landscape_test.go).
  - [x] M5.2d Models view-model: bindings + effective scope resolution,
        wrapping M4.2c2a's `/api/models` shape. `6725d8d` —
        `TestBuildModelsViewModelGolden`,
        `TestBuildModelsViewModelNilConfigEveryKnownRoleStillPresent`,
        `TestBuildModelsViewModelRolesSortedAlphabetically`
        (cmd/cortex/webui_models_test.go). **M5.2 complete
        (a/b/c/d all ticked).**
- [ ] M5.3 The four screens render those view-models; JS bounded
      mechanically: a Go test over the embedded FS asserts each `.js`
      file ≤ 300 lines and total JS ≤ 1200 lines. **Split (2026-07-12,
      this iteration) into:**
  - [x] M5.3a Mechanical JS size-cap test: a Go test walks the embedded
        webui FS and asserts each `.js` file ≤ 300 lines and the total
        ≤ 1200 lines, as a standing regression guard the later screen
        splits build under. `8aedc02` — `TestWebUIJavaScriptSizeCaps`
        (cmd/cortex/webui_jscap_test.go).
  - [x] M5.3b Dashboard screen: `index.html`/`app.js` grow real
        fetch/render logic for the project dashboard, replacing the
        M5.1 placeholder. `754082c` —
        `TestDashboardEndpointReturnsViewModel`,
        `TestDashboardEndpointEmptyRegistryReturnsEmptyProjectsNotNull`,
        `TestDashboardEndpointRequiresAuth` (cmd/cortex/serve_dashboard_test.go),
        `TestServeAuthMiddlewareAllowsStaticAssetsWithoutToken`
        (cmd/cortex/serve_test.go),
        `TestDashboardScreenAppJSFetchesDashboardEndpoint`,
        `TestDashboardScreenIndexHTMLHasDashboardContainer`
        (cmd/cortex/webui_dashboard_screen_test.go),
        `TestWebUIServesEmbeddedAssetsWithNoFilesystemPresence`
        (cmd/cortex/webui_test.go, pre-existing — reconfirmed green,
        "Cortex web UI" marker preserved in app.js).
  - [ ] M5.3c Session screen: transcript render (`buildTranscriptViewModel`)
        + input box + live SSE progress against the M4.2b3 stream
        endpoint. **Split (2026-07-12, this iteration) into:**
    - [x] M5.3c1 `GET /api/projects/{name}/sessions/{id}` endpoint wiring
          `buildTranscriptViewModel` into the HTTP surface (Go-only, no
          screen JS/HTML yet — mirrors M5.3b's own first concern:
          "there is no endpoint yet"). `5298e62` —
          `TestGetSessionEndpointReturnsTranscriptViewModel`,
          `TestGetSessionEndpointUnknownProjectReturns404`,
          `TestGetSessionEndpointUnknownSessionIDReturns404`,
          `TestGetSessionEndpointRequiresAuth`
          (cmd/cortex/serve_transcript_test.go).
    - [x] M5.3c2 Session screen static render: routing (how the page picks
          project+session id — URL query params, following app.js's
          existing `?token=` precedent) + fetch/render of the transcript
          via M5.3c1's endpoint into a `#session` container. `a9866b9` —
          `TestSessionScreenAppJSFetchesSessionEndpoint`,
          `TestSessionScreenAppJSReadsProjectAndSessionQueryParams`,
          `TestSessionScreenIndexHTMLHasSessionContainer`
          (cmd/cortex/webui_session_screen_test.go).
    - [x] M5.3c3 Input box: a text field + submit posting a new turn via
          `POST .../turn` (serve_turn.go), then re-rendering. `a13f686` —
          `TestSessionScreenAppJSPostsTurnOnSubmit`,
          `TestSessionScreenAppJSCreatesInputAndSubmitElements`,
          `TestSessionScreenAppJSReRendersAfterTurnSubmit`
          (cmd/cortex/webui_session_input_test.go).
    - [x] M5.3c4 Live SSE progress: switch the input box's turn submission
          to `POST .../turn/stream` (serve_stream.go) and render
          `progress`/`result`/`error` events as they arrive (`EventSource`
          vs. `fetch` + streaming reader — pick one, record why). `42a2462`
          — `TestSessionScreenAppJSPostsToTurnStreamEndpoint`,
          `TestSSEJSParsesEventAndDataFrames`,
          `TestSessionScreenAppJSHandlesProgressResultAndErrorEvents`,
          `TestSessionScreenAppJSStillRerendersAfterTurnCompletes`,
          `TestIndexHTMLLoadsSSEScriptBeforeAppJS`
          (cmd/cortex/webui_session_stream_test.go). **M5.3c complete
          (c1/c2/c3/c4 all ticked).**
  - [x] M5.3d Landscape screen: renders `buildLandscapeViewModel`
        (M5.2c, a `ScanReport` pass-through) via `GET /api/landscape`.
        `2105e35` — `TestLandscapeScreenJSFetchesLandscapeEndpoint`,
        `TestLandscapeScreenIndexHTMLHasLandscapeContainer`,
        `TestIndexHTMLLoadsAppJSBeforeLandscapeJS`,
        `TestLandscapeScreenJSHandlesNoRootsRefusal`,
        `TestLandscapeScreenJSReportsTruncation`
        (cmd/cortex/webui_landscape_screen_test.go).
  - [x] M5.3e Models screen: renders GET `/api/models` (M4.2c2a's
        `modelsResponse` — role bindings + discovered fleet; no new
        endpoint) — a user/project/session scope switcher plus a
        per-role text field + Save button PUTting to M4.2c2b1/b2's
        `PUT /api/models/{role}?scope=...[&project=...][&session=...]`,
        then reloading. `c1add33` —
        `TestModelsScreenJSFetchesModelsEndpoint`,
        `TestModelsScreenIndexHTMLHasModelsContainer`,
        `TestIndexHTMLLoadsAppJSBeforeModelsJS`,
        `TestModelsScreenJSHasScopeSwitcherWithUserProjectSession`,
        `TestModelsScreenJSPutsBindingOnSave`,
        `TestModelsScreenJSReloadsAfterSave`
        (cmd/cortex/webui_models_screen_test.go). **M5.3 complete
        (a/b/c/d/e all ticked).**
- [x] M5.4 End-to-end smoke: start serve with a scripted sender ⇒ create
      session ⇒ POST turn ⇒ SSE stream renders ⇒ transcript page shows
      the turn. One test, full path, no live model. `ff72df8` —
      `TestServeEndToEndSmokeCreateSessionTurnStreamAndTranscriptReflectsIt`
      (cmd/cortex/serve_e2e_test.go). **M5 complete (M5.1-M5.4 all
      ticked) — all four Phase 5 screens shipped.**

### M6 — Loops (P6)
- [x] M6.1 `internal/loops`: spec CRUD round-trip on `loops.json`;
      validation rejects cadence below the 15-minute floor (typed
      error). `fc14a44` — `TestFileStoreCRUDRoundTrip`,
      `TestFileStoreLookupUnknownNameReturnsTypedError`,
      `TestFileStoreRemoveUnknownNameReturnsTypedError`,
      `TestFileStoreLookupOnMissingFileReturnsTypedError`,
      `TestFileStoreSaveRejectsCadenceBelowFifteenMinuteFloor`,
      `TestFileStoreSaveAllowsExactlyFifteenMinuteFloor`,
      `TestFileStoreSaveAllowsManualOnlyZeroInterval`
      (internal/loops/loops_test.go).
- [x] M6.2 Scheduler on an injected clock: due ⇒ fires; not due ⇒
      doesn't; disabled ⇒ never; overlap ⇒ skips AND journals the
      skip. No test sleeps. `3fa50df` —
      `TestSchedulerDueFiresWhenIntervalElapsed`,
      `TestSchedulerNotDueSkipsWhenIntervalNotElapsed`,
      `TestSchedulerDisabledNeverFires`,
      `TestSchedulerManualOnlyNeverAutoDue`,
      `TestSchedulerOverlapSkipsAndJournalsSkip`,
      `TestSchedulerOverlapPropagatesOnSkipError`
      (internal/loops/scheduler_test.go),
      `TestAppendLoopRunWritesEntryToUserLevelJournal`,
      `TestAppendLoopRunIsolatedByCortexHome`
      (internal/journal/loop_test.go).
- [x] M6.3 Each firing runs a fresh headless session in the target
      project (fixture project + scripted sender), producing a
      `loop.run` event with outcome + change ref. `add1d6f` —
      `TestRunLoopFiringRunsFreshHeadlessSessionAndJournalsSuccessWithChangeRef`,
      `TestRunLoopFiringNoOpTurnJournalsSuccessWithEmptyChangeRef`,
      `TestRunLoopFiringTurnErrorJournalsFailedOutcome`,
      `TestRunLoopFiringUnknownProjectJournalsFailedOutcome`
      (cmd/cortex/loop_run_test.go).
- [x] M6.4 Budget caps, both of D11's: a scripted runaway session
      halts at the turn cap (sub-test 1) and a token-budget breach
      halts before the turn cap (sub-test 2, fake sender reporting
      token counts); both journal `failed: budget`. `c7f13c4` —
      `TestRunLoopTokenBudgetFinalizes` (cmd/cortex/loop_budget_test.go),
      `TestRunLoopFiringRunawaySessionHaltsAtTurnCap`,
      `TestRunLoopFiringTokenBudgetHaltsBeforeTurnCap`
      (cmd/cortex/loop_run_test.go).
- [x] M6.5 Risk posture: a loop firing whose scripted sender issues
      a shellrisk-Risky command is Blocked, asserted on the tool result
      and the `loop.run` event; no prompt surface is reachable. `65cf70b`
      — `TestRunLoopFiringRiskyCommandBlockedNoPromptReachable`
      (cmd/cortex/loop_run_test.go).
- [x] M6.6 Restart resumes: next-run derived from the last
      `loop.run` timestamp + interval; no double-fire across a
      scheduler restart (fake-clock test). `b990801` —
      `TestJournalLastRunReturnsMostRecentMatchingName`,
      `TestSchedulerRestartAcrossProcessDoesNotDoubleFire`
      (internal/loops/lastrun_test.go).
- [ ] M6.7 Loops screen: view-model golden (specs + run history
      from `loop.run` events); create/enable/disable/run-now wired to
      NEW loops endpoints added to `cortex serve` in this milestone
      (httptest). **Split (2026-07-13, this iteration) into:**
  - [x] M6.7a Loops view-model: a builder composing `internal/loops.
        Store.List()` (specs) with run history grouped by name from the
        user-level `loop.run` journal (a new production reader — the
        existing `readLoopRunEntries` is test-only, in `loop_run_test.go`)
        — golden test, mirroring M5.2's dashboard/landscape/models
        view-model pattern. `9d47fa9` —
        `TestJournalRunHistoryReturnsEveryMatchingEntryInWriteOrder`
        (internal/loops/history_test.go),
        `TestBuildLoopsViewModelGolden`,
        `TestBuildLoopsViewModelEmptyStoreReturnsEmptyLoopsNotNull`
        (cmd/cortex/webui_loops_test.go).
  - [x] M6.7b `GET /api/loops` endpoint wiring M6.7a's view-model into
        the HTTP surface (auth-gated, httptest) — Go-only, no screen yet,
        mirroring M4.2a/M5.3c1's endpoint-first precedent. `e8dbaa3` —
        `TestListLoopsEndpointReturnsSpecsAndRunHistory`,
        `TestListLoopsEndpointEmptyStoreReturnsEmptyArray`,
        `TestListLoopsEndpointRequiresAuth` (cmd/cortex/serve_loops_test.go).
  - [x] M6.7c `POST /api/loops` create endpoint via `internal/loops.
        FileStore.Save` (httptest: happy path, cadence-floor rejection
        surfaced as a typed 400, auth). `a08c417` —
        `TestCreateLoopEndpointCreatesLoop`,
        `TestCreateLoopEndpointCadenceBelowFloorReturns400`,
        `TestCreateLoopEndpointRequiresAuth`
        (cmd/cortex/serve_loops_test.go).
  - [x] M6.7d Enable/disable endpoints: `POST /api/loops/{name}/enable`
        and `POST /api/loops/{name}/disable`, flipping `Spec.Enabled` via
        `Store.Save` (httptest, unknown name ⇒ 404, auth) — combined into
        one item since both are the same single-field toggle, mirroring
        M4.2c2b1's combined-writes precedent. `e086fab` —
        `TestSetLoopEnabledEndpointTogglesFlag`,
        `TestSetLoopEnabledEndpointUnknownNameReturns404`,
        `TestSetLoopEnabledEndpointRequiresAuth`
        (cmd/cortex/serve_loops_test.go).
  - [x] M6.7e `POST /api/loops/{name}/run-now` invoking `RunLoopFiring`
        directly (httptest, scripted sender/fixture project, unknown name
        ⇒ 404, auth) — completes the milestone's named DoD surface
        (view-model golden + list/create/enable/disable/run-now wired,
        httptest); see the 2026-07-13 Decisions entry on why the actual
        loops-screen HTML/JS is NOT part of M6.7's literal DoD and is
        deferred pending an owner amendment if wanted. `b9a3792` —
        `TestRunLoopEndpointFiresLoopAndReturnsRunHistory`,
        `TestRunLoopEndpointUnknownNameReturns404`,
        `TestRunLoopEndpointRequiresAuth` (cmd/cortex/serve_loops_test.go).
        **M6.7 is now COMPLETE** (all five sub-items a–e landed).
- [x] M6.7f loops screen render (owner amendment A4): loops.js +
      #loops container over the M6.7a view-model, controls calling the
      M6.7b–e endpoints; structural tests per the M5.3 screen pattern;
      stays within the M5.3a JS size caps. `bbbdf9c` —
      `TestLoopsScreenJSFetchesLoopsEndpoint`,
      `TestLoopsScreenIndexHTMLHasLoopsContainer`,
      `TestIndexHTMLLoadsAppJSBeforeLoopsJS`,
      `TestLoopsScreenJSHasCreateForm`,
      `TestLoopsScreenJSHasEnableDisableControls`,
      `TestLoopsScreenJSHasRunNowControl`,
      `TestLoopsScreenJSAuthenticatesWriteRequests`,
      `TestLoopsScreenJSReloadsAfterActions`
      (cmd/cortex/webui_loops_screen_test.go),
      `TestWebUIJavaScriptSizeCaps` (cmd/cortex/webui_jscap_test.go,
      pre-existing — reconfirmed green with loops.js counted in its
      whole-embedded-FS walk).
- [ ] M6.8 serve-resident scheduler (owner amendment A4): tick goroutine
      in cortex serve composing Scheduler.Due + RunLoopFiring on an
      injected clock, clean shutdown on server stop, overlap/disabled
      never fire; fake-clock test, no sleeps.

## Next Up
Start M6.8: the serve-resident scheduler (owner amendment A4) — a tick
goroutine started by `cortex serve` (serve.go) composing
`internal/loops.Scheduler.Due` (M6.2) with `RunLoopFiring` (M6.3,
cmd/cortex/loop_run.go) on an injected clock, so enabled loop specs
actually fire while `cortex serve` is running rather than only via the
M6.7e run-now endpoint. Requirements per GOAL.md M6.8: ticks on an
injected clock (no test sleeps — mirror M6.2's fake-clock pattern from
internal/loops/scheduler_test.go), stops cleanly on server shutdown, and
never fires while a spec is disabled or an overlap condition holds
(Scheduler.Due/overlap-skip already encode this — the new code is just the
tick-loop wiring + shutdown plumbing, composing existing pieces rather than
reimplementing due/overlap logic). This is the LAST unchecked item in the
entire ladder (GOAL.md §6) — after M6.8 lands, M6 and the whole spec are
complete; re-read GOAL.md §6 in full before starting to confirm nothing
else was missed.

## How to Run / Verify
timeout 900 sh -c './scripts/check.sh && go test ./... -timeout 8m'
Repo is a Go 1.26 module; the coder binary is cmd/cortex.
Product spec: docs/cortex-web.md. Loop spec: GOAL.md (read fully first).

## Decisions Log (append-only)
- 2026-07-13: M6.7f landed `cmd/cortex/webui/loops.js` + a new `#loops`
  container in `index.html` (loaded after `app.js`, matching the
  landscape.js/models.js precedent), mirroring M5.3e's models-screen shape
  most closely since both need write actions + reload-after-write rather
  than models.js's read-only-with-scope-switcher-plus-writes pattern is
  actually the same shape loops needed: render the GET /api/loops
  (loopsViewModel) response, then per-loop Enable/Disable + Run now buttons
  POSTing to M6.7d/e's endpoints, plus a create form POSTing to M6.7c's
  `POST /api/loops` — all four write actions call `loadLoops()` again on
  success (full re-fetch-and-rerender, not a client-side patch, matching
  every prior screen's posture). Added a shared `postJSON(path, body)`
  helper (loops.js) rather than repeating the PUT-with-headers boilerplate
  models.js's `saveBinding` inlines, since loops.js has four write call
  sites (create/enable/disable/run-now) vs. models.js's one. No new Go
  endpoint or view-model needed — M6.7a-e already shipped the full
  read+write surface; this item was purely the JS/HTML render.
  `TestWebUIJavaScriptSizeCaps` (pre-existing, M5.3a) needed no changes —
  it already walks every `.js` file under the embedded FS rather than
  enumerating a fixed list, so `loops.js` was automatically counted; total
  across all five `.js` files is now 793 lines, well under the 1200-line
  cap. Load-bearing check done: wrote
  `webui_loops_screen_test.go` before `loops.js`/the `index.html` edit
  existed — all 8 new tests failed red (`loops.js: file does not exist` /
  missing `#loops` container), confirmed, then implemented and reran green.
- 2026-07-13: M6.7c landed `handleCreateLoop` (`POST /api/loops`,
  cmd/cortex/serve_loops.go) mirroring M4.2c2b1's `PUT /api/models/{role}`
  shape exactly: decode body → `Store.Save` → typed error mapping
  (`errors.Is(err, loops.ErrCadenceTooLow)` ⇒ 400, anything else ⇒ 500) →
  read the persisted value back via `Store.Lookup` rather than echoing the
  request, so the response can't drift from what's actually on disk. No
  name-emptiness validation was added — GOAL.md's M6.7c wording asks only
  for "happy path, cadence-floor rejection, auth", and `FileStore.Save`
  already has no analogous check for empty names to preserve; scope-
  creeping a new validation rule not requested by the spec was avoided.
  Load-bearing check done: ran the two content-asserting tests
  (`TestCreateLoopEndpointCreatesLoop`,
  `TestCreateLoopEndpointCadenceBelowFloorReturns400`) before adding the
  route/handler — both failed with 404 (route not yet registered, prior to
  `handleCreateLoop`/`mux.HandleFunc` landing), confirming they exercise
  the new code; implemented, reran, both green.
- 2026-07-13: OWNER: amendment A4 added M6.7f (loops screen render) and M6.8 (serve-resident scheduler) to GOAL.md §6 and this checklist — the iteration-54 rulings correctly identified a ladder gap vs docs/cortex-web.md Phase 6; both items are now required for M6 completion.
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
- 2026-07-12: M5.1 landed the embed+serve plumbing only: `cmd/cortex/webui/`
  holds placeholder `index.html`/`app.js`/`app.css`; `cmd/cortex/webui.go`
  embeds them (`//go:embed webui`, `fs.Sub` to strip the prefix) and serves
  via stdlib `http.FileServerFS` (no new dependency); `newServeMux` (serve.go)
  registers it at `"/"` — Go 1.22+ ServeMux's longest-match precedence means
  the more specific `"/api/..."` patterns still win regardless of `"/"`
  being registered first. Left the UI route behind the SAME `authMiddleware`
  as every other route (production wiring in `runServeCLI` wraps the whole
  mux) rather than deciding a browser-loads-without-a-header exemption now —
  GOAL.md §3 P4 binds "Token auth on every endpoint" and M5.1's DoD is
  silent on auth, so carving an exemption here would be undecided scope
  creep; the test authenticates the same way every other serve test does
  (`Authorization: Bearer tok` on an `http.Client` request, not a real
  browser navigation). If a real-browser flow needs unauthenticated static
  assets (e.g. a `?token=` bootstrap pattern), that's a decision for M5.3/
  M5.4 when there's an actual page to drive, not this plumbing increment.
  Load-bearing check done: commented out `mux.Handle("/", webUIHandler())`
  in serve.go, reran the new test — confirmed it fails ("got status 404,
  want 200"); route restored, full suite green again before committing.
  Standing-regression-guard check done: `git diff --name-only
  <genesis>..HEAD -- '*_test.go'` lists only files that postdate genesis
  (webui_test.go included, newly created) — no pre-existing test file
  touched.

- 2026-07-12: M5.2 split into M5.2a (dashboard)/b (transcript)/c
  (landscape)/d (models) — one view-model builder per Phase 5 screen,
  mirroring the granularity M4.2's splits already established. M5.2a
  landed `buildDashboardViewModel` (`cmd/cortex/webui_dashboard.go`)
  composing three existing seams rather than inventing new ones (GOAL.md
  pillar 3): `internal/registry.Registry.List` for the project set,
  M4.2a's `listSessions`/`sessionSummary` (serve_routes.go) for the
  per-project session list, and a NEW `changeStatusFor(dir string)`
  helper in `change.go` for git change status — `cortex change status`'s
  existing logic (`currentBranch`/`gitClean`/`onChangeBranch`) is CWD-only
  (`gitCmd` shells out with no `Dir` set), which doesn't work for a
  dashboard rendering arbitrary registered project roots from inside
  `cortex serve`'s process. Rather than duplicate the git-status logic,
  split `gitCmd` into a thin wrapper over a new `gitCmdIn(dir string,
  args ...string)` (dir="" behaves identically to the old CWD-implicit
  gitCmd — a behavior-preserving refactor, `cortex change`'s own three
  existing tests stayed green untouched) and added `changeStatusFor` on
  top of it; `currentBranch`/`gitClean`/`onChangeBranch` themselves were
  left untouched (still CWD-based, still what `cortex change status`
  uses) rather than rewritten to take a dir param, since GOAL.md's
  non-goal list and pillar 5 ("small honest slices") don't ask for that
  wider refactor and every existing call site is correctly CWD-scoped
  already. A project whose git status errors (not a git repo, git not on
  PATH, etc.) still renders in the dashboard with a per-row
  `change_error` field rather than failing the whole view-model build —
  chosen because a partially-registered or non-git project shouldn't take
  down the dashboard for every OTHER project. Sort by project name for a
  deterministic wire order (`registry.Registry.List` makes no ordering
  guarantee — confirmed both `FileRegistry.List` and the test-only
  `fakeRegistry.List`, which iterates a Go map, are unordered). Golden
  test correction: the prior iteration's Next Up note suggested a
  `testdata/*.golden` file; no such mechanism exists anywhere in this
  repo (confirmed by search) — followed the ACTUAL established
  convention instead (a literal expected string inside the Go test
  itself, per M1.5/M2.5's Decisions entries). Two golden-adjacent
  determinism traps found and fixed here, both worth flagging for M5.2b:
  (1) `sessionInfo.ModTime` comes back from `os.Stat` in the host's LOCAL
  timezone, which would make a fixed-instant fixture serialize with a
  machine-dependent UTC offset — `buildDashboardViewModel` normalizes
  every session's `ModTime` to UTC before it's on the wire; (2) a fixture
  git repo with session files written directly under its `.cortex/`
  directory reads as dirty (untracked) unless a committed `.gitignore`
  excludes `.cortex/` first — mirrors CLAUDE.md's real
  "`.cortex/` in `.gitignore`" invariant, so the fixture now matches how
  a real registered project is actually set up, not a fixture-only
  workaround. Standing-regression-guard note: `change.go` (not a test
  file) was modified freely; new tests for it were deliberately placed in
  a brand-new `cmd/cortex/change_status_test.go` rather than appended to
  the genesis-predating `cmd/cortex/change_test.go`, so the guard's
  mechanical `git diff --name-only <genesis>..HEAD -- '*_test.go'` check
  stays clean without needing a correction-rule justification (confirmed:
  `change_test.go` was restored byte-identical to `git show HEAD~1:...`
  before committing). Load-bearing checks done: (1) commented out the
  `sort.Slice` call and the `changeStatusFor` branch/active/clean
  assignment (replaced with a no-op) — confirmed both
  `TestBuildDashboardViewModelGolden` and
  `TestBuildDashboardViewModelNonGitProjectSurfacesChangeError` fail with
  the expected mismatches, then restored from a pre-edit copy; (2)
  standing-regression-guard check via `git cat-file -e
  <genesis>:cmd/cortex/change_status_test.go` and the same for
  `webui_dashboard.go`/`webui_dashboard_test.go` — all three fail ("not
  in <genesis>"), confirming none is a pre-existing file, and `git diff
  --name-only <genesis>..HEAD -- '*_test.go'` after the commit lists no
  genesis-present test file.

- 2026-07-12: M5.2b landed `buildTranscriptViewModel`
  (`cmd/cortex/webui_transcript.go`) as a thin reader over the existing
  `loadSession` (session.go) — no new parsing path, mirrors M5.2a's
  precedent of composing existing seams rather than inventing parallel
  ones. `transcriptEntry`'s tool-call shape (`transcriptToolCall{ID, Name,
  Args}`) flattens `agent.ToolCall`'s nested `Function{Name,Arguments}`
  into two top-level string fields — a presentation simplification for the
  UI layer, not a wire-format passthrough (matches the "rendering logic
  lives in the Go view-model" GOAL.md §3 P5 binding). Decided to OMIT the
  seed system-prompt message (index 0 of every fresh session, written by
  session.go's write-on-create loop) from the rendered entries rather than
  include it with `role:"system"`: GOAL.md/docs/cortex-web.md don't specify
  either way, but `display.go`'s `Message.gutter()` — the REPL's own
  transcript renderer — has no `RoleSystem` case (falls through to the
  assistant icon), so "system isn't a displayed turn" is the existing
  convention this view-model inherits rather than diverges from; a test
  (`TestBuildTranscriptViewModelOmitsSeedSystemMessage`) pins a
  system-only session renders to an empty (non-nil) `Entries` slice. Turn
  numbers come straight from `loadSession`'s parallel `turns []int` (no
  omitempty on `transcriptEntry.Turn` — 0 is a real, meaningful turn index
  post-filtering, not "absent"). Load-bearing check done: moved
  `webui_transcript.go` out of the tree, confirmed all three new tests
  fail to build (`undefined: buildTranscriptViewModel`), restored and
  reran green.

- 2026-07-12: M5.2c landed `buildLandscapeViewModel` (`webui_landscape.go`)
  as a genuine pass-through over `ScanReport` — resolved in favor of "no new
  wrapper type" after reading `scan.go`'s `ScanReport`/`buildScanReport` and
  `serve_landscape.go` per the prior iteration's Next Up question: unlike
  M5.2a (dashboard, which composes three unrelated sources into a new shape)
  and M5.2b (transcript, which flattens JSONL into display entries),
  `ScanReport`'s existing fields (`Roots`, `Tools`, `Runtimes`, `Projects`,
  `Truncated`) are already exactly names-and-paths-only per M2's
  content-non-leak invariant and already exactly what a landscape screen
  renders — a distinct struct would duplicate every field with zero
  presentation-only additions, the "no test would notice reverting"
  anti-pattern GOAL.md §1 warns against. Also refactored `handleLandscape`
  (`serve_landscape.go`) to delegate to the new function instead of
  duplicating `resolveScanRoots`+`buildScanReport` inline — one call path
  for "what the landscape means," matching M4.2c1's own doc-comment intent;
  confirmed via `serve_landscape_test.go`'s three existing httptest-level
  tests (status codes only, no exact-JSON-shape assertions there) that the
  minor 500-path error-message-text change (collapsed two distinct prefixes
  into one) is not test-observed, so it's a safe consolidation. Confirmed
  empirically (a throwaway debug test, removed before commit) that
  `landscape.Tool`/`Runtime`/`Project` carry no JSON struct tags, so their
  fields marshal capitalized (`"Path"`, `"Markers"`, `"Name"`) and an empty
  `ScanHarnesses`/`ScanRuntimes` result marshals as JSON `null` (a nil Go
  slice), not `[]` — both golden strings are written against that verified
  behavior rather than an assumption; worth flagging for M5.2d and M5.3/M5.4
  since any other view-model wrapping an landscape/scan-adjacent struct will
  hit the same nil-slice-is-null behavior. Load-bearing check done: gutted
  `buildLandscapeViewModel` to `return ScanReport{}, nil` unconditionally,
  confirmed both new tests fail with the expected mismatches (empty JSON vs.
  the fixture-derived golden; `nil` err vs. `ErrNoScanRoots`), restored from
  a saved copy and reran green.

- 2026-07-12: M5.2d landed `buildModelsViewModel` (`webui_models.go`) as a
  genuinely NEW shape, NOT a pass-through of `serve_models.go`'s existing
  `modelsResponse` — resolved the prior iteration's open question
  ("does `/api/models` already carry a per-role 'resolved from' field, or
  is this new presentation logic") by reading `Config.resolveBinding`
  (config.go): it collapses an explicit `models.<role>.model` config entry
  and a `selectModel`-picked fleet candidate into the same `Model` field
  with no trace of which happened, so "where each binding resolves from"
  (docs/cortex-web.md Phase 5's wording) has to be computed here, fresh,
  by checking `cfg.Models[role].Model != ""` before calling
  `resolveBinding`. `modelsResponse`/`handleModels` are untouched by this
  increment — this is the M5.2a/b relationship (new shape) not the M5.2c
  one (pure wrapper); a route wiring `buildModelsViewModel` into the HTTP
  surface, if the JS screen ends up needing one distinct from
  `GET /api/models`, is deferred to M5.3/M5.4. Interpreted "across the
  three scopes with a scope switcher" (docs/cortex-web.md) as UI-level
  scope selection for M5.3 to wire against M4.2c2b's already-existing
  `?scope=user|project|session` write endpoints — NOT something this
  view-model builder does itself; `buildModelsViewModel(cfg, fleet)` takes
  whichever already-resolved `*Config` the caller has, one scope at a
  time, matching M4.2c2a's own single-config-path shape. Source values:
  `"configured"` (explicit `models.<role>.model` present and non-empty in
  `cfg`), `"fleet-auto"` (`resolveBinding` filled `Model` in via
  `selectModel(fleet, role)` instead), `"unbound"` (neither). Roles
  returned as a slice sorted alphabetically by role name (`rolePolicies`
  is a Go map — no ordering guarantee), mirroring `dashboardViewModel`'s
  sort-for-determinism precedent from M5.2a. `cfg` accepts nil (mirrors
  `resolveBinding`'s own nil-receiver handling, and `loadMergedConfig` can
  genuinely return nil when no config file exists on either layer) —
  every role still resolves to `"fleet-auto"` or `"unbound"` correctly
  with a nil config, pinned by
  `TestBuildModelsViewModelNilConfigEveryKnownRoleStillPresent`. Golden
  fixture deliberately uses a minimal one-entry fleet (`{"study-model":
  {Role: "reasoner"}}`, `MaxInput: 0`) so `applyFleet`'s Window-fill never
  fires — keeps the golden JSON small while still exercising all three
  Source values in one test (role "code" configured explicitly in the
  fixture config; roles "reason"/"study" share the "reasoner" tag and
  both resolve to "study-model" via fleet-auto; every other role has
  neither a config entry nor a matching fleet tag ⇒ unbound). Confirmed
  `ModelInfo`'s fields carry no JSON struct tags (matches M5.2c's same
  finding for `landscape.Project`), so `Fleet` marshals with capitalized
  keys (`"MaxInput"`, `"Role"`, …) in the golden. Load-bearing check done:
  moved `webui_models.go` out of the tree, confirmed all three new tests
  fail to build (6 `undefined` errors: `buildModelsViewModel`,
  `modelBindingView`, `bindingSourceFleetAuto`, `bindingSourceUnbound`),
  restored and reran green.

- 2026-07-12: M5.3 split into M5.3a (mechanical JS size-cap test) through
  M5.3e (models screen), following the prior iteration's suggested
  "JS-size-cap-test vs. screens-render" axis rather than a pure
  per-screen split — the cap test is a pure Go-side regression guard
  with no dependency on any screen's markup existing yet, so landing it
  standalone first (rather than folding it into whichever screen lands
  first) means it starts gating growth immediately instead of only from
  whichever screen happens to be M5.3's first sub-item. M5.3a landed as
  a test-only increment (no production code): `TestWebUIJavaScriptSizeCaps`
  (cmd/cortex/webui_jscap_test.go) walks `webUIFS()` (M5.1's embedded
  FS accessor, already exported) summing `.js` line counts, asserting
  each file ≤300 lines and the total ≤1200. Chose `strings.Count(data,
  "\n")+1` as the line-count method per the prior iteration's own Next
  Up note (matches the DoD's literal phrasing). Because M5.1's
  placeholder `app.js` is far under both caps already, there was no
  natural implementation change to red-then-green against (unlike most
  increments, this test needs no companion production code — the
  embedded FS and its accessor already exist); load-bearing check done
  the way M2.3's content-leak test did it (planted a fixture that
  violates the invariant, confirmed the test catches it, removed the
  fixture before committing) rather than the more common
  revert-the-implementation pattern: (1) planted a single 321-line
  `.js` fixture under `cmd/cortex/webui/`, confirmed the per-file leg
  fails ("zz_oversized_fixture.js: 321 lines, want <= 300"); (2)
  separately planted five 280-line fixtures (each under the per-file
  cap individually, 1400 lines combined) confirmed the total leg fails
  independently of the per-file leg ("total JS lines ... = 1409, want
  <= 1200"); removed both fixture sets, reran green. Both legs are
  therefore independently load-bearing, not just one covering the
  other. Standing-regression-guard check: `webui_jscap_test.go` is a
  brand-new file (postdates genesis), so no pre-existing test file was
  touched. Deferred to M5.3b: how the served page's own `fetch` calls
  authenticate against `authMiddleware`'s bearer-token gate — no prior
  Decisions entry resolves this, and it blocks any screen's fetch logic
  from actually working end-to-end once written, not just being
  tested.

- 2026-07-12: M5.3b landed the dashboard screen and, with it, resolved
  M5.1's deferred "how does the browser's fetch authenticate" question.
  Two decisions: (1) authMiddleware now exempts non-"/api/" paths from
  the bearer-token check (cmd/cortex/serve.go) — a plain browser
  navigation to http://127.0.0.1:<port>/ can never attach a custom
  Authorization header, so gating the static UI shell would make the
  page unreachable by any normal browser action; there is no cookie/
  session mechanism in scope to bridge that gap without a new
  dependency. Splitting the gate at the "/api/" prefix keeps every
  byte of real project/session/landscape/model data behind the token
  (GOAL.md §3 P4's "token auth on every endpoint" read as every data
  endpoint) while letting the inert HTML/CSS/JS shell load freely; no
  existing test asserted 401 on "/" without a token, so nothing needed
  correcting under the standing-regression guard. New test:
  TestServeAuthMiddlewareAllowsStaticAssetsWithoutToken
  (cmd/cortex/serve_test.go, a file that postdates genesis, so no
  Decisions-Log correction is required to touch it). (2) The token
  itself rides in as a "?token=" query param the page is opened with
  (window.location.search, read once by app.js's new authToken()
  helper and attached as "Authorization: Bearer <token>" to every
  subsequent /api/... fetch via a small apiFetch() wrapper) — the
  simplest mechanism satisfying "no new deps, no framework" (GOAL.md
  D9) that a human can act on directly from `cortex serve`'s printed
  startup line (token: <token>) by appending it to the URL when
  opening the page in a browser.
  Also landed GET /api/dashboard (cmd/cortex/serve_dashboard.go)
  wiring M5.2a's buildDashboardViewModel into the HTTP surface — no
  such endpoint existed yet (M4.2's endpoint set predates M5.2a's
  richer view-model; GET /api/projects, M4.2a, stays the plain
  registry listing). Chose a dedicated endpoint over composing the
  screen client-side from /api/projects + per-project
  /api/projects/{name}/sessions (which the prior iteration's Next Up
  note assumed) because that composition drops the git change-status
  field entirely (no endpoint surfaces it) — a dedicated endpoint is
  the literal reading of GOAL.md §6 M5.3 ("the four screens render
  those view-models") and keeps the JS thin (one fetch, no
  per-project fan-out).
  Testing convention for M5.3 (binding for M5.3c/d/e too, per the
  prior Next Up's request): Go-side httptest coverage for any new
  endpoint a screen needs, plus structural source-content assertions
  over the embedded FS (a Go test reads app.js/index.html via
  fs.ReadFile(webUIFS(), ...) and asserts expected substrings — the
  fetch call target, a container element id) — never executed-DOM/
  JS-engine assertions, since this repo's stdlib-only test suite has
  no JS runtime. See cmd/cortex/webui_dashboard_screen_test.go.
  Load-bearing checks done: (1) moved serve_dashboard.go out of the
  tree, confirmed go vet ./cmd/cortex/... fails (undefined:
  handleDashboard), restored; (2) reverted app.js to a one-line
  placeholder and flipped index.html's id="dashboard" to
  id="notdashboard", confirmed both TestDashboardScreen* tests fail
  with the expected messages, restored from backups; (3) reverted
  authMiddleware's "/api/" exemption, confirmed
  TestServeAuthMiddlewareAllowsStaticAssetsWithoutToken fails to build
  (unused "strings" import once the code using it is removed),
  restored.

- 2026-07-12: M5.3c split into M5.3c1 (transcript endpoint) through M5.3c4
  (live SSE), following the M5.3b Next Up note's own observation that the
  session screen bundles a genuinely novel piece (SSE consumption in JS)
  on top of the now-familiar endpoint+static-render+input-box shape the
  dashboard screen established — splitting on that seam (endpoint / static
  render / mutation / streaming) keeps each sub-item a single new
  capability rather than a fatter one-shot dashboard-style landing. Landed
  M5.3c1 in the same iteration (precedent: M5.3's own split iteration also
  landed M5.3a). `handleGetSession` (serve_transcript.go) is deliberately
  independent of `SessionManager` — like `handleListProjectSessions`
  (M4.2a), it reads the on-disk transcript directly via
  `reg.Lookup`→`NewWorkspace`→`SessionsDir()`+`"<id>.jsonl"` (the same
  join `session.go`'s `StartTranscript`/`ResumeTranscript` and
  `tool_deps.go` already use — no new helper introduced, matching GOAL.md
  §1's "reuse the seam"), so a transcript is viewable whether or not the
  SessionManager currently holds that session live. `buildTranscriptViewModel`
  wraps `loadSession`'s `os.ReadFile` error with two layers of `%w`, so
  `errors.Is(err, os.ErrNotExist)` still resolves correctly through the
  chain to a 404 for an unknown session id (verified, not assumed — see
  load-bearing check below). Load-bearing check done: removed the
  `mux.HandleFunc("GET /api/projects/{name}/sessions/{id}", ...)`
  registration line, confirmed `TestGetSessionEndpointReturnsTranscriptViewModel`
  fails (404 instead of 200, since an unregistered path falls through to
  the catch-all "/" handler) while the negative-path tests still pass
  incidentally (they expect 404/401 anyway), restored the line and reran
  green — the positive-path test is what's load-bearing here.

- 2026-07-12: M5.3c2 landed the session screen's static render entirely in
  `cmd/cortex/webui/app.js`/`index.html` (no new Go). `queryParam(name)`
  factors out `authToken()`'s prior inline
  `new URLSearchParams(window.location.search).get(...)` so `?project=` and
  `?session=` reuse the exact same mechanism as the established `?token=`
  precedent (`authToken` now calls `queryParam("token")` — a pure refactor,
  no behavior change, not separately load-bearing-checked since it's a
  same-file same-commit inlining with no test asserting the old inline
  form). `loadSession()` mirrors `loadDashboard()`'s guard shape exactly
  (`getElementById` null-check ⇒ no-op) plus an additional guard on both
  query params being non-empty, per GOAL.md's read-only-render-only scope
  for this sub-item — a page with no `#session` container (there is none
  yet outside this one `index.html`) or missing params does nothing, so
  `loadSession()` is safe to call unconditionally at module load alongside
  `loadDashboard()`. `renderSession` follows `renderDashboard`'s
  textContent-only DOM-write posture (never innerHTML with response data).
  Testing convention reconfirmed from M5.3b/M5.3c1: structural
  source-content assertions over the embedded FS (fetch path present,
  query-param keys present, container id present) — no executed-DOM
  assertions; three new tests in a fresh `webui_session_screen_test.go`
  (following `webui_dashboard_screen_test.go`'s file-per-screen
  convention). Load-bearing check done: `git stash` on just the two
  modified webui files, confirmed all three new tests fail with the
  expected "does not fetch/read/declare" messages, `git stash pop` restored
  them, reran the full verify suite green before committing. app.js is now
  180 lines (per-file cap 300, well inside); no other `.js` file exists yet
  so the 1200-line total cap isn't in play.

- 2026-07-12: M5.3c3 landed `renderTurnInput` (`cmd/cortex/webui/app.js`) as
  a function separate from `renderSession`, called immediately after it
  inside `loadSession()`'s fetch chain rather than inlined into
  `renderSession` itself: `renderSession` clears (`container.textContent =
  ""`) and rebuilds `#session` purely from the transcript view-model on
  every call, so any input/button markup placed inside it would be wiped on
  every re-render (including the very re-render the submit handler
  triggers) — appending the input box AFTER `renderSession` runs, on every
  `loadSession()` call, is what makes it survive. Considered and rejected:
  static input/button markup in `index.html` outside `#session`'s cleared
  region — rejected because the form needs the current `project`/`session`
  query-param values closed over in its submit handler, which `loadSession`
  already has in scope and `index.html`-level static markup would not.
  Submit handler POSTs `{"input": text}` (matching `serve_turn.go`'s
  `turnRequest` shape) to the same `/api/projects/{name}/sessions/{id}/turn`
  path `apiFetch`'s GET already targets, but via a raw `fetch` (not
  `apiFetch`, which is GET-only/no-body) carrying its own
  `Content-Type`/`Authorization` headers; on success it clears the input and
  calls `loadSession()` again — full re-fetch-and-rerender, not a
  client-side entry append, per the prior iteration's Next Up note (no
  staleness concerns yet at this screen's size). On failure the error
  renders into a dedicated `#turn-status` span rather than blowing away the
  transcript already on screen. Testing convention reconfirmed from
  M5.3b/c1/c2: structural source-content assertions in a new
  `webui_session_input_test.go` (POST path present, `"POST"` literal
  present, `addEventListener("submit"` present; `createElement("input")`/
  `createElement("button")` present; `loadSession()` called ≥2 times as the
  cheapest reliable proxy for "called again after the initial page load
  call"). Load-bearing check done: reverted `app.js` to `HEAD` (via `git
  show HEAD:...`), confirmed all three new tests fail with the expected
  "does not reference/create/wire" messages, restored the new version byte-
  for-byte (diffed against the pre-revert copy) before rerunning the full
  verify suite green and committing. app.js is now 258 lines (per-file cap
  300 — getting close; flagged in this iteration's Next Up as the
  increment that may finally need a second `.js` file); total-JS cap 1200
  still has slack (one file).

- 2026-07-12: M5.3c4 landed live SSE progress by adding a second `.js` file
  (`cmd/cortex/webui/sse.js`, 51 lines) rather than growing `app.js` past
  300 — a generic `streamSSE(response, handlers)` frame parser, kept
  separate from `app.js` since it's a reusable low-level primitive (SSE
  wire-format parsing) with no knowledge of the session screen, matching
  the prior iteration's Next Up flag that a streaming-frame parser was the
  likely trigger for a second file. Chose `fetch` + `response.body.getReader()`
  over `EventSource` for the reason the prior Next Up note already
  identified as decisive: `EventSource` is GET-only, and `POST
  .../turn/stream` needs the turn's `input` text in a request body — a
  workaround (query param, or a POST-then-EventSource two-step) would add
  a second round-trip or leak the message into server logs/URLs for no
  benefit, whereas a single `fetch` POST both carries the JSON body and
  streams the response. `streamSSE` buffers decoded UTF-8 chunks and splits
  on `"\n\n"` (the exact frame boundary `sseEvent`, serve_stream.go, emits),
  parsing each frame's `"event: "`/`"data: "` lines and JSON-decoding the
  payload before dispatching to `handlers[event]` — matching
  `progressEvent`/`turnResponse`'s wire shapes already golden-pinned by
  M4.5's `TestTurnStreamEndpointGoldenFramesForMultiStepTurn`, so no new
  payload-shape assumptions were introduced. `renderTurnInput`'s submit
  handler now passes `streamSSE` three handlers (`progress` writes
  `payload.line` into `#turn-status`; `result` clears the input and calls
  `loadSession()` again, same terminal action M5.3c3's single `.then()`
  took; `error` writes `payload.error` into the same status span) — the
  `.catch()`/`.finally()` wrapping the whole `fetch(...).then(...)` chain
  is unchanged from M5.3c3, so a network-level failure (not a server-sent
  "error" SSE frame) is still caught there. `index.html` loads `sse.js`
  before `app.js` (script order matters — no module system, per D9) since
  `app.js`'s submit handler calls the global `streamSSE` function `sse.js`
  defines. Testing convention reconfirmed from M5.3b/c1/c2/c3: structural
  source-content assertions in a new `webui_session_stream_test.go` (POST
  target is `/turn/stream`; `sse.js` uses `getReader`/`"\n\n"`/`"event: "`/
  `"data: "`/`JSON.parse`; `app.js` registers `progress:`/`result:`/`error:`
  handler keys; `loadSession()` still called ≥2 times; `index.html` loads
  `sse.js` before `app.js` by string index) — no executed-DOM assertions,
  since this stdlib-only suite has no JS engine; the stream endpoint itself
  is already httptest- and golden-covered (serve_stream_test.go,
  serve_sse_golden_test.go). Load-bearing check done: reverted `app.js`/
  `index.html` to `HEAD` (`git show HEAD:...`) and deleted `sse.js`,
  confirmed three of the four new tests fail with the expected "does not
  POST/parse/register/load" messages (the fourth,
  `TestSessionScreenAppJSPostsToTurnStreamEndpoint`, passes even on the
  reverted file because the M5.3c3-era doc comment already mentions the
  literal string `/turn/stream` in prose describing the deferred work —
  noted as a pre-existing weak assertion, not fixed here since the real
  code now contains that literal too and the test remains meaningful
  post-implementation), then restored the implementation from backups
  (byte-for-byte, confirmed via `diff`) before rerunning the full verify
  suite green and committing. `app.js` is now 269 lines, `sse.js` is 51
  (per-file cap 300 each, comfortably under); total JS across
  `cmd/cortex/webui/` is 320 of the 1200 cap.

- 2026-07-12: M5.3d landed the landscape screen as a third `.js` file
  (`cmd/cortex/webui/landscape.js`, 84 lines) rather than growing `app.js`
  further — following the same size-cap reasoning M5.3c4 used to split out
  `sse.js`, and matching the prior iteration's Next Up note that flagged
  this exact outcome ahead of time. `renderLandscape`/`renderLandscapeSection`
  render `GET /api/landscape`'s `ScanReport` JSON (`roots`/`tools`/
  `runtimes`/`projects`/`truncated` — confirmed field-by-field against
  `cmd/cortex/scan.go`'s `ScanReport` struct tags and
  `internal/landscape.Tool`/`Runtime`/`Project`, which carry NO json tags
  and so serialize as capitalized `Name`/`Path`/`Markers` — verified against
  `webui_landscape_test.go`'s existing golden JSON rather than guessing)
  into a `#landscape` container: a roots summary line, then three titled
  sections (Tools/Runtimes/Projects) each with an empty-state "None found."
  message, plus a truncation warning when `report.truncated` is true.
  `loadLandscape()` treats HTTP 412 (the `ErrNoScanRoots` typed refusal
  `handleLandscape`, serve_landscape.go, returns when no scan roots are
  persisted) as a distinct "no scan roots configured yet" message rather
  than a generic fetch-failure string, since it's an expected
  not-yet-onboarded state (M1.7's greeting hasn't asked yet), not an error.
  `landscape.js` reuses `authToken()`/`apiFetch()` from `app.js` as plain
  global functions (no module system, per D9), so `index.html` loads
  `app.js` before `landscape.js` — mirrors M5.3c4's `sse.js`-before-`app.js`
  ordering requirement, just the dependency direction reversed (here
  `landscape.js` depends on `app.js`, not the other way around). No new Go
  endpoint or view-model needed — `GET /api/landscape` (M4.2c1) and
  `buildLandscapeViewModel` (M5.2c) were already complete; this increment
  is JS/HTML-only, same shape as M5.3c2. Testing convention reconfirmed
  from M5.3b/c/d: structural source-content assertions in a new
  `webui_landscape_screen_test.go` (`landscape.js` fetches `/api/landscape`,
  handles `412`, surfaces `truncated`; `index.html` declares a `#landscape`
  container and loads `app.js` before `landscape.js` by string index) — no
  executed-DOM assertions. Load-bearing check done: ran the five new tests
  before creating `landscape.js`/before editing `index.html` (files did not
  exist yet, so this is the "write test first" TDD flow itself, not a
  separate revert-and-recheck step) — confirmed all five fail with "file
  does not exist" / "does not declare" / "does not load" messages, then
  implemented and reran green. `app.js` unchanged at 269 lines; `sse.js`
  51; `landscape.js` 84 (per-file cap 300 each, comfortably under); total
  JS across `cmd/cortex/webui/` is now 404 of the 1200 cap.

- 2026-07-12: M5.3e landed the models screen as a fourth `.js` file
  (`cmd/cortex/webui/models.js`, 186 lines) — same size-cap reasoning
  M5.3c4/M5.3d used to split `sse.js`/`landscape.js` out, and the prior
  iteration's Next Up note flagged this as the most interactive screen yet.
  Unlike M5.3b/c/d (read-only renders), this screen needs a WRITE path:
  `renderModels` builds a `user`/`project`/`session` scope `<select>` plus a
  project-name/session-id text input pair (`renderScopeSwitcher`), backed by
  a module-level `modelScopeState` object (not re-created on each
  `loadModels()` re-render, since `renderModels` clears and rebuilds
  `#models` from scratch every call — same "state must live outside the
  cleared container" lesson M5.3c3 learned for the turn input box) seeded
  from the `?project=`/`&session=` query params `queryParam()` (app.js)
  already exposes, on the theory an operator arriving from the session
  screen most likely wants to write a session-scoped binding for the
  session they were just on. Each role gets its own text field + Save
  button (`saveBinding`) rather than one shared submit for all roles —
  matches `modelsResponse`'s per-role map shape and avoids a multi-field
  form needing its own validation-which-field-changed logic. `saveBinding`
  PUTs `{"model": value}` (the one field this screen edits — `ModelSpec`
  has seven others, deliberately out of scope for a first cut of this
  screen; `endpoint`/`window`/etc. editing is deferred, not asked for by
  GOAL.md's M5.3e text) to `/api/models/{role}?scope=...` with
  `&project=`/`&session=` appended only for those scopes, matching
  `handleSetModelBinding`'s (serve_models.go) own required-query-param
  branches; on success it calls `loadModels()` again — same
  full-re-fetch-and-rerender posture M5.3c3/c4 established for the session
  screen, not a client-side single-row patch. No new Go endpoint or
  view-model needed: `GET /api/models` (M4.2c2a's `modelsResponse` —
  `{roles, fleet}`) already carries everything this screen renders, and the
  M5.2d `buildModelsViewModel`/`Source` field is NOT used here — that
  function's own doc comment already flagged it as a distinct shape from
  `modelsResponse` with no route wired to it, and GOAL.md's M5.3e text
  ("role bindings across the three scopes with a scope switcher") doesn't
  ask for the per-binding provenance label `buildModelsViewModel` computes,
  so wiring it in was out of scope for this increment (a future increment
  could route `buildModelsViewModel` behind its own endpoint if the
  "effective-model column showing where each binding resolves from" text in
  docs/cortex-web.md Phase 5 is picked up later — noted here so that's a
  deliberate deferral, not an oversight). Testing convention reconfirmed
  from M5.3b/c/d: structural source-content assertions in a new
  `webui_models_screen_test.go` (`models.js` fetches `/api/models`; creates
  a `<select>` and offers all three scope literals; PUTs to a per-role
  `/api/models/` path with `scope=` and an `Authorization` header;
  `index.html` declares a `#models` container and loads `app.js` before
  `models.js` by string index; `loadModels()` called ≥2 times). Load-bearing
  check done: `models.js` didn't exist yet and `index.html` had no `#models`
  container (same "write test first" flow M5.3d used, not a separate revert
  step) — confirmed all six new tests fail with "file does not exist" /
  "does not declare" / "does not load" / "does not create" / "does not
  offer" / "does not target" / "is only called once" messages, then
  implemented, additionally re-verified by reverting both files to `HEAD`
  and back (byte-for-byte diff-confirmed restore) before rerunning the full
  verify suite green and committing. `app.js`/`sse.js`/`landscape.js`
  unchanged at 269/51/84 lines; `models.js` is 186 (per-file cap 300,
  comfortably under); total JS across `cmd/cortex/webui/` is now 596 of the
  1200 cap. **M5.3 complete (a/b/c/d/e all ticked)** — only M5.4 (the
  end-to-end smoke test) remains before M5 is done and M6 (loops) starts.

- 2026-07-12: M5.4 landed the end-to-end smoke test as a NEW file,
  `cmd/cortex/serve_e2e_test.go`, rather than folding it into an existing
  `serve_*_test.go` (matches this repo's one-concern-per-file convention
  every prior M4/M5 increment already followed).
  `TestServeEndToEndSmokeCreateSessionTurnStreamAndTranscriptReflectsIt`
  drives the real HTTP surface exactly per the prior iteration's Next Up
  plan: POST `/api/projects/blog/sessions` to create (through the HTTP
  layer, not `mgr.Create` directly — the point of a smoke test is proving
  the wiring, not re-testing `SessionManager` in isolation, which M4.2b1's
  own suite already covers), POST `.../turn/stream` and consume the SSE
  frames, then GET `.../sessions/{id}` and assert the transcript
  view-model reflects the turn. Reused two existing test helpers instead
  of re-inventing them: `streamTurnTestSessionFactory`
  (serve_stream_test.go) for the scripted two-round backend (round 1 a
  `bash("echo hi")` tool call, round 2 final content `"ok"`), and
  `sseEvents` (also serve_stream_test.go) for `"\n\n"`-delimited SSE frame
  parsing — both already proven against the real `runLoop`/`tools.Execute`
  path by M4.2b3/M4.5, so this smoke test adds no new scripted-backend or
  parsing code, only composition. No `streamSSE`-equivalent Go frame
  parser needed to be written new; `sseEvents` already was one. No new
  production code was needed either — M5.2b/M5.3c1-c4 already wired every
  endpoint this test exercises — so this increment is test-only, and
  "smallest implementation that passes" is the empty diff outside the
  test file. Load-bearing check done the way a pure-composition test
  requires (no implementation to revert): temporarily deleted the
  tool-calls-carrying loop in `buildTranscriptViewModel`
  (webui_transcript.go), reran the new test alone, confirmed it fails with
  "transcript view-model is missing the assistant's bash tool call", then
  restored the file from a backup copy (byte-for-byte diff-confirmed via
  `git status`/`git diff` showing no changes) before rerunning the full
  verify suite green and committing — proving the test actually notices
  broken transcript wiring, not just that it's syntactically valid. This
  closes M5 (M5.1-M5.4 all ticked); M6 (loops) starts at M6.1, and
  `internal/loops` (a brand-new package per GOAL.md §2) has zero
  dependency on any M5 code, so no gate-suspension or split was needed to
  start it cleanly.
- 2026-07-12: M6.1 `internal/loops.FileStore` mirrors `internal/registry`'s
  Registry shape verbatim (List/Lookup/Save/Remove, sorted List, same
  ErrXNotFound-wrapped-with-name pattern, same readAll/writeAll plain-JSON
  helpers) — GOAL.md §2's package layout calls for `internal/loops/` as a
  new package but names no different shape, and D4/D5 already settled
  "rebuildable specs as plain JSON" for both projects.json and loops.json,
  so reusing the proven shape needed no new Decisions justification beyond
  this pointer. `Spec` carries `IntervalMinutes int` (not `time.Duration`)
  for a human-readable loops.json (D10's "every-N minutes/hours/days" reads
  naturally as an integer minute count; a `time.Duration` field would
  marshal as raw nanoseconds, unreadable by a human inspecting the file)
  — 0 means manual-run-only (D10), exempt from the D11 cadence floor,
  which non-zero values must clear (`ErrCadenceTooLow`, checked in `Save`
  before any write, confirmed load-bearing by temporarily stubbing the
  floor check to `if false` and observing
  `TestFileStoreSaveRejectsCadenceBelowFifteenMinuteFloor` fail, then
  restoring byte-for-byte). `MaxTurns`/`MaxTokens` fields are present on
  `Spec` now (matching docs/cortex-web.md Phase 6's "bounds" field) but
  unvalidated here — M6.4 owns enforcing them at fire time, this item only
  had to carry the values through the round-trip. Trigger/prompt/enabled
  fields also follow Phase 6's spec shape (name, project, prompt, trigger,
  bounds, enabled) directly, no schema decisions needed.
- 2026-07-12: M6.2 landed `Scheduler.Due` (internal/loops/scheduler.go) as
  pure scheduling logic over three injected seams (`Clock`, `LastRunLookup`,
  `RunningCheck`) plus an `OnSkip` callback, per the prior iteration's Next
  Up analysis — no real session firing (M6.3), no budget caps (M6.4). Also
  landed `internal/journal`'s `loop.run` event type (loop.go, mirroring
  landscape.go's user-level-journal convention exactly: same
  `userhome.Path("journal", <class>)` resolution, same `FsyncPerBatch`
  choice) now rather than deferring it to M6.3, because M6.2's own DoD line
  ("overlap ⇒ skips AND journals the skip") is not satisfiable by
  scheduling logic alone — it needs an actual persisted event, not just an
  in-memory decision. Chose one `loop.run` event type carrying an
  `Outcome` enum (`success`/`failed`/`skipped`) rather than a separate
  `loop.skip` type, since GOAL.md M6.7's run-history view-model reads "run
  history from `loop.run` events" as one stream; M6.3/M6.4 will reuse the
  same type for real firings' success/failed outcomes rather than
  inventing a second one. `OnSkip` is injected (not a hard call to
  `journal.AppendLoopRun` inside `Due`) so the pure scheduling-decision
  tests don't need `CORTEX_HOME` isolation; one dedicated test
  (`TestSchedulerOverlapSkipsAndJournalsSkip`) wires the real
  `journal.AppendLoopRun` as `OnSkip` and reads the persisted entry back,
  proving the skip is actually journaled end-to-end, not just decided in
  memory — same isolation pattern as `internal/journal`'s existing
  landscape tests. `LastRunLookup`/`RunningCheck` remain injected functions
  (not reading the real journal or tracking real in-flight sessions) since
  that wiring is M6.3's firing machinery, which doesn't exist yet — a
  fabricated "real" implementation now would have no caller and no way to
  be proven correct beyond what the injected-fake tests already show.
  Load-bearing check done: moved `scheduler.go` and the new `loop.go` out
  of their packages, confirmed `go vet ./internal/loops/...
  ./internal/journal/...` fails to build (`undefined: Clock`,
  `undefined: LoopRunPayload`), moved both back and reran the full suite
  green.

- 2026-07-12: M6.3 landed `RunLoopFiring` (cmd/cortex/loop_run.go),
  mirroring `SessionManager.Create`'s exact seam sequence
  (`newSession()` → `applyProjectByName` → `StartTranscript`) rather than
  inventing a parallel session-construction path, and a dir-scoped git
  change lifecycle (`gitCleanIn`/`currentBranchIn`/`startChangeIn`/
  `commitChangeIn` in change.go) the existing CLI-facing zero-arg
  `gitClean`/`currentBranch`/`startChange`/`commitChange` now delegate to
  with `dir=""` — a pure refactor (no behavior change for `cortex
  change`'s own CLI), needed because `startChange`/`commitChange`
  previously always ran against the process CWD, which is wrong once a
  single long-lived process (a loop-firing driver, eventually `cortex
  serve`) fires against many different registered projects.
  `RunLoopFiring` always appends exactly one `loop.run` event on every
  exit path (unresolvable project, session-construction failure, a
  `Turn` error, or a clean success) so a firing that goes wrong stays
  visible in run history — the function's own Go `error` return is
  reserved for a failure to WRITE that journal event, an infra problem
  worth surfacing to whatever drives the scheduler loop, not a normal
  failed/errored run (which is itself a successfully-journaled outcome
  and returns nil). ChangeRef shape chosen: `"<branch>@<short-hash>"`
  (e.g. `cortex/nightly@abc1234`) — cheap to parse back into `cortex
  change status`-equivalent facts later (M6.7's run-history view) without
  a second lookup. Discovered a pre-existing gap while designing the
  test: the ordinary tool dispatcher (internal/tools, not the study
  door-guard `ConfinePath`) resolves `write_file`/`edit_file`/`bash`
  paths against the process's actual working directory, NOT any
  `Workspace` root — only `contextDir()`/instructions/`cs.root()`
  confinement are workspace-threaded today (M3.1's own equivalence test,
  `TestApplyProjectByNameRunsAgainstRegisteredRootFromUnrelatedCWD`,
  confirms this scope). This means a multi-project `cortex serve`
  process today would have every live session's file-writing tool calls
  land wherever the server process happens to be running FROM, not each
  session's own project root — a real product gap for Phase 4/6, but
  out of scope for M6.3's DoD (which only asks for the firing + journal
  event, not fixing tool-execution workspace threading). Flagging here
  for whoever next touches multi-project concurrent tool execution
  (M6.7's create/run-now UI, or a dedicated hardening item) rather than
  silently working around it forever — M6.3's own test sidesteps it
  correctly and honestly via `t.Chdir(root)` before firing, matching
  today's actual single-CWD-process assumption instead of pretending the
  gap doesn't exist. `startChangeIn` failing (e.g. the target project's
  tree is already dirty from an unrelated in-flight change) is handled
  leniently: the turn still runs, `ChangeRef` just stays empty — not
  exercised by a dedicated test this iteration (fixture always starts
  clean); flag if a future increment needs that path asserted. Load-
  bearing check done: moved `loop_run.go` out of the tree, confirmed `go
  vet ./cmd/cortex/...` fails to build (`undefined: RunLoopFiring`),
  restored it and reran the full suite green. Also fixed a lint break the
  refactor introduced mid-iteration: `gitCmd` (the old CWD-implicit
  wrapper) became dead code once every caller was moved onto
  `gitCmdIn`/the new dir-scoped helpers — removed it rather than keeping
  an unused function alive, `golangci-lint`'s `unused` check caught it
  before the final green verify run.

- 2026-07-12: M6.4 landed D11's per-run budget caps in three layers.
  (1) `agent.Bounds` gained `TokenBudget int` (cumulative input+output
  tokens, 0 = unbounded, distinct from the existing per-request
  `MaxTokens` completion cap) checked in `runLoop` (loop.go) immediately
  after `accountUsage`, BEFORE this round's response is even extracted/
  appended or its tool calls dispatched — chosen over checking after
  dispatch (where the pre-existing `ReadBudgetBytes` check lives)
  because a token-hungry round shouldn't get to dispatch more tool calls
  once it's already blown the budget; the round's assistant message is
  simply never added to the transcript, which needed no special-casing
  since finalizeLoop's re-ask doesn't depend on it. (2) `Session.turn`
  (turn.go) gained two override parameters (`maxIterOverride`,
  `tokenBudget`, both 0 = "use the normal default") threaded into a new
  `TurnWithBudget` method — `Turn`/`TurnWithProgress` pass 0/0,
  unchanged. `TurnResult` gained `StopReason string` (the engine's raw
  loopStats.StopReason) so a caller enforcing bounds can tell a
  bound-forced stop from a clean answer without an error to key off —
  necessary because a bound trip is NOT a Go error (the engine still
  finalizes and answers); `turnErr` alone can't see it. (3)
  `RunLoopFiring` (loop_run.go) now calls
  `cs.TurnWithBudget(ctx, spec.Prompt, spec.MaxTurns, spec.MaxTokens)`
  and checks `result.StopReason` for `"max-iter"` or `"token-budget"`
  after a nil `turnErr`, journaling `Outcome: LoopOutcomeFailed,
  Reason: "budget"` for either — matching the literal string
  `internal/journal/loop.go`'s `LoopRunPayload` doc comment already
  reserved. Deliberately treats BOTH bound types identically (one
  Reason string) since GOAL.md M6.4's own wording says "both journal
  `failed: budget`" — no need to distinguish which cap tripped in the
  journal event itself (a future run-history UI reading `StopReason`
  directly, if ever surfaced, could still tell them apart; not needed
  by any current DoD).
  Side-effect: adding a field to `TurnResult` broke two existing bare
  type conversions (`turnResponse(result)` in serve_turn.go and
  serve_stream.go — Go's struct-to-struct conversion requires identical
  underlying field sequences) — fixed by making both sites construct
  `turnResponse{Reply: result.Reply, Interrupted: result.Interrupted}`
  explicitly instead, which also means `StopReason` is deliberately NOT
  exposed on the wire (kept the M4.5 SSE golden test's exact JSON byte
  shape unchanged — verified it still passes untouched).
  Standing-regression-guard note: `loop_test.go` is genesis-present, so
  the new engine-level `TestRunLoopTokenBudgetFinalizes` was NOT appended
  there (an earlier draft that did so was reverted via `git checkout --
  cmd/cortex/loop_test.go` once this was caught) — it instead lives in a
  new file, `cmd/cortex/loop_budget_test.go`, which reuses loop_test.go's
  `fakeResp`/`readCall` helpers (same package, no duplication). The two
  `RunLoopFiring`-level sub-tests
  (`TestRunLoopFiringRunawaySessionHaltsAtTurnCap`,
  `TestRunLoopFiringTokenBudgetHaltsBeforeTurnCap`) were added to
  `loop_run_test.go`, which postdates genesis (landed in M6.3) — a clean
  extension, not a violation; confirmed via
  `git diff --name-only <genesis>..HEAD -- '*_test.go'` showing no
  genesis-present file among this iteration's changes.
  Load-bearing checks done: (a) `TestRunLoopTokenBudgetFinalizes` confirmed
  red before the `Bounds.TokenBudget`/runLoop check existed (written
  first, ran red, then the check was added). (b) `git stash push --
  cmd/cortex/loop_run.go` (reverting only the production file, keeping
  the new tests) reran the two `RunLoopFiring`-level tests and both
  failed with `Outcome = "success"`/`Reason = ""` instead of
  `"failed"`/`"budget"`, then `git stash pop` restored the fix and the
  full suite reran green before committing.

- 2026-07-13: M6.5 landed as a test-only increment — no production code
  changed. `RunLoopFiring`'s fresh headless session already inherits
  Risky ⇒ Blocked for free: `newProductionSession` (serve_session.go, M4)
  sets `quiet = true` and leaves `confirmRisky` nil, and `gateShell`
  (cmd/cortex/tool_deps.go) already treats `!cs.quiet && cs.confirmRisky
  != nil` as the sole condition for offering an interactive risky-command
  prompt — false in this state for ANY session, so the classifier's Risky
  verdict falls straight to the "blocked (risk: ...)" refusal string with
  no prompt ever attempted. This is the same mechanism the pre-existing
  `TestBashShellSyntax/risky_command_blocked_when_headless` (main_test.go,
  predates this loop) already proved at the bare `tools.Execute` level;
  `TestRunLoopFiringRiskyCommandBlockedNoPromptReachable`
  (loop_run_test.go) proves it again end-to-end through `RunLoopFiring`'s
  real session construction, which is the specific gap GOAL.md M6.5 names.
  Test design: the scripted sender's round 1 issues a `bash` tool call
  (`echo pwned > sentinel.txt`) that would leave a detectable side effect
  if it ran; the session's `classifyShell` is stubbed to return Risky
  directly (never the live LLM classifier — keeps the run hermetic and
  fast) and `confirmRisky` is wired to `t.Fatalf` if ever invoked, so "no
  prompt surface is reachable" is an affirmative trap, not proved merely
  by omission. Assertions: (1) `sentinel.txt` never appears in the
  fixture's worktree: the command did not run; (2) the transcript's tool-
  result message for that call contains "block" and never contains
  "pwned": the model saw a refusal, not the command's output;
  (3) the resulting `loop.run` event is `Outcome = success`, `ChangeRef =
  ""` — a blocked risky command does not error or budget-fail the
  firing (`TurnWithBudget` returns cleanly, no Go error, no
  max-iter/token-budget stop reason), it is handled entirely at the tool-
  dispatch layer and looks, to the journal, exactly like a no-op turn.
  This deliberately overturns the previous iteration's Next Up guess
  (`Outcome = failed, Reason` mentioning risk) — that would have required
  inventing a new `TurnResult` signal with no other caller, which is the
  "code no test would notice reverting" anti-pattern GOAL.md §1 warns
  against; the honest, already-true behavior is what got pinned instead.
  Load-bearing check done WITHOUT touching production security code (an
  attempt to flip `gateShell`'s headless branch to unconditionally allow
  was blocked by this environment's own safety classifier as a security
  weakening, and was not pursued further per that guardrail): instead,
  the test's own `classifyShell` stub was temporarily flipped from Risky
  to Safe — confirmed `sentinel.txt` then gets created and the test fails
  with "the risky command ran instead of being blocked" — proving the
  test is actually sensitive to the classification rather than vacuously
  passing, then reverted to Risky and the full suite reran green before
  committing.

- 2026-07-13: M6.6 landed `internal/loops.JournalLastRun`
  (internal/loops/lastrun.go) as the production `LastRunLookup`
  implementation — a pure read over the real user-level `loop.run`
  journal (the same one `AppendLoopRun`/`RunLoopFiring`, M6.3, writes to),
  scanning every entry (any outcome) for the most recent timestamp
  matching a spec's name. Placed in `internal/loops` rather than
  `cmd/cortex` (unlike M6.3's `RunLoopFiring`, which needs `CortexSession`
  machinery) because `internal/loops`'s own test file
  (`scheduler_test.go`'s `TestSchedulerOverlapSkipsAndJournalsSkip`)
  already imports `internal/journal` directly to compose `OnSkip` against
  the real journal — this package already depends on journal in practice,
  so a real `LastRunLookup` living there too is the natural home, not a
  new cross-package pattern. `TestSchedulerRestartAcrossProcessDoesNotDoubleFire`
  proves the DoD with three independent `Scheduler` instances sharing one
  on-disk journal (CORTEX_HOME-isolated) and a clock ANCHORED near
  `time.Now()` at test start rather than a fixed arbitrary date — chosen
  because the journal's `TS` field is stamped by the real writer's
  `time.Now().UTC()` (`AppendLoopRun` doesn't accept an injected
  timestamp, and adding one wasn't needed: anchoring the fake clock to
  real "now" once, then only ever advancing it via `Add`, keeps the
  interval math meaningful against real journal timestamps without any
  test ever sleeping or the production journal API growing a
  test-only knob). Deliberately did NOT wire `Scheduler.Due` into a
  running `cortex serve` process — no serve-resident tick loop exists yet,
  and GOAL.md M6.6's DoD text ("next-run derived… no double-fire across a
  scheduler restart") is satisfied by the lookup + Scheduler-level proof
  alone; the prior iteration's own Next Up note flagged this as an open
  question and this iteration resolved it in favor of the narrower reading
  (auto-tick wiring deferred to M6.7, which needs the loops screen's
  create/enable/disable/run-now endpoints regardless). Load-bearing check
  done: swapped `JournalLastRun` for a stub always returning `(zero,
  false)` — both new tests failed (the second entry's lookup returned
  not-found, and the "restarted, too soon" Scheduler re-fired since it
  saw no history) — then restored the real implementation and reran green.

- 2026-07-13: M6.7 split into M6.7a–M6.7e (this iteration's sole commit —
  baseline verify was already green, agreeing with STATE.md, so there was
  no code to write; per GOAL.md §7 step 4, "the split is this iteration's
  commit"). Rationale for the five-way cut: M6.7's un-split text bundles a
  view-model AND five distinct endpoints (list/create/enable/disable/
  run-now) into one checklist line, which is exactly the shape M4.2
  (endpoints) and M5.2/M5.3 (view-models/screens) were split at — M4.2 in
  particular went as granular as one endpoint (or one tightly-related pair)
  per commit (M4.2a, b1, b2, b3, c1, c2a, c2b1, c2b2), so a single
  undifferentiated M6.7 attempt risked either a red-verify iteration or a
  dishonestly-large commit; matching that established granularity keeps
  each sub-item independently landable and revert-testable.
  Two scope rulings recorded alongside the split, both resolving real
  ambiguity in M6.7's wording against GOAL.md's own precedence rule
  (§6 DoD text over docs/cortex-web.md prose) and Pillar 5 ("an increment
  the verify gate can't observe is not an increment"):
  (a) **The actual loops-screen HTML/JS is OUT of M6.7's literal DoD.**
  Every M5.3 sub-item that required a rendered screen explicitly names that
  test surface ("JS bounded mechanically", a `.js`-file walk, HTML
  container tests, screen-specific `_screen_test.go` files — see M5.3b–e
  above). M6.7's text names only two proofs: "view-model golden" and
  "…wired to NEW loops endpoints… (httptest)" — httptest exercises HTTP
  handlers, not DOM/JS, so no screen-render test is implied. "Loops
  screen" in the item's title tracks docs/cortex-web.md's Phase 6 framing
  and Amendment A2 (the fifth screen "lands in M6"), but the checklist
  body — which is what ticking actually requires per §6 ("ticking requires
  that named test to exist and pass") — is narrower. Treating this as
  backend-only is not scope-cutting silently: it's recorded here precisely
  so a future iteration doesn't invent unverified screen work, and so the
  owner can amend GOAL.md if the intent was broader.
  (b) **A standing serve-resident scheduler tick is OUT of M6.7's literal
  DoD**, formalizing the question the M6.6 iteration's Next Up note left
  open. GOAL.md M6.7's text names exactly four actions — create, enable,
  disable, run-now — none of which is "the scheduler ticks automatically
  while serve is up." `run-now` (M6.7e) is a direct synchronous
  `RunLoopFiring` call; enable/disable (M6.7d) only persist the `Enabled`
  flag via `Store.Save`. docs/cortex-web.md Phase 6's "ticks while cortex
  serve runs" describes the phase's eventual shipped value, not this one
  item's tested DoD — if the owner wants a live in-process tick loop
  wired into `cortex serve`, that needs either a GOAL.md amendment
  scoping it into M6.7 explicitly, or a new milestone item; it is NOT
  silently dropped, just not claimed as done by M6.7's tests.
  No load-bearing check applicable (no code changed this iteration).

- 2026-07-13: M6.7a landed (`9d47fa9`). Two design choices worth recording:
  (a) the new production reader `loops.JournalRunHistory` (internal/
  loops/history.go) is a sibling to M6.6's `JournalLastRun`, not a
  refactor of it — same journal, same per-name filter, but keeps every
  matching entry (chronological write order) instead of collapsing to one
  timestamp; `JournalLastRun` is untouched, avoiding risk to the M6.2/M6.6
  scheduler tests that depend on its exact (zero, false) "never run"
  signal. (b) `loopView` (cmd/cortex/webui_loops.go) embeds `loops.Spec`
  directly rather than re-declaring its fields on a wrapper struct — same
  "don't wrap a shape that already has exactly the fields a screen needs"
  reasoning M5.2c recorded for the landscape view-model — so `Runs` is the
  only field the view-model adds. Test-fixture note for future iterations
  touching `loop.run` golden JSON: `journal.AppendLoopRun` always stamps
  `time.Now()` when `Entry.TS` is zero (journal/writer.go's `Append`), so
  a byte-pinned golden needs `journal.NewLoopRunEntry` + a manually-set
  `entry.TS` + a direct `journal.NewWriter`/`Append` call instead (see
  `writeLoopRunAt` in webui_loops_test.go) — `AppendLoopRun` alone can't
  produce a deterministic timestamp. Load-bearing check done: reverted the
  `sort.Slice` call in `buildLoopsViewModel` (caused an `"sort" imported
  and not used` build failure confirming the code path is exercised, not
  a silently-dead line) then restored it and reran green; the golden test
  itself was written before the implementation and observed failing to
  compile (`undefined: buildLoopsViewModel`) before it existed.

- 2026-07-13: M6.7b landed `GET /api/loops` (`e8dbaa3`) by threading a new
  `loopsStore loops.Store` parameter through `newServeMux` — matching
  M4.2c1's precedent of adding a parameter (configPath/homeDir) rather than
  resolving the seam inside the handler, so tests stay hermetic. This
  parameter addition is mechanical but wide: it touched all 48 existing
  `newServeMux(...)` call sites across 12 pre-existing test files
  (serve_dashboard_test.go, serve_e2e_test.go, serve_landscape_test.go,
  serve_models_test.go, serve_restart_test.go, serve_routes_test.go,
  serve_session_test.go, serve_sse_golden_test.go, serve_stream_test.go,
  serve_transcript_test.go, serve_turn_test.go, webui_test.go), each now
  passing a new shared helper `testLoopsStore(t)` (serve_routes_test.go,
  backed by `loops.NewAt` on a temp-dir `loops.json` — mirrors
  `testSessionManager`'s role for a dependency most of those tests don't
  otherwise care about). Confirmed none of these 12 files existed at the
  genesis commit (`git cat-file -e <genesis>:<path>` failed for all 12),
  so the §2 standing regression guard does not apply — they were all
  landed by later M4.2/M5 iterations, not present at genesis. Load-bearing
  check done: removed the `mux.HandleFunc("GET /api/loops", ...)`
  registration line only, confirmed `TestListLoopsEndpointReturnsSpecsAndRunHistory`
  fails (404 instead of 200), restored the line and reran green.

- 2026-07-13: M6.7d landed `handleSetLoopEnabled` (`POST
  /api/loops/{name}/enable` and `.../disable`, `e086fab`) as a single
  handler factory closing over the `enabled bool` each route wants,
  registered as two `mux.HandleFunc` lines in `newServeMux` — this is
  the "combined-writes precedent" the prior iteration's Next Up note
  pointed at: one function, two routes, one item, rather than two
  near-duplicate handlers. The flip itself is Lookup-then-Save (not a
  narrower single-field patch): `Store` has no partial-update method,
  and `FileStore.Save`'s upsert-by-name already rewrites the whole
  record, so reusing it (with only `Enabled` changed on the fetched
  spec) is the natural fit — mirrors M6.7c's read-Lookup-back-after-
  Save shape, just Lookup-then-mutate-then-Save instead of Save-then-
  Lookup, since here the pre-image is required to know every other
  field to preserve. Unknown name maps `loops.ErrLoopNotFound` (via
  `Store.Lookup`) to 404, matching M6.7c's typed-error-to-status-code
  pattern. No new `newServeMux` parameter needed — `loopsStore` was
  already threaded in by M6.7b. Load-bearing check done: removed both
  new `mux.HandleFunc` registration lines only (handler function left
  in place), confirmed `TestSetLoopEnabledEndpointTogglesFlag` fails
  both subtests with 404 instead of 200 (the unknown-name-404 and
  auth-401 tests still pass, since a wholly unregistered route also
  404s/401s — expected and consistent with M6.7b's equivalent check),
  then restored the two lines and reran the full suite green.

- 2026-07-13: M6.7e landed `handleRunLoop` (`POST /api/loops/{name}/run-now`,
  `b9a3792`), completing M6.7. Confirmed the Next Up note's speculation:
  `RunLoopFiring` (loop_run.go, M6.3) already accepts exactly the
  `sessionFactory` seam `SessionManager` holds (`mgr.newSession`, an
  unexported field readable from `handleRunLoop` since both live in
  package `main`), so no new `newServeMux` parameter was needed — unlike
  M6.7b/M6.7d's `loopsStore` addition, which did require one because
  `internal/loops.Store` wasn't otherwise threaded in. The handler is
  Lookup-then-fire-then-read-back: `store.Lookup(name)` (404 via
  `loops.ErrLoopNotFound`, matching M6.7c/d's typed-error-to-status
  pattern), `RunLoopFiring(r.Context(), spec, reg, newSession)`, then
  `loops.JournalRunHistory(spec.Name)` re-read fresh from the journal and
  returned as a `loopView` (M6.7a's spec+runs shape) — so the caller sees
  the firing's outcome (success/failed/reason) in the same response
  without a second `GET /api/loops` round-trip, and the response is
  provably sourced from the same journal `buildLoopsViewModel` reads, not
  a handler-local echo. `RunLoopFiring`'s returned error is reserved for a
  journal-WRITE failure (an infra problem); a normal failed run (bad
  prompt, turn error, budget breach) still returns nil from
  `RunLoopFiring` and therefore still HTTP 200s here — the caller reads
  `Runs[0].Outcome` to see how the firing went, exactly like the
  `loop_run_test.go` M6.3/M6.4/M6.5 tests already treat "failed" as a
  successfully-journaled outcome, not a Go error. Test mirrors
  `loop_run_test.go`'s scripted-Sender-via-httptest + `initGitFixture`
  pattern (a real git-backed fixture project, `t.Chdir` into it — the
  same pre-existing gap `loop_run_test.go` documents, that `write_file`
  resolves paths against process CWD rather than any workspace root) and
  `serve_loops_test.go`'s existing `doAuthedPost`/`fakeRegistry` helpers;
  used `noopTurnTestSessionFactory` (already defined in `loop_run_test.go`,
  same package, reused directly) since the endpoint test only needs to
  prove the firing→response wiring, not re-prove M6.3's change-ref/budget/
  risk-posture behavior, which already has dedicated coverage. Load-bearing
  check done: removed the `mux.HandleFunc("POST
  /api/loops/{name}/run-now", ...)` registration line only, confirmed
  `TestRunLoopEndpointFiresLoopAndReturnsRunHistory` fails with 404
  instead of 200 (the unknown-name-404 and auth-401 tests still pass, same
  "unregistered route also 404s/401s" pattern M6.7b/d's checks noted),
  then restored the line and reran the full suite green.

## Known Issues (append-only)
- (none yet)
