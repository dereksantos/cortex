# STATE — cortex web loop
Updated: 2026-07-11 · Iteration: 16

## Current milestone
M3 — Workspace threading + project registry (M1, M2 complete)

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
- [ ] M3.3 `internal/registry`: CRUD round-trip on `projects.json` under
      a temp home; unknown-name lookup returns a typed error.
- [ ] M3.4 `cortex project add/list/remove` wired to the registry
      (CLI-level tests); `cortex scan --register` feeds discovered
      projects in.
- [ ] M3.5 `--project <name>` on `turn`, `resume`, and `study` resolves
      via the registry and runs against that root (fixture-repo test).

## Next Up
Start M3.3: `internal/registry` CRUD round-trip on `projects.json` under a
temp home (routes through `internal/userhome` per GOAL.md's package-layout
list); unknown-name lookup returns a typed error (mirror `ErrNoScanRoots`'s
sentinel-error pattern from M2.5's `scan.go`). No existing registry code in
the tree yet — this is a from-scratch new package.

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

## Known Issues (append-only)
- (none yet)
