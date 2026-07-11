# STATE — cortex web loop
Updated: 2026-07-11 · Iteration: 0

## Current milestone
M1 — First-run bootstrap + greeting

## Checklist (all milestones, append-only — completed milestones stay)
### M1 — First-run bootstrap + greeting
- [ ] M1.1 `internal/userhome`: single exported resolver ($CORTEX_HOME
      else ~/.cortex); userConfigPath() routes through it; temp-dir
      redirect test.
- [ ] M1.2 BackendResolver interface + chain, table-driven over the
      named rows in GOAL.md §6 (fakes only).
- [ ] M1.3 Resolved backend persists to user config via
      read-modify-write; unknown fields survive byte-for-byte; second
      resolution short-circuits.
- [ ] M1.4 First-run detection per GOAL.md §3 (user-home artifacts
      only; greeting marker; CORTEX_HOME and CWD both isolated).
- [ ] M1.5 Greeting fires exactly once on first run via scripted
      Sender; none otherwise; greeting text golden-pinned.
- [ ] M1.6 Keychain read/write behind pkg/secret interface with fake;
      meta-test fails on "security" as exec arg in any _test.go.
- [ ] M1.7 Greeting asks for scan roots and persists them to user
      config (scripted-Sender test).

## Next Up
Start M1.1: write the failing temp-dir redirect test for a new
internal/userhome package exposing a single exported resolver, then
implement it and route cmd/cortex's userConfigPath() through it.

## How to Run / Verify
timeout 900 sh -c './scripts/check.sh && go test ./... -timeout 8m'
Repo is a Go 1.26 module; the coder binary is cmd/cortex.
Product spec: docs/cortex-web.md. Loop spec: GOAL.md (read fully first).

## Decisions Log (append-only)
- 2026-07-11: GOAL.md v1 finalized after a four-lens adversarial review
  (53 findings applied: canonical increment IDs, commit-before-reset
  recovery, gate-suspension escape, four-screen M5, userhome package,
  read-modify-write config edits, mechanical JS caps).

## Known Issues (append-only)
- (none yet)
