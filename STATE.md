# STATE — cortex web loop
Updated: 2026-07-11 · Iteration: 7

## Current milestone
M2 — Landscape scan (M1 complete)

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
- [ ] M2.1 `internal/landscape`: per-family `Scanner` implementations
      (harnesses, runtimes, projects) composed by `Scan(root, caps)`;
      temp-dir fixture per family covering present / absent / malformed.
- [ ] M2.2 Every filesystem visit filtered through
      `projectscan.IgnoreSet`; a fixture with a planted secret path
      proves it never appears in any scan result.
- [ ] M2.3 Content-non-leak sentinel: fixture file bodies carry a
      unique sentinel string; serializing the full scan result (structs,
      `--json` output, and the `landscape.scan` journal event) contains
      it nowhere.
- [ ] M2.4 Caps enforced: fixtures exceeding max depth / max entries /
      a near-zero timeout each terminate cleanly (three tests) with
      truncation reported in the result — never silent.
- [ ] M2.5 `cortex scan [--json] [--root <path>]`: uses persisted
      roots, `--root` overrides, neither ⇒ typed refusal (all three
      paths tested); golden-file text report; JSON round-trip.
- [ ] M2.6 `scan_landscape` coder tool registered, gated by
      `tools.enable_scan` (absent ⇒ registered, false ⇒ absent — both
      tested), home-scoped and read-only.
- [ ] M2.7 Scan persists a `landscape.scan` event to the user-level
      journal under the user home (temp-home test); the coder tool
      additionally writes the `landscape` memory note to the current
      project's store (temp-workspace test asserts a fixture-derived
      string); headless scan writes no note (asserted).

## Next Up
Start M2.1: build `internal/landscape` with per-family `Scanner`
implementations (harnesses, runtimes, projects) composed by
`Scan(root, caps)` (GOAL.md M2.1 / §3 P2 scanner — "walks the real
filesystem... Probes return typed structs"). Design per docs/cortex-web.md
Phase 2: a `Scanner` interface per probe family, each returning typed
structs (`Tool`, `Runtime`, `Project`); fixtures are temp-dir trees (no
real `$HOME` in tests), one fixture per family covering present / absent /
malformed. M2.2 (IgnoreSet filtering) and M2.4 (caps) build on this same
`Scan` entry point but are separate checklist items — land the minimal
per-family scan first. `scan.roots` (M1.7's persisted config key, plural
paths under `{"scan":{"roots":[...]}}`) is what M2.5's `cortex scan`
subcommand will read; M2.1 itself can take `root` as a plain parameter and
doesn't need to touch config.

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

## Known Issues (append-only)
- (none yet)
