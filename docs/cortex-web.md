# Cortex Web — landscape, projects, loops

**Status: proposed (2026-07-10). Parallel track — additive to the coding-harness
roadmap, not a change to it.**

This doc scopes the web-app side of cortex: launch with a working model out of
the box, introduce itself, ask consent to survey the user's AI landscape, run
the `cmd/cortex` harness across the user's projects, and manage all of it —
including recurring AI loops — from one web app.

> **Relationship to the "don't build dashboards" rule.** The production-harness
> doc (`docs/cortex-production-harness.md`) forbids dashboards *for the coding
> harness*, and that stands: nothing here adds UI concerns to the REPL, the
> loop engine, or the tool surface. This track builds a separate *surface*
> (adapter + UI) that only consumes the same seams the discord adapter already
> proves out (`Session.Turn`, transcripts, journal). The don't-list is scoped
> to the harness; this doc is the authority for the web track.

## The daemon question — answered up front

**No daemon.** The web app is a third adapter on the existing seam, a peer of
the REPL and `cortex discord`:

```
REPL ──────────┐
discord ───────┼──► *CortexSession → Turn() → runLoop → tools/journal/transcript
cortex serve ──┘         (all state on disk: .cortex/sessions, .cortex/journal)
```

- `cortex serve` is a **foreground process** you start when you want the app,
  exactly like `cortex discord`. Kill it and nothing is lost — sessions and
  the journal are on disk (the retired daemon's own invariant: "daemon-down ≠
  system-down", `docs/journal.md`).
- Loops (Phase 6) run **inside** the serve process while it's up. Always-on is
  a *deployment* choice (launchd / `brew services`), not an architecture — we
  never build a second resident program with its own state.
- What the old daemon got wrong — a background process that owned live state
  (`daemon_state.json`) and had to be running for the system to be whole — is
  exactly what this avoids. The serve process owns **zero** canonical state.

The one real gap serving exposes: today **nothing prevents two processes from
appending to the same session JSONL** (the discord mutex is in-process only).
Phase 4 closes this with a per-session single-writer lock before any second
surface ships.

## MECE slice map

Seven phases. Each is independently shippable with standalone value;
boundaries are drawn so no capability appears in two phases.

| # | Slice | One-line value | Depends on |
|---|---|---|---|
| 1 | First-run bootstrap + greeting | `cortex` works with zero config and introduces itself | — |
| 2 | Landscape scan | `cortex scan` inventories the user's AI tools and projects | — |
| 3 | Workspace threading + project registry | run cortex against any registered project from anywhere | 2 (registry is fed by scan) |
| 4 | `cortex serve` HTTP/SSE adapter | drive sessions programmatically; single-writer safety | 3 |
| 5 | Web UI | one place to see and chat with everything | 4 |
| 6 | Loops across projects | recurring AI work, managed from the UI | 4 (UI mgmt: 5) |
| 7 | Discord parity | discord gets the interactive CLI's affordances | 4 + harness roadmap done |

MECE check: 1 = getting a usable model + first impression; 2 = read-only
discovery of the machine; 3 = multi-project *execution* substrate; 4 =
transport; 5 = presentation; 6 = scheduling; 7 = bringing the *existing*
remote surface up to the shared standard. Phases 1–2 are independent of each
other and of 3–7; 3→4→5 is the load-bearing chain.

---

## Phase 1 — First-run bootstrap + greeting

**Value shipped:** a fresh machine runs `cortex` and gets a working agent that
says hello — no config file, no docs-reading.

Today `NewCortexSession` assumes a LiteLLM proxy on `localhost:4000` and, when
fleet discovery fails, limps on with an empty fleet and a yellow warning
(`config.go`, `session_core.go`). Meanwhile `pkg/llm` already has the richer
resolution (`BackendAuto`: OpenRouter keychain → OpenRouter env → Anthropic
env; `BackendOllama` with no credential) that the session path never uses.

Scope:

- **Backend bootstrap chain** — when no config resolves a working backend:
  existing config → key env vars / keychain → probe local Ollama
  (`localhost:11434`) → guided OpenRouter setup defaulting to a `:free` model.
  Wire the `pkg/llm` resolution into the session path instead of duplicating
  it. Honest constraint: nothing is keyless except local Ollama, so "free
  model" = free *tier* (OpenRouter `:free` ids) behind a one-minute key
  paste, or a detected local model. The chain is a `BackendResolver` interface
  with fake probes in tests.
- **First-run detection** — no `~/.cortex/config.json` and no prior sessions.
  Persist the resolved backend to the user config so it's a one-time cost.
- **Greeting turn** — on first run, after `StartTranscript()` +
  `EnableMemory()` and before the read-loop in `main.go`, run one synthetic
  `session.Turn(ctx, greetingPrompt)`. The prompt states principles, not a
  recipe (per the system-prompt rule): who cortex is, and that it can survey
  the user's AI setup **if invited** — consent is the user's reply, not a
  flag. Phase 1 ships the invitation; the scan it offers is Phase 2 (until
  then the agent can only look around with its ordinary tools).

Explicitly out: any filesystem inventory (Phase 2), any UI.

Tests: table-driven resolver-chain tests (each probe faked); first-run
detection against temp `CORTEX_HOME`; greeting-turn wiring via the existing
scripted-Sender test harness. Red/green: write the resolver-chain table first.

## Phase 2 — Landscape scan

**Value shipped:** `cortex scan` prints a structured report of the user's AI
landscape; the same inventory is available to the agent as a consented tool,
so the Phase 1 greeting becomes real ("want me to look? → here's what you
have").

Scope:

- **`internal/landscape`** — deterministic Go, no LLM. Scans well-known
  locations under `$HOME` for:
  - *Agent harnesses & editors*: `~/.claude`, `~/.cursor`, `~/.codex`,
    `~/.aider*`, `~/.continue`, `~/.config/github-copilot`, MCP config files.
  - *Local model runtimes*: `~/.ollama` (+ live probe), LM Studio, llama.cpp
    artifacts.
  - *Projects*: git repos under configured roots (e.g. `~/eng`) with AI
    markers — `AGENTS.md`, `CLAUDE.md`, `.cursor/`, `.cortex/`.
  - Design: `Scanner` interface per probe family, each returning typed
    structs (`Tool`, `Runtime`, `Project`); a composing `Scan(root)` walks
    with depth/entry caps and a hard timeout. Accept `fs.FS`, return structs —
    every probe testable against a fixture tree, no real `$HOME` in tests.
- **Surfaces**: `cortex scan [--json]` subcommand (report to stdout), and a
  `scan_landscape` coder tool (gated `tools.enable_scan`, home-scoped,
  read-only) so the greeting conversation can run it on user consent.
- **Persistence**: scan result → a journal event (`landscape.scan`) and a
  summary memory note, so later sessions know the landscape without
  re-scanning.

Privacy stance (invariant): read-only; **names and paths only, never file
contents**; local-only per `journal.AssertLocalOnly`; runs only on explicit
consent or explicit subcommand.

Explicitly out: acting on discovered projects (Phase 3), rendering (Phase 5).

Tests: fixture `fs.FS` trees per probe (present / absent / malformed);
cap-and-timeout behavior; golden-file report rendering; tool-gate test.

## Phase 3 — Workspace threading + project registry

**Value shipped:** `cortex turn --project blog "fix the RSS feed"` from
anywhere; cortex can run its own harness in any registered project.

This is the substrate refactor. Today the workspace root is implicitly
`os.Getwd()` in at least three places: `contextDir()` (`findUp(".cortex")`),
config/AGENTS.md discovery (`findUp` from CWD), and the tool sandbox root
(`deleteRoot` → `ConfinePath`). A multi-project server cannot chdir per
request.

Scope:

- **Explicit `Workspace`** — a struct (root path + derived `.cortex` dir +
  instructions path) constructed once and threaded through `NewCortexSession`,
  `contextDir`, `projectInstructions`, and `CortexSession.root()`/`ConfinePath`.
  Behavior with a CWD-derived workspace must be bit-identical — this phase is
  red/green against the *existing* session and tool tests first, then adds
  the parameter.
- **Project registry** — `~/.cortex/projects.json`: name, root, last-session,
  notes. Fed by Phase 2's project discovery (`cortex scan --register` or a
  confirm step) and by hand (`cortex project add/list/remove`). `Registry` is
  an interface (lookup/list/save); file-backed struct implementation.
- **`--project <name>`** on `turn`, `resume`, `study`: resolve via registry →
  construct the Workspace → run. Per-project `.cortex/` stays inside each
  project (sessions, journal, config) — the registry holds pointers only,
  which keeps project state self-contained and the registry trivially
  rebuildable from a scan.

Explicitly out: concurrent sessions in one process (Phase 4's problem), any
remote projects.

Tests: workspace-threading equivalence (same fixture repo via CWD vs explicit
root → identical contextDir/instructions/confinement); registry CRUD
round-trip; `ConfinePath` escape attempts against a non-CWD root.

## Phase 4 — `cortex serve`: the HTTP/SSE adapter

**Value shipped:** a local API to list projects/sessions, start sessions, and
run turns with streamed progress — everything the web UI (and any future
surface) needs, usable from `curl` on day one.

Scope:

- **`cmd/cortex serve`** — foreground HTTP server, localhost-bound, bearer
  token generated at start (printed + written under `~/.cortex`). Endpoints
  (v0): projects (list, from registry), sessions per project (list/create/
  resume — transcripts are already on disk), `POST …/turn` (runs
  `session.Turn`), SSE stream of turn progress (rendered from the existing
  `Progress` seam), landscape (Phase 2's persisted result).
- **Session manager** — an in-process map of live `*CortexSession` keyed by
  session id, each guarded per-session (the discord mutex generalized: one
  turn at a time per session, different sessions concurrent — safe because
  they share nothing in-process). Idle sessions evict; resume re-hydrates
  from the transcript.
- **Single-writer session lock** — per-session-file flock (the journal
  already has per-segment flock; sessions have nothing). Second opener gets a
  clear "session busy in another process" error. This fixes today's latent
  REPL-vs-adapter corruption and is a prerequisite for *any* second surface.
- Design: handlers accept small interfaces (`SessionManager`, `Registry`)
  and are `httptest`-testable without a model; the live loop behind a scripted
  `Sender` fake as in existing loop tests.

Explicitly out: HTML (Phase 5), scheduling (Phase 6), non-localhost exposure,
multi-user.

Tests: `httptest` per endpoint; concurrency test (two turns, one session →
serialized; two sessions → parallel); flock contention test (two processes);
token-auth reject; SSE event-order golden.

## Phase 5 — The web UI

**Value shipped:** the revived web app — open `localhost:<port>`, see your
projects, sessions, and landscape, and chat with cortex in any project from
one place.

Scope:

- **Views**: project dashboard (registry + per-project session list + change
  status), session view (transcript render + input box + live SSE progress),
  landscape view (Phase 2 report, rendered).
- **Implementation posture**: served by `cortex serve` from `go:embed` static
  assets; plain HTML/JS or a minimal build step — the binary stays
  self-contained, no node toolchain required to *run* cortex. Talks only to
  the Phase 4 API (no privileged back-channel — keeps the API honest).
- Loop management screens land in Phase 6 with the loops themselves.

Tests: Go-side — embedded-asset serving, transcript-to-view-model rendering
(golden). JS kept thin enough that the view-model boundary carries the logic;
an end-to-end smoke (serve → create session → turn → SSE renders) gates the
phase.

## Phase 6 — Loops across projects

**Value shipped:** the "manage AI work in one place" payoff — recurring or
triggered cortex runs across registered projects, defined and observed from
the web app.

Scope:

- **Loop spec** — `~/.cortex/loops.json` (or a journal class): name, project,
  prompt, cadence (interval/cron) or manual trigger, bounds (max turns, token
  budget), enabled flag.
- **Scheduler in the serve process** — ticks while `cortex serve` runs; each
  firing = a fresh headless session in the target project via the Phase 3/4
  machinery (fresh-session-per-piece, per the loop-harness working style).
  Serve down ⇒ loops pause; state on disk ⇒ nothing lost; next start resumes
  the schedule. No separate daemon — always-on is launchd/`brew services`
  around `cortex serve` if wanted.
- **Run history + management UI** — journal events per run (`loop.run` with
  outcome, cost, change ref); UI to create/enable/disable loops and read run
  history. Guardrails: loops run headless, so `shellrisk` Risky ⇒ Blocked
  already applies; per-loop budget caps enforced by the scheduler.

Explicitly out: multi-machine execution, parallel runs of one loop,
auto-merge of loop-produced changes (each run lands as a reviewable
`cortex change`).

Tests: scheduler with a fake clock (fire/skip/overlap-suppression); spec CRUD
round-trip; budget-cap enforcement; run-history journal golden; UI smoke.

## Phase 7 — Discord parity

**Value shipped:** the discord surface stops being a lagging fork of the
interactive CLI — same capabilities, same session ergonomics, one adapter
pattern instead of two.

**Timing:** deliberately last, and gated on the coding-harness roadmap
completing — parity is a moving target until the harness settles; chasing it
per-feature would pay the sync cost repeatedly. One parity pass after the
harness stabilizes pays it once.

The gap to close is *affordances*, not agent capability. Because discord
already calls `session.Turn()`, everything the agent can do (memory tools,
context curation, study, recall) flows to discord automatically. What discord
lacks is the interactive shell around the turn:

- **Re-base onto the Phase 4 seams** — discord becomes a client of the same
  `SessionManager` (and single-writer session locks) that serves the web app,
  retiring its bespoke mutex + session-swap logic (`discord.go`). One adapter
  pattern, tested once. (This resolves the former open question below.)
- **Command parity** — discord-native equivalents of the REPL slash commands
  (`/compact`, `/clear`, `/sessions`, `/model`) via discord slash commands or
  message prefixes; session pick/resume instead of the route-classifier-only
  lifecycle; `--project` targeting once Phase 3 lands.
- **Interactive risk approval** — today headless treats `shellrisk` Risky as
  Blocked; discord has a human present, so Risky becomes an approval prompt
  (reply/reaction with a timeout that falls back to Blocked). This is the one
  place discord gains something the REPL already had: a human in the loop.
- **Progress + long-turn ergonomics** — stream turn progress as message edits
  (the `Progress` seam, same source as Phase 4's SSE), and an interrupt
  affordance for runaway turns.

Explicitly out: multi-channel/multi-session multiplexing beyond what the
harness supports; any discord-specific agent behavior (the agent stays
surface-agnostic).

Tests: adapter logic against a fake `SessionManager` + scripted `Sender`;
risk-approval state machine table-driven (approve/deny/timeout); command
parsing goldens; the discord API boundary wrapped in an interface so no test
touches discord itself.

---

## Engineering ground rules (all phases)

- **TDD red/green**: each slice starts from failing tests; the phase-3
  refactor in particular moves only under the existing green suite.
- **stdlib `testing` only**, table-driven with `t.Run`, cleanup via `defer` —
  per CLAUDE.md constraints. HTTP via `httptest`; filesystems via `fs.FS`
  fixtures; LLMs via the scripted `Sender` seam — no live-model dependence in
  the gate.
- **Accept interfaces, return structs**: `BackendResolver`, `Scanner`,
  `Registry`, `SessionManager` are the seams; implementations are concrete
  structs. Constructors `NewXxx(cfg)`.
- **Invariants carried over**: local-only by default (`AssertLocalOnly`
  tripwire applies to serve and loops), `.cortex/` gitignored, jq-readable
  JSONL, journal is canonical / serve owns no state.
- `./scripts/check.sh` green at every phase boundary.

## Open questions

- Phase 1 free-model default: which OpenRouter `:free` id to pin as the
  guided-setup default (needs a current-catalog check at build time).
- Phase 2 project-scan roots: fixed default (`~/eng`?) vs. asked during the
  greeting conversation.
- Phase 6 cadence floor and default budgets — decide from real usage after
  Phase 5.
- Phase 7 command surface: discord native slash commands (registration
  ceremony, per-guild) vs. plain message prefixes — decide when it starts.
