# Cortex

An interactive coding agent (`cmd/cortex`) for small and local models, with
working memory built in. The project was deliberately slimmed to center on
this one binary; the prior `cortex` CLI, eval framework, and Claude-Code
host integration were removed — see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

> Direction docs are authoritative for scope. The **live** direction is
> [`docs/memory-tools.md`](docs/memory-tools.md): memory is **tools the model
> drives** (`memory_write/read/search/forget` + `study(journal)`) over free-form
> named notes + an injected index — NOT a mechanical retrieval/distill pipeline.
> It supersedes the mechanical memory line —
> [`docs/archive/memory-distillation.md`](docs/archive/memory-distillation.md),
> [`docs/archive/working-memory.md`](docs/archive/working-memory.md),
> [`docs/archive/working-memory-study.md`](docs/archive/working-memory-study.md) — which is kept
> for history. The harness hardening plan is
> [`docs/cortex-production-harness.md`](docs/cortex-production-harness.md).
>
> The **loop/study refactor is SHIPPED** (was specified by
> [`docs/engine-unification.md`](docs/engine-unification.md) +
> [`docs/study-subagent.md`](docs/study-subagent.md)): the two tool-iteration
> loops collapsed into one `runLoop` engine driven by a `Sender` + `AgentDispatcher`
> seam; study is now a bounded `Study` subagent on that engine using
> `outline`/`grep`/targeted `read_file` (no recursion). `navigator.go` +
> `internal/projectindex/` are gone; the tool-call vocabulary lives in
> `internal/agent`, the tool surface in `internal/tools`, the structural map in
> `internal/outline`. Everything below describes today's (post-refactor) code.

## What Cortex is

A single long-lived REPL process. The turn loop:

```
read input → run agentic tool calls → capture the turn → curate context → reply
```

Sessions accumulate across turns and persist as raw JSONL transcripts in
`.cortex/sessions/<id>.jsonl` (resumable). The agent seeds its system prompt
from the repo's instruction file, resolved priority-ordered with first match
winning (no concatenation): `AGENTS.md`, then `CLAUDE.md`, then
`.github/copilot-instructions.md` — the walk starts at the CWD and goes up to
the filesystem root; the nearest directory containing any candidate wins,
and within it the first file in that order is loaded (findUp semantics with
the list applied per level); the loaded file is capped at
`limits.max_instruction_bytes` and the system prompt's
`# Project instructions (<file>)` header (and `/context`'s system legend row)
names which file it came from.

Three capabilities distinguish it:

1. **Working memory.** The window is a two-zone cache over the immutable
   transcript ([`docs/context-architecture.md`](docs/context-architecture.md)):
   an append-stable prefix (system + a deterministic outline of demoted turns +
   the memory index) and a watermarked hydrated tail (last turns verbatim,
   drains W/2→W/3). Old turns demote mechanically to outline lines with
   `@session/…#m…-…` citations; `recall(citation)` fetches the raw messages
   back; the outline folds via the summarizer only past W/8
   (`cmd/cortex/demote.go`, `internal/cache/`). Prompt size stays bounded
   forever — the old ~80% `Compact` (chunk-and-fold summarize,
   `cmd/cortex/summarize.go`, `/compact`) survives only as a safety net.
2. **Model-driven memory + per-turn capture.** The agent curates durable
   free-form notes through the `memory_write/read/search/forget` tools
   (`internal/memory`); the note index is injected at turn start
   (`memoryIndexNote`) so a fresh session knows what it can recall. Discovered
   [Agent Skills](https://agentskills.io/specification) — `<name>/SKILL.md`
   playbooks under `.cortex/skills` et al (`internal/skills`) — are indexed
   the same way, injected adjacent to the memory index: only name+description
   sit in context until the model reads a skill's `SKILL.md` with `read_file`
   on demand, per the standard's progressive-disclosure design. A one-line
   workspace note (the absolute workspace root — `workspaceNote`) is injected
   on every turn at the same slot, so the model never guesses foreign absolute
   paths (issue #142). Separately,
   `captureTurn()` records each turn (files edited, commands run, final answer)
   to the append-only journal — mechanical, no model — the record
   `study(.cortex/journal)` reads on demand. See
   [`docs/memory-tools.md`](docs/memory-tools.md). A background counterpart —
   the **learning loop** (`cortex learn`; the `Learn` subagent,
   `internal/tools/tools.go`) — mines that same journal off the coder's
   critical path for what the foreground had no task-shaped reason to save,
   writing through the same memory store the index above already reads. See
   [`docs/learning-loop.md`](docs/learning-loop.md).
3. **`study` — a bounded read-only subagent.** `study(path, goal)` runs the
   `Study` profile on the shared `runLoop` engine (`cmd/cortex/study.go`): seeded
   with a structural `outline` of the target plus the goal, it works a small
   bounded loop — `outline`, `grep`, `read_file` — to locate exactly the
   goal-relevant code, read those spans, and report a digest. It does not see the
   coder's conversation and cannot recurse. Engineered so narrow is the only
   option (`read_file` is a clamped span; whole-file reads above a small floor are
   refused → `outline` first) and the obvious one (`outline`/`grep` hand back exact
   line numbers). See [`docs/study-subagent.md`](docs/study-subagent.md).

## Commands

| Command | Purpose |
|---|---|
| `cortex` | Interactive REPL (default) |
| `cortex resume [id]` | Resume a prior session — with no id on an interactive TTY, opens the session picker first (ESC falls back to latest; non-TTY / `NO_COLOR` / `CORTEX_LOOP_RENDER=0` take latest directly); its resume banner goes to stderr (issue #118) |
| `cortex turn [--session id] [--plan] [--json] <input...>` | Headless single turn (drivers/scripts); `--plan` runs plan-then-execute (one planning turn, then each step as its own turn); the verify-before-fix principle rides in the base system prompt (so every turn, loop-driven or not, sees it) and is restated in the planning and step prompts — a reported problem that doesn't reproduce is finished by reporting it with the evidence — the step prompts tell the model to lead such a reply with "Not reproduced:" + the evidence, and that note is carried into the later steps' prompts and the per-step report (issue #178); `--session`'s resume banner and the session id go to stderr — stdout is the answer only (issue #118) |
| `cortex study <path> [goal...]` | One-off study (the `Study` subagent); prints the digest |
| `cortex learn [--project <name>]` | One-off background learning pass (the `Learn` subagent) over the journal since the last cursor; prints a short report |
| `cortex change <start\|commit\|status>` | Git change lifecycle — one reviewable change at a time (local git only) |
| `cortex serve [--port <n>]` | Local HTTP/SSE adapter for the web UI (loopback-only, Host/Origin allowlist; no bearer token — 2026-07-19) |
| `cortex scan [--json] [--root <path>] [--register]` | Scan configured roots and list discovered projects |
| `cortex project <add\|list\|remove>` | Manage the project registry |
| `cortex project trust <add\|remove\|list>` | Manage the per-workspace trust list (the post-edit hook's only gate; user config only) |
| `cortex project commands [--json] [--project <name>]` | Show the project's resolved format/lint/test/build commands (discovery + declarations; `--json` for the machine-readable shape) |
| `cortex discord` | Discord adapter (token from `DISCORD_BOT_TOKEN`) |
| `cortex study-eval` | Study acceptance test (ø gate: goal-hit + clean-finalize + bounded; `CORTEX_STUDY_REPS` reps) |
| `cortex model [--json]` | Catalog code/study role bindings + what the backend serves; suggest a `models` config block from detected RAM |

REPL slash commands: `/help`, `/context`, `/compact`, `/clear`, `/sessions`,
`/model [name]`, `/plan <task>`, `/hook off|format|all` (turns the post-edit
hook down or off for this session — monotone-down, never raises it; bare
`/hook` shows the current mode), `/quit`. Dispatch is in `cmd/cortex/main.go`'s `main()`:
subcommands are the `os.Args[1]` if-chain before the REPL loop starts, slash
commands are the `input ==` checks inside the REPL's input loop (`for {`).
`/help` lists the commands; `/context`
(`cmd/cortex/context_cmd.go` + `context_grid.go`) renders the current
session's two-zone context window (docs/context-architecture.md) as a fixed
8×16 glyph grid spanning the whole model window — one glyph per component
(system prompt, outline, memory index, skills index, workspace note, hydrated
tail, free space), a demote-watermark tick, and a legend — under a header
(carrying the session's cumulative in/out tokens and, when reported, cost)
and a prefix-cache health headline. On an interactive TTY that map opens in the
**inspector** (`internal/lineedit/inspect.go`): an alternate-screen, scrollable
view that restores the user's scrollback byte-for-byte on exit. The REPL stays
scrollback-native by default; the inspector is the on-demand escape hatch for
the few surfaces that want a whole screen, and `/context`
(`cmd/cortex/context_view.go`) is its first consumer — a view implements only
`Title`/`Lines(width)` and calls `Terminal.Inspect`. It is strictly an
enhancement: no TTY, `NO_COLOR`, or `CORTEX_LOOP_RENDER=0` all keep the plain
scrolling report, byte for byte. While a turn's pinned prompt is live the
inspector does not open a competing reader — the anchor is suspended and its
key loop forwards raw bytes, the same "serve it from the loop that owns the
terminal" rule `Anchor.Confirm` follows.

`/sessions` (`cmd/cortex/session_picker.go`) is the inspector's second consumer,
and the first that is **interactive** rather than read-only: the harness grew a
cursor plus `Selecter`/`Cursorer`/`Accepter`/`Filterer` view interfaces so typed
text reaches the view instead of quitting, Enter reports acceptance, and the
caller reads the pick after the screen closes. The picker filters by prompt, id,
and model (case-insensitive substring, newest-first preserved) and resumes on
Enter; ESC leaves the current session alone. It carries the same strict
enhancement gate as `/context` (`sessionsInspectable` mirrors
`contextInspectable`), so a pipe, `NO_COLOR`, or `CORTEX_LOOP_RENDER=0` prints the
plain list exactly as before. `cortex resume` with no id opens the same picker at
startup (`resumePickerUsable` + `pickSessionAtStartup`), cancelling there falls
through to today's latest-session resume. `main.go` reaches the harness through
one seam, `var inspectSession`, so tests can capture the view without a TTY.
Memory is model-driven — ask in natural language ("remember that …" /
"forget the … note") and the agent calls the memory tools; the old
`/remember` and `/forget` slash commands were removed with the mechanical
capture/retract pipeline.

Tab completes in the interactive REPL (issue #108; the engine is pure in
`internal/lineedit/completion.go`, wired by `cmd/cortex/mentions.go`):
slash commands, the `/model <id>` argument (bare model ids from one source —
the slash completer's Sub hook), and `@path` file mentions (workspace-relative,
`.gitignore`-aware, `..`/absolute escapes refused). First Tab fills the common
prefix, later Tabs cycle the candidates — each candidate splices in place of
the word at the cursor, so surrounding text survives. A submitted `@path`
mention attaches the file to the turn with the same size rules as `read_file`
(small files inline, large files as a structural outline + pointer to
study); the mention is replaced by a `[@path attached]` marker in what the
model sees. Only an `@` starting a whitespace-delimited word is a mention
(emails and `@types/node`-style names are prose), and a mention that does not
resolve to a readable file leaves the input unchanged. History records the
line exactly as typed.

The REPL is plain-text by decision (2026-07-19): no icon set (the old
❯◆▸✻⤷⚠✦ glyphs are gone), ANSI color and the context gauge are kept. Tool
actions print as `  tool: verb(args)` (`internal/tools/tools.go`'s
`printToolAction`); role lines are colored by their timestamp instead of a
per-role icon (`cmd/cortex/display.go`'s `gutter`); the plain spinner
(`internal/loopui/spinner.go`) is a static label with only its
elapsed-seconds tick moving (no animated spinner frames). The anchored
status row (`internal/lineedit/live.go`, issue #109) appends live stats to
the activity label, refreshed per model round-trip: the coding model's name,
the context-window fill (`ctx %`), the last request's billed `in / out`
tokens (the in side is the last request's prompt, the out side the last
response's completion — both refreshed per request, so they pair the same
turn, not two different turns), the session's cumulative cost — shown only
when the backend actually reported one, never estimated — and the turn's
elapsed seconds. The row shows ONE elapsed counter: when the activity label
already carries its own seconds tick (the thinking indicator), the
turn-elapsed segment is dropped so the row never shows two disagreeing
counters. When the row exceeds the available width it trims right to left —
cost drops first, then the token counts, then the context fill — with the
model name kept last. Two things print
*under* a tool line, both plain-text by the same rule (`+`/`-`,
indentation, and color only — no connectors or box-drawing):
`edit_file`/`write_file` render the change as a bounded unified diff
(`internal/tools/diff.go` — collapsed context, capped height, `… N more
lines` for the rest), and a subagent's own calls (`study`, `agent`) nest
two spaces per depth between the parent's action line and a closing
`<name> done: N calls, …` line, each announced once on completion with
its elapsed time and a one-line result summary
(`internal/tools/nesting.go`). Both honor the existing degradation
paths: `deps.Quiet()`, `NO_COLOR`, `CORTEX_LOOP_RENDER=0`, and a non-TTY
stdout (no width, so no clipping).

## The agent's tools

Registered in `internal/tools/tools.go` (`All` + dispatch in
`tools.Execute()`): `read_file`, `write_file`, `edit_file`, `study`, `agent`, `outline`,
`grep`, `bash`, `remove_path`, `web_search`, `fetch_url`, `scan_landscape`
(coder-only, home-scoped survey of local agent harnesses/model runtimes;
existence-only, never walks projects), `recall` (resolves a session-outline citation
to the verbatim demoted messages; coder-only — not in the Study profile), and
the model-driven memory tools
`memory_write`, `memory_read`, `memory_search`, `memory_forget`
(`internal/memory`), and the context self-curation tools
`context_evict`, `context_merge`, `context_adjust_watermarks`.
(`project_index` was replaced by `outline` + `grep`.)

- `agent` is a general implementation subagent (`docs/agent-tool.md`):
  Study's read set plus `write_file`/`edit_file`/`bash`, depth cap 1, Risky
  shell treated as Blocked inside it. Runs as the coder's current model by
  default (optional per-call `model` arg); config gate `tools.enable_agent`.
- The built-in system prompt carries a locate-first working-style principle
  (issue #142), spliced into the `# How you work` block and mirrored here
  verbatim (a drift tripwire, same pattern as the debugging principle under
  Constraints → Testing):

  Locate first. Outline or grep a path to find exactly where the content lives, then read_file only the spans you need — never read whole files you haven't outlined, never invent or guess file paths (work only from paths outline/grep actually returned), never re-read content already present in context (already-read spans, earlier tool output, the outline), and never use bash `cat`/`sed`/`head` (or similar) to read files — read_file/outline/grep are your readers.

- `read_file` refuses files over `CurationBudgetTokens` (16000) and
  redirects to `study`; large Go files return a declaration skeleton. A
  directory returns a bounded listing (directories marked `/`) plus a pointer
  to `outline`, and a missing path returns an oriented error: it points at
  `outline`/`grep` instead of guessing, states the workspace root for
  absolute or out-of-workspace paths, and lists nearby existing candidates
  (issue #142).
- `edit_file` is exact-match-first, whitespace-tolerant on retry; prefer it
  over `write_file` for edits. Failure results are self-correcting: an
  ambiguous match lists every occurrence's line number. A not-found match
  scores EVERY line of the old block against the file and anchors a bounded
  snippet of the closest region on the best-scoring line (not just the first,
  so a multi-line span whose first line is absent still finds the region its
  other lines point at); it also appends a directive to re-read the current
  span and retry `edit_file` — or use `write_file` for a whole-file rewrite —
  rather than scripting the change through `bash` (sed/awk/python) (#201:
  scripted multi-line edits corrupt files and skip the diff display + post-
  edit hook). A successful result appends the current changed region (added
  lines marked `>`, removed `-`, context unmarked, capped at 12 lines) so the
  model's view of the file stays in sync (#173).
- After `write_file`/`edit_file` lands, a post-edit hook runs the project's
  own format on the file just touched — it is FORMAT-ONLY. Lint moved to
  the turn END: in mode "all" on a trusted workspace it runs once per turn
  over the distinct `write_file`/`edit_file` paths (including the `agent`
  subagent's, minus files deleted since) — one run per file for `{file}`,
  one per distinct package dir for `{dir}` — under the turn's total lint
  budget (`project.turn_lint_budget_sec`, default 60s); findings reach the
  model in a tools-withheld finalize round and appear in the REPL, in
  `cortex turn` stderr + its `lint` JSON field, and in the journal.
  Workspace trust (the user config's `project.trusted`, set via
  `cortex project trust`) is the ONLY gate: on an untrusted workspace it
  runs nothing (one-line "hook inactive" note on the session's first
  edit), on a trusted one it runs the applicable per-file format command
  as a shell-free argv (templates with shell syntax are skipped with a
  note), each with a 10s budget — and appends a note (what ran, what it
  reported) to the result; the note never fails the edit. Commands are
  declared in `project.commands` or the `## Commands` section of the
  resolved instruction file (AGENTS.md → CLAUDE.md →
  .github/copilot-instructions.md; docs/configuration.md); `cortex
  project commands` shows each command's source and when it runs
  (per-edit / turn-end / never / inactive).
- `bash` is gated by `internal/shellrisk`: Safe runs, Risky prompts (judged
  against `turnIntent`), Blocked refuses. Headless sessions treat Risky as
  Blocked. The Risky prompt shows the classifier's reason above the command
  and offers `y` (once), `n`, `a` (always this session, exact command), and
  `p` (always this session, command prefix, e.g. `make test*`) (issue #107).
  Session approvals are memory-only — a fresh session starts with none —
  and are journaled to `.cortex/journal/shell/` as `shell.approval` entries.
  Approvals never override a Blocked verdict: the gate checks approvals
  after classification, so a Blocked command takes its own arm first.
  Every refusal (blocked, refused, or declined) carries the shared
  unknown-value tail from the shellrisk constructors (issue #200), neutral
  about what the command was for: if it was meant to check something, that
  result is still unknown — don't guess it, don't substitute a check of
  something else, mark it unverified.
  The gate also tracks "same-action" effect classes so a blocked
  action can't be re-routed in a later command: `git-history-write` and
  `hook-disabling` act as one barred group (a refused `git commit` can't
  re-enter as `--no-verify`), and `git-stash` is its OWN group — it covers
  `git stash`/`git stash <sub>` for every mutating sub —
  push/pop/apply/branch/drop/clear/store, bare `git stash` included — with
  the read-only `show` and `list` excluded, and a refused `git stash pop`
  bars a later `git stash push` but NOT an unrelated `git commit` (#201).
  Whether a stash is Risky at all is a classifier decision: the prompt marks
  working-tree-discarding git operations (stash pop/apply/clear,
  `checkout --`, restore) risky because they can lose uncommitted work.
  Separately, when a command rewrites
  a file in place (`sed -i`, `ed -s`, `perl -pi`, gawk's `awk -i inplace`,
  an interpreter `-c` script string, or a redirect/append target —
  quote-aware; plain awk only READS its file list), the tool appends a note
  naming each touched target (workdir-relative when a workdir is anchored) and
  steering to `edit_file`/`write_file`, whose diff display and post-edit hook
  scripted edits skip (#201). When such a rewrite targets a workdir path, the
  same format-only post-edit hook `write_file`/`edit_file` run is run on it
  and its note folded into the result, so script-edits get the same format
  coverage as tool edits (only on the success path — a refused command made no
  change; untrusted/no-format-command no-ops). The hook never runs on a
  script-form target: a `-c` program's arguments are only a guess at the file
  it opens, so the steering note names it but the formatter is not pointed
  at it.
- `remove_path` is workspace-confined (`.git`/`.cortex`/root refused);
  disabled by `tools.allow_delete: false`.
- `web_search` and `fetch_url` provide bounded, read-only public web access;
  `fetch_url` blocks local/private destinations and unsafe redirects. Both are
  coder-only and can be disabled with `tools.enable_web: false`.
- The context tools let the model curate its own working set on top of the
  mechanical demotion policy: evict or merge outline entries (merge installs
  one spanning `#m<first>-<last>` citation, so recall stays lossless) and
  shift the demotion watermarks (±W/4). `recall` takes an optional `budget`
  for a compact digest instead of the raw messages (this subsumed
  `context_summarize`). Curation persists across resume (per-turn session
  snapshot — decision 2026-07-18; the transcript remains the lossless
  record); per-tool gates `tools.enable_context_*`. See
  [`docs/context-window-modification-tools.md`](docs/context-window-modification-tools.md).

## Configuration

Layered, lowest→highest: `~/.cortex/config.json` (user) →
`./.cortex/config.json` (project, field-by-field override) → `CORTEX_BACKEND`
env. Loaded by `LoadConfig()` / `loadMergedConfig()` in `main.go`.

```json
{
  "backend": { "type": "openrouter", "endpoint": "...", "key_env": "OPENROUTER_API_KEY" },
  "models": {
    "code":  { "model": "...", "window": 131072 },
    "study": { "model": "..." }
  }
}
```

Roles: `code` (the agent) and `study` (the `Study` subagent + the summarizer +
the shell-risk classifier — all three build their sub-LLM call off the study
binding and pin reasoning effort off at the call site, docs/thinking-models.md).
`embed` stays parsed-but-reserved for a future semantic `memory_search`
(`CortexSession.resolveEmbedder` maps it to a remote OpenAI-compatible
embedder, but nothing calls that yet — `memory_search` is text-based, and
the in-process Hugot/ONNX embedder was deleted 2026-08-07). The configurable role
surface is exactly `code`/`study`/`embed` (`cmd/cortex/config.go`'s
`rolePolicies`) — a 2026-07-18 audit (`docs/completion-roadmap.md` E1) found
`hard-code`/`reason`/`fast`/`rerank`/`tools` genuinely dead and removed them;
an old config naming one of those still loads, sits inert, and prints one
stderr warning. The mechanical retrieve/rerank/Dream pipeline
(`pkg/cognition/dag`, `internal/cognition`) and the blind-sampling study
engine (`internal/study`) were deleted outright — see
[`docs/archive.md`](docs/archive.md).

**[`docs/configuration.md`](docs/configuration.md) is the authority** for
every `tools.*` gate, every env var, auth resolution (`key_env`/
`key_service`, no automatic provider-named fallback on this path), the
`backend.type` supported set, and the zero-config curated-fleet default —
kept in exactly one place so this section, the README, and the doc itself
can't drift apart. It also covers the full tunables surface beyond the
minimal example above: per-role transport timeouts/retries, `subagents.*`
(Study/Agent profile bounds), `tools.*` numeric caps, the `prompt` section (`prompt.file` replaces the
built-in system prompt, `prompt.append` extends it), and the `limits`/
`network`/`serve`/`repl`/`discord`/`skills` sections — every field optional,
defaulting to today's hardcoded value.

## Journal — source of truth

CQRS event-sourcing: the append-only JSONL journal (`.cortex/journal/<class>/`)
is canonical; storage is regeneratable from it. Per-segment flock makes
capture cross-process safe. See [`docs/journal.md`](docs/journal.md).
Invariants still enforced: **local-only by default**
(`journal.AssertLocalOnly` is a code-review tripwire for outbound paths),
**`.cortex/` is gitignored** (self-ignoring — a session in a git workspace
writes a lone-`*` `.cortex/.gitignore` rather than editing the user's file,
#119), **jq-readable plain JSONL**, closed segments gzippable.

## Go patterns

**Error handling**: wrap with context — `fmt.Errorf("failed to X: %w", err)`.

**Naming**: constructors `NewXxx(cfg *config.Config)`; interfaces are nouns
(`Provider`, `Storage`), not `IProvider`.

**Package structure**: `cmd/` entry points, `internal/` private impl,
`pkg/` public API.

**LLM calls**: go through the `pkg/llm` provider interface (Anthropic,
Ollama, OpenRouter, OpenAI-compatible). There is exactly one LLM layer —
`pkg/llm`. (The old duplicate `internal/llm` was removed.)

## Constraints

**Testing**: standard library `testing` only.
- Assertions via `t.Errorf` / `t.Fatalf` / `t.Fatal` — no testify/assert.
- Table-driven tests with `t.Run` subtests.
- Setup/teardown via `defer` (e.g. `defer os.RemoveAll(tempDir)`).

Debug carefully. Check every error in test and fixture setup with `t.Fatal` so a silently missing fixture can't masquerade as a code bug; confirm the fixture exists before suspecting the code under test. Debug with a focused test and `t.Logf` in the real package — never by copying production code into scratch modules or leaving `DEBUG` prints in shipped code.

Tests are evidence. An existing test's expected value records what someone decided correct behavior is; when it disagrees with your change, the burden of proof is on your change. Rewriting an expectation to match output you just produced is never a fix — it turns a bug into the specification.

A check that was blocked, refused, or declined leaves its result unknown. Don't guess it, and a check of something else doesn't stand in for it. Look for another safe way to observe the same thing; failing that, mark the claim unverified wherever you state it.

**Checks**: `./scripts/check.sh [fmt|vet|lint|all]` runs gofmt + `go vet`
+ golangci-lint (the same gate CI runs). Keep `go build ./...`, `go vet`,
and the test suite green.

## Build & test

```bash
go build ./cmd/cortex          # build the binary
go test ./...                # full suite
./scripts/check.sh           # fmt + vet + lint
```

## Key files

- `cmd/cortex/main.go` — REPL, `CortexSession` (composition root), `Turn`, dispatch, config
- `cmd/cortex/loop.go` — the `runLoop` engine + the `Sender`/`AgentDispatcher`/`Toolset`/`Bounds`/`Progress` seams + `requestFor`
- `cmd/cortex/study.go` — the `Study` subagent wiring (`RunSubagent`, `dispatcherFor`, `Outline`) + telemetry
- `cmd/cortex/learn.go` — the learning loop (`docs/learning-loop.md`): `RunLearningPass`, the journal-window/cursor mechanics, `cortex learn`, and the `loop_run.go` `kind:"learn"` firing
- `internal/loops/` — the loop spec store (`Spec`, incl. `Kind`) + scheduler (`Due`, cadence floor, three-strike disable)
- `cmd/cortex/summarize.go` — free-text summarizer (compaction + shell-output)
- `internal/agent/` — the shared tool-call vocabulary (`Tool`, `ToolCall`, `Bounds`); imports only the stdlib
- `internal/tools/` — the agent's tool surface (`Execute`, the tool decls, `grep`, the `Study` profile, `ConfinePath`/`TargetedRead`)
- `internal/outline/` — the structural map (`Outline`/`Render`; `go/ast` + regex tiers, breadth-first to budget)
- `internal/journal/` — append-only event log (incl. `study.result` telemetry)
- `internal/shellrisk/` — command risk classifier
- `pkg/llm/` — LLM providers
- `pkg/config/` — layered config
