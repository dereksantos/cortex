# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] — 2026-07: the Cortex slimdown

The active product is a single binary, `cmd/cortex`: an interactive coding
agent for small and local models with working memory built in. The daemon,
dashboard, broad `cortex` CLI, automatic retrieval/distillation pipeline, and
Claude Code host integration described by the earlier 0.2.0-alpha work were
removed when the project was slimmed down; see [`docs/archive.md`](docs/archive.md)
for what existed before and why it went.

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
