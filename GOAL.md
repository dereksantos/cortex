# GOAL — Cortex Web track (ralph loop)

Immutable specification for a memoryless build loop. Each iteration: read
this file, read STATE.md, land ONE verified increment, commit locally.
The loop's only memory is the git history of branch `web/loop` in THIS
worktree (`/Users/dereksantos/eng/projects/cortex-web-loop`).
**Never push. Never touch any other checkout or branch.**

The detailed product spec is **`docs/cortex-web.md`** (committed in this
repo, decision log D1–D14). **Precedence on any conflict, total order:**
(1) GOAL.md Section 6 (ordering + DoD), (2) GOAL.md Section 3 (binding
interpretations), (3) the rest of GOAL.md, (4) docs/cortex-web.md.
Before deciding a conflict fresh, search STATE.md's Decisions Log for a
prior ruling on the same conflict and follow it; if none exists, record
your ruling there so every later iteration resolves it identically.

## 1. North star & pillars

Cortex grows a second surface: launch with a working model out of the box,
introduce itself, survey the user's AI landscape on consent, run the
harness across registered projects, and manage it all — including
recurring loops — from a local web app served by `cortex serve`.

More correct when: a slice ships end-to-end usable value, reuses an
existing cortex seam (`Session.Turn`, journal, `fslock`, `projectscan`,
config layering), and its behavior is pinned by a deterministic test.
Less correct when: it adds a dependency, invents a parallel mechanism for
something cortex already has, exposes anything beyond localhost, or lands
code whose behavior no test would notice reverting.

Pillars (tie-breakers, in order):
1. **Deterministic verification** — every behavior claim is a test that
   fails when the behavior is reverted. No live-model calls in the gate.
2. **Adapters over daemons** — `cortex serve` is a foreground peer of the
   REPL and discord; it owns zero canonical state (D13).
3. **Reuse the seam** — `Session.Turn`, `internal/fslock`,
   `internal/projectscan.IgnoreSet`, `pkg/llm` resolution, layered config.
   New mechanisms need a Decisions Log entry explaining why reuse failed.
4. **Local-only, consent-first** — nothing leaves the machine; scans are
   read-only, names-and-paths-only, and run only on explicit consent.
5. **Small honest slices** — an increment the verify gate can't observe
   is not an increment.

### Non-goals (do not relitigate)

- **No daemon, no launchd unit, no background service** — D13.
- **No push to any remote, ever** — owner reviews and pushes manually.
  (Mechanically guarded by the pre-push hook; see §7 step 0.)
- **No new go.mod dependencies** — stdlib + what's already required.
  (`gorilla/websocket` is transitive; do not use it. SSE only, D6.)
- **No node toolchain, no build step, no vendored JS framework** — D9.
- **No cron-expression parser** — intervals + manual only, D10.
- **No Phase 7 / discord work** — gated on the harness roadmap.
- **No non-localhost exposure, no multi-user, no TLS** — out of scope.
- **No dashboards/telemetry beyond the specced screens** (four land in
  M5; the fifth, loops, lands in M6).
- **No editing docs/cortex-web.md, GOAL.md, or the memory-tools docs** —
  spec changes are owner-only (Section 8).
- **No modifying `pkg/llm` provider internals** — compose its exported
  constructors/probes from `cmd/cortex/bootstrap*.go`; do not edit
  `pkg/llm` files. `pkg/secret` is NOT covered by this freeze — the M1
  keychain write lands there.

### Deferred — specced but deliberately in no milestone

Do not treat these as missing work; they are owner-scheduled later:
models-endpoint re-keying re-running the bootstrap chain (spec P4).

## 2. Architecture invariants & verify harness

Locked decisions (from docs/cortex-web.md D1–D14 plus repo reality):

- Go 1.26, stdlib `testing` only: `t.Errorf`/`t.Fatalf`, table-driven
  `t.Run` subtests, `defer` teardown. No testify.
- Errors wrapped with context: `fmt.Errorf("failed to X: %w", err)`.
- Constructors `NewXxx(...)`; accept interfaces, return structs;
  interfaces are nouns (`Scanner`, `Registry`, `SessionManager`).
- Package layout (new code):
  - `internal/userhome/` — single exported resolver for the user home
    (`$CORTEX_HOME` if set, else `~/.cortex`). Every machine-level path
    (user config, user journal, projects.json, loops.json, serve.token)
    resolves through it; tests redirect it with a temp dir. Built in M1.1.
  - `internal/landscape/` — Phase 2 scanners (probe families → structs).
  - `internal/registry/` — Phase 3 project registry (projects.json).
  - `internal/loops/` — Phase 6 loop specs + scheduler.
  - `cmd/cortex/serve*.go` — Phase 4 HTTP/SSE adapter + session manager.
  - `cmd/cortex/webui/` — Phase 5 static assets, `go:embed`, plus their
    Go view-model builders in `cmd/cortex/webui*.go`.
  - `cmd/cortex/bootstrap*.go` — Phase 1 resolver chain + greeting.
- Already landed, reuse as-is (do not re-extract, do not hunt for the
  spec's historical file names): `internal/fslock` IS the D8 lock — the
  spec's "extract acquireExclusiveLock from internal/journal" text is
  historical (see §8 amendment A1); journal and sessions already use it.
  `internal/projectscan.IgnoreSet` is the ignore + secret-filter layer —
  note it is REAL-FILESYSTEM based (`LoadIgnoreSet(root string)`,
  absolute paths, magic-byte sniff via os.Open); it does not accept an
  `fs.FS`, and adapting it is out of scope.
- Config files are user-authored: ALL config writes (M1.3 backend
  persist, M4.2 scoped role-binding writes) are read-modify-write over
  `map[string]any` / `json.RawMessage`, never marshal-through-struct.
  Unknown fields in the existing file must survive a write byte-for-byte
  modulo the touched key (asserted by test).
- Machine-level state (D4): specs as plain JSON under the user home
  (`projects.json`, `loops.json`); events (`landscape.scan`, `loop.run`)
  to a user-level journal at `<userhome>/journal/`.
- Serve (D6/D7): stdlib `net/http`, SSE via flushed chunked responses
  (the `http.Server` must set NO `WriteTimeout` — it would kill streams),
  loopback bind only, default port 7433 (flag-overridable), generated
  bearer token at `<userhome>/serve.token`, mode 0600.
- All tests hermetic: temp dirs (real-FS fixtures where IgnoreSet is
  involved), `httptest`, scripted `Sender` fakes, fake clocks, fake
  keychain. Env-gated live tests (`CORTEX_LIVE_FLEET` etc.) stay
  excluded from the default run.
- Machine prerequisites the owner guarantees: `go`, `git`,
  `golangci-lint` on PATH (check.sh needs it). If a prerequisite is
  missing, verify is red through no fault of the code: record
  `NEEDS OWNER AMENDMENT` per §7 and do not attempt to install tools or
  weaken check.sh.

**Verify (ground truth, single command):**

```
timeout 900 sh -c './scripts/check.sh && go test ./... -timeout 8m'
```

Exit 0 = green; any non-zero exit — including the 15-minute wall clock —
is red. Run it at baseline and before every commit. Never weaken, skip,
or delete an existing check to get green; a genuinely wrong check may be
corrected only with a Decisions Log entry in the same commit explaining
why the old assertion was wrong.

**Standing regression guard.** The genesis commit is the first commit
touching GOAL.md on this branch (`git log --reverse --format=%H --
GOAL.md | head -1`). Pre-existing test files (present at genesis) may
not be modified except under the Decisions-Log-justified correction rule
above: `git diff --name-only <genesis>..HEAD -- '*_test.go'` listing a
pre-existing test file without a matching Decisions entry is a
violation to fix before new work.

## 3. Subsystem spec — binding interpretations

`docs/cortex-web.md` carries the full per-phase scope. Binding
clarifications (these outrank the spec; see the precedence order above):

- **P1 greeting**: the greeting prompt states principles, never task
  recipes; its text is golden-pinned (M1.5) so any change is a visible
  diff requiring a Decisions entry. Consent to scan is the user's
  conversational reply each time — no flag or config grants standing
  consent. `tools.enable_scan` (default **true**) is an availability
  kill-switch, not consent.
- **P1 first-run**: first-run ⇔ no `config.json` under the user home
  AND no greeting marker under the user home. The greeting writes the
  marker when it fires. Sessions are project-scoped and play NO part in
  first-run detection. Detection tests isolate BOTH `$CORTEX_HOME` and
  the working directory (temp workspace).
- **P1 backend chain**: `BackendResolver` chain = existing config →
  key env/keychain → local Ollama probe → guided OpenRouter
  (`openrouter/free`). Each probe is an interface; the chain is pure
  logic over probe results. Seating ANY backend includes the tool-call
  smoke probe; on failure fall through to the next chain entry. The
  chain lives in `cmd/cortex/bootstrap*.go` composing `pkg/llm` exports;
  keychain read/write goes behind an interface in `pkg/secret`, faked in
  tests — no test invokes the real `security` binary (enforced by
  M1.6's meta-test, which verify runs).
- **P2 scanner**: `Scan(root string, caps)` walks the real filesystem
  (IgnoreSet constraint above); fixtures are temp-dir trees. Probes
  return typed structs. Caps = max depth, max entries, hard timeout;
  hitting any cap terminates cleanly with truncation REPORTED in the
  result. Output contains names and paths ONLY — never file contents
  (M2.3 sentinel test). Anything IgnoreSet rejects is invisible.
  Scan roots (D3): persisted user-config roots, or explicit `--root`;
  with neither, `cortex scan` refuses with a typed error — never a
  blind `$HOME` sweep. The memory note: only the `scan_landscape` coder
  tool writes one (fixed name `landscape`, to the CURRENT project's
  memory store); headless `cortex scan` writes no note.
- **P3 workspace**: introduce `Workspace` (root + derived paths) and
  thread it through session construction, `contextDir`, instructions
  discovery, and `ConfinePath`. The refactor lands FIRST as a pure
  no-behavior-change commit proven by the existing suite plus new
  equivalence tests; only then do `--project` flags build on it.
- **P4 sessions over HTTP**: one turn at a time per session (per-session
  mutex); different sessions concurrent. Session files locked via
  `internal/fslock`; a second PROCESS gets a typed "busy" error (the
  cross-process test re-execs the test binary via `os/exec` with a
  helper-mode env var — the stdlib pattern). Token auth on every
  endpoint; SSE events typed and golden-tested.
- **P5 UI**: hand-written HTML/CSS/JS under `go:embed`. Rendering logic
  lives in golden-tested Go view-models; JS is fetch/render/SSE-append
  only, enforced mechanically (M5.3 size caps). UI talks only to the
  P4 API. M5 ships the FOUR Phase 5 screens (dashboard, session,
  landscape, models); the loops screen is M6.7.
- **P6 loops**: every firing = fresh headless session in the target
  project via P3/P4 machinery; headless risk posture (Risky ⇒ Blocked)
  is inherited, never overridden (M6.5 asserts it). Overlap ⇒ skip +
  journaled skip. Scheduler driven by an injected clock; tests never
  sleep. Next-run is DERIVED (last `loop.run` event timestamp +
  interval) — journal-is-canonical; loops.json stays pure spec, no
  run-state file.

## 4–5. (reserved)

Subsystem detail lives in docs/cortex-web.md; this file intentionally
does not duplicate it.

## 6. Milestone ladder — FIXED order, strictly sequential

Every DoD item has a canonical ID (M<k>.<i>). Splits extend the ID
(M2.3a, M2.3b). **All references to an increment — attempt lines,
BLOCKED markers, splits, Decisions entries, commit messages — use the
ID verbatim.**

A milestone is complete when every item is ticked in STATE.md with a
commit hash AND the name(s) of the test function(s) that verify runs
for it. Ticking requires that named test to exist and pass. Checks are
permanent and append-only: a prior ticked item's named tests failing
blocks all forward work until green. Do not start milestone N+1 with N
incomplete (sole exception: the gate-suspension rule, §7).

**M1 — First-run bootstrap + greeting (P1)**
- [ ] **M1.1** `internal/userhome`: single exported resolver
      (`$CORTEX_HOME` else `~/.cortex`); `userConfigPath()` routes
      through it; temp-dir redirect test.
- [ ] **M1.2** `BackendResolver` interface + chain, table-driven over
      these named rows (fakes only): config-hit short-circuits;
      config-miss + env key; keychain hit; all keys miss + ollama up;
      all down ⇒ guided `openrouter/free`; smoke-probe failure at each
      seated stage falls through to the next entry. Ticking requires
      all rows present.
- [ ] **M1.3** Resolved backend persists to the user config via
      read-modify-write; unknown fields survive byte-for-byte (test);
      a second resolution run short-circuits on the persisted value.
- [ ] **M1.4** First-run detection per §3 (user-home artifacts only;
      marker written by the greeting): tests cover marker-absent ⇒
      true, config-present ⇒ false, marker-present ⇒ false, with
      `$CORTEX_HOME` and CWD both isolated.
- [ ] **M1.5** Greeting turn fires exactly once on first run via the
      scripted `Sender` seam; non-first runs fire none; greeting text
      golden-pinned.
- [ ] **M1.6** Keychain read/write behind a `pkg/secret` interface with
      a fake; a meta-test run by verify fails if `"security"` appears
      as an exec argument in any `_test.go` in the repo.
- [ ] **M1.7** Greeting asks where the user's code lives and persists
      scan roots to user config (scripted-Sender test) — D3's
      ask-and-persist half; M2.5 consumes it.

**M2 — Landscape scan (P2)**
- [ ] **M2.1** `internal/landscape`: per-family `Scanner`
      implementations (harnesses, runtimes, projects) composed by
      `Scan(root, caps)`; temp-dir fixture per family covering
      present / absent / malformed.
- [ ] **M2.2** Every filesystem visit filtered through
      `projectscan.IgnoreSet`; a fixture with a planted secret path
      proves it never appears in any scan result.
- [ ] **M2.3** Content-non-leak sentinel: fixture file bodies carry a
      unique sentinel string; serializing the full scan result (structs,
      `--json` output, and the `landscape.scan` journal event) contains
      it nowhere.
- [ ] **M2.4** Caps enforced: fixtures exceeding max depth / max
      entries / a near-zero timeout each terminate cleanly (three
      tests) with truncation reported in the result — never silent.
- [ ] **M2.5** `cortex scan [--json] [--root <path>]`: uses persisted
      roots, `--root` overrides, neither ⇒ typed refusal (all three
      paths tested); golden-file text report; JSON round-trip.
- [ ] **M2.6** `scan_landscape` coder tool registered, gated by
      `tools.enable_scan` (absent ⇒ registered, false ⇒ absent — both
      tested), home-scoped and read-only.
- [ ] **M2.7** Scan persists a `landscape.scan` event to the user-level
      journal under the user home (temp-home test); the coder tool
      additionally writes the `landscape` memory note to the current
      project's store (temp-workspace test asserts a fixture-derived
      string); headless scan writes no note (asserted).

**M3 — Workspace threading + project registry (P3)**
- [ ] **M3.1** `Workspace` threaded through session construction,
      `contextDir`, instructions discovery, and `ConfinePath` in a
      behavior-preserving commit: existing suite green plus equivalence
      tests (same fixture repo via CWD vs explicit root ⇒ identical
      context dir, instructions, confinement verdicts).
- [ ] **M3.2** `ConfinePath` escape attempts against a non-CWD root
      refused (table of traversal and symlink cases).
- [ ] **M3.3** `internal/registry`: CRUD round-trip on `projects.json`
      under a temp home; unknown-name lookup returns a typed error.
- [ ] **M3.4** `cortex project add/list/remove` wired to the registry
      (CLI-level tests); `cortex scan --register` feeds discovered
      projects in.
- [ ] **M3.5** `--project <name>` on `turn`, `resume`, and `study`
      resolves via the registry and runs against that root
      (fixture-repo test).

**M4 — `cortex serve` (P4)**
- [ ] **M4.1** `cortex serve` starts on 7433 (flag-overridable); the
      real listener's address satisfies `ip.IsLoopback()` (test on the
      constructed server, not httptest); bearer token generated,
      written to `<userhome>/serve.token` mode 0600 (path + mode + 
      content asserted); tokenless and wrong-token requests get 401.
- [ ] **M4.2** Endpoints per spec: projects list, sessions
      list/create/resume, `POST …/turn`, SSE progress, landscape,
      models (read merged config + fleet; scoped write user/project/
      session via read-modify-write — unknown-field survival test; key
      material absent from every response, asserted).
- [ ] **M4.3** Session manager: two turns on one session serialize;
      turns on two sessions interleave (concurrency test, scripted
      senders).
- [ ] **M4.4** Cross-process lock: a REAL second process (re-exec
      helper pattern) attempting the same session gets the typed busy
      error. (`internal/fslock` itself pre-dates the loop — this item
      is the serve integration + the two-process test only.)
- [ ] **M4.5** SSE event order and shape golden-tested via the
      `Progress` seam; a test asserts the serve `http.Server` sets no
      `WriteTimeout`.
- [ ] **M4.6** Serve owns no state: kill + restart re-derives every
      list from disk (restart the manager, listings identical).
- [ ] **M4.7** Idle sessions evict; a subsequent request re-hydrates
      from the transcript (test with a shrunk idle threshold).

**M5 — Web UI (P5), four screens**
- [ ] **M5.1** Assets under `cmd/cortex/webui/` served from `go:embed`;
      route-level test proves serving with no filesystem presence.
- [ ] **M5.2** View-models built in Go and golden-tested: project
      dashboard, session transcript (from real JSONL fixtures),
      landscape report, models view (bindings + effective scope
      resolution).
- [ ] **M5.3** The four screens render those view-models; JS bounded
      mechanically: a Go test over the embedded FS asserts each `.js`
      file ≤ 300 lines and total JS ≤ 1200 lines.
- [ ] **M5.4** End-to-end smoke: start serve with a scripted sender ⇒
      create session ⇒ POST turn ⇒ SSE stream renders ⇒ transcript
      page shows the turn. One test, full path, no live model.

**M6 — Loops (P6)**
- [ ] **M6.1** `internal/loops`: spec CRUD round-trip on `loops.json`;
      validation rejects cadence below the 15-minute floor (typed
      error).
- [ ] **M6.2** Scheduler on an injected clock: due ⇒ fires; not due ⇒
      doesn't; disabled ⇒ never; overlap ⇒ skips AND journals the
      skip. No test sleeps.
- [ ] **M6.3** Each firing runs a fresh headless session in the target
      project (fixture project + scripted sender), producing a
      `loop.run` event with outcome + change ref.
- [ ] **M6.4** Budget caps, both of D11's: a scripted runaway session
      halts at the turn cap (sub-test 1) and a token-budget breach
      halts before the turn cap (sub-test 2, fake sender reporting
      token counts); both journal `failed: budget`.
- [ ] **M6.5** Risk posture: a loop firing whose scripted sender issues
      a shellrisk-Risky command is Blocked, asserted on the tool result
      and the `loop.run` event; no prompt surface is reachable.
- [ ] **M6.6** Restart resumes: next-run derived from the last
      `loop.run` timestamp + interval; no double-fire across a
      scheduler restart (fake-clock test).
- [ ] **M6.7** Loops screen: view-model golden (specs + run history
      from `loop.run` events); create/enable/disable/run-now wired to
      NEW loops endpoints added to `cortex serve` in this milestone
      (httptest).

## 7. Iteration protocol

Work ONLY in this worktree. Each iteration, in order:

**Step 0 — Preflight.**
`git rev-parse --abbrev-ref HEAD` must print `web/loop`.
- Prints `HEAD` (detached): if `git branch --contains HEAD` includes
  `web/loop`, run `git checkout web/loop` and continue.
- Prints anything else, or `web/loop` cannot be safely restored: STOP
  without committing — this is the single sanctioned zero-commit
  iteration; the owner will investigate.
Ensure the pre-push guard exists (idempotent): the repo's shared hooks
dir must contain a `pre-push` hook that exits 1 when any pushed ref is
`refs/heads/web/loop`. If missing, install it before anything else.

**Step 1 — Recover state** (only if the working tree is dirty from a
crashed prior iteration):
- Run verify. **Green** ⇒ check every commit hash referenced by dirty
  STATE.md ticks exists (`git cat-file -e <hash>`); note any bad ones
  for correction in step 8; commit everything as
  `chore(loop): salvage prior iteration [<ID from Next Up>]`.
- **Red** ⇒ append to Known Issues, in attempt format so it counts
  toward three strikes:
  `<date>: attempt N at <ID from STATE.md's Next Up>: crashed prior
  iteration, found <one line>`. Then:
  `git add STATE.md && git commit -m "chore(loop): record crashed
  iteration [<ID>]"` and then `git reset --hard && git clean -fd`.
  (The record is committed BEFORE the reset, so nothing is lost;
  GOAL.md and STATE.md are tracked, so reset restores them.)

**Step 2 — Read** GOAL.md fully, then STATE.md, then the relevant
section of docs/cortex-web.md for the current milestone.
If STATE.md is missing: recover per the STATE.md recovery procedure
below. If it has NEVER been committed (`git log -- STATE.md` empty),
create it from the template with milestone M1 and every box unchecked —
that bootstrap plus the baseline verify run is a valid first iteration.

**Step 3 — Baseline.** Run verify.
- STATE.md disagrees with verify ⇒ correcting STATE.md is a legitimate
  increment; do that first.
- Verify red on a CLEAN tree, for any reason ⇒ restoring green IS this
  iteration's increment: diagnose, fix, commit as
  `fix(loop): restore green baseline`, Decisions entry for the cause.
  The three-strike rule applies to it like any increment. Only the
  §2 Decisions-justified correction rule may alter an existing check.
  If red is caused by a missing machine prerequisite (§2), record
  `NEEDS OWNER AMENDMENT` instead and stop after that bookkeeping
  commit.

**Step 4 — Pick ONE increment**: the topmost unchecked item of the
current milestone that is NOT marked `BLOCKED` or
`NEEDS OWNER AMENDMENT`, or its topmost recorded split.
- If that item's tests already exist and pass at baseline (a prior
  iteration crashed between landing and ticking): this iteration's work
  is ticking it with the hash of the commit that landed it (find via
  `git log`), plus a Decisions entry noting the salvage.
- Too big for one iteration ⇒ SPLIT it into ID'd sub-items (M2.3a…) in
  STATE.md; the split is this iteration's commit.
- **Gate suspension**: if EVERY remaining unchecked item in the current
  milestone is BLOCKED or NEEDS OWNER AMENDMENT, the milestone gate is
  suspended for items of the next milestone that do not depend on the
  blocked items; record the suspension in the Decisions Log.

**Step 5 — TDD, red-then-green, inside this iteration**: write the
failing test first (red exists only in the working tree, never in a
commit), implement the smallest change that passes, and confirm the
test is load-bearing (revert-the-code ⇒ test fails — when practical,
note doing so in the Decisions Log).

**Step 6 — Run verify to exit 0.** Never commit red — with ONE
exemption: STATE.md-only bookkeeping commits (attempt lines, BLOCKED
markers, NEEDS OWNER AMENDMENT, split records) may land while verify is
red, provided the working tree is otherwise at HEAD.

**Step 7 — Commit** with a conventional message carrying the increment
ID (e.g. `feat(landscape): ollama probe [M2.1]`). At least one commit
per iteration — bookkeeping commits count; the only sanctioned
zero-commit iteration is step 0's unrecoverable-branch stop.
Do NOT push.

**Step 8 — Update STATE.md** (same commit or an immediately following
`chore(state): tick [Mk.i] <hash>` commit — the crash window between
the two is covered by step 4's salvage rule): tick the item with the
commit hash AND the test function name(s); set Next Up to one specific
startable sentence naming the next ID; append Decisions / Known Issues
entries; apply any hash corrections noted in step 1.

**Three-strike rule.** Every failed attempt MUST append a dated line:
`attempt N at <ID>: tried X, failed because Y` — N = 1 + the count of
existing attempt lines bearing that exact ID. Commit the line even when
nothing else landed (step 6 exemption). On the third line for one ID:
mark the item `BLOCKED (3 strikes)` in the checklist, record what the
owner must decide, and move on per step 4.

**STATE.md recovery** (missing or malformed), in order:
1. A committed version exists (`git log -- STATE.md`) ⇒ restore the
   most recent one verbatim (`git checkout HEAD -- STATE.md` or from
   the last commit touching it). Its splits, ticks, and hashes are
   authoritative. Done.
2. No committed version has ever existed ⇒ create from the template
   (step 2's bootstrap). Never rebuild from scratch while history
   exists.

### STATE.md template

```markdown
# STATE — cortex web loop
Updated: <date> · Iteration: <n>

## Current milestone
M<k> — <name>

## Checklist (all milestones, append-only — completed milestones stay)
### M1 — First-run bootstrap + greeting
- [ ] M1.1 …
(copy the current milestone's items from GOAL.md §6 verbatim, with IDs;
add milestone sections as the loop reaches them; never delete one)

## Next Up
<one specific, immediately startable sentence naming an ID>

## How to Run / Verify
timeout 900 sh -c './scripts/check.sh && go test ./... -timeout 8m'
<plus at most five lines of quickref — this section must stay short>

## Decisions Log (append-only)
- <date>: <decision + why>

## Known Issues (append-only)
- <date>: attempt N at <ID>: tried X, failed because Y
```

## 8. Amendments (project owner only)

The loop never edits this file. If reality contradicts the spec (a
check that cannot run in this environment, an unsatisfiable gate),
record `NEEDS OWNER AMENDMENT` in Known Issues and continue with the
next available increment (or stop per step 3 if verify itself is the
casualty). Owner amendments land as dated entries here:

- **A1 (2026-07-11).** docs/cortex-web.md's D8 text ("extract the
  journal's acquireExclusiveLock…") is historical: the extraction
  already shipped as `internal/fslock` before the loop started, and
  journal + sessions already use it. Treat M4.4 as serve integration +
  the two-process contention test only.
- **A2 (2026-07-11).** docs/cortex-web.md Phase 5 lists the loops
  screen under Phase 6; GOAL.md M5 is correspondingly four screens and
  the loops screen is M6.7. The non-goals line "the specced screens"
  spans both.
