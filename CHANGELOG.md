# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0] - 2026-10-24

### Security
- Built with Go 1.27.3, which fixes GO-2026-6809 (`crypto/x509` now
  distrusts compromised intermediate certificates), reachable through
  `net/http` TLS (#307).
- The `bash` tool's environment no longer carries configured `key_env`
  secrets or the Discord bot token.

### Added
- `apply_patch` edit format for model families trained on it, chosen with
  `tools.edit_format` (`auto`, `replace`, `patch`). `edit_file` now refuses a
  call with no `new_string` instead of deleting the matched text.
- `cortex completion <bash|zsh|fish>` prints a shell completion script.
- `backend.provider`: OpenRouter provider routing on every request path.
- A built-in default skill that explains how to write Cortex config (#305).
- Cortex reports its session lifecycle to Supacode's active-session sidebar.
- A durable benchmark store (`internal/benchstore`) with a shared v1 result
  format, off-machine sync, and a SWE-bench Verified runner. Results no
  longer land in a repo or worktree.

### Changed
- An unknown subcommand now fails with an error instead of starting the REPL.
- The README leads with the turn-key headline and a three-step Quick start.

### Fixed
- The shell-risk classifier accepts a verdict followed by a stray closing
  brace.
- Shell completion keeps file names and registers when sourced.

## [0.4.1] - 2026-10-12

### Fixed
- First-run OpenRouter setup now saves the OpenRouter endpoint. Before, it
  saved only the key, so the first turn went to `localhost:4000` and failed
  with a connection error (#280).
- Ollama models no longer run with Ollama's small default context. Ollama's
  OpenAI-compatible API cannot set the context per request, so long prompts
  were cut off without an error. First-run setup now creates a copy of the
  model with the configured context (no weights are copied), and a session
  warns when an Ollama model's context is smaller than its window (#280).

### Added
- Zero-config local models: on a first run with no config, Cortex finds a
  running Ollama, LM Studio, or llama.cpp server, picks an installed model
  that can call tools and fits this machine's RAM, and saves it for both the
  code and study roles. On a terminal it asks first. An OpenRouter key, when
  one is set, still takes precedence (#280).
- When the picked local model has fewer than 7B parameters, first-run setup
  says that small models often fail at agent work and offers a free hosted
  model instead.

## [0.4.0] - 2026-10-12

Most entries below were implemented by Cortex itself, through a
self-development loop that picks issues, opens pull requests, and fixes
review findings. See "Cortex builds Cortex" in the README.

### Added
- Image input for vision models. `models.<role>.vision` gates image content
  parts; `read_file` attaches png/jpeg/gif/webp files, and `@image` mentions
  (REPL) or an `attachments` field (`cortex serve`) attach them to a turn.
  Images count toward the window and demote to recallable outline lines; a
  text-only model gets a clear refusal (#222, #229, #233).
- Turn receipts. A turn that ran tools reports files changed, exit codes of
  the model's own test/build runs, files the format hook failed on, and the
  `- [ ]` checklist items the reply leaves out: in the REPL, on `cortex turn`
  stderr, and under the `receipt` JSON key. The model never sees them (#226, #234).
- `tests changed:` and `leftover debug:` receipts when a turn removes or
  shrinks tests, or leaves debug prints or scratch files behind; the model
  gets them before its final answer (#145, #166).
- Project commands. Cortex discovers format/lint/test/build commands
  (go.mod, package.json, pyproject.toml, Cargo.toml, Makefile) or reads them
  from `project.commands` or the instruction file's `## Commands` section.
  On a trusted workspace (`cortex project trust`), each edited file is
  formatted and lint runs once at turn end. See `cortex project commands`
  and `/hook off|format|all` (#143).
- Plan-then-execute: `cortex turn --plan` and `/plan <task>` plan a few
  steps and run each as its own turn, with a per-step report. A bug that
  does not reproduce is reported as "Not reproduced:" with the evidence
  (#157, #186).
- `/undo [N]` restores the working tree from per-turn git checkpoints. It
  leaves the index, the stash list, `.cortex/`, and pre-existing untracked
  files alone (#223).
- Risky shell prompts show the classifier's reason and accept `a` (always
  this command) or `p` (always this prefix) for the session (#208).
- REPL: tab completion for slash commands, `/model` ids, and `@path`
  mentions (#202); an interactive session picker for `/sessions` and
  `cortex resume` (#207); a live status row with model, context fill,
  tokens, reported cost, and elapsed time (#206); a turn-state light (#87).
- REPL layout: one line per tool call with its result, folded runs of
  read-only calls, a startup header, an answer footer, `/last` (Ctrl-O) to
  inspect the last turn's tool calls, and pickers for `/memory` and
  `/model` (#254).
- In-turn demotion: when a turn outgrows the window budget, the oldest tool
  results (this turn, then the previous one) become one-line stubs with
  `recall` citations on the wire. `context.in_turn_demotion` (#172, #181).
- The model's reasoning is sent back on each step of the same turn and shed
  first when over budget; it is never replayed into later turns or sent to
  OpenRouter. `context.keep_reasoning`, default on (#257).
- `attribution.*` config: a `Co-Authored-By` trailer on commits Cortex makes
  (`cortex change commit`, loop firings, Discord checkpoints, simple
  `git commit` through `bash`) and a PR-footer line in the prompt. Each
  commit is journaled with whether the trailer landed (#127, #196).
- A write-sanity note after a `.go` file is written: every duplicate
  package-level declaration and undefined name in the package, in one
  pass (#235).
- `thinking: "omit"` on a model role sends no reasoning field, for
  providers that reject it (#211).

### Changed
- The built-in system prompt is six short, language-neutral principles,
  capped at 4,000 characters. Tool descriptions no longer name
  language-specific tools. Guidance for working on this repository moved
  to `AGENTS.md` (#256).
- The full memory guidance is in the system prompt only when there are notes
  or a demoted outline to use, and never when `prompt.file` replaces the
  base prompt (#158, #160).
- Project instructions load from the first of `AGENTS.md`, `CLAUDE.md`, and
  `.github/copilot-instructions.md`; the prompt header and `/context` name
  the file (#152).
- `edit_file` feedback: a failed match shows the file's current closest
  region (or says only whitespace differs), an ambiguous match lists every
  line, and a successful edit shows the changed region and warns on large
  removals or dropped guard lines (`GUARD DROPPED`) (#164, #176, #203, #236).
- `bash`: shell reads and scratch files get no steering note. An in-place
  rewrite of a tracked file gets a note pointing to `edit_file`/`write_file`
  and runs the format hook. `tools.steer_shell_reads: true` restores the
  older notes (#203, #227, #258).
- Orientation: every turn carries a workspace-root note; `read_file` on a
  directory returns a listing; not-found errors name the workspace root,
  point to `outline`/`grep`, and list nearby files (#184).
- Blocked, refused, and declined commands say the check's result is
  unknown. A command with the same effect as one blocked earlier in the
  turn (e.g. `git commit --no-verify`, or a mutating `git stash`) is
  blocked too (#174, #203, #204).
- The model is warned 10 tool calls before the per-turn cap; a forced final
  answer must account for leftover debug files, test changes, and lint
  findings (#179).
- `cortex turn --session` prints its resume banner and session id on
  stderr; stdout carries only the answer (#168).
- Dependency and toolchain updates (Go 1.27.2, golang.org/x/net 0.59.0,
  x/crypto 0.57.0, glamour v2, and others), including bumps that clear
  vulnerabilities govulncheck reported as reachable (#125, #191).

### Fixed
- In a workspace under `$HOME` without its own `.cortex/`, Cortex no longer
  uses `~/.cortex` as the project dir, which mixed sessions and memory
  across projects (#270).
- No "hook inactive" note when the post-edit hook is off (#271).
- The shell-risk classifier recovers a verdict that a reasoning model left
  only in its reasoning (strictly, else it fails closed), re-sends a
  no-answer response once, and retries transient errors. A plain `2>&1`
  stream merge stays on the safe path (#211, #272).
- `cortex --help` prints no color codes when stdout is not a terminal (#273).
- An empty final round no longer returns stale pre-tool text as the answer:
  it gets one bounded salvage re-ask, and with reasoning on, one retry with
  reasoning off (#88, #156).
- Final answers: leaked text-form tool-call markup is stripped (or run as a
  real tool call mid-turn), and truncated answers are re-asked. A reply that
  is still empty, markup, or truncated is flagged as `summary issue:` on
  `cortex turn` stderr and `summary_issue` in `--json` (#211, #239).
- Strict providers no longer reject sessions with an empty tool result or
  malformed tool-call arguments; the wire copy is repaired and the
  transcript is unchanged (#120).
- A turn that recovered from a provider error prints `backend error:
  <status> <message>` and journals a redacted `model.failure` entry (#167).
- A repeated identical edit gets a plain "nothing to change" error that
  says to re-read the span (#211).

### Security
- Secrets (provider keys, `KEY=`/`TOKEN=`/`SECRET=` assignments, PEM private
  keys) are redacted before transcripts, journal entries, and memory notes
  are written (#193).
- A `fetch_url`/`web_search` result taints the turn: the content is framed
  as untrusted data, commands the classifier would auto-approve need
  explicit approval, stored session approvals do not apply, `git push` is
  never auto-approved, and writes are confined to the workspace. Headless
  runs block instead (#212).
- In a git workspace, the first session writes `.cortex/.gitignore` (`*`),
  so `git add -A` cannot commit transcripts or the journal (#175).

## [0.3.0] - 2026-08-07 — the Cortex slimdown

First published release: the earlier `0.1.0` and `0.2.0-alpha` sections
below were never tagged and no binary was ever distributed for them.

The active product is a single binary, `cmd/cortex`: an interactive coding
agent for small and local models with working memory built in. The daemon,
dashboard, broad `cortex` CLI, automatic retrieval/distillation pipeline, and
Claude Code host integration described by the earlier 0.2.0-alpha work were
removed when the project was slimmed down; see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

### Removed
- The in-process Hugot/ONNX embedder (`pkg/llm/hugot.go`) and its local
  `CORTEX_LOCAL_EMBED` default. It downloaded `all-MiniLM-L12-v2` on first
  use to serve a semantic-search path no caller reached — `SetEmbedder` was
  never invoked, so the embedding branch in capture was permanently dead.
  Removing it drops the binary from 23.9 MB to 17.2 MB and eliminates the
  `x/crypto/ssh` attack surface that `hugot.DownloadModel` pulled in. The
  `embed` role and the remote OpenAI-compatible embedder remain as the seam
  a future semantic `memory_search` would build on.

### Added
- Curl installer (`scripts/install.sh`, checksum-verified, `go install`
  fallback) and a tag-driven GoReleaser release pipeline
  (`.goreleaser.yaml`, `.github/workflows/release.yml`); the README's
  "hand it to your agent" paste-ready install prompt.
- Model self-healing (`docs/model-self-healing.md`): typed classification of
  model-call failures; a mid-session healing ladder that falls back onto the
  curated free OpenRouter suite and re-issues the pending request on the
  replacement; startup preflight coverage for pinned models; classified
  turn-error diagnosis lines; `model.failure` journal receipts and a recent
  model-events section in `cortex model`. Gated by `network.self_heal`
  (default on).
- A background learning loop (`docs/learning-loop.md`): `cortex learn` and
  the loop scheduler's `kind:"learn"` firing run a bounded Learn subagent
  over the journal since the last cursor, writing durable notes through the
  same memory store the turn-start index reads from — curated by a
  why-over-what principle (rationale and decisions, never code facts, so
  notes can't go stale against the tree). User-tier memory (dual
  project/user stores, scoped tools, tiered index injection) and a
  cross-project `LearnUser` promotion pass generalize recurring
  project-tier notes up to the user tier (`docs/cross-source-learning.md`).
- A general-purpose `agent` tool (`docs/agent-tool.md`): a bounded
  implementation subagent — Study's read tools plus `write_file`/
  `edit_file`/`bash` — for handing off one self-contained unit of work
  end to end, gated by `tools.enable_agent`.
- `cortex scan` / `cortex project` and the coder-only `scan_landscape` tool:
  discover local AI-tool harnesses and runtimes, and maintain the
  multi-project registry `cortex serve` and the loop scheduler read from.
- `prompt.file` / `prompt.append` config for system-prompt customization —
  replace or extend the built-in system prompt.
- Persistent, resumable Cortex REPL, a headless `turn` driver, a `cortex serve`
  web UI, and a Discord adapter. The web UI now includes a dashboard,
  a projects screen, and a memory screen (tier-tagged note browsing, note
  view/delete, and recent learning activity).
- Bounded two-zone session context with citation-grounded demotion, outline
  folding, persistent restoration, and exact-message `recall`.
- Model-driven durable memory through `memory_write`, `memory_read`,
  `memory_search`, and `memory_forget`.
- Bounded read-only Study subagent over `outline`, `grep`, and targeted
  `read_file`.
- Public, coder-only `web_search` and SSRF-safe `fetch_url` tools.
- Model-directed context working-set controls for evicting, merging, and
  adjusting watermarks, plus an optional `budget` on `recall` for a compact
  digest (replacing `context_summarize`).
- Layered user/project config and Anthropic, Ollama, OpenRouter, and generic
  OpenAI-compatible backends.

### Changed
- The project now centers on the `cmd/cortex` binary and one shared bounded agent
  loop for coder and Study roles.
- Session context uses mechanical two-zone demotion rather than automatic
  retrieval/distillation as its hot path.
- Go 1.26 is required.

### Fixed
- Prompt-cache stability across memory-index updates and incremental context
  demotion.
- Web fetching rejects local/private destinations, unsafe redirects, and
  unbounded response bodies.

---

## [0.2.0-alpha] - historical, pre-slim-down

> The features in this section describe the former daemon/dashboard product,
> not the current Cortex interface. See [`docs/archive.md`](docs/archive.md).

### Added
- **Multi-project support** via single global daemon at `~/.cortex/`
- New `pkg/registry` package: project registry stored at `~/.cortex/projects.json` with slug generation and git-remote detection
- Layered config loading via `config.LoadGlobal()` — global `~/.cortex/config.json` provides defaults, per-project config overlays
- Cross-project Dream sources for discovery across all registered projects
- Dashboard `/api/projects` endpoint and project list in sidebar
- `SECURITY.md` with vulnerability disclosure policy
- GitHub issue and pull-request templates under `.github/`
- README status badges (CI, Go version, license)

### Changed
- **BREAKING:** Daemon is now global (single PID) instead of per-project. Existing users should remove old per-project daemon state and re-init.
- JSONL records are now tagged with `project_id` for cross-project coexistence
- Capture command walks up directories to find the project root and routes to that project's queue
- README reframed as **public alpha** with honest scope ("what works" / "what's early")
- ROADMAP updated to April 2026 with current status
- Long-form docs (`ABSTRACT.md`, `OnContextEvolution.md`, `CORTEX.md`, `eval.md`) moved into `docs/` as lowercase filenames
- Cursor integration README clearly marked "Planned, not yet functional"

### Fixed
- Removed a hardcoded personal path from `docs/prompts/eval-data-gathering.md`
- `.gitignore` now covers root-level runtime artifacts (`/cortex` binary, `daemon_state.json`, `session.json`, `/db/`, `/logs/`)

### Internal
- Stale internal working docs moved to `docs/archive/` (architecture review, pre-launch checklist, paper-references TODO)

---

## [0.1.0] - 2025-01-15

### Added
- Initial release of Cortex, an intelligent development context memory system
- Event sourcing architecture with SQLite database
- Fast event capture with <10ms performance target
- Local LLM integration with Ollama for semantic analysis
- Intelligent insight categorization (decisions, patterns, insights, strategies)
- Background async processing with queue management
- Comprehensive CLI interface with 28 commands
- Full-text search with FTS5 support
- Knowledge graph with entities and relationships
- Real-time status monitoring for Claude Code
- Auto-initialization with environment detection
- Privacy-first design - all processing happens locally

### Core Features
- **Fast Capture**: Sub-10ms event capture for AI tool hooks
- **Semantic Analysis**: Local LLM distinguishes important events from noise
- **Pattern Recognition**: Automatic detection of recurring development patterns
- **Decision Tracking**: Capture and analyze architectural decisions
- **Knowledge Graph**: Structured entity and relationship storage
- **Privacy-First**: Zero telemetry, all processing local with Ollama
- **Zero-Friction**: Silent failure design, doesn't interrupt AI tools
- **Single Binary**: ~14MB static binary with zero dependencies

### CLI Commands
- `cortex init [--auto]` - Initialize project with auto-detection
- `cortex capture` - Fast event capture (used by hooks)
- `cortex daemon` - Background async processor
- `cortex search <query>` - Full-text search across events and insights
- `cortex insights [category]` - View categorized insights
- `cortex entities [type]` - Browse knowledge graph entities
- `cortex graph <type> <name>` - Show entity relationships
- `cortex stats` - Database and system statistics
- `cortex info` - System info and model recommendations
- `cortex test [type]` - Test LLM analysis functionality
- `cortex session-start` - Session initialization hook
- `cortex inject-context` - Context injection for AI prompts
- `cortex overview` - Visual summary of captured knowledge
- `cortex cli` - Slash command router for Claude Code

### Integrations
- **Claude Code**: PostToolUse, SessionStart, UserPromptSubmit hooks
- **Cursor IDE**: LSP adapter (basic implementation)
- **Generic**: stdin/stdout interface for any AI tool

### Technical
- Built with Go 1.21+
- SQLite with event sourcing pattern
- File-based queue system for reliability
- Atomic file operations (temp + rename pattern)
- Graceful degradation when Ollama unavailable
- Deduplication (30s window per file)
- 5 parallel async workers for LLM processing
- Configurable via JSON
- Cross-platform: macOS, Linux, Windows

---

For more details about changes, see the [commit history](https://github.com/dereksantos/cortex/commits/main).
