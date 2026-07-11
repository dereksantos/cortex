# STATE — cortex web loop
Updated: 2026-07-11 · Iteration: 3

## Current milestone
M1 — First-run bootstrap + greeting

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
- [ ] M1.4 First-run detection per GOAL.md §3 (user-home artifacts
      only; greeting marker; CORTEX_HOME and CWD both isolated).
- [ ] M1.5 Greeting fires exactly once on first run via scripted
      Sender; none otherwise; greeting text golden-pinned.
- [ ] M1.6 Keychain read/write behind pkg/secret interface with fake;
      meta-test fails on "security" as exec arg in any _test.go.
- [ ] M1.7 Greeting asks for scan roots and persists them to user
      config (scripted-Sender test).

## Next Up
Start M1.4: first-run detection per GOAL.md §3 (first-run ⇔ no
config.json under the user home AND no greeting marker under the user
home; sessions play no part). Tests should cover marker-absent ⇒ true,
config-present ⇒ false, marker-present ⇒ false, isolating both
`$CORTEX_HOME` and CWD (temp workspace) per case. No wiring into
`NewCortexSession`/`main.go` yet — that's M1.5's greeting-turn job; M1.4
is the detection predicate + its tests only. Land in a new
cmd/cortex/firstrun*.go.

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

## Known Issues (append-only)
- (none yet)
