# Daemon Retirement Plan

The Cortex daemon predates the REPL-as-host pivot. It used to drain
the capture journal into storage, run Think/Dream on a schedule, and
serve a :9090 dashboard. The REPL now owns the long-lived process;
the daemon is vestigial. This plan retires it in phases, each
independently shippable.

## Decisions

These were defaulted to keep momentum; override before launching the
loop if any are wrong.

- **Claude-Code-as-host mode**: DROP. The project's primary deployment is
  the Cortex REPL. Slash commands + hooks in `.claude-plugin/` keep
  working because they shell out to the CLI, not the daemon.
- **Ingest cadence**: REPL idle goroutine. Mirrors the daemon model
  (ticker-driven drain), keeps capture latency at write time low,
  swappable to inline-on-write later if needed.
- **:9090 dashboard**: DROP. If it gets resurrected later, it lives in a
  separate small binary.

## How this is meant to be executed

Each task below is sized for one loop iteration: ~10–30 minutes of work,
one commit, one acceptance check. The loop prompt finds the next
unchecked task, executes it, runs the acceptance check, commits, ticks
the box, and exits. The next iteration picks up the next task.

**Pause points** are explicit tasks (`PAUSE: …`) that the loop must NOT
auto-execute. Human ticks them off after reviewing the prior phase.

---

## Phase 0 — Delete fossils

No behavior change. Removing files nothing reads.

- [ ] **0.1** Delete the stale `daemon_state.json` files.
  - Files: `/daemon_state.json` (repo root), `.cortex/daemon_state.json`.
  - Acceptance: both gone; `go build ./...` still passes.

- [ ] **0.2** Remove the `cortex daemon` advisory from `scripts/install.sh:34`.
  - Acceptance: `grep -n 'cortex daemon' scripts/install.sh` returns nothing.

- [ ] **0.3** Remove the daemon entry from `.claude/settings.local.json`.
  - Target: the `Bash(timeout 2 ./cortex daemon:*)` allowlist line (~line 56).
  - Acceptance: `grep -n 'cortex daemon' .claude/settings.local.json` returns nothing.

---

## Phase 1 — Cut the auto-start path

The only "live" coupling left. After this, nothing spawns the daemon
unless the user runs `cortex daemon` by hand.

- [ ] **1.1** Delete `maybeStartDaemon` and its callsites.
  - File: `cmd/cortex/main.go:311-322` (the function); search for `maybeStartDaemon(` to find call sites.
  - Acceptance: `grep -n 'maybeStartDaemon' cmd/cortex/` returns nothing; `go build ./...` passes.

- [ ] **1.2** Replace daemon-state read in `renderStatusLine`.
  - File: `cmd/cortex/commands/debug.go:700-800`.
  - Drop the `ReadDaemonState()` call and any Dream/Think mode-from-state-file logic.
  - Keep the storage-stats fallback (events + insights counts) — it's already the path the function uses when the state file is stale.
  - Acceptance: `go build ./...` passes; `go test ./cmd/cortex/commands/` passes; `grep -n 'ReadDaemonState' cmd/cortex/commands/` returns only the source definition (will be deleted in Phase 3).

---

## Phase 2 — Move what the daemon was doing into the REPL

This is the only real engineering work. Sized into small tasks so each
is reviewable.

- [ ] **2.0** PAUSE for design review.
  - Before any 2.x task runs, the human must look at this plan, the
    current REPL idle-hook surface (commits `7c23954`, `8b71431`,
    `e71b737` are the recent idle-hook additions — start there), and
    confirm the cadence choice.
  - Tick this box manually to unblock the loop.

- [ ] **2.1** Add a journal-ingest goroutine to the REPL.
  - The REPL already has an idle hook (see `cmd/cortex/commands/repl.go`
    around the "forever-session digest via attend.compact" wiring).
  - Add a ticker (default 30s) that calls the existing journal-drain
    routine the daemon uses (search for `journal.Ingest` / `JournalIngest`
    in `cmd/cortex/commands/daemon.go` for the entry point).
  - Wire it under the REPL's existing context cancellation so it
    shuts down cleanly when the REPL exits.
  - Acceptance: in a fresh `.cortex/`, `cortex capture --content='test'`
    followed by a 60s REPL session causes a storage-side index update
    visible via `cortex search 'test'`. `go build ./...` passes.

- [ ] **2.2** Move Think dispatch to the REPL idle hook.
  - Source: `cmd/cortex/commands/daemon.go` Think dispatch site (search
    for `MaybeThink(`).
  - Wire the same call into the REPL's idle goroutine, respecting the
    existing `ActivityLevel` budget (Think runs only when REPL is
    quiet — same contract the daemon enforced).
  - Acceptance: `go build ./...` passes; `go test ./internal/cognition/...`
    passes; manual smoke: idle the REPL for 60s, confirm a `think.*`
    journal entry appears.

- [ ] **2.3** Move Dream dispatch to the REPL idle hook.
  - Source: `cmd/cortex/commands/daemon.go` Dream dispatch site (search
    for `MaybeDream(`).
  - Same pattern as Think. Dream's `MaxBudget` / `GrowthDuration`
    contract is unchanged; it just runs in a different host process.
  - Acceptance: `go build ./...` passes; `go test ./internal/cognition/...`
    passes; manual smoke: idle the REPL for 10+ minutes, confirm a
    `dream.insight` journal entry appears.

- [ ] **2.4** Drop `SetStateWriter` plumbing.
  - Remove the `SetStateWriter` calls now that no daemon-state file is
    written. Don't delete the `SetStateWriter` methods themselves yet
    — Phase 3 handles that.
  - Files: anything that calls `SetStateWriter` outside the daemon
    package (likely none, but grep to confirm).
  - Acceptance: `grep -rn 'SetStateWriter' cmd/ internal/ pkg/` shows
    only definitions in `internal/cognition/`, no call sites outside
    `cmd/cortex/commands/daemon.go`.

- [ ] **2.5** REPL smoke test.
  - Start the REPL, issue 2-3 prompts, idle 60s, exit.
  - Verify: no panics, ingest goroutine drained captures (check
    `.cortex/journal/capture/` against storage state), no orphan
    goroutines on exit.
  - Acceptance: clean exit, no `goroutine` leak warning in `pprof`
    if you want to be thorough.

---

## Phase 3 — Delete the daemon surface

Now that nothing depends on it.

- [ ] **3.1** Delete `cmd/cortex/commands/daemon.go` entirely.
  - Remove the file. Find and remove the daemon command registration
    in `cmd/cortex/commands/commands.go` (or wherever the registry is).
  - Acceptance: `go build ./...` passes; `cortex --help` does not list
    `daemon`.

- [ ] **3.2** Delete `internal/cognition/daemon_state.go` + test.
  - Files: `internal/cognition/daemon_state.go`, `internal/cognition/daemon_state_test.go`.
  - Acceptance: `go build ./...` passes; `go test ./internal/cognition/...`
    passes.

- [ ] **3.3** Delete `SetStateWriter` methods from Think/Dream/Digest/Cortex.
  - Files: `internal/cognition/think.go`, `dream.go`, `digest.go`, `cortex.go:474-478`.
  - Drop the `stateWriter` field on each type too.
  - Acceptance: `go build ./...` passes; `go test ./internal/cognition/...`
    passes; `grep -rn 'StateWriter' internal/cognition/` returns nothing.

- [ ] **3.4** Delete remaining daemon exports.
  - Targets: `IsDaemonRunning`, `StartDaemonBackground`, `StopDaemon`,
    `GetDaemonPIDPath`, `GetDaemonLockPath` from the commands package
    (the file is gone after 3.1, but verify no stragglers).
  - Acceptance: `grep -rn 'IsDaemonRunning\|StartDaemonBackground\|StopDaemon' cmd/ internal/ pkg/` returns nothing.

- [ ] **3.5** Full-suite green.
  - Run `go test ./... && go vet ./...`.
  - Acceptance: zero failures.

---

## Phase 4 — Docs cleanup

Strip daemon references from docs that still describe it as live.

- [ ] **4.1** Strip daemon paragraphs from `README.md`.
  - Lines roughly 75, 109, 156–165, 234, 258–259 (verify line numbers
    before editing — file may have shifted).
  - Replace "start the daemon" guidance with "start the REPL with `cortex repl`".
  - Acceptance: `grep -ni 'daemon' README.md` returns at most historical mentions (e.g. "the legacy daemon was retired in <commit>").

- [ ] **4.2** Strip daemon paragraphs from `CLAUDE.md`.
  - Lines roughly 43, 60, 65, 73, 90, 92.
  - Remove the "Quick Start (Claude Code as host)" section's daemon
    references; keep the slash-commands listing since those still work.
  - Acceptance: `grep -ni 'daemon' CLAUDE.md` returns nothing load-bearing.

- [ ] **4.3** Strip daemon paragraphs from `docs/product.md`.
  - Lines roughly 75, 109, 156–165.
  - Acceptance: `grep -ni 'daemon' docs/product.md` returns nothing load-bearing.

- [ ] **4.4** Mark ROADMAP.md daemon entries as superseded.
  - Lines roughly 66, 273 ("single global daemon" / multi-project).
  - Either delete or annotate "superseded by REPL-as-host (May 2026)".
  - Acceptance: ROADMAP no longer presents the daemon as a current/future feature.

- [ ] **4.5** Final daemon-reference sweep.
  - Run: `grep -rni 'daemon' docs/ README.md CLAUDE.md CONTRIBUTING.md ROADMAP.md`.
  - Resolve every remaining hit: delete, rewrite, or annotate as historical.
  - Acceptance: every remaining hit is either (a) historical context,
    (b) an unrelated docker daemon mention, or (c) explicitly
    annotated as superseded.

- [ ] **4.6** Memory update — add a project memory pointing at this plan
  and the commit that completed daemon retirement, so future
  conversations don't re-derive what we just decided.
  - Use the project memory type. Short one-liner pointing at the plan.

---

## Done

When every box above is checked, the daemon is retired. Open a PR with
all the phase commits squashed or grouped as you prefer.
